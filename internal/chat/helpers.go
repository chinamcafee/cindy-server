package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/idempotency"
)

var (
	errInvalidInput          = httpapi.Err(http.StatusBadRequest, "INVALID_INPUT", "invalid input")
	errInvalidParams         = httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid params")
	errConversationNotFound  = httpapi.Err(http.StatusNotFound, "CONVERSATION_NOT_FOUND", "conversation not found")
	errMessageNotFound       = httpapi.Err(http.StatusNotFound, "NOT_FOUND", "message not found")
	errMediaNotFound         = httpapi.Err(http.StatusNotFound, "MEDIA_NOT_FOUND", "media not found")
	errActorNotOwned         = httpapi.Err(http.StatusForbidden, "ACTOR_NOT_OWNED", "actor not owned by account")
	errOwnerRequired         = httpapi.Err(http.StatusForbidden, "OWNER_REQUIRED", "owner required")
	errRoleRequired          = httpapi.Err(http.StatusForbidden, "ROLE_REQUIRED", "role required")
	errConversationArchived  = httpapi.Err(http.StatusConflict, "CONVERSATION_ARCHIVED", "conversation archived")
	errConversationDissolved = httpapi.Err(http.StatusGone, "CONVERSATION_DISSOLVED", "conversation dissolved")
	errPlanOpen              = httpapi.Err(http.StatusConflict, "PLAN_OPEN", "a plan is already open")
	errPlanClosed            = httpapi.Err(http.StatusConflict, "PLAN_CLOSED", "plan is closed")
	errLastOwner             = httpapi.Err(http.StatusConflict, "LAST_OWNER", "last owner cannot leave")
	errResetRequired         = httpapi.Err(http.StatusGone, "RESET_REQUIRED", "cursor reset required")
	errOperationConflict     = httpapi.Err(http.StatusConflict, "OPERATION_CONFLICT", "operationId reused with different parameters")
	errUploadExpired         = httpapi.Err(http.StatusGone, "UPLOAD_EXPIRED", "upload expired")
)

func revisionConflict(current int) *httpapi.APIError {
	return httpapi.ErrWithDetails(http.StatusConflict, "REVISION_CONFLICT", "revision conflict", map[string]any{"revision": current})
}

func homeConflict(current *string) *httpapi.APIError {
	var v any
	if current != nil {
		v = *current
	}
	return httpapi.ErrWithDetails(http.StatusConflict, "HOME_DEVICE_CONFLICT", "home device conflict", map[string]any{"homeDeviceId": v})
}

func formatSeq(v int64) string { return strconv.FormatInt(v, 10) }

func mapJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func deviceID(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Device-Id")); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get("deviceId"))
}

func parseLimit(r *http.Request, def int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		return 0, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "limit must be 1..100")
	}
	return n, nil
}

func parseSeq(raw, name string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid "+name)
	}
	return v, nil
}

func isUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}

func nullUUID(v string) any {
	if v == "" {
		return nil
	}
	return v
}

type idemRecord struct {
	Hash       string          `json:"hash"`
	StatusCode int             `json:"statusCode"`
	Body       json.RawMessage `json:"body"`
}

func (m *Module) idemReplay(w http.ResponseWriter, ctx context.Context, key, requestHash string) bool {
	raw, err := m.deps.Redis.Get(ctx, key).Bytes()
	if err != nil {
		return false
	}
	var rec idemRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return false
	}
	if rec.Hash != requestHash {
		httpapi.WriteError(w, errOperationConflict)
		return true
	}
	httpapi.WriteJSON(w, rec.StatusCode, rec.Body)
	return true
}

// idemDoOptional skips idempotency bookkeeping when operationId is omitted.
// Actor home PATCH is compare-and-set (expectedHomeDeviceId), and the desktop
// client intentionally sends no operationId for it.
func (m *Module) idemDoOptional(w http.ResponseWriter, r *http.Request, operationID string, req any, fn func(context.Context) (int, any, error)) {
	if strings.TrimSpace(operationID) == "" {
		status, body, err := fn(r.Context())
		if err != nil {
			m.fail(w, err)
			return
		}
		httpapi.WriteJSON(w, status, body)
		return
	}
	m.idemDo(w, r, operationID, req, fn)
}

