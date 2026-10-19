package admin

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

type accountListItem struct {
	AccountID    string     `json:"accountId"`
	MembershipID string     `json:"membershipId"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"displayName"`
	Kind         string     `json:"kind"`
	Role         string     `json:"role"`
	OrgName      string     `json:"orgName"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

type adminAccount struct {
	ID          string     `json:"id"`
	Region      string     `json:"region"`
	Status      string     `json:"status"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	DeletedAt   *time.Time `json:"deletedAt,omitempty"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

type accountMembership struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Role        string    `json:"role"`
	DisplayName string    `json:"displayName"`
	Email       string    `json:"email"`
	OrgID       string    `json:"orgId"`
	OrgName     string    `json:"orgName"`
	OrgLogoURL  string    `json:"orgLogoUrl"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

type accountIdentity struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	Identifier string     `json:"identifier"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type accountSubscription struct {
	ID                 string     `json:"id"`
	PlanSlug           string     `json:"planSlug"`
	PlanName           string     `json:"planName"`
	Status             string     `json:"status"`
	PriceMinor         int64      `json:"priceMinor"`
	Currency           string     `json:"currency"`
	Interval           string     `json:"interval"`
	CurrentPeriodStart *time.Time `json:"currentPeriodStart,omitempty"`
	CurrentPeriodEnd   *time.Time `json:"currentPeriodEnd,omitempty"`
}

type usageByModel struct {
	ModelID      string `json:"modelId"`
	ModelKey     string `json:"modelKey"`
	DisplayName  string `json:"displayName"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CostMinor    int64  `json:"costMinor"`
	Requests     int64  `json:"requests"`
}

type usageSummary struct {
	WindowDays   int            `json:"windowDays"`
	InputTokens  int64          `json:"inputTokens"`
	OutputTokens int64          `json:"outputTokens"`
	CostMinor    int64          `json:"costMinor"`
	Requests     int64          `json:"requests"`
	ByModel      []usageByModel `json:"byModel"`
}

