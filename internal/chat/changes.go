package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type changeWire struct {
	ChangeID  string          `json:"changeId"`
	Type      string          `json:"type"`
	RoomID    string          `json:"roomId"`
	Seq       string          `json:"seq"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (m *Module) handleChanges(w http.ResponseWriter, r *http.Request) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	after, err := parseSeq(r.URL.Query().Get("after"), "after")
	if err != nil {
		m.fail(w, err)
		return
	}
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	ctx := r.Context()
	if strings.HasPrefix(scope, "actor:") {
		actorID := strings.TrimPrefix(scope, "actor:")
		if actorID != human.ID {
			m.fail(w, httpapi.Err(http.StatusForbidden, "ROLE_REQUIRED", "actor scope is limited to the caller"))
			return
		}
		var head int64
		if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(r.head),0)::bigint FROM chat_rooms r
			JOIN chat_room_members m ON m.room_id=r.id AND m.actor_id=$1 AND m.state='joined'`, human.ID).Scan(&head); err != nil {
			m.fail(w, err)
			return
		}
		httpapi.OK(w, map[string]any{"head": formatSeq(head), "changes": []changeWire{}})
		return
	}
	roomID := strings.TrimPrefix(scope, "conversation:")
	if roomID == "" {
		m.fail(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "scope is required"))
		return
	}
	room, _, err := m.requireJoined(ctx, roomID, human.ID)
	if err != nil {
		m.fail(w, err)
		return
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `SELECT seq, type, payload, created_at FROM chat_room_events
		WHERE room_id=$1 AND seq > $2 ORDER BY seq ASC LIMIT $3`, roomID, after, limit)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	changes := []changeWire{}
	for rows.Next() {
		var seq int64
		var typ string
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&seq, &typ, &payload, &createdAt); err != nil {
			m.fail(w, err)
			return
		}
		changes = append(changes, changeWire{
			ChangeID: formatSeq(seq), Type: typ, RoomID: roomID, Seq: formatSeq(seq),
			Payload: json.RawMessage(payload), CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, map[string]any{"head": formatSeq(room.Head), "changes": changes})
}
