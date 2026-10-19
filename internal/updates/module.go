// Package updates serves the public desktop/mobile update and announcement
// resources: endpoint.json, mobile /latest and OTA manifest, release notes.
package updates

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
)

// Deps is the set of platform services required by the updates module.
type Deps struct {
	DB     *db.DB
	Config *config.Config
	Logger *slog.Logger
}

// Module implements the anonymous update endpoints.
type Module struct {
	deps Deps
	log  *slog.Logger
}

// New builds an updates module. Nil logger falls back to the process default.
func New(deps Deps) *Module {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Module{deps: deps, log: log}
}

// Routes returns the update HTTP routes. The caller mounts it at the server root.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	// Clients fetch `<cdnBaseUrl>/endpoint.json`; the /api/ alias is kept for
	// probes and older integrations.
	r.Get("/endpoint.json", m.endpointJSON)
	r.Get("/api/endpoint.json", m.endpointJSON)
	r.Get("/api/mobile-update/latest", m.mobileLatest)
	r.Get("/api/mobile-update/manifest", m.mobileManifest)
	r.Get("/api/release-notes/{platform}/{version}", m.releaseNotes)
	r.Get("/api/announcements", m.announcements)
	return r
}

// region returns the configured region, defaulting to "global".
func (m *Module) region() string {
	if m.deps.Config != nil && m.deps.Config.Region != "" {
		return m.deps.Config.Region
	}
	return "global"
}
