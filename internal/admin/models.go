package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type providerItem struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Region       string          `json:"region"`
	Status       string          `json:"status"`
	WireProtocol string          `json:"wireProtocol"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type modelItem struct {
	ID              string          `json:"id"`
	ProviderID      string          `json:"providerId"`
	ProviderSlug    string          `json:"providerSlug"`
	ProviderName    string          `json:"providerName"`
	ModelKey        string          `json:"modelKey"`
	DisplayName     string          `json:"displayName"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ContextWindow   *int            `json:"contextWindow,omitempty"`
	MaxOutputTokens *int            `json:"maxOutputTokens,omitempty"`
	Pricing         json.RawMessage `json:"pricing"`
	Currency        string          `json:"currency"`
	Media           json.RawMessage `json:"media"`
	Status          string          `json:"status"`
	Sort            int             `json:"sort"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type catalogRevisionItem struct {
	ID          string     `json:"id"`
	Version     int64      `json:"version"`
	Status      string     `json:"status"`
	ETag        string     `json:"etag"`
	Notes       string     `json:"notes"`
	CreatedBy   string     `json:"createdBy"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (m *Module) listProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	like := "%" + q + "%"
	p := parsePagination(r)
	const where = `WHERE ($1 = '' OR p.slug ILIKE $1 OR p.name ILIKE $1) AND ($2 = '' OR p.status = $2)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM ma_providers p `+where, like, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, slug, name, region, status, COALESCE(wire_protocol,''), metadata, created_at, updated_at
		FROM ma_providers p
		`+where+`
		ORDER BY name
		LIMIT $3 OFFSET $4`, like, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]providerItem, 0, p.PageSize)
	for rows.Next() {
		var it providerItem
		var metadata []byte
		if err := rows.Scan(&it.ID, &it.Slug, &it.Name, &it.Region, &it.Status,
			&it.WireProtocol, &metadata, &it.CreatedAt, &it.UpdatedAt); err != nil {
			m.fail(w, r, err)
			return
		}
		it.Metadata = rawJSON(metadata)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

func (m *Module) loadProvider(ctx context.Context, id string) (*providerItem, error) {
	var it providerItem
	var metadata []byte
	err := m.pool.QueryRow(ctx, `
		SELECT id::text, slug, name, region, status, COALESCE(wire_protocol,''), metadata, created_at, updated_at
		FROM ma_providers WHERE id = $1::uuid`, id).
		Scan(&it.ID, &it.Slug, &it.Name, &it.Region, &it.Status, &it.WireProtocol,
			&metadata, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, err
	}
	it.Metadata = rawJSON(metadata)
	return &it, nil
}

func (m *Module) createProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug         string          `json:"slug"`
		Name         string          `json:"name"`
		Region       string          `json:"region"`
		Status       string          `json:"status"`
		WireProtocol string          `json:"wireProtocol"`
		Metadata     json.RawMessage `json:"metadata"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	req.Name = strings.TrimSpace(req.Name)
	if req.Slug == "" || req.Name == "" {
		m.badRequest(w, "INVALID_PARAMS", "slug and name are required")
		return
	}
	if req.Region == "" {
		req.Region = m.cfg.Region
	}
	if req.Status == "" {
		req.Status = "active"
	}
	metadata := req.Metadata
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = json.RawMessage("{}")
	}
	id, err := m.insertProvider(r.Context(), req.Slug, req.Name, req.Region, req.Status, req.WireProtocol, metadata)
	if err != nil {
		if isUniqueViolation(err) {
			m.conflict(w, "PROVIDER_EXISTS", "provider slug already exists")
			return
		}
		m.fail(w, r, err)
		return
	}
	item, err := m.loadProvider(r.Context(), id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "provider.create", "provider", id, nil, map[string]any{
		"slug": item.Slug, "name": item.Name, "region": item.Region, "status": item.Status,
	}, "")
	httpapi.Created(w, map[string]any{"provider": item})
}

