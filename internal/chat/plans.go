package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type planStepInput struct {
	BotID  string  `json:"botId"`
	Task   string  `json:"task"`
	Status *string `json:"status"`
}

type createPlanRequest struct {
	OperationID     string          `json:"operationId"`
	SourceMessageID *string         `json:"sourceMessageId"`
	OrganizerID     *string         `json:"organizerId"`
	Request         string          `json:"request"`
	Steps           []planStepInput `json:"steps"`
	SourceID        *string         `json:"sourceId"`
	Status          *string         `json:"status"`
	CurrentStep     *int            `json:"currentStep"`
}

type planActionRequest struct {
	OperationID      string  `json:"operationId"`
	ExpectedRevision int     `json:"expectedRevision"`
	Action           string  `json:"action"`
	Position         *int    `json:"position"`
	BotID            *string `json:"botId"`
}

type planMessageRequest struct {
	OperationID string            `json:"operationId"`
	Content     []json.RawMessage `json:"content"`
}

func (m *Module) loadPlanSteps(ctx context.Context, q querier, planIDs []string) (map[string][]planStepWire, error) {
	out := map[string][]planStepWire{}
	if len(planIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT s.plan_id, s.id, s.position, s.bot_actor_id, COALESCE(a.display_name,''),
		       s.task, s.status, s.result_message_id
		FROM chat_plan_steps s LEFT JOIN chat_actors a ON a.id = s.bot_actor_id
		WHERE s.plan_id = ANY($1::uuid[]) ORDER BY s.plan_id, s.position`, planIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var planID string
		var step planStepWire
		if err := rows.Scan(&planID, &step.ID, &step.Position, &step.BotID, &step.BotName, &step.Task, &step.Status, &step.ResultMessageID); err != nil {
			return nil, err
		}
		out[planID] = append(out[planID], step)
	}
	return out, rows.Err()
}

func (m *Module) handleListPlans(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	args := []any{roomID}
	where := `room_id=$1`
	if raw := strings.TrimSpace(r.URL.Query().Get("ids")); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) > 100 {
			m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "too many ids"))
			return
		}
		ids := []string{}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !isUUID(part) {
				m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid ids"))
				return
			}
			ids = append(ids, part)
		}
		args = append(args, ids)
		where += ` AND id = ANY($2::uuid[])`
	}
	args = append(args, limit)
	rows, err := m.deps.DB.Pool.Query(ctx, `SELECT `+planCols+` FROM chat_plans WHERE `+where+
		` ORDER BY created_at DESC LIMIT $`+formatSeq(int64(len(args))), args...)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	plans := []planRow{}
	ids := []string{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			m.fail(w, err)
			return
		}
		plans = append(plans, p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	steps, err := m.loadPlanSteps(ctx, m.deps.DB.Pool, ids)
	if err != nil {
		m.fail(w, err)
		return
	}
	out := []planWire{}
	for _, p := range plans {
		out = append(out, p.wire(steps[p.ID]))
	}
	httpapi.OK(w, out)
}

func (m *Module) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
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
		if len(req.Steps) < 1 || len(req.Steps) > 6 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "steps must be 1..6")
		}
		for i := range req.Steps {
			req.Steps[i].BotID = strings.TrimSpace(req.Steps[i].BotID)
			req.Steps[i].Task = strings.TrimSpace(req.Steps[i].Task)
			if req.Steps[i].BotID == "" || req.Steps[i].Task == "" {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "step botId and task are required")
			}
			if _, err := m.loadMembership(ctx, roomID, req.Steps[i].BotID); err != nil {
				return 0, nil, err
			}
		}
		if req.SourceMessageID != nil {
			if _, err := m.messageBelongsToRoom(ctx, m.deps.DB.Pool, roomID, *req.SourceMessageID); err != nil {
				return 0, nil, err
			}
		}
		var open int
		if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM chat_plans WHERE room_id=$1 AND status IN ('proposed','pending','running','waiting')`, roomID).Scan(&open); err != nil {
			return 0, nil, err
		}
		if open > 0 {
			return 0, nil, errPlanOpen
		}
		status := "pending"
		if req.Status != nil && strings.TrimSpace(*req.Status) != "" {
			status = strings.TrimSpace(*req.Status)
		}
		var plan planRow
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				INSERT INTO chat_plans (room_id, source_message_id, request_text, organizer_id, creator_id, status, current_step)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				RETURNING `+planCols, roomID, req.SourceMessageID, strings.TrimSpace(req.Request),
				req.OrganizerID, acting.ID, status, req.CurrentStep)
			plan, err = scanPlan(row)
			if err != nil {
				return err
			}
			for i, step := range req.Steps {
				stepStatus := "pending"
				if step.Status != nil && strings.TrimSpace(*step.Status) != "" {
					stepStatus = strings.TrimSpace(*step.Status)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO chat_plan_steps (plan_id, position, bot_actor_id, task, status)
					VALUES ($1,$2,$3,$4,$5)`, plan.ID, i+1, step.BotID, step.Task, stepStatus); err != nil {
					return err
				}
			}
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "plan.updated", map[string]any{"planId": plan.ID})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		steps, err := m.loadPlanSteps(ctx, m.deps.DB.Pool, []string{plan.ID})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, plan.wire(steps[plan.ID]), nil
	})
}

