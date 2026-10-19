package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const executionLeaseSeconds = 60

var failureCodes = map[string]bool{
	"IMAGE_INPUT_UNSUPPORTED": true, "MODEL_UNAVAILABLE": true, "AUTH_REQUIRED": true,
	"RATE_LIMITED": true, "UPSTREAM_OVERLOADED": true, "QUOTA_EXCEEDED": true,
	"NETWORK_ERROR": true, "RUNTIME_TIMEOUT": true, "RUNTIME_ERROR": true,
}

type claimRequest struct {
	OperationID         string `json:"operationId"`
	ExecutorID          string `json:"executorId"`
	AccessPolicyVersion int    `json:"accessPolicyVersion"`
	PlanVersion         int    `json:"planVersion"`
	DirectRoomVersion   *int   `json:"directRoomVersion"`
	MemberJoinedVersion *int   `json:"memberJoinedVersion"`
}

type executionActionRequest struct {
	OperationID        string            `json:"operationId"`
	ExecutorID         string            `json:"executorId"`
	Epoch              int               `json:"epoch"`
	Action             string            `json:"action"`
	Content            []json.RawMessage `json:"content"`
	ContinueDiscussion bool              `json:"continueDiscussion"`
	FailureCode        *string           `json:"failureCode"`
	Detail             *string           `json:"detail"`
}

type executionControlRequest struct {
	OperationID string `json:"operationId"`
	Action      string `json:"action"`
}

// The desktop identifies the running bot through X-Chat-Actor; executorId is
// its device-local executor id, not an actor id. Accept the body form too for
// clients that still put the bot actor there.
func (m *Module) executionBot(r *http.Request, executorID string) (actorRow, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Chat-Actor"))
	if raw == "" {
		raw = strings.TrimSpace(executorID)
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	bot, err := m.loadActor(r.Context(), raw)
	if err != nil || bot.Kind != "bot" || bot.AccountID != accountID {
		return actorRow{}, errActorNotOwned
	}
	return bot, nil
}

func (m *Module) handleClaimExecution(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if strings.TrimSpace(req.OperationID) == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "operationId is required"))
		return
	}
	bot, err := m.executionBot(r, req.ExecutorID)
	if err != nil || bot.Disabled {
		m.fail(w, errActorNotOwned)
		return
	}
	ctx := r.Context()
	var execution *executionWire
	var events []roomEvent
	err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "exec:"+bot.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE chat_executions SET status='failed', failure_code='RUNTIME_TIMEOUT', lease_expires_at=NULL, updated_at=now()
			WHERE bot_actor_id=$1 AND status IN ('claimed','running') AND lease_expires_at IS NOT NULL AND lease_expires_at < now()`, bot.ID); err != nil {
			return err
		}
		var active string
		err := tx.QueryRow(ctx, `SELECT id FROM chat_executions WHERE bot_actor_id=$1 AND status IN ('claimed','running') LIMIT 1`, bot.ID).Scan(&active)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var execID string
		err = tx.QueryRow(ctx, `
			SELECT e.id FROM chat_executions e
			JOIN chat_rooms r ON r.id=e.room_id
			JOIN chat_room_members m ON m.room_id=e.room_id AND m.actor_id=e.bot_actor_id AND m.state='joined'
			WHERE e.bot_actor_id=$1 AND e.status='queued' AND r.dissolved_at IS NULL AND r.archived=false
			ORDER BY e.created_at ASC LIMIT 1 FOR UPDATE OF e SKIP LOCKED`, bot.ID).Scan(&execID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chat_executions e SET status='claimed', epoch=e.epoch+1, lease_expires_at=now()+make_interval(secs => $2),
				context_seq=r.head, updated_at=now()
			FROM chat_rooms r WHERE e.id=$1 AND r.id=e.room_id`, execID, executionLeaseSeconds); err != nil {
			return err
		}
		row, err := scanExecution(tx.QueryRow(ctx, `SELECT `+executionCols+` FROM chat_executions WHERE id=$1`, execID))
		if err != nil {
			return err
		}
		wire := row.wire()
		execution = &wire
		ev, err := m.appendEvent(ctx, tx, row.RoomID, bot.ID, "execution.changed", map[string]any{
			"status": "claimed", "botId": bot.ID, "executionId": row.ID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		m.fail(w, err)
		return
	}
	m.publishEvents(events...)
	httpapi.OK(w, map[string]any{"execution": execution})
}

