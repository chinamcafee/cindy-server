package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type desktopReleaseItem struct {
	ID          string          `json:"id"`
	Platform    string          `json:"platform"`
	Arch        string          `json:"arch"`
	Channel     string          `json:"channel"`
	Version     string          `json:"version"`
	Manifest    json.RawMessage `json:"manifest"`
	Files       json.RawMessage `json:"files"`
	Rollout     int             `json:"rollout"`
	Status      string          `json:"status"`
	PublishedAt *time.Time      `json:"publishedAt,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type mobileReleaseItem struct {
	ID             string     `json:"id"`
	Platform       string     `json:"platform"`
	Channel        string     `json:"channel"`
	Version        string     `json:"version"`
	BuildNumber    string     `json:"buildNumber"`
	RuntimeVersion string     `json:"runtimeVersion"`
	InstallURL     string     `json:"installUrl"`
	ItmsURL        string     `json:"itmsUrl"`
	MinVersion     string     `json:"minVersion"`
	ReleaseNotes   string     `json:"releaseNotes"`
	Status         string     `json:"status"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

type announcementItem struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	Version     string     `json:"version"`
	Locale      string     `json:"locale"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

var desktopChannels = map[string]bool{"release": true, "beta": true, "canary": true}

func (m *Module) listDesktopReleases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR platform = $1) AND ($2 = '' OR channel = $2) AND ($3 = '' OR status = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM upd_desktop_releases `+where, platform, channel, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, platform, arch, channel, version, manifest, files, rollout, status, published_at, created_at
		FROM upd_desktop_releases
		`+where+`
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`, platform, channel, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]desktopReleaseItem, 0, p.PageSize)
	for rows.Next() {
		var it desktopReleaseItem
		var manifest, files []byte
		if err := rows.Scan(&it.ID, &it.Platform, &it.Arch, &it.Channel, &it.Version,
			&manifest, &files, &it.Rollout, &it.Status, &it.PublishedAt, &it.CreatedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		it.Manifest = rawJSON(manifest)
		it.Files = rawJSON(files)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

func (m *Module) createDesktopRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Platform string          `json:"platform"`
		Arch     string          `json:"arch"`
		Channel  string          `json:"channel"`
		Version  string          `json:"version"`
		Manifest json.RawMessage `json:"manifest"`
		Files    json.RawMessage `json:"files"`
		Rollout  *int            `json:"rollout"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.Arch = strings.TrimSpace(req.Arch)
	req.Version = strings.TrimSpace(req.Version)
	if req.Platform == "" || req.Arch == "" || req.Version == "" {
		m.badRequest(w, "INVALID_PARAMS", "platform, arch and version are required")
		return
	}
	if req.Channel == "" {
		req.Channel = "release"
	}
	if !desktopChannels[req.Channel] {
		m.badRequest(w, "INVALID_PARAMS", "channel must be release, beta or canary")
		return
	}
	if len(req.Manifest) == 0 || string(req.Manifest) == "null" || !json.Valid(req.Manifest) {
		m.badRequest(w, "INVALID_PARAMS", "manifest is required")
		return
	}
	files := req.Files
	if len(files) == 0 || string(files) == "null" {
		files = json.RawMessage("{}")
	}
	rollout := 100
	if req.Rollout != nil {
		rollout = *req.Rollout
	}
	if rollout < 0 || rollout > 100 {
		m.badRequest(w, "INVALID_PARAMS", "rollout must be between 0 and 100")
		return
	}
	var it desktopReleaseItem
	var manifest, filesOut []byte
	err := m.pool.QueryRow(ctx, `
		INSERT INTO upd_desktop_releases (platform, arch, channel, version, manifest, files, rollout, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft')
		RETURNING id::text, platform, arch, channel, version, manifest, files, rollout, status, published_at, created_at`,
		req.Platform, req.Arch, req.Channel, req.Version, req.Manifest, files, rollout).
		Scan(&it.ID, &it.Platform, &it.Arch, &it.Channel, &it.Version, &manifest, &filesOut,
			&it.Rollout, &it.Status, &it.PublishedAt, &it.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			m.conflict(w, "RELEASE_EXISTS", "release already exists for this platform/arch/channel/version")
			return
		}
		m.fail(w, r, err)
		return
	}
	it.Manifest = rawJSON(manifest)
	it.Files = rawJSON(filesOut)
	m.auditEvent(r, "release.desktop.create", "desktop_release", it.ID, nil, map[string]any{
		"platform": it.Platform, "arch": it.Arch, "channel": it.Channel, "version": it.Version,
	}, "")
	httpapi.Created(w, map[string]any{"release": it})
}

