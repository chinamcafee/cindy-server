package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type orgListItem struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Region       string    `json:"region"`
	Status       string    `json:"status"`
	Plan         string    `json:"plan"`
	LogoURL      *string   `json:"logoUrl,omitempty"`
	SSORequired  bool      `json:"ssoRequired"`
	PluginPrefix *string   `json:"pluginPrefix,omitempty"`
	MemberCount  int64     `json:"memberCount"`
	DomainCount  int64     `json:"domainCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type orgDetail struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Region       string          `json:"region"`
	Status       string          `json:"status"`
	Plan         string          `json:"plan"`
	LogoURL      *string         `json:"logoUrl,omitempty"`
	SSORequired  bool            `json:"ssoRequired"`
	PluginPrefix *string         `json:"pluginPrefix,omitempty"`
	Settings     json.RawMessage `json:"settings"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type orgMember struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Kind        string    `json:"kind"`
	Role        string    `json:"role"`
	DisplayName string    `json:"displayName"`
	Email       string    `json:"email"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

type orgDomain struct {
	ID                string     `json:"id"`
	Domain            string     `json:"domain"`
	VerifiedAt        *time.Time `json:"verifiedAt,omitempty"`
	VerificationToken string     `json:"verificationToken"`
	CreatedAt         time.Time  `json:"createdAt"`
}

type orgSSO struct {
	ID        string          `json:"id"`
	Protocol  string          `json:"protocol"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	Status    string          `json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (m *Module) listOrganizations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	like := "%" + q + "%"
	p := parsePagination(r)
	const where = `
		WHERE ($1 = '' OR o.name ILIKE $1 OR o.slug::text ILIKE $1)
		  AND ($2 = '' OR o.status = $2)`
	var total int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM auth_organizations o `+where, like, status).Scan(&total); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT o.id::text, o.slug::text, o.name, o.region, o.status, o.plan,
		       o.logo_url, o.sso_required, o.plugin_prefix, o.created_at, o.updated_at,
		       (SELECT count(*) FROM auth_memberships m WHERE m.org_id = o.id),
		       (SELECT count(*) FROM auth_org_domains d WHERE d.org_id = o.id)
		FROM auth_organizations o
		`+where+`
		ORDER BY o.created_at DESC
		LIMIT $3 OFFSET $4`, like, status, p.PageSize, p.Offset)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]orgListItem, 0, p.PageSize)
	for rows.Next() {
		var it orgListItem
		if err := rows.Scan(&it.ID, &it.Slug, &it.Name, &it.Region, &it.Status, &it.Plan,
			&it.LogoURL, &it.SSORequired, &it.PluginPrefix, &it.CreatedAt, &it.UpdatedAt,
			&it.MemberCount, &it.DomainCount); err != nil {
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

func (m *Module) loadOrg(ctx context.Context, orgID string) (*orgDetail, error) {
	var o orgDetail
	var settings []byte
	err := m.pool.QueryRow(ctx, `
		SELECT id::text, slug::text, name, region, status, plan, logo_url, sso_required,
		       plugin_prefix, settings, created_at, updated_at
		FROM auth_organizations WHERE id = $1::uuid`, orgID).
		Scan(&o.ID, &o.Slug, &o.Name, &o.Region, &o.Status, &o.Plan, &o.LogoURL,
			&o.SSORequired, &o.PluginPrefix, &settings, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	o.Settings = rawJSON(settings)
	return &o, nil
}

func (m *Module) organizationDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	if !validUUID(orgID) {
		m.notFound(w, "organization not found")
		return
	}
	org, err := m.loadOrg(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	members, err := m.loadOrgMembers(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	domains, err := m.loadOrgDomains(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	sso, err := m.loadOrgSSO(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, map[string]any{
		"organization": org,
		"members":      members,
		"domains":      domains,
		"sso":          sso,
	})
}

func (m *Module) loadOrgMembers(ctx context.Context, orgID string) ([]orgMember, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, account_id::text, kind, role, display_name, COALESCE(email::text,''), status, created_at
		FROM auth_memberships
		WHERE org_id = $1::uuid
		ORDER BY (role = 'owner') DESC, created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]orgMember, 0)
	for rows.Next() {
		var it orgMember
		if err := rows.Scan(&it.ID, &it.AccountID, &it.Kind, &it.Role, &it.DisplayName,
			&it.Email, &it.Status, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) loadOrgDomains(ctx context.Context, orgID string) ([]orgDomain, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, domain::text, verified_at, COALESCE(verification_token,''), created_at
		FROM auth_org_domains
		WHERE org_id = $1::uuid
		ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]orgDomain, 0)
	for rows.Next() {
		var it orgDomain
		if err := rows.Scan(&it.ID, &it.Domain, &it.VerifiedAt, &it.VerificationToken, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) loadOrgSSO(ctx context.Context, orgID string) ([]orgSSO, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, protocol, name, config, enabled, status, created_at, updated_at
		FROM auth_sso_connections
		WHERE org_id = $1::uuid
		ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]orgSSO, 0)
	for rows.Next() {
		var it orgSSO
		var config []byte
		if err := rows.Scan(&it.ID, &it.Protocol, &it.Name, &config, &it.Enabled,
			&it.Status, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Config = rawJSON(config)
		items = append(items, it)
	}
	return items, rows.Err()
}

func (m *Module) updateOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	if !validUUID(orgID) {
		m.notFound(w, "organization not found")
		return
	}
	var req struct {
		Name         *string         `json:"name"`
		LogoURL      *string         `json:"logoUrl"`
		SSORequired  *bool           `json:"ssoRequired"`
		PluginPrefix *string         `json:"pluginPrefix"`
		Settings     json.RawMessage `json:"settings"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	org, err := m.loadOrg(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	before := map[string]any{
		"name": org.Name, "logoUrl": org.LogoURL, "ssoRequired": org.SSORequired,
		"pluginPrefix": org.PluginPrefix, "settings": org.Settings,
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			m.badRequest(w, "INVALID_PARAMS", "name must not be empty")
			return
		}
		org.Name = name
	}
	if req.LogoURL != nil {
		org.LogoURL = ptrString(strings.TrimSpace(*req.LogoURL))
	}
	if req.SSORequired != nil {
		org.SSORequired = *req.SSORequired
	}
	if req.PluginPrefix != nil {
		org.PluginPrefix = ptrString(strings.TrimSpace(*req.PluginPrefix))
	}
	if len(req.Settings) > 0 && string(req.Settings) != "null" {
		var check map[string]any
		if err := json.Unmarshal(req.Settings, &check); err != nil {
			m.badRequest(w, "INVALID_PARAMS", "settings must be a JSON object")
			return
		}
		org.Settings = req.Settings
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE auth_organizations
		SET name = $2, logo_url = $3, sso_required = $4, plugin_prefix = $5, settings = $6, updated_at = now()
		WHERE id = $1::uuid`, orgID, org.Name, org.LogoURL, org.SSORequired, org.PluginPrefix, org.Settings); err != nil {
		m.fail(w, r, err)
		return
	}
	updated, err := m.loadOrg(ctx, orgID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "org.update", "organization", orgID, before, map[string]any{
		"name": updated.Name, "logoUrl": updated.LogoURL, "ssoRequired": updated.SSORequired,
		"pluginPrefix": updated.PluginPrefix, "settings": updated.Settings,
	}, "")
	httpapi.OK(w, map[string]any{"organization": updated})
}

func (m *Module) createOrgDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	if !validUUID(orgID) {
		m.notFound(w, "organization not found")
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if len(domain) < 3 || len(domain) > 253 || !strings.Contains(domain, ".") || strings.ContainsAny(domain, " /:@\t\n") {
		m.badRequest(w, "INVALID_PARAMS", "invalid domain")
		return
	}
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		m.fail(w, r, err)
		return
	}
	if !exists {
		m.notFound(w, "organization not found")
		return
	}
	token := crypto.RandomToken(16)
	var d orgDomain
	err := m.pool.QueryRow(ctx, `
		INSERT INTO auth_org_domains (org_id, domain, verification_token)
		VALUES ($1::uuid, $2, $3)
		RETURNING id::text, domain::text, verified_at, COALESCE(verification_token,''), created_at`,
		orgID, domain, token).
		Scan(&d.ID, &d.Domain, &d.VerifiedAt, &d.VerificationToken, &d.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			m.conflict(w, "DOMAIN_EXISTS", "domain is already registered")
			return
		}
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "org.domain.create", "org_domain", d.ID,
		nil, map[string]any{"orgId": orgID, "domain": d.Domain}, "")
	httpapi.Created(w, map[string]any{"domain": d})
}

func (m *Module) verifyOrgDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	domainID := chi.URLParam(r, "domainId")
	if !validUUID(orgID) || !validUUID(domainID) {
		m.notFound(w, "domain not found")
		return
	}
	var d orgDomain
	err := m.pool.QueryRow(ctx, `
		UPDATE auth_org_domains
		SET verified_at = COALESCE(verified_at, now())
		WHERE id = $1::uuid AND org_id = $2::uuid
		RETURNING id::text, domain::text, verified_at, COALESCE(verification_token,''), created_at`,
		domainID, orgID).
		Scan(&d.ID, &d.Domain, &d.VerifiedAt, &d.VerificationToken, &d.CreatedAt)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "org.domain.verify", "org_domain", d.ID,
		nil, map[string]any{"orgId": orgID, "domain": d.Domain, "verifiedAt": d.VerifiedAt}, "")
	httpapi.OK(w, map[string]any{"domain": d})
}

func (m *Module) createOrgSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	if !validUUID(orgID) {
		m.notFound(w, "organization not found")
		return
	}
	var req struct {
		Protocol string          `json:"protocol"`
		Name     string          `json:"name"`
		Config   json.RawMessage `json:"config"`
		Enabled  bool            `json:"enabled"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Protocol = strings.ToLower(strings.TrimSpace(req.Protocol))
	if req.Protocol != "oidc" && req.Protocol != "saml" && req.Protocol != "cas" {
		m.badRequest(w, "INVALID_PARAMS", "protocol must be oidc, saml or cas")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		m.badRequest(w, "INVALID_PARAMS", "name is required")
		return
	}
	config := req.Config
	if len(config) == 0 || string(config) == "null" {
		config = json.RawMessage("{}")
	}
	var check map[string]any
	if err := json.Unmarshal(config, &check); err != nil {
		m.badRequest(w, "INVALID_PARAMS", "config must be a JSON object")
		return
	}
	var s orgSSO
	var rawConfig []byte
	err := m.pool.QueryRow(ctx, `
		INSERT INTO auth_sso_connections (org_id, protocol, name, config, enabled)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text, protocol, name, config, enabled, status, created_at, updated_at`,
		orgID, req.Protocol, req.Name, config, req.Enabled).
		Scan(&s.ID, &s.Protocol, &s.Name, &rawConfig, &s.Enabled, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	s.Config = rawJSON(rawConfig)
	m.auditEvent(r, "org.sso.create", "sso_connection", s.ID,
		nil, map[string]any{"orgId": orgID, "protocol": s.Protocol, "name": s.Name, "enabled": s.Enabled}, "")
	httpapi.Created(w, map[string]any{"sso": s})
}

func (m *Module) updateOrgSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := chi.URLParam(r, "id")
	ssoID := chi.URLParam(r, "ssoId")
	if !validUUID(orgID) || !validUUID(ssoID) {
		m.notFound(w, "sso connection not found")
		return
	}
	var req struct {
		Name    *string         `json:"name"`
		Config  json.RawMessage `json:"config"`
		Enabled *bool           `json:"enabled"`
		Status  *string         `json:"status"`
	}
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	var current orgSSO
	var rawConfig []byte
	err := m.pool.QueryRow(ctx, `
		SELECT id::text, protocol, name, config, enabled, status, created_at, updated_at
		FROM auth_sso_connections
		WHERE id = $1::uuid AND org_id = $2::uuid`, ssoID, orgID).
		Scan(&current.ID, &current.Protocol, &current.Name, &rawConfig, &current.Enabled,
			&current.Status, &current.CreatedAt, &current.UpdatedAt)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	current.Config = rawJSON(rawConfig)
	before := map[string]any{"name": current.Name, "config": current.Config, "enabled": current.Enabled, "status": current.Status}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			m.badRequest(w, "INVALID_PARAMS", "name must not be empty")
			return
		}
		current.Name = name
	}
	if len(req.Config) > 0 && string(req.Config) != "null" {
		var check map[string]any
		if err := json.Unmarshal(req.Config, &check); err != nil {
			m.badRequest(w, "INVALID_PARAMS", "config must be a JSON object")
			return
		}
		current.Config = req.Config
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if status == "" {
			m.badRequest(w, "INVALID_PARAMS", "status must not be empty")
			return
		}
		current.Status = status
	}
	if _, err := m.pool.Exec(ctx, `
		UPDATE auth_sso_connections
		SET name = $3, config = $4, enabled = $5, status = $6, updated_at = now()
		WHERE id = $1::uuid AND org_id = $2::uuid`,
		ssoID, orgID, current.Name, current.Config, current.Enabled, current.Status); err != nil {
		m.fail(w, r, err)
		return
	}
	updated, err := m.loadOrgSSOByID(ctx, ssoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w, "sso connection not found")
			return
		}
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "org.sso.update", "sso_connection", ssoID, before, map[string]any{
		"name": updated.Name, "config": updated.Config, "enabled": updated.Enabled, "status": updated.Status,
	}, "")
	httpapi.OK(w, map[string]any{"sso": updated})
}

func (m *Module) loadOrgSSOByID(ctx context.Context, ssoID string) (*orgSSO, error) {
	var s orgSSO
	var rawConfig []byte
	err := m.pool.QueryRow(ctx, `
		SELECT id::text, protocol, name, config, enabled, status, created_at, updated_at
		FROM auth_sso_connections WHERE id = $1::uuid`, ssoID).
		Scan(&s.ID, &s.Protocol, &s.Name, &rawConfig, &s.Enabled, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.Config = rawJSON(rawConfig)
	return &s, nil
}
