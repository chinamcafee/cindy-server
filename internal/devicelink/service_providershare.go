package devicelink

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const providerShareRequestTTL = 24 * time.Hour

type providerShareRow struct {
	ID             string
	OwnerAccountID string
	ProviderID     string
	ProviderLabel  string
	HostDeviceID   string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type providerShareMemberView struct {
	MemberID    string    `json:"memberId"`
	AccountID   string    `json:"accountId"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl"`
	Region      string    `json:"region"`
	Status      string    `json:"status"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	JoinedAt    string    `json:"joinedAt"`
}

type providerShareRequestView struct {
	RequestID   string     `json:"requestId"`
	AccountID   string     `json:"accountId"`
	DisplayName string     `json:"displayName"`
	AvatarURL   *string    `json:"avatarUrl"`
	Region      string     `json:"region"`
	LinkID      *string    `json:"linkId,omitempty"`
	Status      string     `json:"status"`
	PairingCode string     `json:"pairingCode"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
}

type providerShareView struct {
	ShareID       string                     `json:"shareId"`
	ProviderID    string                     `json:"providerId"`
	ProviderLabel string                     `json:"providerLabel"`
	HostDeviceID  string                     `json:"hostDeviceId"`
	Status        string                     `json:"status"`
	CreatedAt     time.Time                  `json:"createdAt"`
	UpdatedAt     time.Time                  `json:"updatedAt"`
	Role          string                     `json:"role"`
	MemberID      string                     `json:"memberId,omitempty"`
	Members       []providerShareMemberView  `json:"members"`
	Requests      []providerShareRequestView `json:"requests"`
}

type receivedProviderShareView struct {
	ShareID       string `json:"shareId"`
	MemberID      string `json:"memberId"`
	ProviderID    string `json:"providerId"`
	ProviderLabel string `json:"providerLabel"`
	HostDeviceID  string `json:"hostDeviceId"`
	DeviceName    string `json:"deviceName,omitempty"`
	Status        string `json:"status"`
	HostOnline    bool   `json:"hostOnline"`
	HostCapable   bool   `json:"hostCapable"`
	Owner         struct {
		AccountID   string  `json:"accountId"`
		DisplayName string  `json:"displayName"`
		AvatarURL   *string `json:"avatarUrl"`
		Region      string  `json:"region"`
	} `json:"owner"`
}

func (m *Module) loadProviderShare(ctx context.Context, id string) (*providerShareRow, error) {
	row := &providerShareRow{}
	err := m.db.Pool.QueryRow(ctx, `
		SELECT id::text, owner_account_id::text, provider_id, provider_label, host_device_id,
		       status, created_at, updated_at
		FROM dl_provider_shares WHERE id = $1::uuid`, id).
		Scan(&row.ID, &row.OwnerAccountID, &row.ProviderID, &row.ProviderLabel,
			&row.HostDeviceID, &row.Status, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// providerShareRole mirrors sharedTaskRole for provider shares. Only active
// members are considered so paused guests cannot relay.
func (m *Module) providerShareRole(ctx context.Context, shareID, account string) (role, memberID, hostDeviceID, status string, err error) {
	if !validUUID(shareID) {
		return "", "", "", "", pgx.ErrNoRows
	}
	var owner string
	err = m.db.Pool.QueryRow(ctx,
		`SELECT owner_account_id::text, host_device_id, status FROM dl_provider_shares WHERE id = $1::uuid`, shareID).
		Scan(&owner, &hostDeviceID, &status)
	if err != nil {
		return "", "", "", "", err
	}
	if owner == account {
		return "host", account, hostDeviceID, status, nil
	}
	var guestMember string
	err = m.db.Pool.QueryRow(ctx, `
		SELECT id::text FROM dl_provider_share_members
		WHERE share_id = $1::uuid AND account_id = $2::uuid AND status = 'active'
		ORDER BY created_at DESC LIMIT 1`, shareID, account).Scan(&guestMember)
	if err == nil {
		return "guest", guestMember, hostDeviceID, status, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", hostDeviceID, status, nil
	}
	return "", "", "", "", err
}

func (m *Module) providerShareMemberActive(ctx context.Context, shareID, memberID string) (bool, error) {
	if !validUUID(shareID) {
		return false, nil
	}
	var exists bool
	err := m.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM dl_provider_share_members
			WHERE share_id = $1::uuid AND member_id::text = $2 AND status = 'active'
		)`, shareID, memberID).Scan(&exists)
	return exists, err
}

func (m *Module) loadProviderShareMembers(ctx context.Context, shareID string) ([]providerShareMemberView, error) {
	rows, err := m.db.Pool.Query(ctx, `
		SELECT id::text, account_id::text, COALESCE(display_name, ''), status, version, created_at
		FROM dl_provider_share_members
		WHERE share_id = $1::uuid AND status IN ('active','paused')
		ORDER BY created_at`, shareID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]providerShareMemberView, 0, 4)
	for rows.Next() {
		var member providerShareMemberView
		if err := rows.Scan(&member.MemberID, &member.AccountID, &member.DisplayName,
			&member.Status, &member.Version, &member.CreatedAt); err != nil {
			return nil, err
		}
		member.Region = m.region()
		member.JoinedAt = member.CreatedAt.UTC().Format(time.RFC3339Nano)
		out = append(out, member)
	}
	return out, rows.Err()
}

func (m *Module) loadProviderShareRequests(ctx context.Context, shareID string) ([]providerShareRequestView, error) {
	rows, err := m.db.Pool.Query(ctx, `
		SELECT id::text, account_id::text, status, pairing_code, created_at, expires_at, decided_at
		FROM dl_provider_share_requests
		WHERE share_id = $1::uuid
		ORDER BY created_at DESC`, shareID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]providerShareRequestView, 0, 4)
	for rows.Next() {
		var req providerShareRequestView
		if err := rows.Scan(&req.RequestID, &req.AccountID, &req.Status, &req.PairingCode,
			&req.CreatedAt, &req.ExpiresAt, &req.DecidedAt); err != nil {
			return nil, err
		}
		req.DisplayName, req.AvatarURL, req.Region = m.accountPerson(ctx, req.AccountID)
		out = append(out, req)
	}
	return out, rows.Err()
}

func (m *Module) buildProviderShareView(ctx context.Context, row *providerShareRow, role, memberID string, full bool) (*providerShareView, error) {
	view := &providerShareView{
		ShareID:       row.ID,
		ProviderID:    row.ProviderID,
		ProviderLabel: row.ProviderLabel,
		HostDeviceID:  row.HostDeviceID,
		Status:        row.Status,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		Role:          role,
		MemberID:      memberID,
		Members:       []providerShareMemberView{},
		Requests:      []providerShareRequestView{},
	}
	if full {
		members, err := m.loadProviderShareMembers(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		requests, err := m.loadProviderShareRequests(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		view.Members = members
		view.Requests = requests
	}
	return view, nil
}

func shareIDParam(r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	return id, validUUID(id)
}

func (m *Module) listProviderShares(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT id::text, owner_account_id::text, provider_id, provider_label, host_device_id,
		       status, created_at, updated_at
		FROM dl_provider_shares
		WHERE owner_account_id = $1::uuid
		ORDER BY created_at DESC`, accountID(r))
	if err != nil {
		m.internalError(w, "list_provider_shares", err)
		return
	}
	defer rows.Close()

	out := make([]providerShareView, 0, 8)
	for rows.Next() {
		var row providerShareRow
		if err := rows.Scan(&row.ID, &row.OwnerAccountID, &row.ProviderID, &row.ProviderLabel,
			&row.HostDeviceID, &row.Status, &row.CreatedAt, &row.UpdatedAt); err != nil {
			m.internalError(w, "scan_provider_share", err)
			return
		}
		view, err := m.buildProviderShareView(r.Context(), &row, "host", row.OwnerAccountID, true)
		if err != nil {
			m.internalError(w, "build_provider_share", err)
			return
		}
		out = append(out, *view)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, "list_provider_shares", err)
		return
	}
	httpapi.OK(w, map[string]any{"shares": out})
}