func (m *Module) publishDesktopRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "release not found")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var beforeStatus, platform, arch, channel string
	if err := tx.QueryRow(ctx, `
		SELECT status, platform, arch, channel FROM upd_desktop_releases
		WHERE id = $1::uuid FOR UPDATE`, id).Scan(&beforeStatus, &platform, &arch, &channel); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE upd_desktop_releases SET status = 'archived'
		WHERE platform = $1 AND arch = $2 AND channel = $3 AND status = 'published' AND id <> $4::uuid`,
		platform, arch, channel, id); err != nil {
		m.fail(w, r, err)
		return
	}
	var it desktopReleaseItem
	var manifest, files []byte
	if err := tx.QueryRow(ctx, `
		UPDATE upd_desktop_releases SET status = 'published', published_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, platform, arch, channel, version, manifest, files, rollout, status, published_at, created_at`, id).
		Scan(&it.ID, &it.Platform, &it.Arch, &it.Channel, &it.Version, &manifest, &files,
			&it.Rollout, &it.Status, &it.PublishedAt, &it.CreatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	it.Manifest = rawJSON(manifest)
	it.Files = rawJSON(files)
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "release.desktop.publish", "desktop_release", id,
		map[string]any{"status": beforeStatus}, map[string]any{"status": it.Status}, "")
	httpapi.OK(w, map[string]any{"release": it})
}

func (m *Module) listMobileReleases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR platform = $1) AND ($2 = '' OR channel = $2) AND ($3 = '' OR status = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM upd_mobile_releases `+where, platform, channel, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, platform, channel, version, build_number, runtime_version,
		       COALESCE(install_url,''), COALESCE(itms_url,''), COALESCE(min_version,''),
		       COALESCE(release_notes,''), status, published_at, created_at
		FROM upd_mobile_releases
		`+where+`
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`, platform, channel, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]mobileReleaseItem, 0, p.PageSize)
	for rows.Next() {
		var it mobileReleaseItem
		if err := rows.Scan(&it.ID, &it.Platform, &it.Channel, &it.Version, &it.BuildNumber,
			&it.RuntimeVersion, &it.InstallURL, &it.ItmsURL, &it.MinVersion, &it.ReleaseNotes,
			&it.Status, &it.PublishedAt, &it.CreatedAt); err != nil {
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

func (m *Module) createMobileRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Platform       string `json:"platform"`
		Channel        string `json:"channel"`
		Version        string `json:"version"`
		BuildNumber    string `json:"buildNumber"`
		RuntimeVersion string `json:"runtimeVersion"`
		InstallURL     string `json:"installUrl"`
		ItmsURL        string `json:"itmsUrl"`
		MinVersion     string `json:"minVersion"`
		ReleaseNotes   string `json:"releaseNotes"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.Version = strings.TrimSpace(req.Version)
	req.BuildNumber = strings.TrimSpace(req.BuildNumber)
	req.RuntimeVersion = strings.TrimSpace(req.RuntimeVersion)
	if req.Platform == "" || req.Version == "" || req.BuildNumber == "" || req.RuntimeVersion == "" {
		m.badRequest(w, "INVALID_PARAMS", "platform, version, buildNumber and runtimeVersion are required")
		return
	}
	if req.Channel == "" {
		req.Channel = "release"
	}
	var it mobileReleaseItem
	err := m.pool.QueryRow(ctx, `
		INSERT INTO upd_mobile_releases (platform, channel, version, build_number, runtime_version,
		                                 install_url, itms_url, min_version, release_notes, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft')
		RETURNING id::text, platform, channel, version, build_number, runtime_version,
		          COALESCE(install_url,''), COALESCE(itms_url,''), COALESCE(min_version,''),
		          COALESCE(release_notes,''), status, published_at, created_at`,
		req.Platform, req.Channel, req.Version, req.BuildNumber, req.RuntimeVersion,
		nilIfEmpty(req.InstallURL), nilIfEmpty(req.ItmsURL), nilIfEmpty(req.MinVersion), nilIfEmpty(req.ReleaseNotes)).
		Scan(&it.ID, &it.Platform, &it.Channel, &it.Version, &it.BuildNumber, &it.RuntimeVersion,
			&it.InstallURL, &it.ItmsURL, &it.MinVersion, &it.ReleaseNotes, &it.Status,
			&it.PublishedAt, &it.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			m.conflict(w, "RELEASE_EXISTS", "release already exists for this platform/channel/version")
			return
		}
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "release.mobile.create", "mobile_release", it.ID, nil, map[string]any{
		"platform": it.Platform, "channel": it.Channel, "version": it.Version,
	}, "")
	httpapi.Created(w, map[string]any{"release": it})
}

