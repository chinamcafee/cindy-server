// Package oss exposes object-storage presign endpoints for public assets.
//
// Only the avatar scene is served here; device media presign/get/delete is
// owned by the device-link module and must not be duplicated.
package oss

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

// Deps is the set of platform services required by the oss module.
type Deps struct {
	DB     *db.DB
	Store  *objectstore.Store
	Tokens *authn.Manager
	Config *config.Config
	Logger *slog.Logger
	Audit  *audit.Logger
}

// Module implements the public asset presign endpoints.
type Module struct {
	deps Deps
	log  *slog.Logger
}

// New builds an oss module. Nil logger falls back to the process default.
func New(deps Deps) *Module {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Module{deps: deps, log: log}
}

// Routes returns the oss HTTP routes. The caller mounts it at the server root.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.deps.Tokens))
		r.Post("/api/oss/presign-put", m.presignPut)
	})
	return r
}