func (m *Module) handlePlanAction(w http.ResponseWriter, r *http.Request) {
	var req planActionRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	planID := chi.URLParam(r, "planId")
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
		var plan planRow
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			plan, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM chat_plans WHERE id=$1 AND room_id=$2 FOR UPDATE`, planID, roomID))
			if errors.Is(err, pgx.ErrNoRows) {
				return httpapi.Err(http.StatusNotFound, "NOT_FOUND", "plan not found")
			}
			if err != nil {
				return err
			}
			if plan.Revision != req.ExpectedRevision {
				return revisionConflict(plan.Revision)
			}
			if plan.Status == "done" || plan.Status == "dismissed" || plan.Status == "stopped" {
				return errPlanClosed
			}
			switch req.Action {
			case "start":
				if _, err := tx.Exec(ctx, `UPDATE chat_plans SET status='running', current_step=COALESCE(current_step,1) WHERE id=$1`, planID); err != nil {
					return err
				}
			case "dismiss", "stop":
				final := "dismissed"
				if req.Action == "stop" {
					final = "stopped"
				}
				if _, err := tx.Exec(ctx, `UPDATE chat_plans SET status=$2 WHERE id=$1`, planID, final); err != nil {
					return err
				}
			case "continue":
				if _, err := tx.Exec(ctx, `UPDATE chat_plans SET status='running', current_step=COALESCE(current_step,0)+1 WHERE id=$1`, planID); err != nil {
					return err
				}
			case "retry", "reassign", "remove":
				if req.Position == nil {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "position is required")
				}
				if req.Action == "remove" {
					if _, err := tx.Exec(ctx, `DELETE FROM chat_plan_steps WHERE plan_id=$1 AND position=$2`, planID, *req.Position); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE chat_plan_steps SET position=position-1 WHERE plan_id=$1 AND position>$2`, planID, *req.Position); err != nil {
						return err
					}
				} else if req.Action == "retry" {
					if _, err := tx.Exec(ctx, `UPDATE chat_plan_steps SET status='pending' WHERE plan_id=$1 AND position=$2`, planID, *req.Position); err != nil {
						return err
					}
				} else {
					if req.BotID == nil || strings.TrimSpace(*req.BotID) == "" {
						return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "botId is required")
					}
					if _, err := m.loadMembership(ctx, roomID, strings.TrimSpace(*req.BotID)); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE chat_plan_steps SET bot_actor_id=$3, status='pending' WHERE plan_id=$1 AND position=$2`,
						planID, *req.Position, strings.TrimSpace(*req.BotID)); err != nil {
						return err
					}
				}
			default:
				return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "unknown action")
			}
			err = tx.QueryRow(ctx, `UPDATE chat_plans SET revision=revision+1, updated_at=now() WHERE id=$1 RETURNING `+planCols, planID).Scan(
				&plan.ID, &plan.RoomID, &plan.SourceMessage, &plan.RequestText, &plan.OrganizerID, &plan.CreatorID,
				&plan.Status, &plan.CurrentStep, &plan.Revision, &plan.CreatedAt, &plan.UpdatedAt)
			if err != nil {
				return err
			}
			ev, err = m.appendEvent(ctx, tx, roomID, acting.ID, "plan.updated", map[string]any{"planId": planID, "status": plan.Status})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		steps, err := m.loadPlanSteps(ctx, m.deps.DB.Pool, []string{plan.ID})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, plan.wire(steps[plan.ID]), nil
	})
}

func (m *Module) handlePlanMessage(w http.ResponseWriter, r *http.Request) {
	var req planMessageRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	planID := chi.URLParam(r, "planId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if len(req.Content) == 0 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "content is required")
		}
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := m.requireWritable(ctx, roomID, acting.ID); err != nil {
			return 0, nil, err
		}
		plan, err := scanPlan(m.deps.DB.Pool.QueryRow(ctx, `SELECT `+planCols+` FROM chat_plans WHERE id=$1 AND room_id=$2`, planID, roomID))
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "plan not found")
		}
		if err != nil {
			return 0, nil, err
		}
		if plan.Status == "done" || plan.Status == "dismissed" || plan.Status == "stopped" {
			return 0, nil, errPlanClosed
		}
		clientID := req.OperationID
		var msg messageRow
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			msg, ev, err = m.insertMessageTx(ctx, tx, newMessage{
				RoomID: roomID, AuthorID: acting.ID, Content: req.Content, ClientID: &clientID,
			})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		loaded, err := m.messageBelongsToRoom(ctx, m.deps.DB.Pool, roomID, msg.ID)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, loaded.wire(), nil
	})
}
