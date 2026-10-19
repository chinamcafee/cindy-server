// Package admin implements the embedded admin console backend.
//
// All routes live under /admin/api/v1 and are served by the same Go process as
// the public API. The module only talks to the shared platform packages and the
// shared PostgreSQL/Redis stores: no other domain package is imported.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// Deps are the platform dependencies wired by the application shell.
type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Config *config.Config
	Audit  *audit.Logger
	Logger *slog.Logger
	Box    *crypto.Box
}

// Module is the admin console HTTP module.
type Module struct {
	db    *db.DB
	rdb   *redis.Client
	cfg   *config.Config
	audit *audit.Logger
	log   *slog.Logger
	box   *crypto.Box
	pool  *pgxpool.Pool
	start time.Time
}

// New builds the module and bootstraps the first super_admin when admin_users is
// empty. Bootstrapping is best effort: a failure is logged but does not prevent
// the process from starting.
func New(d Deps) *Module {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg := d.Config
	if cfg == nil {
		cfg = &config.Config{Env: "dev", Region: "global"}
	}
	m := &Module{
		db:    d.DB,
		rdb:   d.Redis,
		cfg:   cfg,
		audit: d.Audit,
		log:   logger,
		box:   d.Box,
		start: time.Now(),
	}
	if d.DB != nil {
		m.pool = d.DB.Pool
	}
	m.bootstrap(context.Background())
	return m
}

func (m *Module) bootstrap(parent context.Context) {
	if m.pool == nil {
		m.log.Warn("admin_bootstrap_skipped", "reason", "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	var count int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&count); err != nil {
		m.log.Warn("admin_bootstrap_skipped", "reason", "admin_users not readable", "error", err)
		return
	}
	if count > 0 {
		return
	}
	email := strings.ToLower(strings.TrimSpace(m.cfg.AdminBootstrapEmail))
	password := m.cfg.AdminBootstrapPassword
	if email == "" || password == "" {
		m.log.Warn("admin_bootstrap_skipped", "reason", "bootstrap credentials are empty")
		return
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		m.log.Error("admin_bootstrap_failed", "error", err)
		return
	}
	if _, err := m.pool.Exec(ctx, `
		INSERT INTO admin_users (email, password_hash, role, status)
		VALUES ($1, $2, 'super_admin', 'active')
		ON CONFLICT (email) DO NOTHING`, email, hash); err != nil {
		m.log.Error("admin_bootstrap_failed", "error", err)
		return
	}
	m.log.Info("admin_bootstrap_created", "email", email, "role", "super_admin")
}

// Routes returns the handler rooted at /admin/api/v1.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed"))
	})
	r.Route("/admin/api/v1", func(api chi.Router) {
		api.Post("/auth/login", m.login)

		api.Group(func(p chi.Router) {
			p.Use(m.session)

			p.Get("/auth/me", m.me)
			p.Post("/auth/logout", m.logout)

			p.Get("/dashboard/summary", m.dashboardSummary)
			p.Get("/dashboard/timeseries", m.dashboardTimeseries)

			p.Get("/accounts", m.listAccounts)
			p.Get("/accounts/{id}", m.accountDetail)
			p.Post("/accounts/{id}/ban", m.require(permBansWrite, m.banAccount))
			p.Post("/accounts/{id}/unban", m.require(permBansWrite, m.unbanAccount))
			p.Get("/accounts/{id}/sessions", m.accountSessions)
			p.Post("/accounts/{id}/sessions/revoke", m.require(permAccountsWrite, m.revokeAccountSessions))
			p.Post("/sessions/revoke", m.require(permAccountsWrite, m.revokeGlobalSession))

			p.Get("/organizations", m.listOrganizations)
			p.Get("/organizations/{id}", m.organizationDetail)
			p.Patch("/organizations/{id}", m.require(permOrgsWrite, m.updateOrganization))
			p.Post("/organizations/{id}/domains", m.require(permOrgsWrite, m.createOrgDomain))
			p.Post("/organizations/{id}/domains/{domainId}/verify", m.require(permOrgsWrite, m.verifyOrgDomain))
			p.Post("/organizations/{id}/sso", m.require(permOrgsWrite, m.createOrgSSO))
			p.Patch("/organizations/{id}/sso/{ssoId}", m.require(permOrgsWrite, m.updateOrgSSO))

			p.Get("/devices", m.listDevices)
			p.Delete("/devices/{id}", m.require(permDevicesWrite, m.deleteDevice))
			p.Get("/shared-tasks", m.listSharedTasks)
			p.Post("/shared-tasks/{id}/close", m.require(permDevicesWrite, m.closeSharedTask))
			p.Get("/provider-shares", m.listProviderShares)
			p.Post("/provider-shares/{id}/revoke", m.require(permDevicesWrite, m.revokeProviderShare))

			p.Get("/models", m.listModels)
			p.Post("/models", m.require(permModelsWrite, m.createModel))
			p.Patch("/models/{id}", m.require(permModelsWrite, m.updateModel))
			p.Get("/providers", m.listProviders)
			p.Post("/providers", m.require(permModelsWrite, m.createProvider))
			p.Patch("/providers/{id}", m.require(permModelsWrite, m.updateProvider))
			p.Get("/catalog/revisions", m.listCatalogRevisions)
			p.Post("/catalog/publish", m.require(permModelsWrite, m.publishCatalog))
			p.Post("/catalog/rollback", m.require(permModelsWrite, m.rollbackCatalog))

			p.Get("/plugins", m.listPlugins)
			p.Get("/plugins/{id}", m.pluginDetail)
			p.Post("/plugins/{id}/releases/{releaseId}/approve", m.require(permPluginsReview, m.approvePluginRelease))
			p.Post("/plugins/{id}/releases/{releaseId}/reject", m.require(permPluginsReview, m.rejectPluginRelease))
			p.Post("/plugins/{id}/remove", m.require(permPluginsReview, m.removePlugin))

			p.Get("/skills", m.listSkills)
			p.Get("/skills/{id}", m.skillDetail)
			p.Post("/skills/{id}/versions/{version}/approve", m.require(permSkillsReview, m.approveSkillVersion))
			p.Post("/skills/{id}/versions/{version}/reject", m.require(permSkillsReview, m.rejectSkillVersion))
			p.Patch("/skills/{id}", m.require(permSkillsReview, m.updateSkill))

			p.Get("/releases/desktop", m.listDesktopReleases)
			p.Post("/releases/desktop", m.require(permUpdatesWrite, m.createDesktopRelease))
			p.Post("/releases/desktop/{id}/publish", m.require(permUpdatesWrite, m.publishDesktopRelease))
			p.Get("/releases/mobile", m.listMobileReleases)
			p.Post("/releases/mobile", m.require(permUpdatesWrite, m.createMobileRelease))
			p.Post("/releases/mobile/{id}/publish", m.require(permUpdatesWrite, m.publishMobileRelease))

			p.Get("/announcements", m.listAnnouncements)
			p.Post("/announcements", m.require(permUpdatesWrite, m.createAnnouncement))
			p.Post("/announcements/{id}/publish", m.require(permUpdatesWrite, m.publishAnnouncement))

			p.Get("/feature-flags", m.listFeatureFlags)
			p.Put("/feature-flags/{key}", m.require(permConfigWrite, m.putFeatureFlag))
			p.Get("/endpoint-manifest", m.getEndpointManifest)
			p.Put("/endpoint-manifest", m.require(permConfigWrite, m.putEndpointManifest))
			p.Get("/settings/email", m.getEmailSettings)
			p.Put("/settings/email", m.require(permConfigWrite, m.putEmailSettings))
			p.Post("/settings/email/test", m.require(permConfigWrite, m.testEmailSettings))

			p.Get("/audit", m.listAudit)
			p.Get("/system/health", m.systemHealth)
			p.Get("/admins", m.listAdmins)
			p.Post("/admins", m.require(permAdminsWrite, m.createAdmin))
		})
	})
	return r
}

