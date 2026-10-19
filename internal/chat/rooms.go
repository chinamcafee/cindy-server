package chat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type createConversationRequest struct {
	OperationID  string   `json:"operationId"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
	SourceID     *string  `json:"sourceId"`
	ResponseMode *string  `json:"responseMode"`
	SpeakingMode *string  `json:"speakingMode"`
	History      *string  `json:"history"`
}

type patchConversationRequest struct {
	OperationID      string  `json:"operationId"`
	ExpectedRevision int     `json:"expectedRevision"`
	Name             *string `json:"name"`
	ResponseMode     *string `json:"responseMode"`
	SpeakingMode     *string `json:"speakingMode"`
	OrganizerID      *string `json:"organizerId"`
	Archived         *bool   `json:"archived"`
	Topic            *string `json:"topic"`
	Description      *string `json:"description"`
}

type dissolveRequest struct {
	OperationID      string `json:"operationId"`
	ExpectedRevision int    `json:"expectedRevision"`
}

type memberActionRequest struct {
	OperationID string  `json:"operationId"`
	ActorID     string  `json:"actorId"`
	Action      string  `json:"action"`
	Role        *string `json:"role"`
}

type botAccessRequest struct {
	OperationID      string `json:"operationId"`
	ActorID          string `json:"actorId"`
	Access           string `json:"access"`
	ExpectedRevision int    `json:"expectedRevision"`
}

type nicknameRequest struct {
	OperationID string  `json:"operationId"`
	ActorID     string  `json:"actorId"`
	Nickname    *string `json:"nickname"`
}

type transferRequest struct {
	OperationID string `json:"operationId"`
	ActorID     string `json:"actorId"`
}

type listStateRequest struct {
	OperationID      string `json:"operationId"`
	Hidden           bool   `json:"hidden"`
	ExpectedRevision int    `json:"expectedRevision"`
}

func (m *Module) handleListConversations(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	ctx := r.Context()
	args := []any{human.ID}
	where := `r.kind='group'`
	if after := strings.TrimSpace(r.URL.Query().Get("after")); after != "" {
		if !isUUID(after) {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid after"))
			return
		}
		var ts time.Time
		if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT updated_at FROM chat_rooms WHERE id=$1`, after).Scan(&ts); err != nil {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid after"))
			return
		}
		args = append(args, ts, after)
		where += ` AND (r.updated_at, r.id) < ($2, $3)`
	}
	args = append(args, limit)
	query := `SELECT ` + roomCols + `, m.state FROM chat_rooms r
		JOIN chat_room_members m ON m.room_id=r.id AND m.actor_id=$1 AND m.state='joined'
		WHERE ` + where + `
		ORDER BY r.updated_at DESC, r.id DESC LIMIT $` + formatSeq(int64(len(args)))
	rows, err := m.deps.DB.Pool.Query(ctx, query, args...)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []roomWire{}
	for rows.Next() {
		var room roomRow
		var state string
		if err := rows.Scan(&room.ID, &room.Kind, &room.OwnerAccountID, &room.Name, &room.Topic, &room.Description,
			&room.ResponseMode, &room.SpeakingMode, &room.OrganizerID, &room.Revision, &room.Head, &room.Archived,
			&room.DissolvedAt, &room.SourceID, &room.CreatedAt, &room.UpdatedAt, &state); err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, room.wire(state))
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}

func (m *Module) loadBotsForAccount(ctx context.Context, accountID string, ids []string) ([]actorRow, error) {
	out := make([]actorRow, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "duplicate participant")
		}
		seen[id] = true
		actor, err := m.loadActor(ctx, id)
		if err != nil || actor.AccountID != accountID || actor.Kind != "bot" {
			return nil, errActorNotOwned
		}
		if actor.Disabled {
			return nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "actor disabled")
		}
		out = append(out, actor)
	}
	return out, nil
}

