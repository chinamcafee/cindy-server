package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type featureFlagItem struct {
	Key          string          `json:"key"`
	DefaultValue json.RawMessage `json:"defaultValue"`
	Description  string          `json:"description"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

func (m *Module) listFeatureFlags(w http.ResponseWriter, r *http.Request) {
	rows, err := m.pool.Query(r.Context(), `
		SELECT key, default_value, COALESCE(description,''), updated_at
		FROM feature_flags ORDER BY key`)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]featureFlagItem, 0)
	for rows.Next() {
		var it featureFlagItem
		var value []byte
		if err := rows.Scan(&it.Key, &value, &it.Description, &it.UpdatedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		it.DefaultValue = rawJSON(value)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	p := parsePagination(r)
	total := len(items)
	start := p.Offset
	if start > total {
		start = total
	}
	end := start + p.PageSize
	if end > total {
		end = total
	}
	httpapi.OK(w, newList(items[start:end], total, p))
}

func (m *Module) putFeatureFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" || len(key) > 128 {
		m.badRequest(w, "INVALID_PARAMS", "invalid feature flag key")
		return
	}
	var req struct {
		DefaultValue json.RawMessage `json:"defaultValue"`
		Description  *string         `json:"description"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	value := req.DefaultValue
	if len(value) == 0 || string(value) == "null" {
		value = json.RawMessage("{}")
	}
	var description *string
	if req.Description != nil {
		description = ptrString(strings.TrimSpace(*req.Description))
	}

	var beforeValue []byte
	var beforeDescription *string
	var before map[string]any
	err := m.pool.QueryRow(ctx,
		`SELECT default_value, description FROM feature_flags WHERE key = $1`, key).
		Scan(&beforeValue, &beforeDescription)
	if err == nil {
		before = map[string]any{"defaultValue": rawJSON(beforeValue), "description": beforeDescription}
	}

	var it featureFlagItem
	var outValue []byte
	if err := m.pool.QueryRow(ctx, `
		INSERT INTO feature_flags (key, default_value, description, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (key) DO UPDATE
		SET default_value = EXCLUDED.default_value, description = EXCLUDED.description, updated_at = now()
		RETURNING key, default_value, COALESCE(description,''), updated_at`,
		key, value, description).
		Scan(&it.Key, &outValue, &it.Description, &it.UpdatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	it.DefaultValue = rawJSON(outValue)
	m.auditEvent(r, "feature_flag.put", "feature_flag", key, before,
		map[string]any{"defaultValue": it.DefaultValue, "description": it.Description}, "")
	httpapi.OK(w, map[string]any{"flag": it})
}

func (m *Module) getEndpointManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	region := strings.TrimSpace(r.URL.Query().Get("region"))
	if region == "" {
		region = m.cfg.Region
	}
	var version int
	var payload []byte
	var status string
	var publishedAt *time.Time
	var createdAt *time.Time
	err := m.pool.QueryRow(ctx, `
		SELECT version, payload, status, published_at, created_at
		FROM upd_endpoint_manifests
		WHERE region = $1
		ORDER BY (status = 'published') DESC, version DESC
		LIMIT 1`, region).
		Scan(&version, &payload, &status, &publishedAt, &createdAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		m.fail(w, r, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.OK(w, map[string]any{
			"region": region, "version": 0, "manifest": json.RawMessage("{}"),
			"status": "", "publishedAt": nil, "updatedAt": nil,
		})
		return
	}
	httpapi.OK(w, map[string]any{
		"region": region, "version": version, "manifest": rawJSON(payload),
		"status": status, "publishedAt": publishedAt, "updatedAt": createdAt,
	})
}

func (m *Module) putEndpointManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Region   string          `json:"region"`
		Manifest json.RawMessage `json:"manifest"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = strings.TrimSpace(r.URL.Query().Get("region"))
	}
	if region == "" {
		region = m.cfg.Region
	}
	manifest := req.Manifest
	if len(manifest) == 0 || string(manifest) == "null" {
		manifest = req.Payload
	}
	if len(manifest) == 0 || string(manifest) == "null" || !json.Valid(manifest) {
		m.badRequest(w, "INVALID_PARAMS", "manifest is required and must be valid JSON")
		return
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(version),0) + 1 FROM upd_endpoint_manifests WHERE region = $1`, region).
		Scan(&version); err != nil {
		m.fail(w, r, err)
		return
	}
	var it struct {
		Region      string          `json:"region"`
		Version     int             `json:"version"`
		Manifest    json.RawMessage `json:"manifest"`
		Status      string          `json:"status"`
		PublishedAt *time.Time      `json:"publishedAt,omitempty"`
		UpdatedAt   *time.Time      `json:"updatedAt,omitempty"`
	}
	var payload []byte
	if err := tx.QueryRow(ctx, `
		INSERT INTO upd_endpoint_manifests (region, version, payload, status, published_at)
		VALUES ($1, $2, $3, 'published', now())
		RETURNING region, version, payload, status, published_at, created_at`,
		region, version, manifest).
		Scan(&it.Region, &it.Version, &payload, &it.Status, &it.PublishedAt, &it.UpdatedAt); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	it.Manifest = rawJSON(payload)
	m.auditEvent(r, "endpoint_manifest.put", "endpoint_manifest", region, nil,
		map[string]any{"region": region, "version": it.Version}, "")
	httpapi.OK(w, it)
}

