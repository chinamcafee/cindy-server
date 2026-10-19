// Package githubissue proxies user feedback to GitHub issues and serves the
// caller's own submitted issues.
package githubissue

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// Deps is the set of platform services required by the githubissue module.
type Deps struct {
	DB     *db.DB
	Config *config.Config
	Tokens *authn.Manager
	Logger *slog.Logger
	Audit  *audit.Logger
}

// Module implements the GitHub issue endpoints.
type Module struct {
	deps   Deps
	log    *slog.Logger
	client *http.Client
}

// New builds a githubissue module. Nil logger falls back to the process default.
func New(deps Deps) *Module {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Module{deps: deps, log: log, client: &http.Client{Timeout: 10 * time.Second}}
}

// Routes returns the GitHub issue HTTP routes. The caller mounts it at the root.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.deps.Tokens))
		r.Post("/api/github/issues", m.createIssue)
		r.Get("/api/github/issues/mine", m.myIssues)
	})
	return r
}

func (m *Module) githubToken() string {
	if m.deps.Config == nil {
		return ""
	}
	return m.deps.Config.GitHubToken
}
