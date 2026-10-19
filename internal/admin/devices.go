package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type deviceItem struct {
	ID                   string     `json:"id"`
	AccountID            string     `json:"accountId"`
	AccountEmail         string     `json:"accountEmail,omitempty"`
	Name                 string     `json:"name"`
	Platform             string     `json:"platform"`
	AppVersion           string     `json:"appVersion"`
	RemoteControlEnabled bool       `json:"remoteControlEnabled"`
	Busy                 bool       `json:"busy"`
	Online               bool       `json:"online"`
	LastSeenAt           *time.Time `json:"lastSeenAt,omitempty"`
	RemovedAt            *time.Time `json:"removedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
}

// onlineDevices checks the per-account presence hashes for the given device IDs.
func (m *Module) onlineDevices(ctx context.Context, ids []string) map[string]bool {
	all := m.onlineDeviceSet(ctx)
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if all[id] {
			out[id] = true
		}
	}
	return out
}

func (m *Module) listDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID != "" && !validUUID(accountID) {
		m.badRequest(w, "INVALID_PARAMS", "accountId must be a UUID")
		return
	}
	onlineFilter, filterOnline := queryBool(r, "online")
	like := "%" + q + "%"
	p := parsePagination(r)

	// Presence lives in Redis, so candidates are loaded first, filtered by
	// presence in memory and then paginated.
	rows, err := m.pool.Query(ctx, `
		SELECT d.id, d.account_id::text, d.name, COALESCE(d.platform,''), COALESCE(d.app_version,''),
		       d.remote_control_enabled, d.busy, d.last_seen_at, d.removed_at, d.created_at,
		       COALESCE(em.identifier::text,'')
		FROM dl_devices d
		LEFT JOIN LATERAL (
			SELECT i.identifier FROM auth_identities i
			WHERE i.account_id = d.account_id AND i.type = 'email'
			ORDER BY i.verified_at DESC NULLS LAST, i.created_at
			LIMIT 1
		) em ON true
		WHERE d.removed_at IS NULL
		  AND ($1 = '' OR d.id = $1 OR d.account_id::text = $1 OR d.name ILIKE $2)
		  AND ($3 = '' OR d.account_id::text = $3)
		ORDER BY d.last_seen_at DESC NULLS LAST, d.created_at DESC
		LIMIT 5000`, q, like, accountID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()

	items := make([]deviceItem, 0)
	ids := make([]string, 0)
	for rows.Next() {
		var it deviceItem
		if err := rows.Scan(&it.ID, &it.AccountID, &it.Name, &it.Platform, &it.AppVersion,
			&it.RemoteControlEnabled, &it.Busy, &it.LastSeenAt, &it.RemovedAt, &it.CreatedAt,
			&it.AccountEmail); err != nil {
			m.fail(w, r, err)
			return
		}
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	online := m.onlineDevices(ctx, ids)
	filtered := make([]deviceItem, 0, len(items))
	for _, it := range items {
		it.Online = online[it.ID]
		if filterOnline && it.Online != onlineFilter {
			continue
		}
		filtered = append(filtered, it)
	}
	total := len(filtered)
	start := p.Offset
	if start > total {
		start = total
	}
	end := start + p.PageSize
	if end > total {
		end = total
	}
	httpapi.OK(w, newList(filtered[start:end], total, p))
}

func (m *Module) deleteDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		m.notFound(w, "device not found")
		return
	}
	tag, err := m.pool.Exec(ctx, `
		UPDATE dl_devices SET removed_at = now(), updated_at = now()
		WHERE id = $1 AND removed_at IS NULL`, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w, "device not found")
		return
	}
	if m.rdb != nil {
		iter := m.rdb.Scan(ctx, 0, m.presencePattern(), 100).Iterator()
		for iter.Next(ctx) {
			_ = m.rdb.HDel(ctx, iter.Val(), id).Err()
		}
	}
	m.auditEvent(r, "device.remove", "device", id, nil, map[string]any{"removed": true}, "")
	httpapi.OK(w, map[string]any{"id": id, "removed": true})
}

type sharedTaskItem struct {
	ID             string     `json:"id"`
	SessionID      string     `json:"sessionId"`
	OwnerAccountID string     `json:"ownerAccountId"`
	HostDeviceID   string     `json:"hostDeviceId"`
	Revision       int        `json:"revision"`
	Status         string     `json:"status"`
	GuestCount     int64      `json:"guestCount"`
	CreatedAt      time.Time  `json:"createdAt"`
	ClosedAt       *time.Time `json:"closedAt,omitempty"`
}

func (m *Module) listSharedTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID != "" && !validUUID(accountID) {
		m.badRequest(w, "INVALID_PARAMS", "accountId must be a UUID")
		return
	}
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR t.status = $1) AND ($2 = '' OR t.owner_account_id::text = $2)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM dl_shared_tasks t `+where, status, accountID).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT t.id::text, t.session_id, t.owner_account_id::text, t.host_device_id,
		       t.revision, t.status, t.created_at, t.closed_at,
		       (SELECT count(*) FROM dl_shared_task_guests g
		        WHERE g.shared_task_id = t.id AND g.removed_at IS NULL)
		FROM dl_shared_tasks t
		`+where+`
		ORDER BY t.created_at DESC
		LIMIT $3 OFFSET $4`, status, accountID, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]sharedTaskItem, 0, p.PageSize)
	for rows.Next() {
		var it sharedTaskItem
		if err := rows.Scan(&it.ID, &it.SessionID, &it.OwnerAccountID, &it.HostDeviceID,
			&it.Revision, &it.Status, &it.CreatedAt, &it.ClosedAt, &it.GuestCount); err != nil {
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

func (m *Module) closeSharedTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "shared task not found")
		return
	}
	var before string
	if err := m.pool.QueryRow(ctx, `SELECT status FROM dl_shared_tasks WHERE id = $1::uuid`, id).Scan(&before); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE dl_shared_tasks SET status = 'closed', closed_at = COALESCE(closed_at, now())
		WHERE id = $1::uuid`, id); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "shared_task.close", "shared_task", id,
		map[string]any{"status": before}, map[string]any{"status": "closed"}, "")
	httpapi.OK(w, map[string]any{"id": id, "status": "closed"})
}

