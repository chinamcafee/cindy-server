package devicelink

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type providerShareLinkRow struct {
	ID        string
	ShareID   string
	ExpiresAt time.Time
	UsedAt    *time.Time
}

func (m *Module) findProviderShareLink(ctx context.Context, token string) (providerShareLinkRow, error) {
	var row providerShareLinkRow
	token = strings.TrimSpace(token)
	if len(token) != 43 {
		return row, pgx.ErrNoRows
	}
	err := m.db.Pool.QueryRow(ctx, `
		SELECT id::text, share_id::text, expires_at, used_at
		FROM dl_provider_share_links WHERE token_hash=$1`, hashToken(token)).
		Scan(&row.ID, &row.ShareID, &row.ExpiresAt, &row.UsedAt)
	return row, err
}

func providerShareLinkState(link providerShareLinkRow) string {
	if link.UsedAt != nil {
		return "used"
	}
	if !link.ExpiresAt.After(time.Now()) {
		return "expired"
	}
	return "unused"
}

func (m *Module) createProviderShareLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID    string `json:"providerId"`
		ProviderLabel string `json:"providerLabel"`
		DeviceName    string `json:"deviceName"`
		IdentityCard  string `json:"identityCard"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.ProviderLabel = strings.TrimSpace(req.ProviderLabel)
	if req.ProviderID == "" || len(req.ProviderID) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid providerId"))
		return
	}
	if req.ProviderLabel == "" {
		req.ProviderLabel = req.ProviderID
	}
	if len(req.ProviderLabel) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid providerLabel"))
		return
	}
	host := deviceIDFromRequest(r, "")
	if host == "" {
		host = "host"
	}
	shareID := uuid.NewString()
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_provider_shares (id, owner_account_id, provider_id, provider_label, host_device_id)
		VALUES ($1::uuid,$2::uuid,$3,$4,$5)`,
		shareID, accountID(r), req.ProviderID, req.ProviderLabel, host); err != nil {
		m.internalError(w, "create_share_link", err)
		return
	}
	token, err := newInviteToken()
	if err != nil {
		m.internalError(w, "create_share_link", err)
		return
	}
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_provider_share_links (share_id, token_hash, created_by, expires_at)
		VALUES ($1::uuid,$2,$3::uuid,$4)`, shareID, hashToken(token), accountID(r), expiresAt); err != nil {
		m.internalError(w, "create_share_link", err)
		return
	}
	m.auditEvent(r.Context(), "devicelink.provider_share.link", "provider_share", shareID, nil)
	httpapi.OK(w, map[string]any{
		"shareId": shareID, "invitation": token, "expiresAt": expiresAt.UTC(),
		"providerId": req.ProviderID, "providerLabel": req.ProviderLabel,
	})
}

func (m *Module) previewProviderShareLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Invitation string `json:"invitation"`
		Token      string `json:"token"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	token := req.Invitation
	if token == "" {
		token = req.Token
	}
	link, err := m.findProviderShareLink(r.Context(), token)
	if err != nil {
		m.notFound(w)
		return
	}
	share, err := m.loadProviderShare(r.Context(), link.ShareID)
	if err != nil {
		m.notFound(w)
		return
	}
	ownerName, ownerAvatar, ownerRegion := m.accountPerson(r.Context(), share.OwnerAccountID)
	deviceName := "Cindy device"
	_ = m.db.Pool.QueryRow(r.Context(), `SELECT name FROM dl_devices WHERE id=$1`, share.HostDeviceID).Scan(&deviceName)
	if deviceName == "" {
		deviceName = "Cindy device"
	}
	httpapi.OK(w, map[string]any{
		"shareId": share.ID, "providerId": share.ProviderID, "providerLabel": share.ProviderLabel,
		"deviceName": deviceName,
		"owner":      map[string]any{"displayName": ownerName, "avatarUrl": ownerAvatar, "region": ownerRegion},
		"state":      providerShareLinkState(link), "expiresAt": link.ExpiresAt.UTC(),
	})
}