func (m *Module) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var req createConversationRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	human, err := m.ensureHumanActor(r.Context(), accountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		kind := strings.TrimSpace(req.Kind)
		if kind != "group" && kind != "direct" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "kind must be group or direct")
		}
		responseMode := "all"
		if req.ResponseMode != nil {
			responseMode = strings.TrimSpace(*req.ResponseMode)
		}
		if responseMode != "all" && responseMode != "mentioned" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "responseMode must be all or mentioned")
		}
		speakingMode := "auto"
		if req.SpeakingMode != nil {
			speakingMode = strings.TrimSpace(*req.SpeakingMode)
		}
		if speakingMode != "auto" && speakingMode != "sequential" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "speakingMode must be auto or sequential")
		}
		name := strings.TrimSpace(req.Name)
		if kind == "group" {
			if n := utf8.RuneCountInString(name); n < 1 || n > 40 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "name must be 1..40 characters")
			}
			if len(req.Participants) < 2 || len(req.Participants) > 6 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "participants must be 2..6")
			}
		} else if len(req.Participants) != 1 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "direct room needs exactly one participant")
		}
		bots, err := m.loadBotsForAccount(ctx, accountID, req.Participants)
		if err != nil {
			return 0, nil, err
		}
		sourceID := ""
		if req.SourceID != nil {
			sourceID = strings.TrimSpace(*req.SourceID)
		}
		if kind == "direct" && sourceID == "" {
			sourceID = bots[0].ID
		}
		if kind == "direct" {
			if name == "" {
				name = bots[0].DisplayName
			}
			var existing string
			err := m.deps.DB.Pool.QueryRow(ctx, `SELECT id FROM chat_rooms WHERE owner_account_id=$1 AND kind='direct' AND source_id=$2`, accountID, sourceID).Scan(&existing)
			if err == nil {
				return http.StatusOK, map[string]any{"id": existing}, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, err
			}
		}
		var room roomRow
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var created roomRow
			row := tx.QueryRow(ctx, `
				INSERT INTO chat_rooms (kind, owner_account_id, name, response_mode, speaking_mode, organizer_id, source_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (owner_account_id, source_id) WHERE kind='direct' AND source_id IS NOT NULL DO NOTHING
				RETURNING `+roomCols, kind, accountID, name, responseMode, speakingMode, bots[0].ID, nullString(sourceID))
			created, err = scanRoom(row)
			if errors.Is(err, pgx.ErrNoRows) {
				return scanRoomInto(tx.QueryRow(ctx, `SELECT `+roomCols+` FROM chat_rooms WHERE owner_account_id=$1 AND kind='direct' AND source_id=$2`, accountID, sourceID), &room)
			}
			if err != nil {
				return err
			}
			room = created
			if _, err := tx.Exec(ctx, `INSERT INTO chat_room_members (room_id, actor_id, role, position) VALUES ($1,$2,'owner',0)`, room.ID, human.ID); err != nil {
				return err
			}
			for i, bot := range bots {
				if _, err := tx.Exec(ctx, `INSERT INTO chat_room_members (room_id, actor_id, role, position) VALUES ($1,$2,'member',$3)`, room.ID, bot.ID, i+1); err != nil {
					return err
				}
			}
			_, err := m.appendEvent(ctx, tx, room.ID, human.ID, "conversation.created", map[string]any{"kind": kind})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"id": room.ID}, nil
	})
}

