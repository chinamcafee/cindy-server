package modelaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type agentOverride struct {
	ContextWindow    *int     `json:"contextWindow,omitempty"`
	Efforts          []string `json:"efforts,omitempty"`
	DefaultEffort    *string  `json:"defaultEffort,omitempty"`
	SupportsFastMode *bool    `json:"supportsFastMode,omitempty"`
	DefaultEnabled   *bool    `json:"defaultEnabled,omitempty"`
	WireProtocol     string   `json:"wireProtocol,omitempty"`
}

type modelCaps struct {
	Group             string                   `json:"group"`
	Description       string                   `json:"description"`
	Mode              string                   `json:"mode"`
	Agents            []string                 `json:"agents"`
	ContextWindow     int                      `json:"contextWindow"`
	MaxOutputTokens   int                      `json:"maxOutputTokens"`
	Modalities        *modalities              `json:"modalities"`
	Efforts           []string                 `json:"efforts"`
	DefaultEffort     *string                  `json:"defaultEffort"`
	SupportsFastMode  bool                     `json:"supportsFastMode"`
	DefaultEnabled    *bool                    `json:"defaultEnabled"`
	PerAgent          map[string]agentOverride `json:"perAgent"`
	NewSessionDefault []string                 `json:"newSessionDefault"`
	SortOrder         *int                     `json:"sortOrder"`
	NativeAPI         string                   `json:"nativeApi"`
}

type modelPricing struct {
	CostDiscount                *float64 `json:"costDiscount"`
	InputCostPerToken           *float64 `json:"inputCostPerToken"`
	OutputCostPerToken          *float64 `json:"outputCostPerToken"`
	InputPerMillion             *float64 `json:"inputPerMillion"`
	OutputPerMillion            *float64 `json:"outputPerMillion"`
	CacheReadInputTokenCost     *float64 `json:"cacheReadInputTokenCost"`
	CacheCreationInputTokenCost *float64 `json:"cacheCreationInputTokenCost"`
}

type modelRow struct {
	ProviderID        string
	ProviderSlug      string
	ProviderName      string
	ProviderWire      string
	ProviderUpdatedAt time.Time
	ID                string
	Key               string
	Name              string
	Caps              modelCaps
	ContextWindow     int
	MaxOutput         int
	Pricing           modelPricing
	Currency          string
	Status            string
	Sort              int
	UpdatedAt         time.Time
}

type accessModel struct {
	ID                      string                   `json:"id"`
	Name                    string                   `json:"name"`
	Group                   string                   `json:"group,omitempty"`
	Description             string                   `json:"description,omitempty"`
	ContextWindow           int                      `json:"contextWindow"`
	MaxOutputTokens         int                      `json:"maxOutputTokens"`
	Modalities              modalities               `json:"modalities"`
	Efforts                 []string                 `json:"efforts"`
	DefaultEffort           *string                  `json:"defaultEffort"`
	SupportsFastMode        bool                     `json:"supportsFastMode"`
	DefaultEnabled          bool                     `json:"defaultEnabled"`
	PerAgent                map[string]agentOverride `json:"perAgent"`
	SortOrder               int                      `json:"sortOrder"`
	NewSessionDefault       []string                 `json:"newSessionDefault,omitempty"`
	Availability            string                   `json:"availability"`
	Currency                string                   `json:"currency"`
	InputCostPerToken       float64                  `json:"inputCostPerToken"`
	OutputCostPerToken      float64                  `json:"outputCostPerToken"`
	CostDiscount            *float64                 `json:"costDiscount,omitempty"`
	CacheReadInputTokenCost *float64                 `json:"cacheReadInputTokenCost,omitempty"`
	Agents                  []string                 `json:"agents"`
	Mode                    string                   `json:"mode"`
}

type listModelsResponse struct {
	SchemaVersion int           `json:"schemaVersion"`
	AccountTier   string        `json:"accountTier"`
	Models        []accessModel `json:"models"`
}

const (
	defaultContextWindow = 128000
	defaultMode          = "chat"
)

// Client agents supported by the model-access catalog. The legacy "chat"
// value is a mode, not an agent, and is rejected by the client validator.
var supportedModelAgents = map[string]bool{
	"claude-code": true,
	"codex":       true,
	"pi":          true,
}

var defaultModelAgents = []string{"claude-code", "codex"}