// pagination is the shared list paging envelope.
type pagination struct {
	Page     int
	PageSize int
	Offset   int
}

func parsePagination(r *http.Request) pagination {
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	size := queryInt(r, "pageSize", 50)
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	return pagination{Page: page, PageSize: size, Offset: (page - 1) * size}
}

type listEnvelope struct {
	Items    any `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

func newList(items any, total int, p pagination) listEnvelope {
	return listEnvelope{Items: items, Total: total, Page: p.Page, PageSize: p.PageSize}
}

func queryInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryBool(r *http.Request, key string) (bool, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}

// decodeOptional decodes a JSON body when one is present.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return httpapi.DecodeJSONLoose(w, r, dst)
}

// fail writes a JSON error, translating unexpected errors to a generic 500 and
// logging the underlying cause.
func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpapi.APIError
	if errors.As(err, &apiErr) {
		httpapi.WriteError(w, apiErr)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	m.log.Error("admin_api_error", "path", r.URL.Path, "method", r.Method, "error", err)
	httpapi.WriteError(w, httpapi.ErrInternal)
}

func (m *Module) badRequest(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, code, message))
}

func (m *Module) notFound(w http.ResponseWriter, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "NOT_FOUND", message))
}

func (m *Module) conflict(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusConflict, code, message))
}

func validUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func nullableRawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

func ptrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// auditEvent records an admin write. Actor details are taken from the session.
func (m *Module) auditEvent(r *http.Request, action, targetType, targetID string, before, after any, reason string) {
	if m.audit == nil {
		return
	}
	a := adminFrom(r.Context())
	m.audit.Log(r.Context(), audit.Event{
		ActorType:  "admin",
		ActorID:    a.AdminID,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Before:     before,
		After:      after,
		Reason:     reason,
	})
}

// Device-link presence is a per-account Redis hash of device IDs with a short TTL.
func (m *Module) presenceKey(accountID string) string {
	return "cindy:" + m.cfg.Env + ":dl:presence:" + accountID
}

func (m *Module) presencePattern() string {
	return "cindy:" + m.cfg.Env + ":dl:presence:*"
}

func (m *Module) onlineDeviceSet(ctx context.Context) map[string]bool {
	out := make(map[string]bool)
	if m.rdb == nil {
		return out
	}
	iter := m.rdb.Scan(ctx, 0, m.presencePattern(), 100).Iterator()
	for iter.Next(ctx) {
		ids, err := m.rdb.HKeys(ctx, iter.Val()).Result()
		if err != nil {
			continue
		}
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
