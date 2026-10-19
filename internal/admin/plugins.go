package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type pluginReleaseSummary struct {
	ID           string     `json:"id"`
	Version      string     `json:"version"`
	ReviewStatus string     `json:"reviewStatus"`
	Status       string     `json:"status"`
	PublishedAt  *time.Time `json:"publishedAt,omitempty"`
}

type pluginListItem struct {
	ID              string                `json:"id"`
	GhostID         string                `json:"ghostId"`
	Scope           string                `json:"scope"`
	OrgID           string                `json:"orgId"`
	Namespace       string                `json:"namespace"`
	Name            string                `json:"name"`
	Description     string                `json:"description"`
	Author          string                `json:"author"`
	Status          string                `json:"status"`
	DefaultInstall  bool                  `json:"defaultInstall"`
	IconKey         string                `json:"iconKey"`
	PendingReleases int64                 `json:"pendingReleases"`
	CurrentRelease  *pluginReleaseSummary `json:"currentRelease"`
	CreatedAt       time.Time             `json:"createdAt"`
	UpdatedAt       time.Time             `json:"updatedAt"`
}

type pluginReleaseItem struct {
	ID           string          `json:"id"`
	Version      string          `json:"version"`
	Manifest     json.RawMessage `json:"manifest"`
	SHA256       string          `json:"sha256"`
	SizeBytes    int64           `json:"sizeBytes"`
	IconKey      string          `json:"iconKey"`
	Status       string          `json:"status"`
	ReviewStatus string          `json:"reviewStatus"`
	ReviewReason string          `json:"reviewReason"`
	PublishedAt  *time.Time      `json:"publishedAt,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
}

func (m *Module) listPlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	like := "%" + q + "%"
	p := parsePagination(r)
	const where = `
		WHERE ($1 = '' OR p.name ILIKE $1 OR p.ghost_id ILIKE $1 OR COALESCE(p.namespace,'') ILIKE $1)
		  AND ($2 = '' OR p.scope = $2)
		  AND ($3 = ''
		       OR ($3 = 'pending_review' AND EXISTS (
		           SELECT 1 FROM plugin_releases pr WHERE pr.plugin_id = p.id AND pr.review_status = 'pending'))
		       OR p.status = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_plugins p`+where, like, scope, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT p.id::text, p.ghost_id, p.scope, COALESCE(p.org_id::text,''), COALESCE(p.namespace,''),
		       p.name, COALESCE(p.description,''), COALESCE(p.author,''), p.status, p.default_install,
		       COALESCE(p.icon_key,''), p.created_at, p.updated_at,
		       (SELECT count(*) FROM plugin_releases pr WHERE pr.plugin_id = p.id AND pr.review_status = 'pending'),
		       cr.id::text, cr.version, cr.review_status, cr.status, cr.published_at
		FROM plugin_plugins p
		LEFT JOIN LATERAL (
			SELECT r.id, r.version, r.review_status, r.status, r.published_at
			FROM plugin_releases r
			WHERE r.plugin_id = p.id AND r.review_status = 'approved' AND r.status = 'active'
			ORDER BY r.published_at DESC NULLS LAST, r.created_at DESC
			LIMIT 1
		) cr ON true
		`+where+`
		ORDER BY p.updated_at DESC
		LIMIT $4 OFFSET $5`, like, scope, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]pluginListItem, 0, p.PageSize)
	for rows.Next() {
		var it pluginListItem
		var curID, curVersion, curReview, curStatus *string
		var curPublishedAt *time.Time
		if err := rows.Scan(&it.ID, &it.GhostID, &it.Scope, &it.OrgID, &it.Namespace,
			&it.Name, &it.Description, &it.Author, &it.Status, &it.DefaultInstall,
			&it.IconKey, &it.CreatedAt, &it.UpdatedAt, &it.PendingReleases,
			&curID, &curVersion, &curReview, &curStatus, &curPublishedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		if curID != nil {
			it.CurrentRelease = &pluginReleaseSummary{
				ID:           *curID,
				Version:      deref(curVersion),
				ReviewStatus: deref(curReview),
				Status:       deref(curStatus),
				PublishedAt:  curPublishedAt,
			}
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (m *Module) loadPlugin(ctx context.Context, id string) (*pluginListItem, error) {
	var it pluginListItem
	err := m.pool.QueryRow(ctx, `
		SELECT p.id::text, p.ghost_id, p.scope, COALESCE(p.org_id::text,''), COALESCE(p.namespace,''),
		       p.name, COALESCE(p.description,''), COALESCE(p.author,''), p.status, p.default_install,
		       COALESCE(p.icon_key,''), p.created_at, p.updated_at,
		       (SELECT count(*) FROM plugin_releases pr WHERE pr.plugin_id = p.id AND pr.review_status = 'pending')
		FROM plugin_plugins p
		WHERE p.id = $1::uuid`, id).
		Scan(&it.ID, &it.GhostID, &it.Scope, &it.OrgID, &it.Namespace, &it.Name, &it.Description,
			&it.Author, &it.Status, &it.DefaultInstall, &it.IconKey, &it.CreatedAt, &it.UpdatedAt,
			&it.PendingReleases)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (m *Module) pluginDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "plugin not found")
		return
	}
	plugin, err := m.loadPlugin(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, version, manifest, sha256, size_bytes, COALESCE(icon_key,''),
		       status, review_status, COALESCE(review_reason,''), published_at, created_at
		FROM plugin_releases
		WHERE plugin_id = $1::uuid
		ORDER BY created_at DESC
		LIMIT 200`, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	releases := make([]pluginReleaseItem, 0)
	for rows.Next() {
		var it pluginReleaseItem
		var manifest []byte
		if err := rows.Scan(&it.ID, &it.Version, &manifest, &it.SHA256, &it.SizeBytes,
			&it.IconKey, &it.Status, &it.ReviewStatus, &it.ReviewReason, &it.PublishedAt, &it.CreatedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		it.Manifest = rawJSON(manifest)
		releases = append(releases, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	if len(releases) > 0 {
		for i := range releases {
			if releases[i].ReviewStatus == "approved" && releases[i].Status == "active" {
				plugin.CurrentRelease = &pluginReleaseSummary{
					ID: releases[i].ID, Version: releases[i].Version,
					ReviewStatus: releases[i].ReviewStatus, Status: releases[i].Status,
					PublishedAt: releases[i].PublishedAt,
				}
				break
			}
		}
	}
	httpapi.OK(w, map[string]any{"plugin": plugin, "releases": releases})
}

func (m *Module) approvePluginRelease(w http.ResponseWriter, r *http.Request) {
	m.reviewPluginRelease(w, r, true)
}

func (m *Module) rejectPluginRelease(w http.ResponseWriter, r *http.Request) {
	m.reviewPluginRelease(w, r, false)
}

func (m *Module) reviewPluginRelease(w http.ResponseWriter, r *http.Request, approve bool) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	releaseID := chi.URLParam(r, "releaseId")
	if !validUUID(pluginID) || !validUUID(releaseID) {
		m.notFound(w, "plugin release not found")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if !approve && reason == "" {
		m.badRequest(w, "REASON_REQUIRED", "reason is required when rejecting a release")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var beforeReview, beforeStatus string
	if err := tx.QueryRow(ctx, `
		SELECT review_status, status FROM plugin_releases
		WHERE id = $1::uuid AND plugin_id = $2::uuid FOR UPDATE`, releaseID, pluginID).
		Scan(&beforeReview, &beforeStatus); err != nil {
		m.fail(w, r, err)
		return
	}
	var it pluginReleaseItem
	var manifest []byte
	var query string
	if approve {
		query = `
			UPDATE plugin_releases
			SET review_status = 'approved', review_reason = $3, status = 'active',
			    published_at = COALESCE(published_at, now())
			WHERE id = $1::uuid AND plugin_id = $2::uuid
			RETURNING id::text, version, manifest, sha256, size_bytes, COALESCE(icon_key,''),
			          status, review_status, COALESCE(review_reason,''), published_at, created_at`
	} else {
		query = `
			UPDATE plugin_releases
			SET review_status = 'rejected', review_reason = $3, status = 'rejected'
			WHERE id = $1::uuid AND plugin_id = $2::uuid
			RETURNING id::text, version, manifest, sha256, size_bytes, COALESCE(icon_key,''),
			          status, review_status, COALESCE(review_reason,''), published_at, created_at`
	}
	if err := tx.QueryRow(ctx, query, releaseID, pluginID, nilIfEmpty(reason)).
		Scan(&it.ID, &it.Version, &manifest, &it.SHA256, &it.SizeBytes, &it.IconKey,
			&it.Status, &it.ReviewStatus, &it.ReviewReason, &it.PublishedAt, &it.CreatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	it.Manifest = rawJSON(manifest)
	if _, err := tx.Exec(ctx, `UPDATE plugin_plugins SET updated_at = now() WHERE id = $1::uuid`, pluginID); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	action := "plugin.release.reject"
	if approve {
		action = "plugin.release.approve"
	}
	m.auditEvent(r, action, "plugin_release", releaseID,
		map[string]any{"reviewStatus": beforeReview, "status": beforeStatus},
		map[string]any{"reviewStatus": it.ReviewStatus, "status": it.Status}, reason)
	httpapi.OK(w, map[string]any{"release": it})
}

func (m *Module) removePlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	if !validUUID(pluginID) {
		m.notFound(w, "plugin not found")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ghostID, scope, namespace, beforeStatus string
	var orgID *string
	if err := tx.QueryRow(ctx, `
		SELECT ghost_id, scope, org_id::text, COALESCE(namespace,''), status
		FROM plugin_plugins WHERE id = $1::uuid FOR UPDATE`, pluginID).
		Scan(&ghostID, &scope, &orgID, &namespace, &beforeStatus); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_removals (plugin_id, ghost_id, scope, org_id, namespace, action)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, 'purge')`,
		pluginID, ghostID, scope, orgID, nullIfEmpty(namespace)); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE plugin_plugins SET status = 'removed', updated_at = now() WHERE id = $1::uuid`, pluginID); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "plugin.remove", "plugin", pluginID,
		map[string]any{"status": beforeStatus}, map[string]any{"status": "removed", "ghostId": ghostID}, "")
	httpapi.OK(w, map[string]any{"pluginId": pluginID, "status": "removed"})
}