func scanRoomInto(row rowScanner, dst *roomRow) error {
	r, err := scanRoom(row)
	if err != nil {
		return err
	}
	*dst = r
	return nil
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (m *Module) handlePatchConversation(w http.ResponseWriter, r *http.Request) {
	var req patchConversationRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	human, err := m.ensureHumanActor(r.Context(), accountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		room, err := m.loadRoom(ctx, roomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
			return 0, nil, err
		}
		if req.ExpectedRevision <= 0 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "expectedRevision is required")
		}
		if req.Name != nil {
			n := utf8.RuneCountInString(strings.TrimSpace(*req.Name))
			if n < 1 || n > 40 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "name must be 1..40 characters")
			}
		}
		if req.OrganizerID != nil {
			if _, err := m.loadMembership(ctx, roomID, *req.OrganizerID); err != nil {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "organizer must be a member")
			}
		}
		var updated roomRow
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				UPDATE chat_rooms SET name=COALESCE($1,name), response_mode=COALESCE($2,response_mode),
					speaking_mode=COALESCE($3,speaking_mode), organizer_id=COALESCE($4,organizer_id),
					archived=COALESCE($5,archived), topic=COALESCE($6,topic), description=COALESCE($7,description),
					revision=revision+1, updated_at=now()
				WHERE id=$8 AND revision=$9 AND dissolved_at IS NULL
				RETURNING `+roomCols, req.Name, req.ResponseMode, req.SpeakingMode, req.OrganizerID,
				req.Archived, req.Topic, req.Description, roomID, req.ExpectedRevision)
			updated, err = scanRoom(row)
			if errors.Is(err, pgx.ErrNoRows) {
				current, loadErr := m.loadRoom(ctx, roomID)
				if loadErr != nil {
					return loadErr
				}
				if current.DissolvedAt != nil {
					return errConversationDissolved
				}
				return revisionConflict(current.Revision)
			}
			if err != nil {
				return err
			}
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "conversation.updated", map[string]any{"revision": updated.Revision})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, updated.wire("joined"), nil
	})
}

func (m *Module) handleDissolveConversation(w http.ResponseWriter, r *http.Request) {
	var req dissolveRequest
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
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		room, err := m.loadRoom(ctx, roomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		if _, err := m.requireRole(ctx, roomID, acting, "owner"); err != nil {
			return 0, nil, err
		}
		if req.ExpectedRevision <= 0 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "expectedRevision is required")
		}
		var revised int
		var dissolvedAt time.Time
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `
				UPDATE chat_rooms SET dissolved_at=now(), revision=revision+1, updated_at=now()
				WHERE id=$1 AND revision=$2 AND dissolved_at IS NULL
				RETURNING revision, dissolved_at`, roomID, req.ExpectedRevision).Scan(&revised, &dissolvedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					current, loadErr := m.loadRoom(ctx, roomID)
					if loadErr != nil {
						return loadErr
					}
					if current.DissolvedAt != nil {
						return errConversationDissolved
					}
					return revisionConflict(current.Revision)
				}
				return err
			}
			var err error
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "room.dissolved", map[string]any{"revision": revised})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, map[string]any{
			"revision": revised, "dissolvedAt": dissolvedAt, "dissolvedBy": acting.ID,
		}, nil
	})
}

func (m *Module) memberViews(ctx context.Context, q querier, roomID string, limit int) ([]memberWire, error) {
	query := `
		SELECT m.actor_id, a.kind, a.display_name, m.nickname, a.avatar_url, m.state, m.role,
			m.guest_access, m.access_revision, h.id, h.display_name
		FROM chat_room_members m
		JOIN chat_actors a ON a.id=m.actor_id
		LEFT JOIN LATERAL (
			SELECT h2.id, h2.display_name FROM chat_actors h2
			WHERE h2.account_id=a.account_id AND h2.kind='human'
			ORDER BY h2.created_at ASC LIMIT 1
		) h ON a.kind='bot'
		WHERE m.room_id=$1
		ORDER BY m.position ASC, m.joined_at ASC`
	args := []any{roomID}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memberWire{}
	for rows.Next() {
		var v memberView
		if err := rows.Scan(&v.ActorID, &v.Kind, &v.DisplayName, &v.Nickname, &v.Avatar, &v.State, &v.Role,
			&v.GuestAccess, &v.AccessRevision, &v.OwnerActorID, &v.OwnerName); err != nil {
			return nil, err
		}
		out = append(out, v.wire())
	}
	return out, rows.Err()
}

func (m *Module) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	ctx := r.Context()
	tx, err := m.deps.DB.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		m.fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	room, err := func() (roomRow, error) {
		if !isUUID(roomID) {
			return roomRow{}, errConversationNotFound
		}
		return scanRoom(tx.QueryRow(ctx, `SELECT `+roomCols+` FROM chat_rooms WHERE id=$1`, roomID))
	}()
	if errors.Is(err, pgx.ErrNoRows) {
		m.fail(w, errConversationNotFound)
		return
	}
	if err != nil {
		m.fail(w, err)
		return
	}
	var mbr memberRow
	err = tx.QueryRow(ctx, `SELECT room_id, actor_id, role, state, guest_access, access_revision, nickname, position, last_read_seq, joined_at
		FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, human.ID).
		Scan(&mbr.RoomID, &mbr.ActorID, &mbr.Role, &mbr.State, &mbr.GuestAccess, &mbr.AccessRevision, &mbr.Nickname, &mbr.Position, &mbr.LastReadSeq, &mbr.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.fail(w, errConversationNotFound)
			return
		}
		m.fail(w, err)
		return
	}
	if mbr.State != "joined" {
		m.fail(w, errConversationNotFound)
		return
	}
	members, err := m.memberViews(ctx, tx, roomID, 0)
	if err != nil {
		m.fail(w, err)
		return
	}
	messages, err := m.listMessages(ctx, tx, roomID, messageQuery{limit: 100, all: true})
	if err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, map[string]any{
		"room":     room.wire(mbr.State),
		"members":  members,
		"messages": messages,
		"cursor":   formatSeq(room.Head),
		"reads": []map[string]any{{
			"threadKey": "main", "readSeq": formatSeq(mbr.LastReadSeq),
			"thread_key": "main", "read_seq": formatSeq(mbr.LastReadSeq),
		}},
	})
}

