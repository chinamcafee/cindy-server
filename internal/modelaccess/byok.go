package modelaccess

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type byokModel struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	Mode             string                   `json:"mode,omitempty"`
	Agents           []string                 `json:"agents"`
	PerAgent         map[string]agentOverride `json:"perAgent"`
	ContextWindow    int                      `json:"contextWindow"`
	MaxOutputTokens  int                      `json:"maxOutputTokens,omitempty"`
	Efforts          []string                 `json:"efforts"`
	DefaultEffort    *string                  `json:"defaultEffort"`
	SupportsFastMode bool                     `json:"supportsFastMode,omitempty"`
	DefaultEnabled   bool                     `json:"defaultEnabled"`
	SortOrder        int                      `json:"sortOrder,omitempty"`
	Currency         string                   `json:"currency,omitempty"`
}

type byokProvider struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	ConnectionRevision int64       `json:"connectionRevision"`
	Models             []byokModel `json:"models"`
}

type byokProvidersResponse struct {
	SchemaVersion  int            `json:"schemaVersion"`
	OrganizationID string         `json:"organizationId"`
	Revision       string         `json:"revision"`
	Providers      []byokProvider `json:"providers"`
}

type byokCredential struct {
	ProviderID         string `json:"providerId"`
	ConnectionRevision int64  `json:"connectionRevision"`
	Status             string `json:"status"`
	Endpoint           string `json:"endpoint,omitempty"`
	APIKey             string `json:"apiKey,omitempty"`
	Code               string `json:"code,omitempty"`
}

type byokCredentialsResponse struct {
	SchemaVersion  int              `json:"schemaVersion"`
	OrganizationID string           `json:"organizationId"`
	Credentials    []byokCredential `json:"credentials"`
}

