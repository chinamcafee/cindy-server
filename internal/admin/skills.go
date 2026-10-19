package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type skillListItem struct {
	ID              string    `json:"id"`
	Slug            string    `json:"slug"`
	OwnerAccountID  string    `json:"ownerAccountId"`
	OwnerType       string    `json:"ownerType"`
	DisplayName     string    `json:"displayName"`
	Summary         string    `json:"summary"`
	Visibility      string    `json:"visibility"`
	Status          string    `json:"status"`
	LatestVersion   string    `json:"latestVersion"`
	IconKey         string    `json:"iconKey"`
	GithubURL       string    `json:"githubUrl"`
	PendingVersions int64     `json:"pendingVersions"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type skillVersionItem struct {
	ID              string    `json:"id"`
	Version         string    `json:"version"`
	FolderHash      string    `json:"folderHash"`
	ZipSHA256       string    `json:"zipSha256"`
	FileSize        int64     `json:"fileSize"`
	Status          string    `json:"status"`
	ScanStatus      string    `json:"scanStatus"`
	RejectionReason string    `json:"rejectionReason"`
	CreatedAt       time.Time `json:"createdAt"`
}

var skillVisibilities = map[string]bool{"private": true, "shared": true, "public": true}

func (m *Module) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	visibility := strings.TrimSpace(r.URL.Query().Get("visibility"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	like := "%" + q + "%"
	p := parsePagination(r)
	const where = `
		WHERE ($1 = '' OR s.display_name ILIKE $1 OR s.slug ILIKE $1)
		  AND ($2 = '' OR s.visibility = $2)
		  AND ($3 = ''
		       OR ($3 = 'pending_review' AND EXISTS (
		           SELECT 1 FROM sk_versions v WHERE v.skill_id = s.id AND v.status = 'pending'))
		       OR s.status = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM sk_skills s`+where, like, visibility, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT s.id::text, s.slug, s.owner_account_id::text, s.owner_type, s.display_name,
		       COALESCE(s.summary,''), s.visibility, s.status, COALESCE(s.latest_version,''),
		       COALESCE(s.icon_key,''), COALESCE(s.github_url,''), s.created_at, s.updated_at,
		       (SELECT count(*) FROM sk_versions v WHERE v.skill_id = s.id AND v.status = 'pending')
		FROM sk_skills s
		`+where+`
		ORDER BY s.updated_at DESC
		LIMIT $4 OFFSET $5`, like, visibility, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]skillListItem, 0, p.PageSize)
	for rows.Next() {
		var it skillListItem
		if err := rows.Scan(&it.ID, &it.Slug, &it.OwnerAccountID, &it.OwnerType, &it.DisplayName,
			&it.Summary, &it.Visibility, &it.Status, &it.LatestVersion, &it.IconKey,
			&it.GithubURL, &it.CreatedAt, &it.UpdatedAt, &it.PendingVersions); err != nil {
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

func (m *Module) loadSkill(ctx context.Context, id string) (*skillListItem, error) {
	var it skillListItem
	err := m.pool.QueryRow(ctx, `
		SELECT s.id::text, s.slug, s.owner_account_id::text, s.owner_type, s.display_name,
		       COALESCE(s.summary,''), s.visibility, s.status, COALESCE(s.latest_version,''),
		       COALESCE(s.icon_key,''), COALESCE(s.github_url,''), s.created_at, s.updated_at,
		       (SELECT count(*) FROM sk_versions v WHERE v.skill_id = s.id AND v.status = 'pending')
		FROM sk_skills s WHERE s.id = $1::uuid`, id).
		Scan(&it.ID, &it.Slug, &it.OwnerAccountID, &it.OwnerType, &it.DisplayName, &it.Summary,
			&it.Visibility, &it.Status, &it.LatestVersion, &it.IconKey, &it.GithubURL,
			&it.CreatedAt, &it.UpdatedAt, &it.PendingVersions)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (m *Module) skillDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "skill not found")
		return
	}
	skill, err := m.loadSkill(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, version, COALESCE(folder_hash,''), zip_sha256, file_size, status,
		       scan_status, COALESCE(rejection_reason,''), created_at
		FROM sk_versions
		WHERE skill_id = $1::uuid
		ORDER BY created_at DESC
		LIMIT 200`, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	versions := make([]skillVersionItem, 0)
	for rows.Next() {
		var it skillVersionItem
		if err := rows.Scan(&it.ID, &it.Version, &it.FolderHash, &it.ZipSHA256, &it.FileSize,
			&it.Status, &it.ScanStatus, &it.RejectionReason, &it.CreatedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		versions = append(versions, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, map[string]any{"skill": skill, "versions": versions})
}

func (m *Module) approveSkillVersion(w http.ResponseWriter, r *http.Request) {
	m.reviewSkillVersion(w, r, true)
}

func (m *Module) rejectSkillVersion(w http.ResponseWriter, r *http.Request) {
	m.reviewSkillVersion(w, r, false)
}

func (m *Module) reviewSkillVersion(w http.ResponseWriter, r *http.Request, approve bool) {
	ctx := r.Context()
	skillID := chi.URLParam(r, "id")
	version := strings.TrimSpace(chi.URLParam(r, "version"))
	if !validUUID(skillID) || version == "" {
		m.notFound(w, "skill version not found")
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
		m.badRequest(w, "REASON_REQUIRED", "reason is required when rejecting a version")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var beforeStatus, beforeScan string
	if err := tx.QueryRow(ctx, `
		SELECT status, scan_status FROM sk_versions
		WHERE skill_id = $1::uuid AND (version = $2 OR id::text = $2)
		FOR UPDATE`, skillID, version).Scan(&beforeStatus, &beforeScan); err != nil {
		m.fail(w, r, err)
		return
	}

	var it skillVersionItem
	var query string
	var args []any
	if approve {
		query = `
			UPDATE sk_versions
			SET status = 'approved', scan_status = 'clean', rejection_reason = NULL
			WHERE skill_id = $1::uuid AND (version = $2 OR id::text = $2)
			RETURNING id::text, version, COALESCE(folder_hash,''), zip_sha256, file_size,
			          status, scan_status, COALESCE(rejection_reason,''), created_at`
		args = []any{skillID, version}
	} else {
		query = `
			UPDATE sk_versions
			SET status = 'rejected', rejection_reason = $3
			WHERE skill_id = $1::uuid AND (version = $2 OR id::text = $2)
			RETURNING id::text, version, COALESCE(folder_hash,''), zip_sha256, file_size,
			          status, scan_status, COALESCE(rejection_reason,''), created_at`
		args = []any{skillID, version, nilIfEmpty(reason)}
	}
	if err := tx.QueryRow(ctx, query, args...).
		Scan(&it.ID, &it.Version, &it.FolderHash, &it.ZipSHA256, &it.FileSize,
			&it.Status, &it.ScanStatus, &it.RejectionReason, &it.CreatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	if approve {
		if _, err := tx.Exec(ctx, `
			UPDATE sk_skills
			SET latest_version = $2, status = 'active', updated_at = now()
			WHERE id = $1::uuid`, skillID, it.Version); err != nil {
			m.fail(w, r, err)
			return
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE sk_skills SET updated_at = now() WHERE id = $1::uuid`, skillID); err != nil {
			m.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	action := "skill.version.reject"
	if approve {
		action = "skill.version.approve"
	}
	m.auditEvent(r, action, "skill_version", it.ID,
		map[string]any{"status": beforeStatus, "scanStatus": beforeScan},
		map[string]any{"status": it.Status, "scanStatus": it.ScanStatus}, reason)
	httpapi.OK(w, map[string]any{"version": it})
}

func (m *Module) updateSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "skill not found")
		return
	}
	var req struct {
		Visibility  *string `json:"visibility"`
		DisplayName *string `json:"displayName"`
		Summary     *string `json:"summary"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	current, err := m.loadSkill(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	before := map[string]any{
		"visibility": current.Visibility, "displayName": current.DisplayName, "summary": current.Summary,
	}
	if req.Visibility != nil {
		visibility := strings.TrimSpace(*req.Visibility)
		if !skillVisibilities[visibility] {
			m.badRequest(w, "INVALID_PARAMS", "visibility must be private, shared or public")
			return
		}
		current.Visibility = visibility
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			m.badRequest(w, "INVALID_PARAMS", "displayName must not be empty")
			return
		}
		current.DisplayName = name
	}
	if req.Summary != nil {
		current.Summary = *req.Summary
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE sk_skills
		SET visibility = $2, display_name = $3, summary = $4, updated_at = now()
		WHERE id = $1::uuid`, id, current.Visibility, current.DisplayName, nilIfEmpty(current.Summary)); err != nil {
		m.fail(w, r, err)
		return
	}
	updated, err := m.loadSkill(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "skill.update", "skill", id, before, map[string]any{
		"visibility": updated.Visibility, "displayName": updated.DisplayName, "summary": updated.Summary,
	}, "")
	httpapi.OK(w, map[string]any{"skill": updated})
}