func (m *Module) insertProvider(ctx context.Context, slug, name, region, status, wire string, metadata json.RawMessage) (string, error) {
	var id string
	err := m.pool.QueryRow(ctx, `
		INSERT INTO ma_providers (slug, name, region, status, wire_protocol, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text`, slug, name, region, status, nullIfEmpty(wire), metadata).Scan(&id)
	return id, err
}

func (m *Module) updateProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "provider not found")
		return
	}
	var req struct {
		Name         *string         `json:"name"`
		Region       *string         `json:"region"`
		Status       *string         `json:"status"`
		WireProtocol *string         `json:"wireProtocol"`
		Metadata     json.RawMessage `json:"metadata"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	current, err := m.loadProvider(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	before := map[string]any{"name": current.Name, "region": current.Region, "status": current.Status, "wireProtocol": current.WireProtocol}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			m.badRequest(w, "INVALID_PARAMS", "name must not be empty")
			return
		}
		current.Name = name
	}
	if req.Region != nil && strings.TrimSpace(*req.Region) != "" {
		current.Region = strings.TrimSpace(*req.Region)
	}
	if req.Status != nil && strings.TrimSpace(*req.Status) != "" {
		current.Status = strings.TrimSpace(*req.Status)
	}
	if req.WireProtocol != nil {
		current.WireProtocol = strings.TrimSpace(*req.WireProtocol)
	}
	if len(req.Metadata) > 0 && string(req.Metadata) != "null" {
		var check map[string]any
		if err := json.Unmarshal(req.Metadata, &check); err != nil {
			m.badRequest(w, "INVALID_PARAMS", "metadata must be a JSON object")
			return
		}
		current.Metadata = req.Metadata
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE ma_providers
		SET name = $2, region = $3, status = $4, wire_protocol = $5, metadata = $6, updated_at = now()
		WHERE id = $1::uuid`, id, current.Name, current.Region, current.Status,
		nullIfEmpty(current.WireProtocol), current.Metadata); err != nil {
		m.fail(w, r, err)
		return
	}
	updated, err := m.loadProvider(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "provider.update", "provider", id, before, map[string]any{
		"name": updated.Name, "region": updated.Region, "status": updated.Status, "wireProtocol": updated.WireProtocol,
	}, "")
	httpapi.OK(w, map[string]any{"provider": updated})
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (m *Module) listModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	providerID := strings.TrimSpace(r.URL.Query().Get("providerId"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if providerID != "" && !validUUID(providerID) {
		m.badRequest(w, "INVALID_PARAMS", "providerId must be a UUID")
		return
	}
	like := "%" + q + "%"
	p := parsePagination(r)
	const where = `
		WHERE ($1 = '' OR m.model_key ILIKE $1 OR m.display_name ILIKE $1)
		  AND ($2 = '' OR m.provider_id::text = $2)
		  AND ($3 = '' OR m.status = $3)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM ma_models m `+where, like, providerID, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT m.id::text, m.provider_id::text, p.slug, p.name, m.model_key, m.display_name,
		       m.capabilities, m.context_window, m.max_output_tokens, m.pricing, m.currency,
		       m.media, m.status, m.sort, m.created_at, m.updated_at
		FROM ma_models m
		JOIN ma_providers p ON p.id = m.provider_id
		`+where+`
		ORDER BY p.name, m.sort, m.model_key
		LIMIT $4 OFFSET $5`, like, providerID, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	items, err := scanModels(rows)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, newList(items, total, p))
}

func scanModels(rows pgx.Rows) ([]modelItem, error) {
	defer rows.Close()
	items := make([]modelItem, 0)
	for rows.Next() {
		var it modelItem
		var capabilities, pricing, media []byte
		if err := rows.Scan(&it.ID, &it.ProviderID, &it.ProviderSlug, &it.ProviderName,
			&it.ModelKey, &it.DisplayName, &capabilities, &it.ContextWindow, &it.MaxOutputTokens,
			&pricing, &it.Currency, &media, &it.Status, &it.Sort, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Capabilities = rawJSON(capabilities)
		it.Pricing = rawJSON(pricing)
		it.Media = rawJSON(media)
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) loadModel(ctx context.Context, id string) (*modelItem, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT m.id::text, m.provider_id::text, p.slug, p.name, m.model_key, m.display_name,
		       m.capabilities, m.context_window, m.max_output_tokens, m.pricing, m.currency,
		       m.media, m.status, m.sort, m.created_at, m.updated_at
		FROM ma_models m
		JOIN ma_providers p ON p.id = m.provider_id
		WHERE m.id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	items, err := scanModels(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, pgx.ErrNoRows
	}
	return &items[0], nil
}

var modelStatuses = map[string]bool{"active": true, "preview": true, "deprecated": true, "retired": true}

func (m *Module) createModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		ProviderID      string          `json:"providerId"`
		ModelKey        string          `json:"modelKey"`
		DisplayName     string          `json:"displayName"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ContextWindow   *int            `json:"contextWindow"`
		MaxOutputTokens *int            `json:"maxOutputTokens"`
		Pricing         json.RawMessage `json:"pricing"`
		Currency        string          `json:"currency"`
		Media           json.RawMessage `json:"media"`
		Status          string          `json:"status"`
		Sort            *int            `json:"sort"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.ModelKey = strings.TrimSpace(req.ModelKey)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if !validUUID(req.ProviderID) {
		m.badRequest(w, "INVALID_PARAMS", "providerId is required")
		return
	}
	if req.ModelKey == "" || req.DisplayName == "" {
		m.badRequest(w, "INVALID_PARAMS", "modelKey and displayName are required")
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if !modelStatuses[req.Status] {
		m.badRequest(w, "INVALID_PARAMS", "invalid status")
		return
	}
	if req.Currency == "" {
		req.Currency = "USD"
	}
	capabilities := req.Capabilities
	if len(capabilities) == 0 || string(capabilities) == "null" {
		capabilities = json.RawMessage("{}")
	}
	pricing := req.Pricing
	if len(pricing) == 0 || string(pricing) == "null" {
		pricing = json.RawMessage("{}")
	}
	media := req.Media
	if len(media) == 0 || string(media) == "null" {
		media = json.RawMessage("{}")
	}
	sort := 0
	if req.Sort != nil {
		sort = *req.Sort
	}
	var id string
	err := m.pool.QueryRow(ctx, `
		INSERT INTO ma_models (provider_id, model_key, display_name, capabilities, context_window,
		                       max_output_tokens, pricing, currency, media, status, sort)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id::text`,
		req.ProviderID, req.ModelKey, req.DisplayName, capabilities, req.ContextWindow,
		req.MaxOutputTokens, pricing, req.Currency, media, req.Status, sort).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				m.conflict(w, "MODEL_EXISTS", "model key already exists for this provider")
				return
			case "23503":
				m.notFound(w, "provider not found")
				return
			}
		}
		m.fail(w, r, err)
		return
	}
	item, err := m.loadModel(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "model.create", "model", id, nil, map[string]any{
		"providerId": item.ProviderID, "modelKey": item.ModelKey, "displayName": item.DisplayName, "status": item.Status,
	}, "")
	httpapi.Created(w, map[string]any{"model": item})
}

func (m *Module) updateModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		m.notFound(w, "model not found")
		return
	}
	var req struct {
		DisplayName     *string         `json:"displayName"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ContextWindow   *int            `json:"contextWindow"`
		MaxOutputTokens *int            `json:"maxOutputTokens"`
		Pricing         json.RawMessage `json:"pricing"`
		Currency        *string         `json:"currency"`
		Media           json.RawMessage `json:"media"`
		Status          *string         `json:"status"`
		Sort            *int            `json:"sort"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	current, err := m.loadModel(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	before := map[string]any{
		"displayName": current.DisplayName, "capabilities": current.Capabilities,
		"contextWindow": current.ContextWindow, "maxOutputTokens": current.MaxOutputTokens,
		"pricing": current.Pricing, "currency": current.Currency, "media": current.Media,
		"status": current.Status, "sort": current.Sort,
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			m.badRequest(w, "INVALID_PARAMS", "displayName must not be empty")
			return
		}
		current.DisplayName = name
	}
	if req.Capabilities != nil && string(req.Capabilities) != "null" {
		current.Capabilities = req.Capabilities
	}
	if req.ContextWindow != nil {
		current.ContextWindow = req.ContextWindow
	}
	if req.MaxOutputTokens != nil {
		current.MaxOutputTokens = req.MaxOutputTokens
	}
	if req.Pricing != nil && string(req.Pricing) != "null" {
		current.Pricing = req.Pricing
	}
	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		current.Currency = strings.TrimSpace(*req.Currency)
	}
	if req.Media != nil && string(req.Media) != "null" {
		current.Media = req.Media
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if !modelStatuses[status] {
			m.badRequest(w, "INVALID_PARAMS", "invalid status")
			return
		}
		current.Status = status
	}
	if req.Sort != nil {
		current.Sort = *req.Sort
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE ma_models
		SET display_name = $2, capabilities = $3, context_window = $4, max_output_tokens = $5,
		    pricing = $6, currency = $7, media = $8, status = $9, sort = $10, updated_at = now()
		WHERE id = $1::uuid`, id, current.DisplayName, current.Capabilities, current.ContextWindow,
		current.MaxOutputTokens, current.Pricing, current.Currency, current.Media,
		current.Status, current.Sort); err != nil {
		m.fail(w, r, err)
		return
	}
	updated, err := m.loadModel(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "model.update", "model", id, before, map[string]any{
		"displayName": updated.DisplayName, "capabilities": updated.Capabilities,
		"contextWindow": updated.ContextWindow, "maxOutputTokens": updated.MaxOutputTokens,
		"pricing": updated.Pricing, "currency": updated.Currency, "media": updated.Media,
		"status": updated.Status, "sort": updated.Sort,
	}, "")
	httpapi.OK(w, map[string]any{"model": updated})
}