func (m *Module) handleListMembers(w http.ResponseWriter, r *http.Request) {
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
	members, err := m.memberViews(r.Context(), m.deps.DB.Pool, roomID, limit)
	if err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, members)
}

func (m *Module) memberByID(ctx context.Context, roomID, actorID string) (memberWire, error) {
	members, err := m.memberViews(ctx, m.deps.DB.Pool, roomID, 0)
	if err != nil {
		return memberWire{}, err
	}
	for _, item := range members {
		if item.ID == actorID {
			return item, nil
		}
	}
	return memberWire{}, errConversationNotFound
}

func (m *Module) countOwners(ctx context.Context, tx pgx.Tx, roomID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM chat_room_members WHERE room_id=$1 AND role='owner' AND state='joined'`, roomID).Scan(&n)
	return n, err
}

func (m *Module) cancelMemberExecutions(ctx context.Context, tx pgx.Tx, roomID string, target actorRow) error {
	if target.Kind == "bot" {
		_, err := tx.Exec(ctx, `UPDATE chat_executions SET status='stopped', lease_expires_at=NULL, epoch=epoch+1, updated_at=now()
			WHERE room_id=$1 AND bot_actor_id=$2 AND status IN ('queued','claimed','running')`, roomID, target.ID)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE chat_executions SET status='stopped', lease_expires_at=NULL, epoch=epoch+1, updated_at=now()
		WHERE room_id=$1 AND status IN ('queued','claimed','running')
		AND bot_actor_id IN (SELECT id FROM chat_actors WHERE account_id=$2 AND kind='bot')`, roomID, target.AccountID)
	return err
}

