package chat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const inviteTTL = 14 * 24 * time.Hour

type inviteAcceptRequest struct {
	OperationID string `json:"operationId"`
}

func (m *Module) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	var req struct {
		OperationID string `json:"operationId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
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
		if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
			return 0, nil, err
		}
		token, err := newInviteToken()
		if err != nil {
			return 0, nil, err
		}
		expiresAt := time.Now().UTC().Add(inviteTTL)
		var inviteID string
		if err := m.deps.DB.Pool.QueryRow(ctx, `INSERT INTO chat_invitations (room_id, token_hash, created_by, expires_at)
			VALUES ($1,$2,$3,$4) RETURNING id`, roomID, hashToken(token), acting.ID, expiresAt).Scan(&inviteID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"id": inviteID, "token": token, "expiresAt": expiresAt, "reusable": false}, nil
	})
}

func (m *Module) handleDeleteInvite(w http.ResponseWriter, r *http.Request) {
	inviteID := chi.URLParam(r, "inviteId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	operationID := strings.TrimSpace(r.URL.Query().Get("operationId"))
	if operationID == "" {
		var req struct {
			OperationID string `json:"operationId"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err == nil {
			operationID = req.OperationID
		}
	}
	m.idemDo(w, r, operationID, inviteID, func(ctx context.Context) (int, any, error) {
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		var roomID string
		err = m.deps.DB.Pool.QueryRow(ctx, `SELECT room_id FROM chat_invitations WHERE id=$1`, inviteID).Scan(&roomID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "invite not found")
		}
		if err != nil {
			return 0, nil, err
		}
		if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
			return 0, nil, err
		}
		if _, err := m.deps.DB.Pool.Exec(ctx, `DELETE FROM chat_invitations WHERE id=$1`, inviteID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"revoked": true}, nil
	})
}

type inviteRow struct {
	ID        string
	RoomID    string
	CreatedBy string
	ExpiresAt time.Time
	UsedAt    *time.Time
}

func (m *Module) findInvite(ctx context.Context, token string) (inviteRow, error) {
	var inv inviteRow
	err := m.deps.DB.Pool.QueryRow(ctx, `SELECT id, room_id, created_by, expires_at, used_at FROM chat_invitations WHERE token_hash=$1`, hashToken(token)).
		Scan(&inv.ID, &inv.RoomID, &inv.CreatedBy, &inv.ExpiresAt, &inv.UsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return inviteRow{}, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "invite not found")
	}
	return inv, err
}

func (m *Module) inviteStatusError(inv inviteRow) error {
	if inv.UsedAt != nil || time.Now().UTC().After(inv.ExpiresAt) {
		return httpapi.Err(http.StatusGone, "INVITE_EXPIRED", "invite expired or already used")
	}
	return nil
}

func (m *Module) handlePreviewInvite(w http.ResponseWriter, r *http.Request) {
	m.previewInvite(w, r, chi.URLParam(r, "token"))
}

func (m *Module) handlePreviewInviteLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.previewInvite(w, r, strings.TrimSpace(req.Token))
}

func (m *Module) previewInvite(w http.ResponseWriter, r *http.Request, token string) {
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	ctx := r.Context()
	inv, err := m.findInvite(ctx, token)
	if err != nil {
		m.fail(w, err)
		return
	}
	if err := m.inviteStatusError(inv); err != nil {
		m.fail(w, err)
		return
	}
	var name string
	var dissolved *time.Time
	if err := m.deps.DB.Pool.QueryRow(ctx, `SELECT name, dissolved_at FROM chat_rooms WHERE id=$1`, inv.RoomID).Scan(&name, &dissolved); err != nil {
		m.fail(w, err)
		return
	}
	if dissolved != nil {
		m.fail(w, errConversationDissolved)
		return
	}
	var inviterName string
	_ = m.deps.DB.Pool.QueryRow(ctx, `SELECT display_name FROM chat_actors WHERE id=$1`, inv.CreatedBy).Scan(&inviterName)
	var state string
	err = m.deps.DB.Pool.QueryRow(ctx, `SELECT state FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, inv.RoomID, human.ID).Scan(&state)
	joined := err == nil && state == "joined"
	httpapi.OK(w, map[string]any{
		"groupId":     inv.RoomID,
		"roomId":      inv.RoomID,
		"name":        name,
		"inviterName": inviterName,
		"expiresAt":   inv.ExpiresAt,
		"reusable":    false,
		"joined":      joined,
	})
}

func (m *Module) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req inviteAcceptRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.acceptInvite(w, r, chi.URLParam(r, "token"), req)
}

func (m *Module) handleAcceptInviteLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		OperationID string `json:"operationId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.acceptInvite(w, r, strings.TrimSpace(req.Token), inviteAcceptRequest{OperationID: req.OperationID})
}

func (m *Module) acceptInvite(w http.ResponseWriter, r *http.Request, token string, req inviteAcceptRequest) {
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		inv, err := m.findInvite(ctx, token)
		if err != nil {
			return 0, nil, err
		}
		room, err := m.loadRoom(ctx, inv.RoomID)
		if err != nil {
			return 0, nil, err
		}
		if room.DissolvedAt != nil {
			return 0, nil, errConversationDissolved
		}
		var state string
		err = m.deps.DB.Pool.QueryRow(ctx, `SELECT state FROM chat_room_members WHERE room_id=$1 AND actor_id=$2`, inv.RoomID, human.ID).Scan(&state)
		if err == nil && state == "joined" {
			return http.StatusOK, map[string]any{"groupId": inv.RoomID}, nil
		}
		if err := m.inviteStatusError(inv); err != nil {
			return 0, nil, err
		}
		var ev roomEvent
		err = m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var usedRoom string
			err := tx.QueryRow(ctx, `UPDATE chat_invitations SET used_at=now() WHERE id=$1 AND used_at IS NULL RETURNING room_id`, inv.ID).Scan(&usedRoom)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpapi.Err(http.StatusGone, "INVITE_EXPIRED", "invite already used")
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO chat_room_members (room_id, actor_id, role, position, state)
				VALUES ($1,$2,'member',(SELECT COALESCE(MAX(position),0)+1 FROM chat_room_members WHERE room_id=$1),'joined')
				ON CONFLICT (room_id, actor_id) DO UPDATE SET state='joined', role='member', guest_access='none',
					access_revision=chat_room_members.access_revision+1, removed_at=NULL, joined_at=now()`,
				inv.RoomID, human.ID); err != nil {
				return err
			}
			ev, err = m.appendEvent(ctx, tx, inv.RoomID, human.ID, "member.joined", map[string]any{"actorId": human.ID})
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		m.publishEvents(ev)
		return http.StatusOK, map[string]any{"groupId": inv.RoomID}, nil
	})
}

func (m *Module) handleRevokeInviteLink(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "roomId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	var req struct {
		Token       string `json:"token"`
		OperationID string `json:"operationId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		if _, err := m.requireRole(ctx, roomID, acting, "owner", "admin"); err != nil {
			return 0, nil, err
		}
		inv, err := m.findInvite(ctx, strings.TrimSpace(req.Token))
		if err != nil {
			return 0, nil, err
		}
		if inv.RoomID != roomID {
			return 0, nil, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "invite not found")
		}
		if _, err := m.deps.DB.Pool.Exec(ctx, `DELETE FROM chat_invitations WHERE id=$1`, inv.ID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"revoked": true}, nil
	})
}