type providerShareItem struct {
	ID             string    `json:"id"`
	OwnerAccountID string    `json:"ownerAccountId"`
	ProviderID     string    `json:"providerId"`
	ProviderLabel  string    `json:"providerLabel"`
	HostDeviceID   string    `json:"hostDeviceId"`
	Status         string    `json:"status"`
	MemberCount    int64     `json:"memberCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (m *Module) listProviderShares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID != "" && !validUUID(accountID) {
		m.badRequest(w, "INVALID_PARAMS", "accountId must be a UUID")
		return
	}
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR s.status = $1) AND ($2 = '' OR s.owner_account_id::text = $2)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM dl_provider_shares s `+where, status, accountID).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT s.id::text, s.owner_account_id::text, s.provider_id, s.provider_label,
		       s.host_device_id, s.status, s.created_at, s.updated_at,
		       (SELECT count(*) FROM dl_provider_share_members pm
		        WHERE pm.share_id = s.id AND pm.status <> 'revoked')
		FROM dl_provider_shares s
		`+where+`
		ORDER BY s.created_at DESC
		LIMIT $3 OFFSET $4`, status, accountID, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]providerShareItem, 0, p.PageSize)
	for rows.Next() {
		var it providerShareItem
		if err := rows.Scan(&it.ID, &it.OwnerAccountID, &it.ProviderID, &it.ProviderLabel,
			&it.HostDeviceID, &it.Status, &it.CreatedAt, &it.UpdatedAt, &it.MemberCount); err != nil {
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

func (m *Module) revokeProviderShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "provider share not found")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before string
	if err := tx.QueryRow(ctx, `SELECT status FROM dl_provider_shares WHERE id = $1::uuid FOR UPDATE`, id).Scan(&before); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dl_provider_shares SET status = 'revoked', updated_at = now() WHERE id = $1::uuid`, id); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dl_provider_share_members SET status = 'revoked' WHERE share_id = $1::uuid AND status <> 'revoked'`, id); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dl_provider_share_guest_credentials SET revoked_at = now()
		WHERE share_id = $1::uuid AND revoked_at IS NULL`, id); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "provider_share.revoke", "provider_share", id,
		map[string]any{"status": before}, map[string]any{"status": "revoked"}, "")
	httpapi.OK(w, map[string]any{"id": id, "status": "revoked"})
}