func (m *Module) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	like := "%" + q + "%"
	p := parsePagination(r)

	const where = `
		WHERE a.status <> 'deleted'
		  AND ($1 = '' OR a.id::text = $1
		       OR EXISTS (SELECT 1 FROM auth_memberships m WHERE m.account_id = a.id
		                  AND (m.display_name ILIKE $2 OR COALESCE(m.org_name,'') ILIKE $2))
		       OR EXISTS (SELECT 1 FROM auth_identities i WHERE i.account_id = a.id
		                  AND i.type IN ('email','phone') AND i.identifier::text ILIKE $2))
		  AND ($3 = '' OR a.status = $3)`

	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM auth_accounts a`+where, q, like, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}

	rows, err := m.pool.Query(ctx, `
		SELECT a.id::text, COALESCE(pm.id::text,''), COALESCE(em.identifier::text,''),
		       COALESCE(pm.display_name,''), COALESCE(pm.kind,''), COALESCE(pm.role,''),
		       COALESCE(pm.org_name,''), a.status, a.created_at,
		       (SELECT max(rs.created_at) FROM auth_refresh_sessions rs WHERE rs.account_id = a.id)
		FROM auth_accounts a
		LEFT JOIN LATERAL (
			SELECT m.id, m.display_name, m.kind, m.role, m.org_name
			FROM auth_memberships m
			WHERE m.account_id = a.id
			ORDER BY (m.kind = 'personal') DESC, m.created_at
			LIMIT 1
		) pm ON true
		LEFT JOIN LATERAL (
			SELECT i.identifier
			FROM auth_identities i
			WHERE i.account_id = a.id AND i.type IN ('email','phone')
			ORDER BY (i.type = 'email') DESC, i.verified_at DESC NULLS LAST, i.created_at
			LIMIT 1
		) em ON true
		`+where+`
		ORDER BY a.created_at DESC
		LIMIT $4 OFFSET $5`, q, like, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]accountListItem, 0, p.PageSize)
	for rows.Next() {
		var it accountListItem
		if err := rows.Scan(&it.AccountID, &it.MembershipID, &it.Email, &it.DisplayName,
			&it.Kind, &it.Role, &it.OrgName, &it.Status, &it.CreatedAt, &it.LastLoginAt); err != nil {
			m.fail(w, r, err)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

func (m *Module) resolveAccountID(ctx context.Context, id string) (string, error) {
	if !validUUID(id) {
		return "", pgx.ErrNoRows
	}
	var accountID string
	err := m.pool.QueryRow(ctx, `SELECT id::text FROM auth_accounts WHERE id = $1::uuid`, id).Scan(&accountID)
	if err == nil {
		return accountID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = m.pool.QueryRow(ctx, `SELECT account_id::text FROM auth_memberships WHERE id = $1::uuid`, id).Scan(&accountID)
	if err == nil {
		return accountID, nil
	}
	return "", err
}

func (m *Module) loadAccount(ctx context.Context, accountID string) (*adminAccount, error) {
	var a adminAccount
	err := m.pool.QueryRow(ctx, `
		SELECT a.id::text, a.region, a.status,
		       COALESCE(em.identifier::text,''), COALESCE(pm.display_name,''),
		       a.created_at, a.updated_at, a.deleted_at,
		       (SELECT max(rs.created_at) FROM auth_refresh_sessions rs WHERE rs.account_id = a.id)
		FROM auth_accounts a
		LEFT JOIN LATERAL (
			SELECT m.display_name FROM auth_memberships m
			WHERE m.account_id = a.id
			ORDER BY (m.kind = 'personal') DESC, m.created_at
			LIMIT 1
		) pm ON true
		LEFT JOIN LATERAL (
			SELECT i.identifier FROM auth_identities i
			WHERE i.account_id = a.id AND i.type = 'email'
			ORDER BY i.verified_at DESC NULLS LAST, i.created_at
			LIMIT 1
		) em ON true
		WHERE a.id = $1::uuid`, accountID).
		Scan(&a.ID, &a.Region, &a.Status, &a.Email, &a.DisplayName, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt, &a.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (m *Module) loadAccountMemberships(ctx context.Context, accountID string) ([]accountMembership, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, kind, role, display_name, COALESCE(email::text,''),
		       COALESCE(org_id::text,''), COALESCE(org_name,''), COALESCE(org_logo_url,''),
		       status, created_at
		FROM auth_memberships
		WHERE account_id = $1::uuid
		ORDER BY (kind = 'personal') DESC, created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]accountMembership, 0)
	for rows.Next() {
		var it accountMembership
		if err := rows.Scan(&it.ID, &it.Kind, &it.Role, &it.DisplayName, &it.Email,
			&it.OrgID, &it.OrgName, &it.OrgLogoURL, &it.Status, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) loadAccountIdentities(ctx context.Context, accountID string) ([]accountIdentity, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, type, identifier::text, verified_at, created_at
		FROM auth_identities
		WHERE account_id = $1::uuid
		ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]accountIdentity, 0)
	for rows.Next() {
		var it accountIdentity
		if err := rows.Scan(&it.ID, &it.Type, &it.Identifier, &it.VerifiedAt, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) loadAccountDevices(ctx context.Context, accountID string) ([]deviceItem, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id, account_id::text, name, COALESCE(platform,''), COALESCE(app_version,''),
		       remote_control_enabled, busy, last_seen_at, removed_at, created_at
		FROM dl_devices
		WHERE account_id = $1::uuid
		ORDER BY last_seen_at DESC NULLS LAST, created_at DESC
		LIMIT 200`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]deviceItem, 0)
	ids := make([]string, 0)
	for rows.Next() {
		var it deviceItem
		if err := rows.Scan(&it.ID, &it.AccountID, &it.Name, &it.Platform, &it.AppVersion,
			&it.RemoteControlEnabled, &it.Busy, &it.LastSeenAt, &it.RemovedAt, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	online := m.onlineDevices(ctx, ids)
	for i := range items {
		items[i].Online = online[items[i].ID]
	}
	return items, nil
}

func (m *Module) loadAccountSubscription(ctx context.Context, accountID string) (*accountSubscription, error) {
	var s accountSubscription
	err := m.pool.QueryRow(ctx, `
		SELECT s.id::text, p.slug, p.name, s.status, p.price_minor, p.currency, p.interval,
		       s.current_period_start, s.current_period_end
		FROM ma_subscriptions s
		JOIN ma_plans p ON p.id = s.plan_id
		WHERE s.account_id = $1::uuid
		ORDER BY (s.status IN ('trialing','active','past_due')) DESC, s.created_at DESC
		LIMIT 1`, accountID).
		Scan(&s.ID, &s.PlanSlug, &s.PlanName, &s.Status, &s.PriceMinor, &s.Currency, &s.Interval,
			&s.CurrentPeriodStart, &s.CurrentPeriodEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *Module) loadAccountUsage(ctx context.Context, accountID string) (*usageSummary, error) {
	u := usageSummary{WindowDays: 30, ByModel: make([]usageByModel, 0)}
	if err := m.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(input_tokens),0), COALESCE(sum(output_tokens),0),
		       COALESCE(sum(cost_minor),0), count(*)
		FROM ma_usage_events
		WHERE account_id = $1::uuid AND occurred_at >= now() - interval '30 days'`, accountID).
		Scan(&u.InputTokens, &u.OutputTokens, &u.CostMinor, &u.Requests); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT COALESCE(u.model_id::text,''), COALESCE(mm.model_key,''), COALESCE(mm.display_name,'unknown'),
		       COALESCE(sum(u.input_tokens),0), COALESCE(sum(u.output_tokens),0),
		       COALESCE(sum(u.cost_minor),0), count(*)
		FROM ma_usage_events u
		LEFT JOIN ma_models mm ON mm.id = u.model_id
		WHERE u.account_id = $1::uuid AND u.occurred_at >= now() - interval '30 days'
		GROUP BY u.model_id, mm.model_key, mm.display_name
		ORDER BY COALESCE(sum(u.cost_minor),0) DESC
		LIMIT 20`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it usageByModel
		if err := rows.Scan(&it.ModelID, &it.ModelKey, &it.DisplayName,
			&it.InputTokens, &it.OutputTokens, &it.CostMinor, &it.Requests); err != nil {
			return nil, err
		}
		u.ByModel = append(u.ByModel, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &u, nil
}

func (m *Module) accountDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := m.resolveAccountID(ctx, chi.URLParam(r, "id"))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	account, err := m.loadAccount(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	memberships, err := m.loadAccountMemberships(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	identities, err := m.loadAccountIdentities(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	devices, err := m.loadAccountDevices(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	subscription, err := m.loadAccountSubscription(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	usage, err := m.loadAccountUsage(ctx, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, map[string]any{
		"account":      account,
		"memberships":  memberships,
		"identities":   identities,
		"devices":      devices,
		"subscription": subscription,
		"usage":        usage,
	})
}

func (m *Module) banAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		m.badRequest(w, "REASON_REQUIRED", "reason is required")
		return
	}
	m.setAccountStatus(w, r, "suspended", "account.ban", req.Reason)
}

func (m *Module) unbanAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.setAccountStatus(w, r, "active", "account.unban", strings.TrimSpace(req.Reason))
}

func (m *Module) setAccountStatus(w http.ResponseWriter, r *http.Request, newStatus, action, reason string) {
	ctx := r.Context()
	accountID, err := m.resolveAccountID(ctx, chi.URLParam(r, "id"))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before string
	if err := tx.QueryRow(ctx, `SELECT status FROM auth_accounts WHERE id = $1::uuid FOR UPDATE`, accountID).Scan(&before); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_accounts SET status = $2, updated_at = now() WHERE id = $1::uuid`, accountID, newStatus); err != nil {
		m.fail(w, r, err)
		return
	}
	var revoked int64
	if newStatus == "suspended" {
		if tag, err := tx.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at = now() WHERE account_id = $1::uuid AND revoked_at IS NULL`, accountID); err != nil {
			m.fail(w, r, err)
			return
		} else {
			revoked += tag.RowsAffected()
		}
		if tag, err := tx.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at = now() WHERE account_id = $1::uuid AND revoked_at IS NULL`, accountID); err != nil {
			m.fail(w, r, err)
			return
		} else {
			revoked += tag.RowsAffected()
		}
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, action, "account", accountID,
		map[string]any{"status": before}, map[string]any{"status": newStatus}, reason)
	httpapi.OK(w, map[string]any{"accountId": accountID, "status": newStatus, "revokedSessions": revoked})
}

type accountSessionItem struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	MembershipID string     `json:"membershipId"`
	DeviceID     string     `json:"deviceId"`
	IP           string     `json:"ip"`
	UserAgent    string     `json:"userAgent"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
}

func (m *Module) accountSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := m.resolveAccountID(ctx, chi.URLParam(r, "id"))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	p := parsePagination(r)
	var total int
	if err := m.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM auth_refresh_sessions WHERE account_id = $1::uuid)
		     + (SELECT count(*) FROM auth_account_sessions WHERE account_id = $1::uuid)`, accountID).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, 'refresh', membership_id::text, COALESCE(device_id,''), COALESCE(ip,''),
		       COALESCE(user_agent,''), created_at, expires_at, revoked_at
		FROM auth_refresh_sessions
		WHERE account_id = $1::uuid
		UNION ALL
		SELECT id::text, 'account', '', '', '', '', created_at, expires_at, revoked_at
		FROM auth_account_sessions
		WHERE account_id = $1::uuid
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, accountID, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]accountSessionItem, 0, p.PageSize)
	for rows.Next() {
		var it accountSessionItem
		if err := rows.Scan(&it.ID, &it.Type, &it.MembershipID, &it.DeviceID, &it.IP,
			&it.UserAgent, &it.CreatedAt, &it.ExpiresAt, &it.RevokedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

type revokeSessionsRequest struct {
	SessionID string `json:"sessionId"`
	AccountID string `json:"accountId"`
}

func (m *Module) revokeAccountSessions(w http.ResponseWriter, r *http.Request) {
	m.revokeSessions(w, r, chi.URLParam(r, "id"))
}

func (m *Module) revokeGlobalSession(w http.ResponseWriter, r *http.Request) {
	m.revokeSessions(w, r, "")
}

func (m *Module) revokeSessions(w http.ResponseWriter, r *http.Request, pathAccountID string) {
	ctx := r.Context()
	var req revokeSessionsRequest
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	accountID := strings.TrimSpace(pathAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(req.AccountID)
	}
	if accountID != "" {
		resolved, err := m.resolveAccountID(ctx, accountID)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		accountID = resolved
	}
	sessionID := strings.TrimSpace(req.SessionID)

	if accountID == "" && sessionID == "" {
		m.badRequest(w, "INVALID_PARAMS", "accountId or sessionId is required")
		return
	}
	if sessionID != "" && !validUUID(sessionID) {
		m.notFound(w, "session not found")
		return
	}

	var revoked int64
	if sessionID != "" {
		var refreshAffected, accountAffected int64
		if accountID != "" {
			tag, err := m.pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at = now() WHERE id = $1::uuid AND account_id = $2::uuid AND revoked_at IS NULL`, sessionID, accountID)
			if err != nil {
				m.fail(w, r, err)
				return
			}
			refreshAffected = tag.RowsAffected()
			tag, err = m.pool.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at = now() WHERE id = $1::uuid AND account_id = $2::uuid AND revoked_at IS NULL`, sessionID, accountID)
			if err != nil {
				m.fail(w, r, err)
				return
			}
			accountAffected = tag.RowsAffected()
		} else {
			tag, err := m.pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, sessionID)
			if err != nil {
				m.fail(w, r, err)
				return
			}
			refreshAffected = tag.RowsAffected()
			tag, err = m.pool.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, sessionID)
			if err != nil {
				m.fail(w, r, err)
				return
			}
			accountAffected = tag.RowsAffected()
		}
		revoked = refreshAffected + accountAffected
		if revoked == 0 && accountID == "" {
			// The session may already be revoked; confirm existence before 404.
			var exists bool
			if err := m.pool.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM auth_refresh_sessions WHERE id = $1::uuid)
				     OR EXISTS (SELECT 1 FROM auth_account_sessions WHERE id = $1::uuid)`, sessionID).Scan(&exists); err != nil {
				m.fail(w, r, err)
				return
			}
			if !exists {
				m.notFound(w, "session not found")
				return
			}
		}
		if revoked == 0 && accountID != "" {
			m.notFound(w, "session not found")
			return
		}
	} else {
		tag, err := m.pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at = now() WHERE account_id = $1::uuid AND revoked_at IS NULL`, accountID)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		revoked = tag.RowsAffected()
		tag2, err := m.pool.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at = now() WHERE account_id = $1::uuid AND revoked_at IS NULL`, accountID)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		revoked += tag2.RowsAffected()
	}
	m.auditEvent(r, "account.sessions.revoke", "account", accountID,
		nil, map[string]any{"sessionId": sessionID, "revoked": revoked}, "")
	httpapi.OK(w, map[string]any{"revoked": revoked})
}