func (m *Module) handleMemberAction(w http.ResponseWriter, r *http.Request) {
	var req memberActionRequest
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
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		room, err := m.loadRoom(ctx, roomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		if room.Archived {
			return 0, nil, errConversationArchived
		}
		targetID := strings.TrimSpace(req.ActorID)
		if targetID == "" {
			targetID = acting.ID
		}
		target, err := m.loadActor(ctx, targetID)
		if err != nil {
			return 0, nil, err
		}
		action := strings.TrimSpace(req.Action)
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			switch action {
			case "invite":
				if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
					return err
				}
				if target.Kind == "bot" && target.AccountID != acting.AccountID {
					return errActorNotOwned
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO chat_room_members (room_id, actor_id, role, position, state)
					VALUES ($1,$2,'member',(SELECT COALESCE(MAX(position),0)+1 FROM chat_room_members WHERE room_id=$1),'joined')
					ON CONFLICT (room_id, actor_id) DO UPDATE SET state='joined', role='member', guest_access='none',
						access_revision=chat_room_members.access_revision+1, removed_at=NULL, joined_at=now()
					WHERE chat_room_members.state <> 'joined'`,
					roomID, targetID); err != nil {
					return err
				}
				ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "member.joined", map[string]any{"actorId": targetID})
				return err
			case "leave":
				if targetID != acting.ID && !(target.Kind == "bot" && target.AccountID == acting.AccountID) {
					return errRoleRequired
				}
				mbr, err := m.loadMembership(ctx, roomID, targetID)
				if err != nil {
					return err
				}
				if mbr.Role == "owner" {
					n, err := m.countOwners(ctx, tx, roomID)
					if err != nil {
						return err
					}
					if n <= 1 {
						return errLastOwner
					}
				}
				if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET state='left', removed_at=now(), access_revision=access_revision+1 WHERE room_id=$1 AND actor_id=$2`, roomID, targetID); err != nil {
					return err
				}
				if target.Kind == "human" {
					if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET state='left', removed_at=now(), access_revision=access_revision+1
						WHERE room_id=$1 AND actor_id IN (SELECT id FROM chat_actors WHERE account_id=$2 AND kind='bot')`, roomID, target.AccountID); err != nil {
						return err
					}
				}
				if err := m.cancelMemberExecutions(ctx, tx, roomID, target); err != nil {
					return err
				}
				ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "member.left", map[string]any{"actorId": targetID})
				return err
			case "remove", "ban":
				if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
					return err
				}
				mbr, err := m.loadMembership(ctx, roomID, targetID)
				if err != nil {
					return err
				}
				if mbr.Role == "owner" {
					return errOwnerRequired
				}
				state := "removed"
				if action == "ban" {
					state = "banned"
				}
				if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET state=$3, removed_at=now(), access_revision=access_revision+1 WHERE room_id=$1 AND actor_id=$2`, roomID, targetID, state); err != nil {
					return err
				}
				if target.Kind == "human" {
					if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET state=$3, removed_at=now(), access_revision=access_revision+1
						WHERE room_id=$1 AND actor_id IN (SELECT id FROM chat_actors WHERE account_id=$2 AND kind='bot')`, roomID, target.AccountID, state); err != nil {
						return err
					}
				}
				if err := m.cancelMemberExecutions(ctx, tx, roomID, target); err != nil {
					return err
				}
				ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "member.left", map[string]any{"actorId": targetID, "state": state})
				return err
			case "unban":
				if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET state='left', removed_at=now(), access_revision=access_revision+1
					WHERE room_id=$1 AND actor_id=$2 AND state='banned'`, roomID, targetID); err != nil {
					return err
				}
				ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "member.updated", map[string]any{"actorId": targetID, "state": "left"})
				return err
			case "role":
				role := ""
				if req.Role != nil {
					role = strings.TrimSpace(*req.Role)
				}
				if role != "owner" && role != "admin" && role != "member" && role != "guest" {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid role")
				}
				caller, err := m.requireRole(ctx, roomID, acting, "owner", "admin")
				if err != nil {
					return err
				}
				if role == "owner" && caller.Role != "owner" {
					return errOwnerRequired
				}
				mbr, err := m.loadMembership(ctx, roomID, targetID)
				if err != nil {
					return err
				}
				if mbr.State != "joined" {
					return errConversationNotFound
				}
				if mbr.Role == "owner" && role != "owner" {
					n, err := m.countOwners(ctx, tx, roomID)
					if err != nil {
						return err
					}
					if n <= 1 {
						return errLastOwner
					}
				}
				if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET role=$3 WHERE room_id=$1 AND actor_id=$2`, roomID, targetID, role); err != nil {
					return err
				}
				ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "member.updated", map[string]any{"actorId": targetID, "role": role})
				return err
			default:
				return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "unknown action")
			}
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		if action == "invite" || action == "role" || action == "unban" {
			item, err := m.memberByID(ctx, roomID, targetID)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, item, nil
		}
		return http.StatusOK, map[string]any{"ok": true}, nil
	})
}

func (m *Module) handleBotAccess(w http.ResponseWriter, r *http.Request) {
	var req botAccessRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if req.Access != "none" && req.Access != "chat" && req.Access != "tools" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid access")
		}
		room, err := m.loadRoom(ctx, roomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		if room.Archived {
			return 0, nil, errConversationArchived
		}
		bot, err := m.loadActor(ctx, req.ActorID)
		if err != nil || bot.Kind != "bot" || bot.AccountID != accountID {
			return 0, nil, errActorNotOwned
		}
		var accessRevision int
		err = m.deps.DB.Pool.QueryRow(ctx, `
			UPDATE chat_room_members SET guest_access=$1, access_revision=access_revision+1
			WHERE room_id=$2 AND actor_id=$3 AND access_revision=$4
			RETURNING access_revision`, req.Access, roomID, bot.ID, req.ExpectedRevision).Scan(&accessRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			var current int
			readErr := m.deps.DB.Pool.QueryRow(ctx, `SELECT access_revision FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, bot.ID).Scan(&current)
			if readErr != nil {
				return 0, nil, errConversationNotFound
			}
			return 0, nil, revisionConflict(current)
		}
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"accessRevision": accessRevision}, nil
	})
}

func (m *Module) handleNickname(w http.ResponseWriter, r *http.Request) {
	var req nicknameRequest
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
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := m.requireWritable(ctx, roomID, acting.ID); err != nil {
			return 0, nil, err
		}
		targetID := strings.TrimSpace(req.ActorID)
		if targetID == "" {
			targetID = acting.ID
		}
		if targetID != acting.ID {
			target, err := m.loadActor(ctx, targetID)
			if err != nil {
				return 0, nil, err
			}
			allowed := target.Kind == "bot" && target.AccountID == acting.AccountID
			if !allowed {
				if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
					return 0, nil, err
				}
			}
		}
		var nickname any
		if req.Nickname != nil {
			n := strings.TrimSpace(*req.Nickname)
			if utf8.RuneCountInString(n) > 40 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "nickname too long")
			}
			if n == "" {
				nickname = nil
			} else {
				nickname = n
			}
		}
		if _, err := m.deps.DB.Pool.Exec(ctx, `UPDATE chat_room_members SET nickname=$3 WHERE room_id=$1 AND actor_id=$2`, roomID, targetID, nickname); err != nil {
			return 0, nil, err
		}
		item, err := m.memberByID(ctx, roomID, targetID)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, item, nil
	})
}