func (m *Module) createProviderShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID    string `json:"providerId"`
		ProviderLabel string `json:"providerLabel"`
		HostDeviceID  string `json:"hostDeviceId"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.ProviderLabel = strings.TrimSpace(req.ProviderLabel)
	req.HostDeviceID = strings.TrimSpace(req.HostDeviceID)
	if req.ProviderID == "" || len(req.ProviderID) > 128 || len(req.ProviderLabel) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid provider id"))
		return
	}
	if req.ProviderLabel == "" {
		req.ProviderLabel = req.ProviderID
	}
	if req.HostDeviceID == "" {
		req.HostDeviceID = deviceIDFromRequest(r, "")
	}
	if len(req.HostDeviceID) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid hostDeviceId"))
		return
	}

	shareID := uuid.NewString()
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_provider_shares (id, owner_account_id, provider_id, provider_label, host_device_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
		shareID, accountID(r), req.ProviderID, req.ProviderLabel, req.HostDeviceID); err != nil {
		m.internalError(w, "create_provider_share", err)
		return
	}
	row, err := m.loadProviderShare(r.Context(), shareID)
	if err != nil {
		m.internalError(w, "create_provider_share", err)
		return
	}
	view, err := m.buildProviderShareView(r.Context(), row, "host", accountID(r), true)
	if err != nil {
		m.internalError(w, "create_provider_share", err)
		return
	}
	m.auditEvent(r.Context(), "devicelink.provider_share.create", "provider_share", shareID, nil)
	httpapi.Created(w, view)
}

func (m *Module) getProviderShare(w http.ResponseWriter, r *http.Request) {
	id, ok := shareIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	account := accountID(r)
	row, err := m.loadProviderShare(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "get_provider_share", err)
		return
	}
	role, memberID := "host", account
	if row.OwnerAccountID != account {
		if row.Status == "revoked" {
			m.notFound(w)
			return
		}
		role, memberID, _, _, err = m.providerShareRole(r.Context(), id, account)
		if err != nil || role == "" {
			m.notFound(w)
			return
		}
	}
	full := row.OwnerAccountID == account
	view, err := m.buildProviderShareView(r.Context(), row, role, memberID, full)
	if err != nil {
		m.internalError(w, "get_provider_share", err)
		return
	}
	httpapi.OK(w, view)
}

func (m *Module) updateProviderShare(w http.ResponseWriter, r *http.Request) {
	id, ok := shareIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	var req struct {
		Status        string `json:"status"`
		ProviderLabel string `json:"providerLabel"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Status = strings.TrimSpace(req.Status)
	req.ProviderLabel = strings.TrimSpace(req.ProviderLabel)
	if req.Status != "" && req.Status != "active" && req.Status != "paused" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "status must be active or paused"))
		return
	}
	if len(req.ProviderLabel) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid providerLabel"))
		return
	}

	row, err := m.loadProviderShare(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "update_provider_share", err)
		return
	}
	if row.OwnerAccountID != accountID(r) {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_shares
		SET status = CASE WHEN $3 = '' THEN status ELSE $3 END,
		    provider_label = CASE WHEN $4 = '' THEN provider_label ELSE $4 END,
		    updated_at = now()
		WHERE id = $1::uuid AND owner_account_id = $2::uuid`,
		id, accountID(r), req.Status, req.ProviderLabel)
	if err != nil {
		m.internalError(w, "update_provider_share", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	row, err = m.loadProviderShare(r.Context(), id)
	if err != nil {
		m.internalError(w, "update_provider_share", err)
		return
	}
	view, err := m.buildProviderShareView(r.Context(), row, "host", accountID(r), true)
	if err != nil {
		m.internalError(w, "update_provider_share", err)
		return
	}
	httpapi.OK(w, view)
}

func (m *Module) deleteProviderShare(w http.ResponseWriter, r *http.Request) {
	id, ok := shareIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_shares SET status = 'revoked', updated_at = now()
		WHERE id = $1::uuid AND owner_account_id = $2::uuid AND status <> 'revoked'`,
		id, accountID(r))
	if err != nil {
		m.internalError(w, "delete_provider_share", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	m.auditEvent(r.Context(), "devicelink.provider_share.revoke", "provider_share", id, nil)
	httpapi.OK(w, map[string]any{"status": "revoked", "shareId": id})
}

func (m *Module) listReceivedProviderShares(w http.ResponseWriter, r *http.Request) {
	account := accountID(r)
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT s.id::text, s.provider_id, s.provider_label, s.host_device_id, s.status,
		       s.owner_account_id::text, m.id::text, m.created_at
		FROM dl_provider_share_members m
		JOIN dl_provider_shares s ON s.id = m.share_id
		WHERE m.account_id = $1::uuid AND m.status = 'active'
		  AND s.status IN ('active', 'paused')
		ORDER BY m.created_at DESC`, account)
	if err != nil {
		m.internalError(w, "list_received_shares", err)
		return
	}
	defer rows.Close()

	out := make([]receivedProviderShareView, 0, 8)
	for rows.Next() {
		var (
			item      receivedProviderShareView
			owner     string
			createdAt time.Time
		)
		if err := rows.Scan(&item.ShareID, &item.ProviderID, &item.ProviderLabel,
			&item.HostDeviceID, &item.Status, &owner, &item.MemberID, &createdAt); err != nil {
			m.internalError(w, "scan_received_share", err)
			return
		}
		item.Owner.AccountID = owner
		item.Owner.DisplayName, item.Owner.AvatarURL, item.Owner.Region = m.accountPerson(r.Context(), owner)

		var deviceName string
		err := m.db.Pool.QueryRow(r.Context(),
			`SELECT name FROM dl_devices WHERE id = $1`, item.HostDeviceID).Scan(&deviceName)
		if err == nil {
			item.DeviceName = deviceName
		} else if errors.Is(err, pgx.ErrNoRows) {
			item.DeviceName = "Cindy device"
		} else if !errors.Is(err, pgx.ErrNoRows) {
			m.internalError(w, "received_share_device", err)
			return
		}
		presence := m.getPresence(r.Context(), owner)
		if entry, ok := presence[item.HostDeviceID]; ok {
			item.HostOnline = entry.Online
			item.HostCapable = entry.RemoteControlEnabled
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, "list_received_shares", err)
		return
	}
	httpapi.OK(w, map[string]any{"shares": out})
}

func (m *Module) createProviderShareRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := shareIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	var req struct {
		PairingCode string `json:"pairingCode"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
	}
	account := accountID(r)
	row, err := m.loadProviderShare(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "create_share_request", err)
		return
	}
	if row.Status != "active" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARE_UNAVAILABLE", "provider share is not active"))
		return
	}
	if row.OwnerAccountID == account {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "owner cannot request access"))
		return
	}
	var memberExists bool
	if err := m.db.Pool.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM dl_provider_share_members
			WHERE share_id = $1::uuid AND account_id = $2::uuid AND status = 'active'
		)`, id, account).Scan(&memberExists); err != nil {
		m.internalError(w, "create_share_request", err)
		return
	}
	if memberExists {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "ALREADY_MEMBER", "already a member"))
		return
	}

	code, err := newPairingCode()
	if err != nil {
		m.internalError(w, "create_share_request", err)
		return
	}
	requestID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(providerShareRequestTTL)
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_provider_share_requests (id, share_id, account_id, pairing_code, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)`,
		requestID, id, account, code, expiresAt); err != nil {
		m.internalError(w, "create_share_request", err)
		return
	}
	httpapi.Created(w, map[string]any{
		"requestId":   requestID,
		"shareId":     id,
		"status":      "pending",
		"pairingCode": code,
		"expiresAt":   expiresAt,
	})
}

