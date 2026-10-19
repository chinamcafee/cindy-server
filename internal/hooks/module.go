// Package hooks implements the minimal desktop hook lane: bearer-authenticated
// WSS upgrade, hello/welcome negotiation, keepalive, and a dispatch/ack
// skeleton. The remaining protocol types are declared but not yet executed.
package hooks

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// Deps is the set of platform services required by the hooks module.
type Deps struct {
	DB     *db.DB
	Config *config.Config
	Tokens *authn.Manager
	Logger *slog.Logger
}

// Module owns the hook lane connections.
type Module struct {
	deps Deps
	log  *slog.Logger
	hub  *hub
}

// New builds a hooks module. Nil logger falls back to the process default.
func New(deps Deps) *Module {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Module{deps: deps, log: log, hub: newHub()}
}

// Routes returns the hook HTTP routes. The caller mounts it at the server root.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/hook/v1/health", func(w http.ResponseWriter, r *http.Request) {
		httpapi.OK(w, map[string]any{"ok": true})
	})

	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.deps.Tokens))
		r.Get("/ws", m.upgrade)
		r.Get("/hook/v1/ws", m.upgrade)
	})

	return r
}

// upgrade performs the WSS handshake and runs the lane until it closes.
func (m *Module) upgrade(w http.ResponseWriter, r *http.Request) {
	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		if errors.Is(err, errUpgradeAborted) {
			// The connection was already hijacked; no HTTP response is possible.
			return
		}
		httpapi.WriteError(w, err)
		return
	}

	principal := httpapi.MustPrincipal(r.Context())
	session := newSession(m, conn, principal.AccountID)
	defer session.close()
	session.run()
}