func (m *Module) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
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
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		room, err := m.loadRoom(ctx, roomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		if room.Archived {
			return 0, nil, errConversationArchived
		}
		if _, err := m.requireRole(ctx, roomID, acting, "owner"); err != nil {
			return 0, nil, err
		}
		target, err := m.loadActor(ctx, req.ActorID)
		if err != nil || target.Kind != "human" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "target must be a human actor")
		}
		targetMember, err := m.loadMembership(ctx, roomID, target.ID)
		if err != nil || targetMember.State != "joined" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "target is not a joined member")
		}
		var revision int
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET role='admin' WHERE room_id=$1 AND actor_id=$2 AND role='owner'`, roomID, human.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE chat_room_members SET role='owner' WHERE room_id=$1 AND actor_id=$2`, roomID, target.ID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `UPDATE chat_rooms SET owner_account_id=$1, revision=revision+1, updated_at=now() WHERE id=$2 RETURNING revision`, target.AccountID, roomID).Scan(&revision); err != nil {
				return err
			}
			var err error
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "room.updated", map[string]any{"revision": revision})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, map[string]any{"revision": revision}, nil
	})
}

func (m *Module) handleSetListState(w http.ResponseWriter, r *http.Request) {
	var req listStateRequest
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
		if _, _, err := m.requireJoined(ctx, roomID, human.ID); err != nil {
			return 0, nil, err
		}
		var revision int
		var err error
		if req.ExpectedRevision == 0 {
			err = m.deps.DB.Pool.QueryRow(ctx, `
				INSERT INTO chat_list_state (room_id, actor_id, hidden, revision, updated_at)
				VALUES ($1,$2,$3,1,now())
				ON CONFLICT (room_id, actor_id) DO NOTHING
				RETURNING revision`, roomID, human.ID, req.Hidden).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				var current int
				readErr := m.deps.DB.Pool.QueryRow(ctx, `SELECT revision FROM chat_list_state WHERE room_id=$1 AND actor_id=$2`, roomID, human.ID).Scan(&current)
				if readErr != nil {
					return 0, nil, readErr
				}
				return 0, nil, revisionConflict(current)
			}
		} else {
			err = m.deps.DB.Pool.QueryRow(ctx, `
				UPDATE chat_list_state SET hidden=$3, revision=revision+1, updated_at=now()
				WHERE room_id=$1 AND actor_id=$2 AND revision=$4
				RETURNING revision`, roomID, human.ID, req.Hidden, req.ExpectedRevision).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				var current int
				readErr := m.deps.DB.Pool.QueryRow(ctx, `SELECT revision FROM chat_list_state WHERE room_id=$1 AND actor_id=$2`, roomID, human.ID).Scan(&current)
				if errors.Is(readErr, pgx.ErrNoRows) {
					current = 0
				} else if readErr != nil {
					return 0, nil, readErr
				}
				return 0, nil, revisionConflict(current)
			}
		}
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"conversationId": roomID, "hidden": req.Hidden, "revision": revision}, nil
	})
}

func (m *Module) handleGetListState(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	ctx := r.Context()
	args := []any{human.ID}
	where := ""
	if after := strings.TrimSpace(r.URL.Query().Get("after")); after != "" {
		if !isUUID(after) {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid after"))
			return
		}
		var ts time.Time
		if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT updated_at FROM chat_list_state WHERE room_id=$1 AND actor_id=$2`, after, human.ID).Scan(&ts); err != nil {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid after"))
			return
		}
		args = append(args, ts, after)
		where = ` AND (updated_at, room_id) < ($2,$3)`
	}
	args = append(args, limit)
	rows, err := m.deps.DB.Pool.Query(ctx, `SELECT room_id, hidden, revision FROM chat_list_state WHERE actor_id=$1`+where+
		` ORDER BY updated_at DESC, room_id DESC LIMIT $`+formatSeq(int64(len(args))), args...)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var roomID string
		var hidden bool
		var revision int
		if err := rows.Scan(&roomID, &hidden, &revision); err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, map[string]any{"conversationId": roomID, "hidden": hidden, "revision": revision})
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}
