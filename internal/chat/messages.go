package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type sendMessageRequest struct {
	OperationID    string            `json:"operationId"`
	Content        []json.RawMessage `json:"content"`
	Mentions       []string          `json:"mentions"`
	MentionsAll    bool              `json:"mentionsAll"`
	ThreadRootID   *string           `json:"threadRootId"`
	ReplyToID      *string           `json:"replyToId"`
	DeferExecution bool              `json:"deferExecution"`
	SourceID       *string           `json:"sourceId"`
	CreatedAt      *time.Time        `json:"createdAt"`
}

type continueRequest struct {
	OperationID string `json:"operationId"`
	Automatic   bool   `json:"automatic"`
}

type cancelPlanningRequest struct {
	OperationID string `json:"operationId"`
	Failed      bool   `json:"failed"`
}

type reactionRequest struct {
	OperationID string `json:"operationId"`
	Emoji       string `json:"emoji"`
	Present     *bool  `json:"present"`
}

type messageQuery struct {
	limit        int
	before       int64
	threadRootID *string
	all          bool
}

func (m *Module) listMessages(ctx context.Context, q querier, roomID string, mq messageQuery) ([]messageWire, error) {
	args := []any{roomID}
	where := `m.room_id=$1`
	if mq.threadRootID != nil && *mq.threadRootID != "" {
		args = append(args, *mq.threadRootID)
		where += ` AND m.thread_root_id=$` + formatSeq(int64(len(args)))
	} else if !mq.all {
		where += ` AND m.thread_root_id IS NULL`
	}
	if mq.before > 0 {
		args = append(args, mq.before)
		where += ` AND m.seq < $` + formatSeq(int64(len(args)))
	}
	args = append(args, mq.limit)
	rows, err := q.Query(ctx, `SELECT `+messageCols+`
		FROM chat_messages m JOIN chat_actors a ON a.id=m.sender_actor_id
		WHERE `+where+` ORDER BY m.seq DESC LIMIT $`+formatSeq(int64(len(args))), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []messageWire{}
	ids := []string{}
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg.wire())
		ids = append(ids, msg.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	if err := m.enrichMessages(ctx, q, roomID, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) enrichMessages(ctx context.Context, q querier, roomID string, out []messageWire, ids []string) error {
	index := map[string]*messageWire{}
	for i := range out {
		index[out[i].ID] = &out[i]
	}
	rows, err := q.Query(ctx, `SELECT message_id, emoji, count(*) FROM chat_message_reactions
		WHERE message_id = ANY($1::uuid[]) GROUP BY message_id, emoji ORDER BY emoji`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var messageID, emoji string
		var count int
		if err := rows.Scan(&messageID, &emoji, &count); err != nil {
			rows.Close()
			return err
		}
		if item, ok := index[messageID]; ok {
			item.Reactions = append(item.Reactions, reactionWire{Emoji: emoji, Count: count})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT thread_root_id, count(*) FROM chat_messages
		WHERE room_id=$1 AND thread_root_id = ANY($2::uuid[]) GROUP BY thread_root_id`, roomID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rootID string
		var count int
		if err := rows.Scan(&rootID, &count); err != nil {
			return err
		}
		if item, ok := index[rootID]; ok {
			item.ReplyCount = count
		}
	}
	return rows.Err()
}

func (m *Module) handleListMessages(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	if _, _, err := m.requireJoined(r.Context(), roomID, human.ID); err != nil {
		m.fail(w, err)
		return
	}
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	before, err := parseSeq(r.URL.Query().Get("before"), "before")
	if err != nil {
		m.fail(w, err)
		return
	}
	var threadRootID *string
	if raw := strings.TrimSpace(r.URL.Query().Get("threadRootId")); raw != "" {
		if !isUUID(raw) {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid threadRootId"))
			return
		}
		threadRootID = &raw
	}
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	messages, err := m.listMessages(r.Context(), m.deps.DB.Pool, roomID, messageQuery{
		limit: limit, before: before, threadRootID: threadRootID, all: all,
	})
	if err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, messages)
}

func (m *Module) messageBelongsToRoom(ctx context.Context, q querier, roomID, messageID string) (messageRow, error) {
	if !isUUID(messageID) {
		return messageRow{}, errMessageNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+messageCols+`
		FROM chat_messages m JOIN chat_actors a ON a.id=m.sender_actor_id
		WHERE m.id=$1 AND m.room_id=$2`, messageID, roomID)
	if err != nil {
		return messageRow{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return messageRow{}, err
		}
		return messageRow{}, errMessageNotFound
	}
	msg, err := scanMessage(rows)
	if err != nil {
		return messageRow{}, err
	}
	return msg, nil
}

func (m *Module) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req sendMessageRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if len(req.Content) == 0 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "content is required")
		}
		total := 0
		for _, block := range req.Content {
			if len(block) > 64*1024 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "content block too large")
			}
			total += len(block)
		}
		if total > 256*1024 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "content too large")
		}
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		var msg messageRow
		var events []roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var room roomRow
			var err error
			if !isUUID(roomID) {
				return errConversationNotFound
			}
			room, err = scanRoom(tx.QueryRow(ctx, `SELECT `+roomCols+` FROM chat_rooms WHERE id=$1 FOR UPDATE`, roomID))
			if errors.Is(err, pgx.ErrNoRows) {
				return errConversationNotFound
			}
			if err != nil {
				return err
			}
			if room.DissolvedAt != nil {
				return errConversationDissolved
			}
			if room.Archived {
				return errConversationArchived
			}
			var mbr memberRow
			err = tx.QueryRow(ctx, `SELECT room_id, actor_id, role, state, guest_access, access_revision, nickname, position, last_read_seq, joined_at
				FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, acting.ID).
				Scan(&mbr.RoomID, &mbr.ActorID, &mbr.Role, &mbr.State, &mbr.GuestAccess, &mbr.AccessRevision, &mbr.Nickname, &mbr.Position, &mbr.LastReadSeq, &mbr.JoinedAt)
			if errors.Is(err, pgx.ErrNoRows) || mbr.State != "joined" {
				return errConversationNotFound
			}
			if err != nil {
				return err
			}
			threadRootID, err := optionalUUID(req.ThreadRootID, "threadRootId")
			if err != nil {
				return err
			}
			replyToID, err := optionalUUID(req.ReplyToID, "replyToId")
			if err != nil {
				return err
			}
			if threadRootID != nil {
				var rootThread *string
				var deleted *time.Time
				err := tx.QueryRow(ctx, `SELECT thread_root_id, deleted_at FROM chat_messages WHERE id=$1 AND room_id=$2`, *threadRootID, roomID).Scan(&rootThread, &deleted)
				if errors.Is(err, pgx.ErrNoRows) {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "thread root not found")
				}
				if err != nil {
					return err
				}
				if rootThread != nil || deleted != nil {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "thread root must be a main timeline message")
				}
			}
			if replyToID != nil {
				var replyThread *string
				var deleted *time.Time
				err := tx.QueryRow(ctx, `SELECT thread_root_id, deleted_at FROM chat_messages WHERE id=$1 AND room_id=$2`, *replyToID, roomID).Scan(&replyThread, &deleted)
				if errors.Is(err, pgx.ErrNoRows) || deleted != nil {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "reply target not found")
				}
				if err != nil {
					return err
				}
				sameThread := replyThread == nil && threadRootID == nil
				if replyThread != nil && threadRootID != nil && *replyThread == *threadRootID {
					sameThread = true
				}
				if replyThread == nil && threadRootID != nil && *replyToID == *threadRootID {
					sameThread = true
				}
				if !sameThread {
					return httpapi.Err(http.StatusBadRequest, "REPLY_THREAD_MISMATCH", "reply target is in another thread")
				}
			}
			botIDs, err := m.validateMentionsTx(ctx, tx, roomID, req.Mentions)
			if err != nil {
				return err
			}
			if req.SourceID != nil && strings.TrimSpace(*req.SourceID) != "" {
				var existingID string
				var existingSeq int64
				var existingAt time.Time
				err := tx.QueryRow(ctx, `SELECT id, seq, created_at FROM chat_messages WHERE room_id=$1 AND source_id=$2`, roomID, *req.SourceID).
					Scan(&existingID, &existingSeq, &existingAt)
				if err == nil {
					msg = messageRow{ID: existingID, RoomID: roomID, Seq: existingSeq, CreatedAt: existingAt}
					return nil
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			var existingID string
			var existingSeq int64
			var existingAt time.Time
			err = tx.QueryRow(ctx, `SELECT id, seq, created_at FROM chat_messages WHERE room_id=$1 AND client_message_id=$2`, roomID, req.OperationID).
				Scan(&existingID, &existingSeq, &existingAt)
			if err == nil {
				msg = messageRow{ID: existingID, RoomID: roomID, Seq: existingSeq, CreatedAt: existingAt}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			clientID := req.OperationID
			var ev roomEvent
			msg, ev, err = m.insertMessageTx(ctx, tx, newMessage{
				RoomID: roomID, AuthorID: acting.ID, Content: req.Content,
				ThreadRootID: threadRootID, ReplyToID: replyToID,
				SourceID: req.SourceID, ClientID: &clientID, CreatedAt: req.CreatedAt,
			})
			if err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" {
					return httpapi.Err(http.StatusConflict, "OPERATION_CONFLICT", "duplicate message")
				}
				return err
			}
			events = append(events, ev)
			if acting.Kind == "human" && !req.DeferExecution {
				targets, err := m.dispatchTargetsTx(ctx, tx, room, acting.ID, botIDs, req.MentionsAll)
				if err != nil {
					return err
				}
				for _, botID := range targets {
					_, ev, err := m.queueExecutionTx(ctx, tx, room, msg.ID, botID, acting.ID, msg.Seq)
					if errors.Is(err, pgx.ErrNoRows) {
						continue
					}
					if err != nil {
						return err
					}
					events = append(events, ev)
				}
			}
			return nil
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(events...)
		return http.StatusOK, map[string]any{"id": msg.ID, "seq": formatSeq(msg.Seq), "createdAt": msg.CreatedAt}, nil
	})
}