func (m *Module) idemDo(w http.ResponseWriter, r *http.Request, operationID string, req any, fn func(context.Context) (int, any, error)) {
	if strings.TrimSpace(operationID) == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "operationId is required"))
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	requestHash := idempotency.Hash(req)
	ctx := r.Context()

	if m.deps.Redis != nil {
		key := "cindy:chat:idem:" + accountID + ":" + operationID
		if m.idemReplay(w, ctx, key, requestHash) {
			return
		}
		lockKey := key + ":lock"
		acquired, err := m.deps.Redis.SetNX(ctx, lockKey, requestHash, 10*time.Second).Result()
		if err != nil {
			m.fail(w, httpapi.ErrUnavailable)
			return
		}
		if !acquired {
			for i := 0; i < 40; i++ {
				if m.idemReplay(w, ctx, key, requestHash) {
					return
				}
				select {
				case <-ctx.Done():
					m.fail(w, httpapi.ErrUnavailable)
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			httpapi.WriteError(w, errOperationConflict)
			return
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			m.deps.Redis.Del(cleanupCtx, lockKey)
		}()
		status, body, err := fn(ctx)
		if err != nil {
			m.fail(w, err)
			return
		}
		encoded, _ := json.Marshal(body)
		rec, _ := json.Marshal(idemRecord{Hash: requestHash, StatusCode: status, Body: encoded})
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		m.deps.Redis.Set(storeCtx, key, rec, 24*time.Hour)
		cancel()
		httpapi.WriteJSON(w, status, json.RawMessage(encoded))
		return
	}

	if m.deps.Idem == nil {
		status, body, err := fn(ctx)
		if err != nil {
			m.fail(w, err)
			return
		}
		httpapi.WriteJSON(w, status, body)
		return
	}
	res, err := m.deps.Idem.Do(ctx, "chat:"+accountID, operationID, requestHash, fn)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict), errors.Is(err, idempotency.ErrInProgress):
			httpapi.WriteError(w, errOperationConflict)
		default:
			m.fail(w, err)
		}
		return
	}
	httpapi.WriteJSON(w, res.StatusCode, json.RawMessage(res.Body))
}

func (m *Module) ensureHumanActor(ctx context.Context, accountID string) (actorRow, error) {
	var displayName string
	err := m.deps.DB.Pool.QueryRow(ctx, `SELECT display_name FROM auth_memberships WHERE account_id=$1 AND status='active' ORDER BY (kind='personal') DESC, created_at ASC LIMIT 1`, accountID).Scan(&displayName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return actorRow{}, err
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = "Me"
	}
	var actor actorRow
	err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "human:"+accountID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `SELECT `+actorCols+` FROM chat_actors WHERE account_id=$1 AND kind='human' ORDER BY created_at ASC LIMIT 1`, accountID)
		a, err := scanActor(row)
		if err == nil {
			actor = a
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row = tx.QueryRow(ctx, `INSERT INTO chat_actors (account_id, kind, display_name) VALUES ($1, 'human', $2) RETURNING `+actorCols, accountID, displayName)
		a, err = scanActor(row)
		if err != nil {
			return err
		}
		actor = a
		return nil
	})
	return actor, err
}