func (m *Module) membershipOrg(ctx context.Context, membershipID string) string {
	if m.db == nil || !isUUID(membershipID) {
		return ""
	}
	var orgID string
	err := m.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(m.org_id::text,'')
		FROM auth_memberships m
		WHERE m.id::text = $1 AND m.kind = 'org' AND m.status = 'active'`, membershipID).Scan(&orgID)
	if err != nil {
		return ""
	}
	return orgID
}

func byokProviderID(slug string) string {
	id := strings.Trim(providerID(slug), "-")
	if id == "" {
		id = "provider"
	}
	return "byok-" + id
}

func byokAgent(wire string) (string, string) {
	switch normalizeWireProtocol(wire) {
	case "anthropic-messages":
		return "claude-code", "anthropic-messages"
	case "openai-completions":
		return "pi", "openai-completions"
	default:
		return "codex", "openai-responses"
	}
}

func revisionOf(t time.Time) int64 {
	if t.IsZero() {
		return 1
	}
	if t.Unix() <= 0 {
		return 1
	}
	return t.Unix()
}

func (m *Module) byokProviders(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("schemaVersion") != "1" {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "schemaVersion must be 1"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	orgID := m.membershipOrg(r.Context(), p.MembershipID)
	res := byokProvidersResponse{
		SchemaVersion:  1,
		OrganizationID: p.AccountID,
		Revision:       "1",
		Providers:      []byokProvider{},
	}
	if orgID == "" {
		httpapi.OK(w, res)
		return
	}
	res.OrganizationID = orgID
	if m.db == nil {
		httpapi.OK(w, res)
		return
	}

	type providerRow struct {
		id, slug, name, wire string
		updated              time.Time
	}
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT p.id::text, p.slug, p.name, COALESCE(p.wire_protocol,''), p.updated_at
		FROM ma_byok_policies pol
		JOIN ma_providers p ON p.id = pol.provider_id
		WHERE pol.enabled AND p.status = 'active'
		ORDER BY p.slug`)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	var providerRows []providerRow
	for rows.Next() {
		var row providerRow
		if err := rows.Scan(&row.id, &row.slug, &row.name, &row.wire, &row.updated); err != nil {
			rows.Close()
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		providerRows = append(providerRows, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	modelsByProvider := map[string][]modelRow{}
	models, err := m.loadModels(r.Context())
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	for _, row := range models {
		modelsByProvider[row.ProviderID] = append(modelsByProvider[row.ProviderID], row)
	}

	var maxUpdated time.Time
	for _, row := range providerRows {
		agent, wire := byokAgent(row.wire)
		var modelsOut []byokModel
		for _, mr := range modelsByProvider[row.id] {
			efforts, def := normalizeEfforts(mr.Caps.Efforts, mr.Caps.DefaultEffort)
			modelsOut = append(modelsOut, byokModel{
				ID:               mr.Key,
				Name:             mr.Name,
				Mode:             mr.mode(),
				Agents:           []string{agent},
				PerAgent:         map[string]agentOverride{agent: {WireProtocol: wire}},
				ContextWindow:    mr.contextWindow(),
				MaxOutputTokens:  mr.maxOutput(),
				Efforts:          efforts,
				DefaultEffort:    def,
				SupportsFastMode: mr.Caps.SupportsFastMode,
				DefaultEnabled:   mr.defaultEnabled(),
				SortOrder:        mr.sortOrder(),
				Currency:         mr.Currency,
			})
		}
		if len(modelsOut) == 0 {
			continue
		}
		res.Providers = append(res.Providers, byokProvider{
			ID:                 byokProviderID(row.slug),
			Name:               row.name,
			ConnectionRevision: revisionOf(row.updated),
			Models:             modelsOut,
		})
		if row.updated.After(maxUpdated) {
			maxUpdated = row.updated
		}
	}
	res.Revision = strconv.FormatInt(revisionOf(maxUpdated), 10)
	httpapi.OK(w, res)
}

func (m *Module) byokCredentials(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("schemaVersion") != "1" {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "schemaVersion must be 1"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	res := byokCredentialsResponse{
		SchemaVersion:  1,
		OrganizationID: p.AccountID,
		Credentials:    []byokCredential{},
	}
	if orgID := m.membershipOrg(r.Context(), p.MembershipID); orgID != "" {
		res.OrganizationID = orgID
	}
	if m.db == nil {
		httpapi.OK(w, res)
		return
	}
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT c.id::text, c.status, c.label, p.slug, COALESCE(p.metadata->>'endpoint',''),
		       c.ciphertext, c.nonce, p.updated_at
		FROM ma_credentials c
		JOIN ma_providers p ON p.id = c.provider_id
		WHERE c.account_id::text = $1 AND c.status <> 'revoked'
		ORDER BY c.created_at`, p.AccountID)
	if err != nil {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, status, slug, endpoint string
		var label *string
		var ciphertext, nonce []byte
		var updated time.Time
		if err := rows.Scan(&id, &status, &label, &slug, &endpoint, &ciphertext, &nonce, &updated); err != nil {
			httpapi.WriteError(w, apiErrorForByok())
			return
		}
		cred := byokCredential{
			ProviderID:         byokProviderID(slug),
			ConnectionRevision: revisionOf(updated),
			Status:             status,
		}
		switch status {
		case "ready":
			secret := ""
			if m.box != nil {
				if plain, err := m.box.Decrypt(ciphertext, nonce, id); err == nil {
					secret = string(plain)
				}
			}
			if secret == "" {
				cred.Status = "error"
				cred.Code = "UNAVAILABLE"
			} else {
				cred.Endpoint = endpoint
				if cred.Endpoint == "" {
					cred.Endpoint = m.gatewayEndpoint()
				}
				cred.APIKey = secret
			}
		case "error":
			cred.Code = "UNAVAILABLE"
		case "pending":
		default:
			cred.Status = "error"
			cred.Code = "UNAVAILABLE"
		}
		res.Credentials = append(res.Credentials, cred)
	}
	if err := rows.Err(); err != nil {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}
	httpapi.OK(w, res)
}

func apiErrorForByok() *httpapi.APIError {
	return httpapi.Err(http.StatusBadGateway, "ORG_AI_GATEWAY_ERROR", "byok gateway error")
}

func (m *Module) byokCreateCredential(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID string `json:"providerId"`
		Label      string `json:"label"`
		Secret     string `json:"secret"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.Secret = strings.TrimSpace(req.Secret)
	if req.ProviderID == "" || len(req.ProviderID) > 256 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "providerId is required"))
		return
	}
	if req.Secret == "" || len(req.Secret) > 8192 || hasControlChar(req.Secret) {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "secret is invalid"))
		return
	}
	if len(req.Label) > 128 || hasControlChar(req.Label) {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "label is invalid"))
		return
	}
	if m.db == nil || m.box == nil {
		httpapi.WriteError(w, unavailable("BYOK_UNAVAILABLE", "byok is unavailable"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())

	slug := strings.TrimPrefix(req.ProviderID, "byok-")
	var providerUUID, providerSlug string
	err := m.db.Pool.QueryRow(r.Context(), `
		SELECT id::text, slug FROM ma_providers
		WHERE slug = $1 OR id::text = $2
		LIMIT 1`, slug, req.ProviderID).Scan(&providerUUID, &providerSlug)
	if err != nil {
		httpapi.WriteError(w, notFound("BYOK_PROVIDER_NOT_FOUND", "byok provider not found"))
		return
	}
	var policyEnabled bool
	err = m.db.Pool.QueryRow(r.Context(), `SELECT enabled FROM ma_byok_policies WHERE provider_id::text = $1`, providerUUID).Scan(&policyEnabled)
	if err == nil && !policyEnabled {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	if err != nil && err != pgx.ErrNoRows {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}

	id := uuid.NewString()
	ciphertext, nonce, err := m.box.Encrypt([]byte(req.Secret), id)
	if err != nil {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}
	var label any
	if req.Label != "" {
		label = req.Label
	}
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO ma_credentials (id, account_id, provider_id, label, ciphertext, nonce, key_version, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 1, 'ready')`,
		id, p.AccountID, providerUUID, label, ciphertext, nonce); err != nil {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}
	m.auditEvent(r, "model_access.byok.credential.create", "ma_credential", id, map[string]any{
		"providerId": byokProviderID(providerSlug),
	})
	httpapi.OK(w, map[string]any{"id": id, "status": "ready", "providerId": byokProviderID(providerSlug)})
}

func (m *Module) byokRevokeCredential(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	id := strings.TrimSpace(chiURLParam(r, "id"))
	if m.db == nil || !isUUID(id) {
		httpapi.WriteError(w, notFound("BYOK_CREDENTIAL_NOT_FOUND", "credential not found"))
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE ma_credentials SET status = 'revoked', updated_at = now()
		WHERE id::text = $1 AND account_id::text = $2 AND status <> 'revoked'`, id, p.AccountID)
	if err != nil {
		httpapi.WriteError(w, apiErrorForByok())
		return
	}
	if tag.RowsAffected() == 0 {
		httpapi.WriteError(w, notFound("BYOK_CREDENTIAL_NOT_FOUND", "credential not found"))
		return
	}
	m.auditEvent(r, "model_access.byok.credential.revoke", "ma_credential", id, nil)
	httpapi.OK(w, map[string]any{"id": id, "status": "revoked"})
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