func optionalUUID(v *string, field string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil, nil
	}
	if !isUUID(s) {
		return nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid "+field)
	}
	return &s, nil
}

func (m *Module) validateMentionsTx(ctx context.Context, tx pgx.Tx, roomID string, mentions []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range mentions {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var kind string
		err := tx.QueryRow(ctx, `SELECT a.kind FROM chat_room_members m JOIN chat_actors a ON a.id=m.actor_id
			WHERE m.room_id=$1 AND m.actor_id=$2 AND m.state='joined'`, roomID, id).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpapi.Err(http.StatusBadRequest, "MENTION_UNAVAILABLE", "mention is not a joined member")
		}
		if err != nil {
			return nil, err
		}
		if kind == "bot" {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *Module) dispatchTargetsTx(ctx context.Context, tx pgx.Tx, room roomRow, authorID string, explicitBots []string, mentionsAll bool) ([]string, error) {
	if len(explicitBots) > 0 {
		return explicitBots, nil
	}
	if room.Kind != "direct" && !mentionsAll && (room.ResponseMode == nil || *room.ResponseMode != "all") {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id FROM chat_room_members m JOIN chat_actors a ON a.id=m.actor_id
		WHERE m.room_id=$1 AND m.state='joined' AND a.kind='bot'
		ORDER BY m.position ASC, m.joined_at ASC`, room.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != authorID {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (m *Module) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	messageID := chi.URLParam(r, "messageId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	if _, _, err := m.requireJoined(r.Context(), roomID, human.ID); err != nil {
		m.fail(w, err)
		return
	}
	msg, err := m.messageBelongsToRoom(r.Context(), m.deps.DB.Pool, roomID, messageID)
	if err != nil {
		m.fail(w, err)
		return
	}
	wire := msg.wire()
	if err := m.enrichMessages(r.Context(), m.deps.DB.Pool, roomID, []messageWire{wire}, []string{msg.ID}); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, wire)
}

func (m *Module) handleContinue(w http.ResponseWriter, r *http.Request) {
	var req continueRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	messageID := chi.URLParam(r, "messageId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		room, _, err := m.requireWritable(ctx, roomID, acting.ID)
		if err != nil {
			return 0, nil, err
		}
		msg, err := m.messageBelongsToRoom(ctx, m.deps.DB.Pool, roomID, messageID)
		if err != nil {
			return 0, nil, err
		}
		var open int
		if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM chat_plans WHERE room_id=$1 AND status IN ('proposed','pending','running','waiting')`, roomID).Scan(&open); err != nil {
			return 0, nil, err
		}
		if open > 0 {
			return 0, nil, errPlanOpen
		}
		var events []roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			targets, err := m.dispatchTargetsTx(ctx, tx, room, msg.AuthorID, nil, true)
			if err != nil {
				return err
			}
			for _, botID := range targets {
				_, ev, err := m.queueExecutionTx(ctx, tx, room, msg.ID, botID, acting.ID, msg.Seq)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				events = append(events, ev)
			}
			return nil
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(events...)
		return http.StatusOK, map[string]any{"ok": true}, nil
	})
}