func (m *Module) approveProviderShareRequest(w http.ResponseWriter, r *http.Request) {
	m.decideProviderShareRequest(w, r, true)
}

func (m *Module) rejectProviderShareRequest(w http.ResponseWriter, r *http.Request) {
	m.decideProviderShareRequest(w, r, false)
}

func (m *Module) decideProviderShareRequest(w http.ResponseWriter, r *http.Request, approve bool) {
	id, ok := shareIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	requestID := strings.TrimSpace(chi.URLParam(r, "requestId"))
	if !validUUID(requestID) {
		m.notFound(w)
		return
	}
	m.decideProviderShareRequestByID(w, r, id, requestID, approve)
}

func (m *Module) decideProviderShareRequestByID(w http.ResponseWriter, r *http.Request, id, requestID string, approve bool) {
	account := accountID(r)
	row, err := m.loadProviderShare(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "decide_share_request", err)
		return
	}
	if row.OwnerAccountID != account {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}

	var (
		requestAccount string
		status         string
		expiresAt      time.Time
	)
	err = m.db.Pool.QueryRow(r.Context(), `
		SELECT account_id::text, status, expires_at
		FROM dl_provider_share_requests
		WHERE id = $1::uuid AND share_id = $2::uuid`, requestID, id).
		Scan(&requestAccount, &status, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "decide_share_request", err)
		return
	}
	if status != "pending" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "REQUEST_NOT_PENDING", "request was already decided"))
		return
	}
	if !expiresAt.After(time.Now()) {
		_, _ = m.db.Pool.Exec(r.Context(), `
			UPDATE dl_provider_share_requests SET status = 'expired', decided_at = now()
			WHERE id = $1::uuid AND status = 'pending'`, requestID)
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "REQUEST_EXPIRED", "request expired"))
		return
	}

	if !approve {
		if _, err := m.db.Pool.Exec(r.Context(), `
			UPDATE dl_provider_share_requests SET status = 'rejected', decided_at = now()
			WHERE id = $1::uuid AND status = 'pending'`, requestID); err != nil {
			m.internalError(w, "reject_share_request", err)
			return
		}
		httpapi.OK(w, map[string]any{"requestId": requestID, "status": "rejected"})
		return
	}
	if row.Status != "active" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARE_UNAVAILABLE", "provider share is not active"))
		return
	}

	var memberID string
	err = m.db.Pool.QueryRow(r.Context(), `
		SELECT id::text FROM dl_provider_share_members
		WHERE share_id = $1::uuid AND account_id = $2::uuid
		ORDER BY created_at DESC LIMIT 1`, id, requestAccount).Scan(&memberID)
	switch {
	case err == nil:
		if _, err := m.db.Pool.Exec(r.Context(), `
			UPDATE dl_provider_share_members
			SET status = 'active', version = version + 1
			WHERE id = $1::uuid`, memberID); err != nil {
			m.internalError(w, "approve_share_request", err)
			return
		}
	case errors.Is(err, pgx.ErrNoRows):
		memberID = uuid.NewString()
		if _, err := m.db.Pool.Exec(r.Context(), `
			INSERT INTO dl_provider_share_members (id, share_id, member_id, account_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'active')`,
			memberID, id, memberID, requestAccount); err != nil {
			m.internalError(w, "approve_share_request", err)
			return
		}
	default:
		m.internalError(w, "approve_share_request", err)
		return
	}

	if _, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_share_requests SET status = 'approved', decided_at = now()
		WHERE id = $1::uuid AND status = 'pending'`, requestID); err != nil {
		m.internalError(w, "approve_share_request", err)
		return
	}
	httpapi.OK(w, map[string]any{
		"requestId": requestID,
		"shareId":   id,
		"status":    "approved",
		"memberId":  memberID,
	})
}

func (m *Module) providerShareCrossRegion(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteError(w, httpapi.Err(http.StatusNotImplemented, "NOT_IMPLEMENTED", "cross-region provider sharing is not enabled"))
}

func newPairingCode() (string, error) {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", int(binary.BigEndian.Uint16(buf[:]))%10000), nil
}
