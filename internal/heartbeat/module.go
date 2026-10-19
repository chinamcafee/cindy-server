// Package heartbeat implements the anonymous client heartbeat endpoint.
package heartbeat

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
)

// Deps is the set of platform services required by the heartbeat module.
type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Config *config.Config
	Logger *slog.Logger
}

// Module implements POST /heartbeat.
type Module struct {
	deps Deps
	log  *slog.Logger
}

// New builds a heartbeat module. Nil logger falls back to the process default.
func New(deps Deps) *Module {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Module{deps: deps, log: log}
}

// Routes returns the heartbeat HTTP routes. The caller mounts it at the server root.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/heartbeat", m.heartbeat)
	return r
}