func (m *Module) listCatalogRevisions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := parsePagination(r)
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM ma_catalog_revisions`).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, version, status, COALESCE(etag,''), COALESCE(notes,''), COALESCE(created_by,''),
		       published_at, created_at
		FROM ma_catalog_revisions
		ORDER BY version DESC
		LIMIT $1 OFFSET $2`, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]catalogRevisionItem, 0, p.PageSize)
	for rows.Next() {
		var it catalogRevisionItem
		if err := rows.Scan(&it.ID, &it.Version, &it.Status, &it.ETag, &it.Notes,
			&it.CreatedBy, &it.PublishedAt, &it.CreatedAt); err != nil {
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

type catalogSnapshotModel struct {
	ID              string          `json:"id"`
	ProviderID      string          `json:"providerId"`
	ProviderSlug    string          `json:"providerSlug"`
	ProviderName    string          `json:"providerName"`
	ModelKey        string          `json:"modelKey"`
	DisplayName     string          `json:"displayName"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ContextWindow   *int            `json:"contextWindow,omitempty"`
	MaxOutputTokens *int            `json:"maxOutputTokens,omitempty"`
	Pricing         json.RawMessage `json:"pricing"`
	Currency        string          `json:"currency"`
	Media           json.RawMessage `json:"media"`
	Status          string          `json:"status"`
	Sort            int             `json:"sort"`
}

type catalogRegistry struct {
	Version     int64                  `json:"version"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Models      []catalogSnapshotModel `json:"models"`
}

func (m *Module) publishCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Notes string `json:"notes"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	s := adminFrom(ctx)
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) + 1 FROM ma_catalog_revisions`).Scan(&version); err != nil {
		m.fail(w, r, err)
		return
	}
	registry, etag, err := m.buildCatalogSnapshotWithQuerier(ctx, tx, version)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rev, err := insertCatalogRevision(ctx, tx, version, registry, etag, strings.TrimSpace(req.Notes), s.Email)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE ma_catalog_revisions SET status = 'archived' WHERE status = 'published' AND version <> $1`, version); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "catalog.publish", "catalog_revision", rev.ID, nil, map[string]any{
		"version": rev.Version, "etag": rev.ETag,
	}, "")
	httpapi.Created(w, map[string]any{"revision": rev})
}

func (m *Module) buildCatalogSnapshotWithQuerier(ctx context.Context, tx pgx.Tx, version int64) (json.RawMessage, string, error) {
	rows, err := tx.Query(ctx, `
		SELECT m.id::text, m.provider_id::text, p.slug, p.name, m.model_key, m.display_name,
		       m.capabilities, m.context_window, m.max_output_tokens, m.pricing, m.currency,
		       m.media, m.status, m.sort
		FROM ma_models m
		JOIN ma_providers p ON p.id = m.provider_id
		WHERE m.status <> 'retired'
		ORDER BY p.slug, m.sort, m.model_key`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	models := make([]catalogSnapshotModel, 0)
	for rows.Next() {
		var it catalogSnapshotModel
		var capabilities, pricing, media []byte
		if err := rows.Scan(&it.ID, &it.ProviderID, &it.ProviderSlug, &it.ProviderName,
			&it.ModelKey, &it.DisplayName, &capabilities, &it.ContextWindow, &it.MaxOutputTokens,
			&pricing, &it.Currency, &media, &it.Status, &it.Sort); err != nil {
			return nil, "", err
		}
		it.Capabilities = rawJSON(capabilities)
		it.Pricing = rawJSON(pricing)
		it.Media = rawJSON(media)
		models = append(models, it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(catalogRegistry{Version: version, GeneratedAt: time.Now().UTC(), Models: models})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func insertCatalogRevision(ctx context.Context, tx pgx.Tx, version int64, registry json.RawMessage, etag, notes, createdBy string) (*catalogRevisionItem, error) {
	var rev catalogRevisionItem
	err := tx.QueryRow(ctx, `
		INSERT INTO ma_catalog_revisions (version, status, registry, etag, notes, created_by, published_at)
		VALUES ($1, 'published', $2, $3, $4, $5, now())
		RETURNING id::text, version, status, COALESCE(etag,''), COALESCE(notes,''), COALESCE(created_by,''),
		          published_at, created_at`,
		version, registry, etag, nilIfEmpty(notes), nilIfEmpty(createdBy)).
		Scan(&rev.ID, &rev.Version, &rev.Status, &rev.ETag, &rev.Notes, &rev.CreatedBy,
			&rev.PublishedAt, &rev.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (m *Module) rollbackCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Version int64 `json:"version"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if req.Version <= 0 {
		m.badRequest(w, "INVALID_PARAMS", "version is required")
		return
	}
	s := adminFrom(ctx)
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var registry []byte
	var etag string
	if err := tx.QueryRow(ctx, `
		SELECT registry, COALESCE(etag,'') FROM ma_catalog_revisions WHERE version = $1`, req.Version).
		Scan(&registry, &etag); err != nil {
		m.fail(w, r, err)
		return
	}
	var next int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) + 1 FROM ma_catalog_revisions`).Scan(&next); err != nil {
		m.fail(w, r, err)
		return
	}
	rev, err := insertCatalogRevision(ctx, tx, next, registry, etag,
		"rollback to v"+strconv.FormatInt(req.Version, 10), s.Email)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE ma_catalog_revisions SET status = 'archived' WHERE status = 'published' AND version <> $1`, next); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "catalog.rollback", "catalog_revision", rev.ID, nil, map[string]any{
		"version": rev.Version, "rollbackTo": req.Version,
	}, "")
	httpapi.Created(w, map[string]any{"revision": rev})
}