func (m *Module) publishMobileRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "release not found")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var beforeStatus, platform, channel string
	if err := tx.QueryRow(ctx, `
		SELECT status, platform, channel FROM upd_mobile_releases
		WHERE id = $1::uuid FOR UPDATE`, id).Scan(&beforeStatus, &platform, &channel); err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE upd_mobile_releases SET status = 'archived'
		WHERE platform = $1 AND channel = $2 AND status = 'published' AND id <> $3::uuid`,
		platform, channel, id); err != nil {
		m.fail(w, r, err)
		return
	}
	var it mobileReleaseItem
	if err := tx.QueryRow(ctx, `
		UPDATE upd_mobile_releases SET status = 'published', published_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, platform, channel, version, build_number, runtime_version,
		          COALESCE(install_url,''), COALESCE(itms_url,''), COALESCE(min_version,''),
		          COALESCE(release_notes,''), status, published_at, created_at`, id).
		Scan(&it.ID, &it.Platform, &it.Channel, &it.Version, &it.BuildNumber, &it.RuntimeVersion,
			&it.InstallURL, &it.ItmsURL, &it.MinVersion, &it.ReleaseNotes, &it.Status,
			&it.PublishedAt, &it.CreatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "release.mobile.publish", "mobile_release", id,
		map[string]any{"status": beforeStatus}, map[string]any{"status": it.Status}, "")
	httpapi.OK(w, map[string]any{"release": it})
}

func (m *Module) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	locale := strings.TrimSpace(r.URL.Query().Get("locale"))
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR platform = $1) AND ($2 = '' OR status = $2) AND ($3 = '' OR locale = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM announcements `+where, platform, status, locale).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, platform, COALESCE(version,''), locale, title, body, status, published_at, created_at
		FROM announcements
		`+where+`
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`, platform, status, locale, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]announcementItem, 0, p.PageSize)
	for rows.Next() {
		var it announcementItem
		if err := rows.Scan(&it.ID, &it.Platform, &it.Version, &it.Locale, &it.Title,
			&it.Body, &it.Status, &it.PublishedAt, &it.CreatedAt); err != nil {
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

var announcementStatuses = map[string]bool{"draft": true, "published": true, "archived": true}

func (m *Module) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Platform string `json:"platform"`
		Version  string `json:"version"`
		Locale   string `json:"locale"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		Status   string `json:"status"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	if req.Platform == "" || req.Title == "" || req.Body == "" {
		m.badRequest(w, "INVALID_PARAMS", "platform, title and body are required")
		return
	}
	if req.Locale == "" {
		req.Locale = "en"
	}
	if req.Status == "" {
		req.Status = "draft"
	}
	if !announcementStatuses[req.Status] {
		m.badRequest(w, "INVALID_PARAMS", "status must be draft, published or archived")
		return
	}
	var it announcementItem
	err := m.pool.QueryRow(ctx, `
		INSERT INTO announcements (platform, version, locale, title, body, status, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6 = 'published' THEN now() ELSE NULL END)
		RETURNING id::text, platform, COALESCE(version,''), locale, title, body, status, published_at, created_at`,
		req.Platform, nilIfEmpty(req.Version), req.Locale, req.Title, req.Body, req.Status).
		Scan(&it.ID, &it.Platform, &it.Version, &it.Locale, &it.Title, &it.Body,
			&it.Status, &it.PublishedAt, &it.CreatedAt)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "announcement.create", "announcement", it.ID, nil, map[string]any{
		"platform": it.Platform, "locale": it.Locale, "title": it.Title, "status": it.Status,
	}, "")
	httpapi.Created(w, map[string]any{"announcement": it})
}

func (m *Module) publishAnnouncement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "announcement not found")
		return
	}
	var beforeStatus string
	if err := m.pool.QueryRow(ctx, `SELECT status FROM announcements WHERE id = $1::uuid`, id).Scan(&beforeStatus); err != nil {
		m.fail(w, r, err)
		return
	}
	var it announcementItem
	if err := m.pool.QueryRow(ctx, `
		UPDATE announcements SET status = 'published', published_at = COALESCE(published_at, now())
		WHERE id = $1::uuid
		RETURNING id::text, platform, COALESCE(version,''), locale, title, body, status, published_at, created_at`, id).
		Scan(&it.ID, &it.Platform, &it.Version, &it.Locale, &it.Title, &it.Body,
			&it.Status, &it.PublishedAt, &it.CreatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "announcement.publish", "announcement", id,
		map[string]any{"status": beforeStatus}, map[string]any{"status": it.Status}, "")
	httpapi.OK(w, map[string]any{"announcement": it})
}