func (m *Module) listAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	from, err := parseTimeParam(r.URL.Query().Get("from"), false)
	if err != nil {
		m.badRequest(w, "INVALID_PARAMS", "invalid from timestamp")
		return
	}
	to, err := parseTimeParam(r.URL.Query().Get("to"), true)
	if err != nil {
		m.badRequest(w, "INVALID_PARAMS", "invalid to timestamp")
		return
	}
	p := parsePagination(r)
	const where = `
		WHERE ($1 = '' OR actor_id = $1 OR actor_type = $1)
		  AND ($2 = '' OR action LIKE $2 || '%')
		  AND ($3 = '' OR target_id = $3 OR target_type = $3)
		  AND ($4::timestamptz IS NULL OR occurred_at >= $4)
		  AND ($5::timestamptz IS NULL OR occurred_at <= $5)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`+where, actor, action, target, from, to).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, occurred_at, region, actor_type, COALESCE(actor_id,''), COALESCE(actor_ip,''),
		       COALESCE(user_agent,''), action, COALESCE(target_type,''), COALESCE(target_id,''),
		       before, after, COALESCE(request_id,''), result, COALESCE(reason,'')
		FROM audit_events
		`+where+`
		ORDER BY occurred_at DESC
		LIMIT $6 OFFSET $7`, actor, action, target, from, to, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]auditItem, 0, p.PageSize)
	for rows.Next() {
		var it auditItem
		var before, after []byte
		if err := rows.Scan(&it.ID, &it.OccurredAt, &it.Region, &it.ActorType, &it.ActorID,
			&it.ActorIP, &it.UserAgent, &it.Action, &it.TargetType, &it.TargetID,
			&before, &after, &it.RequestID, &it.Result, &it.Reason); err != nil {
			m.fail(w, r, err)
			return
		}
		it.Before = nullableRawJSON(before)
		it.After = nullableRawJSON(after)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

type auditItem struct {
	ID         string          `json:"id"`
	OccurredAt time.Time       `json:"occurredAt"`
	Region     string          `json:"region"`
	ActorType  string          `json:"actorType"`
	ActorID    string          `json:"actorId"`
	ActorIP    string          `json:"actorIp"`
	UserAgent  string          `json:"userAgent"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	RequestID  string          `json:"requestId"`
	Result     string          `json:"result"`
	Reason     string          `json:"reason"`
}

func parseTimeParam(s string, endOfDay bool) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24 * time.Hour)
		}
		return &t, nil
	}
	return nil, errInvalidTime
}

var errInvalidTime = &httpapi.APIError{Status: http.StatusBadRequest, Code: "INVALID_PARAMS", Message: "invalid timestamp"}

func (m *Module) systemHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbStatus := "ok"
	if m.db == nil || m.db.Ping(ctx) != nil {
		dbStatus = "error"
	}
	redisStatus := "ok"
	if m.rdb == nil {
		redisStatus = "unavailable"
	} else if err := m.rdb.Ping(ctx).Err(); err != nil {
		redisStatus = "error"
	}
	var migrations int64 = -1
	if m.db != nil {
		if v, err := m.db.Version(ctx); err == nil {
			migrations = v
		}
	}
	httpapi.OK(w, map[string]any{
		"ok":            dbStatus == "ok" && redisStatus == "ok",
		"version":       buildVersion(),
		"migrations":    migrations,
		"db":            dbStatus,
		"redis":         redisStatus,
		"uptimeSeconds": int64(time.Since(m.start).Seconds()),
	})
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

type adminUserItem struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (m *Module) listAdmins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := parsePagination(r)
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, email::text, role, status, last_login_at, created_at
		FROM admin_users
		ORDER BY created_at
		LIMIT $1 OFFSET $2`, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]adminUserItem, 0, p.PageSize)
	for rows.Next() {
		var it adminUserItem
		if err := rows.Scan(&it.ID, &it.Email, &it.Role, &it.Status, &it.LastLoginAt, &it.CreatedAt); err != nil {
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

func (m *Module) createAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	email := normalizeEmail(req.Email)
	role := strings.TrimSpace(req.Role)
	if len(email) < 3 || len(email) > 254 || !strings.Contains(email, "@") {
		m.badRequest(w, "INVALID_PARAMS", "invalid email")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 1024 {
		m.badRequest(w, "INVALID_PARAMS", "password must be between 8 and 1024 characters")
		return
	}
	if role == "" {
		role = "readonly"
	}
	if !validRole[role] {
		m.badRequest(w, "INVALID_PARAMS", "invalid role")
		return
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	var it adminUserItem
	err = m.pool.QueryRow(ctx, `
		INSERT INTO admin_users (email, password_hash, role, status)
		VALUES ($1, $2, $3, 'active')
		RETURNING id::text, email::text, role, status, last_login_at, created_at`,
		email, hash, role).
		Scan(&it.ID, &it.Email, &it.Role, &it.Status, &it.LastLoginAt, &it.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			m.conflict(w, "ADMIN_EXISTS", "admin email already exists")
			return
		}
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "admin.create", "admin", it.ID, nil,
		map[string]any{"email": it.Email, "role": it.Role, "status": it.Status}, "")
	httpapi.Created(w, map[string]any{"admin": it})
}