func (m *Module) handleCancelPlanning(w http.ResponseWriter, r *http.Request) {
	var req cancelPlanningRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	messageID := chi.URLParam(r, "messageId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if _, _, err := m.requireWritable(ctx, roomID, human.ID); err != nil {
			return 0, nil, err
		}
		if _, err := m.messageBelongsToRoom(ctx, m.deps.DB.Pool, roomID, messageID); err != nil {
			return 0, nil, err
		}
		if req.Failed {
			if _, err := m.deps.DB.Pool.Exec(ctx, `UPDATE chat_executions SET status='stopped', lease_expires_at=NULL, updated_at=now()
				WHERE room_id=$1 AND source_message_id=$2 AND status='queued'`, roomID, messageID); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusOK, map[string]any{"ok": true}, nil
	})
}

func (m *Module) handleAddReaction(w http.ResponseWriter, r *http.Request) {
	var req reactionRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	messageID := chi.URLParam(r, "messageId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := m.requireWritable(ctx, roomID, acting.ID); err != nil {
			return 0, nil, err
		}
		emoji := strings.TrimSpace(req.Emoji)
		if emoji == "" || len(emoji) > 32 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid emoji")
		}
		msg, err := m.messageBelongsToRoom(ctx, m.deps.DB.Pool, roomID, messageID)
		if err != nil {
			return 0, nil, err
		}
		present := true
		if req.Present != nil {
			present = *req.Present
		}
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if present {
				if _, err := tx.Exec(ctx, `INSERT INTO chat_message_reactions (message_id, actor_id, emoji)
					VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, msg.ID, acting.ID, emoji); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(ctx, `DELETE FROM chat_message_reactions WHERE message_id=$1 AND actor_id=$2 AND emoji=$3`, msg.ID, acting.ID, emoji); err != nil {
					return err
				}
			}
			var err error
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "reaction.changed", map[string]any{"messageId": msg.ID, "emoji": emoji, "present": present})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, map[string]any{"ok": true}, nil
	})
}

func (m *Module) handleListReactions(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	messageID := chi.URLParam(r, "messageId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	if _, _, err := m.requireJoined(r.Context(), roomID, human.ID); err != nil {
		m.fail(w, err)
		return
	}
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	if _, err := m.messageBelongsToRoom(r.Context(), m.deps.DB.Pool, roomID, messageID); err != nil {
		m.fail(w, err)
		return
	}
	args := []any{messageID}
	where := `message_id=$1`
	if emoji := strings.TrimSpace(r.URL.Query().Get("emoji")); emoji != "" {
		args = append(args, emoji)
		where += ` AND emoji=$` + formatSeq(int64(len(args)))
	}
	if after := strings.TrimSpace(r.URL.Query().Get("after")); after != "" {
		if !isUUID(after) {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid after"))
			return
		}
		args = append(args, after)
		where += ` AND actor_id > $` + formatSeq(int64(len(args))) + `::uuid`
	}
	args = append(args, limit)
	rows, err := m.deps.DB.Pool.Query(r.Context(), `SELECT actor_id FROM chat_message_reactions WHERE `+where+
		` ORDER BY actor_id ASC LIMIT $`+formatSeq(int64(len(args))), args...)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var actorID string
		if err := rows.Scan(&actorID); err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, map[string]any{"actor_id": actorID})
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}