func (m *Module) createProviderShareRequestByLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Invitation   string `json:"invitation"`
		IdentityCard string `json:"identityCard"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	link, err := m.findProviderShareLink(r.Context(), req.Invitation)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "share link not found"))
		return
	}
	if state := providerShareLinkState(link); state != "unused" {
		code := "PROVIDER_SHARE_LINK_USED"
		if state == "expired" {
			code = "PROVIDER_SHARE_LINK_EXPIRED"
		}
		httpapi.WriteError(w, httpapi.Err(http.StatusGone, code, "share link is not available"))
		return
	}
	share, err := m.loadProviderShare(r.Context(), link.ShareID)
	if err != nil || share.Status != "active" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "NOT_FOUND", "share is not active"))
		return
	}
	account := accountID(r)
	if share.OwnerAccountID == account {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "PROVIDER_SHARE_SELF", "owner cannot request own share"))
		return
	}
	var memberExists bool
	_ = m.db.Pool.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM dl_provider_share_members
			WHERE share_id=$1::uuid AND account_id=$2::uuid AND status IN ('active','paused'))`,
		share.ID, account).Scan(&memberExists)
	if memberExists {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "PROVIDER_SHARE_ALREADY_MEMBER", "already a member"))
		return
	}
	code, err := newPairingCode()
	if err != nil {
		m.internalError(w, "share_request", err)
		return
	}
	requestID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(providerShareRequestTTL)
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_provider_share_requests (id, share_id, account_id, pairing_code, expires_at)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5)`, requestID, share.ID, account, code, expiresAt); err != nil {
		m.internalError(w, "share_request", err)
		return
	}
	_, _ = m.db.Pool.Exec(r.Context(), `UPDATE dl_provider_share_links SET used_at=now() WHERE id=$1::uuid`, link.ID)
	httpapi.OK(w, map[string]any{
		"requestId": requestID, "shareId": share.ID, "status": "pending",
		"pairingCode": code, "expiresAt": expiresAt.UTC(),
	})
}

func (m *Module) providerShareRequestState(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(chi.URLParam(r, "requestId"))
	if !validUUID(requestID) {
		m.notFound(w)
		return
	}
	account := accountID(r)
	var shareID, requestAccount, status, pairingCode string
	var expiresAt time.Time
	err := m.db.Pool.QueryRow(r.Context(), `
		SELECT share_id::text, account_id::text, status, pairing_code, expires_at
		FROM dl_provider_share_requests WHERE id=$1::uuid`, requestID).
		Scan(&shareID, &requestAccount, &status, &pairingCode, &expiresAt)
	if err != nil {
		m.notFound(w)
		return
	}
	var owner string
	_ = m.db.Pool.QueryRow(r.Context(), `SELECT owner_account_id::text FROM dl_provider_shares WHERE id=$1::uuid`, shareID).Scan(&owner)
	if account != requestAccount && account != owner {
		m.notFound(w)
		return
	}
	res := map[string]any{
		"requestId": requestID, "status": status, "pairingCode": pairingCode,
		"expiresAt": expiresAt.UTC(), "shareId": shareID,
	}
	if status == "approved" {
		var memberID string
		if err := m.db.Pool.QueryRow(r.Context(), `
			SELECT id::text FROM dl_provider_share_members
			WHERE share_id=$1::uuid AND account_id=$2::uuid AND status IN ('active','paused')
			ORDER BY created_at DESC LIMIT 1`, shareID, requestAccount).Scan(&memberID); err == nil {
			res["memberId"] = memberID
		}
	}
	httpapi.OK(w, res)
}

func (m *Module) withdrawProviderShareRequest(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(chi.URLParam(r, "requestId"))
	if !validUUID(requestID) {
		m.notFound(w)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_share_requests SET status='withdrawn', decided_at=now()
		WHERE id=$1::uuid AND account_id=$2::uuid AND status='pending'`, requestID, accountID(r))
	if err != nil {
		m.internalError(w, "withdraw_share_request", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	httpapi.OK(w, map[string]any{"requestId": requestID, "status": "withdrawn"})
}

func (m *Module) decideProviderShareRequestAlias(w http.ResponseWriter, r *http.Request, approve bool) {
	requestID := strings.TrimSpace(chi.URLParam(r, "requestId"))
	if !validUUID(requestID) {
		m.notFound(w)
		return
	}
	var shareID string
	if err := m.db.Pool.QueryRow(r.Context(), `SELECT share_id::text FROM dl_provider_share_requests WHERE id=$1::uuid`, requestID).Scan(&shareID); err != nil {
		m.notFound(w)
		return
	}
	m.decideProviderShareRequestByID(w, r, shareID, requestID, approve)
}

func (m *Module) setProviderShareMemberState(w http.ResponseWriter, r *http.Request) {
	memberID := strings.TrimSpace(chi.URLParam(r, "memberId"))
	action := strings.TrimSpace(chi.URLParam(r, "action"))
	if !validUUID(memberID) || (action != "pause" && action != "resume" && action != "remove") {
		m.notFound(w)
		return
	}
	var shareID string
	if err := m.db.Pool.QueryRow(r.Context(), `
		SELECT share_id::text FROM dl_provider_share_members WHERE id=$1::uuid`, memberID).Scan(&shareID); err != nil {
		m.notFound(w)
		return
	}
	var owner string
	_ = m.db.Pool.QueryRow(r.Context(), `SELECT owner_account_id::text FROM dl_provider_shares WHERE id=$1::uuid`, shareID).Scan(&owner)
	if owner != accountID(r) {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	status := "active"
	switch action {
	case "pause":
		status = "paused"
	case "remove":
		status = "revoked"
	}
	if _, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_share_members SET status=$2, version=version+1 WHERE id=$1::uuid`, memberID, status); err != nil {
		m.internalError(w, "share_member_state", err)
		return
	}
	if action == "remove" {
		status = "removed"
	}
	httpapi.OK(w, map[string]any{"memberId": memberID, "status": status})
}

func (m *Module) leaveReceivedProviderShare(w http.ResponseWriter, r *http.Request) {
	memberID := strings.TrimSpace(chi.URLParam(r, "memberId"))
	if !validUUID(memberID) {
		m.notFound(w)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_provider_share_members SET status='revoked', version=version+1
		WHERE id=$1::uuid AND account_id=$2::uuid AND status IN ('active','paused')`, memberID, accountID(r))
	if err != nil {
		m.internalError(w, "leave_share", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	httpapi.OK(w, map[string]any{"memberId": memberID, "status": "left"})
}

var _ = errors.Is