func (m *Module) loadActor(ctx context.Context, actorID string) (actorRow, error) {
	if !isUUID(actorID) {
		return actorRow{}, errActorNotOwned
	}
	a, err := scanActor(m.deps.DB.Pool.QueryRow(ctx, `SELECT `+actorCols+` FROM chat_actors WHERE id=$1`, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return actorRow{}, errActorNotOwned
	}
	return a, err
}

func (m *Module) humanForAccount(ctx context.Context, accountID string) (actorRow, error) {
	a, err := scanActor(m.deps.DB.Pool.QueryRow(ctx, `SELECT `+actorCols+` FROM chat_actors WHERE account_id=$1 AND kind='human' ORDER BY created_at ASC LIMIT 1`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return actorRow{}, errConversationNotFound
	}
	return a, err
}

func (m *Module) resolveActingActor(r *http.Request, human actorRow) (actorRow, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Chat-Actor"))
	if raw == "" || raw == human.ID {
		return human, nil
	}
	actor, err := m.loadActor(r.Context(), raw)
	if err != nil {
		return actorRow{}, errActorNotOwned
	}
	if actor.AccountID != human.AccountID || actor.Kind != "bot" || actor.Disabled {
		return actorRow{}, errActorNotOwned
	}
	return actor, nil
}

func (m *Module) loadRoom(ctx context.Context, roomID string) (roomRow, error) {
	if !isUUID(roomID) {
		return roomRow{}, errConversationNotFound
	}
	r, err := scanRoom(m.deps.DB.Pool.QueryRow(ctx, `SELECT `+roomCols+` FROM chat_rooms WHERE id=$1`, roomID))
	if errors.Is(err, pgx.ErrNoRows) {
		return roomRow{}, errConversationNotFound
	}
	return r, err
}

func (m *Module) loadMembership(ctx context.Context, roomID, actorID string) (memberRow, error) {
	var mbr memberRow
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT room_id, actor_id, role, state, guest_access, access_revision, nickname, position, last_read_seq, joined_at
		FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, actorID).
		Scan(&mbr.RoomID, &mbr.ActorID, &mbr.Role, &mbr.State, &mbr.GuestAccess, &mbr.AccessRevision, &mbr.Nickname, &mbr.Position, &mbr.LastReadSeq, &mbr.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return memberRow{}, errConversationNotFound
	}
	return mbr, err
}

func (m *Module) requireJoined(ctx context.Context, roomID string, actorID string) (roomRow, memberRow, error) {
	room, err := m.loadRoom(ctx, roomID)
	if err != nil {
		return roomRow{}, memberRow{}, err
	}
	mbr, err := m.loadMembership(ctx, roomID, actorID)
	if err != nil {
		return roomRow{}, memberRow{}, err
	}
	if mbr.State != "joined" {
		return roomRow{}, memberRow{}, errConversationNotFound
	}
	return room, mbr, nil
}

func (m *Module) requireWritable(ctx context.Context, roomID string, actorID string) (roomRow, memberRow, error) {
	room, mbr, err := m.requireJoined(ctx, roomID, actorID)
	if err != nil {
		return roomRow{}, memberRow{}, err
	}
	if room.DissolvedAt != nil {
		return roomRow{}, memberRow{}, errConversationDissolved
	}
	if room.Archived {
		return roomRow{}, memberRow{}, errConversationArchived
	}
	return room, mbr, nil
}

func (m *Module) roleMember(ctx context.Context, roomID string, actor actorRow) (memberRow, error) {
	subject := actor
	if actor.Kind == "bot" {
		h, err := m.humanForAccount(ctx, actor.AccountID)
		if err != nil {
			return memberRow{}, err
		}
		subject = h
	}
	mbr, err := m.loadMembership(ctx, roomID, subject.ID)
	if err != nil {
		return memberRow{}, err
	}
	if mbr.State != "joined" {
		return memberRow{}, errConversationNotFound
	}
	return mbr, nil
}

func (m *Module) requireRole(ctx context.Context, roomID string, actor actorRow, roles ...string) (memberRow, error) {
	mbr, err := m.roleMember(ctx, roomID, actor)
	if err != nil {
		return memberRow{}, err
	}
	for _, role := range roles {
		if mbr.Role == role {
			return mbr, nil
		}
	}
	if len(roles) == 1 && roles[0] == "owner" {
		return memberRow{}, errOwnerRequired
	}
	return memberRow{}, errRoleRequired
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type roomEvent struct {
	RoomID    string
	Seq       int64
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

func (m *Module) appendEvent(ctx context.Context, tx pgx.Tx, roomID, actorID, typ string, payload any) (roomEvent, error) {
	raw := []byte("{}")
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return roomEvent{}, err
		}
		raw = encoded
	}
	var seq int64
	if err := tx.QueryRow(ctx, `UPDATE chat_rooms SET head=head+1, updated_at=now() WHERE id=$1 RETURNING head`, roomID).Scan(&seq); err != nil {
		return roomEvent{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO chat_room_events (room_id, seq, type, actor_id, payload) VALUES ($1,$2,$3,$4,$5::jsonb)`,
		roomID, seq, typ, nullUUID(actorID), raw); err != nil {
		return roomEvent{}, err
	}
	return roomEvent{RoomID: roomID, Seq: seq, Type: typ, Payload: raw}, nil
}

type newMessage struct {
	RoomID       string
	AuthorID     string
	Kind         string
	Content      []json.RawMessage
	ThreadRootID *string
	ReplyToID    *string
	SourceID     *string
	ClientID     *string
	CreatedAt    *time.Time
}

func (m *Module) insertMessageTx(ctx context.Context, tx pgx.Tx, nm newMessage) (messageRow, roomEvent, error) {
	content, err := json.Marshal(nm.Content)
	if err != nil {
		return messageRow{}, roomEvent{}, err
	}
	var head int64
	if err := tx.QueryRow(ctx, `SELECT head FROM chat_rooms WHERE id=$1 FOR UPDATE`, nm.RoomID).Scan(&head); err != nil {
		return messageRow{}, roomEvent{}, err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM chat_messages WHERE room_id=$1`, nm.RoomID).Scan(&seq); err != nil {
		return messageRow{}, roomEvent{}, err
	}
	if seq > head {
		if _, err := tx.Exec(ctx, `UPDATE chat_rooms SET head=$2 WHERE id=$1`, nm.RoomID, seq); err != nil {
			return messageRow{}, roomEvent{}, err
		}
	}
	createdAt := time.Now().UTC()
	if nm.CreatedAt != nil {
		createdAt = nm.CreatedAt.UTC()
	}
	kind := nm.Kind
	if kind == "" {
		kind = "message"
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat_messages (room_id, seq, sender_actor_id, client_message_id, kind, content, reply_to_id, thread_root_id, source_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10)
		RETURNING id`,
		nm.RoomID, seq, nm.AuthorID, nm.ClientID, kind, content, nm.ReplyToID, nm.ThreadRootID, nm.SourceID, createdAt).Scan(&id)
	if err != nil {
		return messageRow{}, roomEvent{}, err
	}
	ev, err := m.appendEvent(ctx, tx, nm.RoomID, nm.AuthorID, "message.created", map[string]any{
		"messageId": id,
		"seq":       formatSeq(seq),
		"authorId":  nm.AuthorID,
	})
	if err != nil {
		return messageRow{}, roomEvent{}, err
	}
	msg := messageRow{
		ID: id, RoomID: nm.RoomID, Seq: seq, AuthorID: nm.AuthorID, Content: content,
		ReplyToID: nm.ReplyToID, ThreadRootID: nm.ThreadRootID, CreatedAt: createdAt,
	}
	return msg, ev, nil
}

func (m *Module) publishEvents(events ...roomEvent) {
	for _, ev := range events {
		m.hub.publishEvent(ev)
	}
}

func (m *Module) queueExecutionTx(ctx context.Context, tx pgx.Tx, room roomRow, messageID, botID, requesterID string, contextSeq int64) (string, roomEvent, error) {
	var execID string
	err := tx.QueryRow(ctx, `
		INSERT INTO chat_executions (room_id, source_message_id, bot_actor_id, requester_id, status, context_seq, access_mode, access_revision)
		SELECT $1,$2,$3,$4,'queued',$5,
			CASE WHEN $6='direct' THEN 'owner' WHEN m.guest_access='none' THEN 'chat' ELSE m.guest_access END,
			m.access_revision
		FROM chat_room_members m
		WHERE m.room_id=$1 AND m.actor_id=$3 AND m.state='joined'
		RETURNING id`, room.ID, messageID, botID, requesterID, contextSeq, room.Kind).Scan(&execID)
	if err != nil {
		return "", roomEvent{}, err
	}
	ev, err := m.appendEvent(ctx, tx, room.ID, requesterID, "execution.changed", map[string]any{
		"status": "queued", "botId": botID, "executionId": execID,
	})
	return execID, ev, err
}

func newInviteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
