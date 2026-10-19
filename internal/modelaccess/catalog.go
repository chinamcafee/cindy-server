package modelaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type catalogAuth struct {
	Method string `json:"method"`
}

type catalogAccess struct {
	Kind string `json:"kind"`
}

type catalogRouting struct {
	WireProtocol string `json:"wireProtocol,omitempty"`
}

type catalogModel struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	ContextWindow    int         `json:"contextWindow"`
	MaxOutputTokens  int         `json:"maxOutputTokens,omitempty"`
	Efforts          []string    `json:"efforts"`
	DefaultEffort    *string     `json:"defaultEffort"`
	Mode             string      `json:"mode,omitempty"`
	Modalities       *modalities `json:"modalities,omitempty"`
	Description      string      `json:"description,omitempty"`
	Group            string      `json:"group,omitempty"`
	DefaultEnabled   *bool       `json:"defaultEnabled,omitempty"`
	SortOrder        *int        `json:"sortOrder,omitempty"`
	SupportsFastMode bool        `json:"supportsFastMode,omitempty"`
}

type catalogProvider struct {
	ID      string                    `json:"id"`
	Name    string                    `json:"name"`
	Source  string                    `json:"source"`
	Agents  []string                  `json:"agents"`
	Auth    catalogAuth               `json:"auth"`
	Access  catalogAccess             `json:"access"`
	Routing map[string]catalogRouting `json:"routing"`
	Models  map[string][]catalogModel `json:"models"`
}

type registryRoute struct {
	ProviderID string   `json:"providerId"`
	ModelID    string   `json:"modelId"`
	Agents     []string `json:"agents"`
}

type registryEntry struct {
	ID                string                   `json:"id"`
	Name              string                   `json:"name"`
	Status            string                   `json:"status,omitempty"`
	Group             string                   `json:"group,omitempty"`
	Description       string                   `json:"description,omitempty"`
	ContextWindow     int                      `json:"contextWindow,omitempty"`
	MaxOutputTokens   int                      `json:"maxOutputTokens,omitempty"`
	Efforts           []string                 `json:"efforts,omitempty"`
	DefaultEffort     *string                  `json:"defaultEffort,omitempty"`
	SortOrder         int                      `json:"sortOrder"`
	SupportsFastMode  bool                     `json:"supportsFastMode,omitempty"`
	DefaultEnabled    *bool                    `json:"defaultEnabled,omitempty"`
	PerAgent          map[string]agentOverride `json:"perAgent,omitempty"`
	NewSessionDefault []string                 `json:"newSessionDefault,omitempty"`
	Mode              string                   `json:"mode,omitempty"`
	Modalities        *modalities              `json:"modalities,omitempty"`
	Routes            []registryRoute          `json:"routes"`
}

type modelRegistry struct {
	SchemaVersion int               `json:"schemaVersion"`
	UpdatedAt     string            `json:"updatedAt"`
	BaseModels    []json.RawMessage `json:"baseModels"`
	Models        []registryEntry   `json:"models"`
}

type catalogResponse struct {
	Version       string            `json:"version"`
	Providers     []catalogProvider `json:"providers"`
	Presets       []json.RawMessage `json:"presets"`
	ModelRegistry modelRegistry     `json:"modelRegistry"`
}

func (r modelRow) catalogModel() catalogModel {
	efforts, def := normalizeEfforts(r.Caps.Efforts, r.Caps.DefaultEffort)
	name := r.Name
	if name == "" {
		name = r.Key
	}
	modalities := r.modelModalities()
	sortOrder := r.sortOrder()
	enabled := r.defaultEnabled()
	return catalogModel{
		ID:               r.Key,
		Name:             name,
		ContextWindow:    r.contextWindow(),
		MaxOutputTokens:  r.maxOutput(),
		Efforts:          efforts,
		DefaultEffort:    def,
		Mode:             r.mode(),
		Modalities:       &modalities,
		Description:      r.Caps.Description,
		Group:            r.Caps.Group,
		DefaultEnabled:   &enabled,
		SortOrder:        &sortOrder,
		SupportsFastMode: r.Caps.SupportsFastMode,
	}
}

// Model registry per-agent metadata predates wireProtocol; the catalog client
// rejects that field in registry entries (routes carry protocol instead).
func registryPerAgent(overrides map[string]agentOverride) map[string]agentOverride {
	out := make(map[string]agentOverride, len(overrides))
	for agent, override := range overrides {
		override.WireProtocol = ""
		out[agent] = override
	}
	return out
}

func nonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

func protocolForAgent(agent, providerProtocol string) string {
	switch agent {
	case "claude-code":
		return "anthropic-messages"
	case "codex":
		return "openai-responses"
	default:
		return providerProtocol
	}
}

func providerID(slug string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(slug) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			b.WriteRune(ch)
		} else {
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "provider"
	}
	return id
}