func normalizeModelAgents(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, agent := range raw {
		agent = strings.TrimSpace(agent)
		if supportedModelAgents[agent] && !seen[agent] {
			seen[agent] = true
			out = append(out, agent)
		}
	}
	if len(out) == 0 {
		return append([]string{}, defaultModelAgents...)
	}
	return out
}

func defaultWireProtocol(agent string) string {
	switch agent {
	case "claude-code":
		return "anthropic-messages"
	case "codex":
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

func defaultPerAgent(agents []string) map[string]agentOverride {
	out := make(map[string]agentOverride, len(agents))
	for _, agent := range agents {
		out[agent] = agentOverride{WireProtocol: defaultWireProtocol(agent)}
	}
	return out
}

func (m *Module) loadModels(ctx context.Context) ([]modelRow, error) {
	if m.db == nil {
		return nil, nil
	}
	rows, err := m.db.Pool.Query(ctx, `
		SELECT p.id::text, p.slug, p.name, COALESCE(p.wire_protocol,''), p.updated_at,
		       m.id::text, m.model_key, m.display_name, m.capabilities,
		       COALESCE(m.context_window,0), COALESCE(m.max_output_tokens,0),
		       m.pricing, m.currency, m.status, m.sort, m.updated_at
		FROM ma_models m
		JOIN ma_providers p ON p.id = m.provider_id
		WHERE p.status = 'active' AND m.status <> 'retired'
		ORDER BY p.slug, m.sort, m.model_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []modelRow
	for rows.Next() {
		var r modelRow
		var capsRaw, pricingRaw []byte
		if err := rows.Scan(
			&r.ProviderID, &r.ProviderSlug, &r.ProviderName, &r.ProviderWire, &r.ProviderUpdatedAt,
			&r.ID, &r.Key, &r.Name, &capsRaw, &r.ContextWindow, &r.MaxOutput,
			&pricingRaw, &r.Currency, &r.Status, &r.Sort, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(capsRaw, &r.Caps)
		_ = json.Unmarshal(pricingRaw, &r.Pricing)
		if r.Currency == "" {
			r.Currency = "USD"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Module) availabilityMap(ctx context.Context, accountID string) map[string]string {
	out := map[string]string{}
	if m.db == nil {
		return out
	}
	query := `SELECT model_id::text, availability FROM ma_model_availability WHERE scope = 'all' ORDER BY 1`
	args := []any{}
	if isUUID(accountID) {
		query = `SELECT model_id::text, availability FROM ma_model_availability
			WHERE scope = 'all' OR (scope = 'account' AND target::text = $1)
			ORDER BY (scope = 'account')`
		args = append(args, accountID)
	}
	rows, err := m.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, availability string
		if err := rows.Scan(&id, &availability); err != nil {
			return out
		}
		out[id] = availability
	}
	return out
}

func (m *Module) accountTier(ctx context.Context, accountID string) string {
	if m.db == nil || !isUUID(accountID) {
		return "free"
	}
	var exists bool
	err := m.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ma_subscriptions
			WHERE account_id::text = $1 AND status IN ('trialing','active')
		)`, accountID).Scan(&exists)
	if err != nil || !exists {
		return "free"
	}
	return "paid"
}

func normalizeEfforts(efforts []string, def *string) ([]string, *string) {
	if efforts == nil {
		efforts = []string{}
	}
	if def != nil {
		found := false
		for _, e := range efforts {
			if e == *def {
				found = true
				break
			}
		}
		if !found {
			return efforts, nil
		}
	}
	return efforts, def
}

func (r modelRow) contextWindow() int {
	if r.ContextWindow > 0 {
		return r.ContextWindow
	}
	if r.Caps.ContextWindow > 0 {
		return r.Caps.ContextWindow
	}
	return defaultContextWindow
}

func (r modelRow) maxOutput() int {
	if r.MaxOutput > 0 {
		return r.MaxOutput
	}
	if r.Caps.MaxOutputTokens > 0 {
		return r.Caps.MaxOutputTokens
	}
	return 8192
}

func (r modelRow) mode() string {
	if r.Caps.Mode != "" {
		return r.Caps.Mode
	}
	return defaultMode
}

func (r modelRow) agents() []string {
	return normalizeModelAgents(r.Caps.Agents)
}

func (r modelRow) modelModalities() modalities {
	if r.Caps.Modalities != nil {
		mm := *r.Caps.Modalities
		if mm.Input == nil {
			mm.Input = []string{}
		}
		if mm.Output == nil {
			mm.Output = []string{}
		}
		return mm
	}
	return modalities{Input: []string{"text"}, Output: []string{"text"}}
}

func (r modelRow) defaultEnabled() bool {
	if r.Caps.DefaultEnabled != nil {
		return *r.Caps.DefaultEnabled
	}
	return true
}

func (r modelRow) sortOrder() int {
	if r.Caps.SortOrder != nil {
		return *r.Caps.SortOrder
	}
	return r.Sort
}

func (r modelRow) perAgent(agents []string) map[string]agentOverride {
	out := defaultPerAgent(agents)
	for agent, override := range r.Caps.PerAgent {
		if !supportedModelAgents[agent] {
			continue
		}
		current := out[agent]
		if override.ContextWindow != nil {
			current.ContextWindow = override.ContextWindow
		}
		if override.Efforts != nil {
			current.Efforts = override.Efforts
		}
		if override.DefaultEffort != nil {
			current.DefaultEffort = override.DefaultEffort
		}
		if override.SupportsFastMode != nil {
			current.SupportsFastMode = override.SupportsFastMode
		}
		if override.DefaultEnabled != nil {
			current.DefaultEnabled = override.DefaultEnabled
		}
		if override.WireProtocol != "" {
			current.WireProtocol = normalizeWireProtocol(override.WireProtocol)
		}
		out[agent] = current
	}
	return out
}

func (r modelRow) accessModel(availability string) accessModel {
	efforts, def := normalizeEfforts(r.Caps.Efforts, r.Caps.DefaultEffort)
	if availability == "" {
		availability = "available"
	}
	agents := r.agents()
	model := accessModel{
		ID:               r.Key,
		Name:             r.Name,
		Group:            r.Caps.Group,
		Description:      r.Caps.Description,
		ContextWindow:    r.contextWindow(),
		MaxOutputTokens:  r.maxOutput(),
		Modalities:       r.modelModalities(),
		Efforts:          efforts,
		DefaultEffort:    def,
		SupportsFastMode: r.Caps.SupportsFastMode,
		DefaultEnabled:   r.defaultEnabled(),
		PerAgent:         r.perAgent(agents),
		SortOrder:        r.sortOrder(),
		Availability:     availability,
		Currency:         r.Currency,
		CostDiscount:     r.Pricing.CostDiscount,
		Agents:           agents,
		Mode:             r.mode(),
	}
	if len(r.Caps.NewSessionDefault) > 0 {
		model.NewSessionDefault = append([]string{}, r.Caps.NewSessionDefault...)
	}
	if r.Pricing.InputCostPerToken != nil {
		model.InputCostPerToken = *r.Pricing.InputCostPerToken
	} else if r.Pricing.InputPerMillion != nil {
		model.InputCostPerToken = *r.Pricing.InputPerMillion / 1_000_000
	}
	if r.Pricing.OutputCostPerToken != nil {
		model.OutputCostPerToken = *r.Pricing.OutputCostPerToken
	} else if r.Pricing.OutputPerMillion != nil {
		model.OutputCostPerToken = *r.Pricing.OutputPerMillion / 1_000_000
	}
	model.CacheReadInputTokenCost = r.Pricing.CacheReadInputTokenCost
	return model
}

func (m *Module) listModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("schemaVersion") != "5" {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "schemaVersion must be 5"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadModels(r.Context())
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	availability := m.availabilityMap(r.Context(), p.AccountID)
	models := make([]accessModel, 0, len(rows))
	for _, row := range rows {
		models = append(models, row.accessModel(availability[row.ID]))
	}
	httpapi.OK(w, listModelsResponse{
		SchemaVersion: 5,
		AccountTier:   m.accountTier(r.Context(), p.AccountID),
		Models:        models,
	})
}

func normalizeWireProtocol(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(v, "anthropic"):
		return "anthropic-messages"
	case strings.Contains(v, "responses"):
		return "openai-responses"
	case strings.Contains(v, "google") || strings.Contains(v, "gemini"):
		return "google-generative-ai"
	default:
		return "openai-completions"
	}
}
