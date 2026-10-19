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

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type importMessage struct {
	SourceID  string            `json:"sourceId"`
	AuthorID  string            `json:"authorId"`
	CreatedAt string            `json:"createdAt"`
	Content   []json.RawMessage `json:"content"`
	Mentions  []string          `json:"mentions"`
}

type importRequest struct {
	OperationID string          `json:"operationId"`
	Messages    []importMessage `json:"messages"`
}

// handleImportProgress exposes the last source id persisted for a room so the
// client can resume a local-history upgrade after a crash or restart.
func (m *Module) handleImportProgress(w http.ResponseWriter, r *http.Request) {
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
	var last string
	err = m.deps.DB.Pool.QueryRow(r.Context(), `SELECT last_source_id FROM chat_import_progress WHERE room_id=$1`, roomID).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.OK(w, map[string]any{"sourceId": nil})
		return
	}
	if err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, map[string]any{"sourceId": last})
}

func (m *Module) handleImportMessages(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	var req importRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if strings.TrimSpace(req.OperationID) == "" || len(req.Messages) == 0 || len(req.Messages) > 50 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid import batch"))
		return
	}
	if _, _, err := m.requireJoined(r.Context(), roomID, human.ID); err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		imported := 0
		lastSourceID := ""
		var events []roomEvent
		err := m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			for _, msg := range req.Messages {
				sourceID := strings.TrimSpace(msg.SourceID)
				authorID := strings.TrimSpace(msg.AuthorID)
				if sourceID == "" || len(sourceID) > 256 || !isUUID(authorID) || len(msg.Content) == 0 || len(msg.Content) > 32 {
					return httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid import message")
				}
				var memberState string
				if err := tx.QueryRow(ctx, `SELECT state FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, roomID, authorID).Scan(&memberState); err != nil || memberState != "joined" {
					return httpapi.Err(http.StatusBadRequest, "IMPORT_AUTHOR_UNAVAILABLE", "import author is not a joined member")
				}
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_messages WHERE room_id=$1 AND source_id=$2)`, roomID, sourceID).Scan(&exists); err != nil {
					return err
				}
				if exists {
					lastSourceID = sourceID
					continue
				}
				createdAt := time.Now().UTC()
				if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(msg.CreatedAt)); err == nil {
					createdAt = parsed.UTC()
				}
				clientID := sourceID
				_, ev, err := m.insertMessageTx(ctx, tx, newMessage{
					RoomID: roomID, AuthorID: authorID, Content: msg.Content,
					SourceID: &sourceID, ClientID: &clientID, CreatedAt: &createdAt,
				})
				if err != nil {
					return err
				}
				events = append(events, ev)
				imported++
				lastSourceID = sourceID
			}
			if lastSourceID == "" {
				return nil
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO chat_import_progress (room_id, last_source_id, updated_at)
				VALUES ($1,$2,now())
				ON CONFLICT (room_id) DO UPDATE SET last_source_id=EXCLUDED.last_source_id, updated_at=now()`,
				roomID, lastSourceID)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(events...)
		return http.StatusOK, map[string]any{"status": "ok", "imported": imported}, nil
	})
}

// handleImportPlanSource creates (or reuses) the server message that anchors a
// locally imported plan, so the subsequent plan creation can reference it.
func (m *Module) handleImportPlanSource(w http.ResponseWriter, r *http.Request) {
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
	sourceID := strings.TrimSpace(r.URL.Query().Get("sourceId"))
	planID := strings.TrimSpace(r.URL.Query().Get("planId"))
	if sourceID == "" || len(sourceID) > 200 || planID == "" || len(planID) > 200 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "sourceId and planId are required"))
		return
	}
	anchor := "plan-source:" + sourceID + ":" + planID
	var existing string
	err = m.deps.DB.Pool.QueryRow(r.Context(), `
		SELECT id::text FROM chat_messages WHERE room_id=$1 AND source_id=$2`, roomID, anchor).Scan(&existing)
	if err == nil {
		httpapi.OK(w, map[string]any{"messageId": existing})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		m.fail(w, err)
		return
	}
	content := []json.RawMessage{json.RawMessage(`{"type":"text","text":"Imported plan"}`)}
	var messageID string
	err = m.deps.DB.WithTx(r.Context(), func(tx pgx.Tx) error {
		clientID := anchor
		msg, ev, err := m.insertMessageTx(r.Context(), tx, newMessage{
			RoomID: roomID, AuthorID: human.ID, Content: content, SourceID: &anchor, ClientID: &clientID,
		})
		if err != nil {
			return err
		}
		messageID = msg.ID
		m.publishEvents(ev)
		return nil
	})
	if err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, map[string]any{"messageId": messageID})
}