func (m *Module) handleExecutionAction(w http.ResponseWriter, r *http.Request) {
	var req executionActionRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	executionID := chi.URLParam(r, "executionId")
	ctx := r.Context()
	bot, err := m.executionBot(r, req.ExecutorID)
	if err != nil {
		m.fail(w, errActorNotOwned)
		return
	}
	var status string
	var events []roomEvent
	err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var exec executionRow
		if !isUUID(executionID) {
			return errConversationNotFound
		}
		exec, err = scanExecution(tx.QueryRow(ctx, `SELECT `+executionCols+` FROM chat_executions WHERE id=$1 FOR UPDATE`, executionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpapi.Err(http.StatusNotFound, "NOT_FOUND", "execution not found")
		}
		if err != nil {
			return err
		}
		if exec.RoomID != roomID || exec.BotActorID != bot.ID {
			return httpapi.Err(http.StatusNotFound, "NOT_FOUND", "execution not found")
		}
		if req.Epoch != exec.Epoch {
			return revisionConflict(exec.Epoch)
		}
		switch req.Action {
		case "heartbeat":
			if exec.Status != "claimed" && exec.Status != "running" {
				return revisionConflict(exec.Epoch)
			}
			if _, err := tx.Exec(ctx, `UPDATE chat_executions SET status='running', lease_expires_at=now()+make_interval(secs => $2), updated_at=now() WHERE id=$1`, exec.ID, executionLeaseSeconds); err != nil {
				return err
			}
			status = "running"
			return nil
		case "complete":
			if exec.Status == "completed" {
				status = "completed"
				return nil
			}
			if exec.Status != "claimed" && exec.Status != "running" {
				return revisionConflict(exec.Epoch)
			}
			var messageID *string
			if len(req.Content) > 0 {
				var memberState string
				if err := tx.QueryRow(ctx, `SELECT state FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, bot.ID).Scan(&memberState); err != nil || memberState != "joined" {
					return revisionConflict(exec.Epoch)
				}
				key := "chat:" + exec.ID + ":" + strconv.Itoa(exec.Epoch) + ":complete"
				var existing string
				err := tx.QueryRow(ctx, `SELECT id FROM chat_messages WHERE room_id=$1 AND client_message_id=$2`, roomID, key).Scan(&existing)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					messageID = &existing
				} else {
					msg, ev, err := m.insertMessageTx(ctx, tx, newMessage{
						RoomID: roomID, AuthorID: bot.ID, Content: req.Content, ClientID: &key,
					})
					if err != nil {
						return err
					}
					messageID = &msg.ID
					events = append(events, ev)
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE chat_executions SET status='completed', failure_code=NULL, lease_expires_at=NULL, updated_at=now() WHERE id=$1`, exec.ID); err != nil {
				return err
			}
			payload := map[string]any{"status": "completed", "botId": bot.ID, "executionId": exec.ID}
			if messageID != nil {
				payload["messageId"] = *messageID
			}
			ev, err := m.appendEvent(ctx, tx, roomID, bot.ID, "execution.changed", payload)
			if err != nil {
				return err
			}
			events = append(events, ev)
			status = "completed"
			return nil
		case "fail":
			if exec.Status == "failed" {
				status = "failed"
				return nil
			}
			if exec.Status != "claimed" && exec.Status != "running" {
				return revisionConflict(exec.Epoch)
			}
			code := normalizeFailureCode(req.FailureCode, req.Detail)
			detail := ""
			if req.Detail != nil {
				detail = *req.Detail
				if len(detail) > 512 {
					detail = detail[:512]
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE chat_executions SET status='failed', failure_code=$2, error=$3::jsonb, lease_expires_at=NULL, updated_at=now() WHERE id=$1`,
				exec.ID, code, mapJSON(map[string]any{"detail": detail})); err != nil {
				return err
			}
			ev, err := m.appendEvent(ctx, tx, roomID, bot.ID, "execution.changed", map[string]any{
				"status": "failed", "botId": bot.ID, "executionId": exec.ID, "failureCode": code,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
			status = "failed"
			return nil
		default:
			return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid action")
		}
	})
	if err != nil {
		m.fail(w, err)
		return
	}
	m.publishEvents(events...)
	httpapi.OK(w, map[string]any{"status": status})
}

func normalizeFailureCode(code *string, detail *string) string {
	candidate := ""
	if code != nil {
		candidate = strings.TrimSpace(*code)
	}
	if candidate == "" && detail != nil {
		candidate = strings.TrimSpace(strings.TrimPrefix(*detail, "cindy-runtime-error:"))
	}
	candidate = strings.ToUpper(candidate)
	if failureCodes[candidate] {
		return candidate
	}
	return "RUNTIME_ERROR"
}

func (m *Module) handleExecutionControl(w http.ResponseWriter, r *http.Request) {
	var req executionControlRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	executionID := chi.URLParam(r, "executionId")
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
		if req.Action != "stop" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid action")
		}
		if _, _, err := m.requireJoined(ctx, roomID, acting.ID); err != nil {
			return 0, nil, err
		}
		var botID string
		err = m.deps.DB.Pool.QueryRow(ctx, `SELECT bot_actor_id FROM chat_executions WHERE id=$1 AND room_id=$2`, executionID, roomID).Scan(&botID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "execution not found")
		}
		if err != nil {
			return 0, nil, err
		}
		bot, err := m.loadActor(ctx, botID)
		if err != nil {
			return 0, nil, err
		}
		if bot.AccountID != acting.AccountID {
			if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
				return 0, nil, err
			}
		}
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE chat_executions SET status='stopped', lease_expires_at=NULL, updated_at=now()
				WHERE id=$1 AND status IN ('queued','claimed','running')`, executionID); err != nil {
				return err
			}
			var err error
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "execution.changed", map[string]any{
				"status": "stopped", "botId": botID, "executionId": executionID,
			})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, map[string]any{"status": "stopped"}, nil
	})
}

func (m *Module) handleListExecutions(w http.ResponseWriter, r *http.Request) {
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
	rows, err := m.deps.DB.Pool.Query(r.Context(), `SELECT `+executionCols+` FROM chat_executions
		WHERE room_id=$1 ORDER BY created_at DESC LIMIT $2`, roomID, limit)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []executionWire{}
	for rows.Next() {
		exec, err := scanExecution(rows)
		if err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, exec.wire())
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}

type executionFailureWire struct {
	ID              string    `json:"id"`
	ConversationID  string    `json:"conversation_id"`
	SourceMessageID *string   `json:"source_message_id"`
	BotID           string    `json:"bot_id"`
	Epoch           int       `json:"epoch"`
	Status          string    `json:"status"`
	FailureCode     *string   `json:"failure_code"`
	PlanID          *string   `json:"plan_id"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (m *Module) handleExecutionFailures(w http.ResponseWriter, r *http.Request) {
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
	raw := strings.TrimSpace(r.URL.Query().Get("sourceIds"))
	if raw == "" {
		httpapi.OK(w, []executionFailureWire{})
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 100 {
		m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "too many sourceIds"))
		return
	}
	ids := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !isUUID(part) {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid sourceIds"))
			return
		}
		ids = append(ids, part)
	}
	rows, err := m.deps.DB.Pool.Query(r.Context(), `SELECT id, room_id, source_message_id, bot_actor_id, epoch, status, failure_code, plan_id, updated_at
		FROM chat_executions WHERE room_id=$1 AND status='failed' AND source_message_id = ANY($2::uuid[])`, roomID, ids)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []executionFailureWire{}
	for rows.Next() {
		var item executionFailureWire
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.SourceMessageID, &item.BotID, &item.Epoch,
			&item.Status, &item.FailureCode, &item.PlanID, &item.UpdatedAt); err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}