func (m *Module) catalogDocument(ctx context.Context) (catalogResponse, error) {
	doc := catalogResponse{
		Version:   "5.0",
		Providers: []catalogProvider{},
		Presets:   []json.RawMessage{},
		ModelRegistry: modelRegistry{
			SchemaVersion: 5,
			BaseModels:    []json.RawMessage{},
			Models:        []registryEntry{},
		},
	}
	updated := time.Time{}

	providers := map[string]*catalogProvider{}
	if m.db != nil {
		rows, err := m.db.Pool.Query(ctx, `
			SELECT slug, name, COALESCE(wire_protocol,''), updated_at
			FROM ma_providers WHERE status = 'active' ORDER BY slug`)
		if err != nil {
			return doc, err
		}
		for rows.Next() {
			var slug, name, wire string
			var at time.Time
			if err := rows.Scan(&slug, &name, &wire, &at); err != nil {
				rows.Close()
				return doc, err
			}
			provider := &catalogProvider{
				ID:      providerID(slug),
				Name:    name,
				Source:  "builtin",
				Agents:  append([]string{}, defaultModelAgents...),
				Auth:    catalogAuth{Method: "managed"},
				Access:  catalogAccess{Kind: "managed"},
				Routing: map[string]catalogRouting{},
				Models:  map[string][]catalogModel{},
			}
			protocol := normalizeWireProtocol(wire)
			for _, agent := range provider.Agents {
				provider.Routing[agent] = catalogRouting{WireProtocol: protocolForAgent(agent, protocol)}
				provider.Models[agent] = []catalogModel{}
			}
			providers[slug] = provider
			if at.After(updated) {
				updated = at
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return doc, err
		}
	}

	models, err := m.loadModels(ctx)
	if err != nil {
		return doc, err
	}

	entries := map[string]*registryEntry{}
	var entryOrder []string
	for _, row := range models {
		cp, ok := providers[row.ProviderSlug]
		if !ok {
			continue
		}
		cm := row.catalogModel()
		agents := row.agents()
		for _, agent := range agents {
			cp.Models[agent] = append(cp.Models[agent], cm)
		}
		if row.UpdatedAt.After(updated) {
			updated = row.UpdatedAt
		}
		if row.ProviderUpdatedAt.After(updated) {
			updated = row.ProviderUpdatedAt
		}
		entry, ok := entries[row.Key]
		if !ok {
			entry = &registryEntry{
				ID:                row.Key,
				Name:              cm.Name,
				Status:            row.Status,
				Group:             row.Caps.Group,
				Description:       row.Caps.Description,
				ContextWindow:     cm.ContextWindow,
				MaxOutputTokens:   cm.MaxOutputTokens,
				Efforts:           cm.Efforts,
				DefaultEffort:     cm.DefaultEffort,
				SortOrder:         *cm.SortOrder,
				SupportsFastMode:  cm.SupportsFastMode,
				DefaultEnabled:    cm.DefaultEnabled,
				PerAgent:          registryPerAgent(row.perAgent(agents)),
				NewSessionDefault: nonEmpty(row.Caps.NewSessionDefault),
				Mode:              cm.Mode,
				Modalities:        cm.Modalities,
				Routes:            []registryRoute{},
			}
			entries[row.Key] = entry
			entryOrder = append(entryOrder, row.Key)
		}
		entry.Routes = append(entry.Routes, registryRoute{
			ProviderID: row.ProviderSlug,
			ModelID:    row.Key,
			Agents:     agents,
		})
	}

	for _, cp := range providers {
		doc.Providers = append(doc.Providers, *cp)
	}
	sort.Slice(doc.Providers, func(i, j int) bool { return doc.Providers[i].ID < doc.Providers[j].ID })
	for _, key := range entryOrder {
		doc.ModelRegistry.Models = append(doc.ModelRegistry.Models, *entries[key])
	}

	if m.db != nil {
		var revision int
		var registryRaw []byte
		var publishedAt *time.Time
		err := m.db.Pool.QueryRow(ctx, `
			SELECT version, registry, published_at
			FROM ma_catalog_revisions WHERE status = 'published'
			ORDER BY version DESC LIMIT 1`).Scan(&revision, &registryRaw, &publishedAt)
		if err == nil {
			var payload struct {
				Presets    []json.RawMessage `json:"presets"`
				BaseModels []json.RawMessage `json:"baseModels"`
			}
			_ = json.Unmarshal(registryRaw, &payload)
			if payload.Presets != nil {
				doc.Presets = payload.Presets
			}
			if payload.BaseModels != nil {
				doc.ModelRegistry.BaseModels = payload.BaseModels
			}
			if revision > 0 {
				doc.Version = "5." + strconv.Itoa(revision)
			}
			if publishedAt != nil && publishedAt.After(updated) {
				updated = *publishedAt
			}
		}
	}

	if updated.IsZero() {
		updated = time.Unix(0, 0).UTC()
	}
	doc.ModelRegistry.UpdatedAt = isoMillis(updated)
	return doc, nil
}

func (m *Module) catalog(w http.ResponseWriter, r *http.Request) {
	doc, err := m.catalogDocument(r.Context())
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	body, err := json.Marshal(doc)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(part)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func (m *Module) catalogReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.OK(w, map[string]any{"ok": true, "schemaVersion": 5})
}
