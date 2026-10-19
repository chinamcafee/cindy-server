// Package skillhub implements the Cindy Skill Hub market, publishing,
// visibility and organization distribution APIs under /api/skills-hub.
package skillhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

// Deps is the set of platform services a Skill Hub module needs.
type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Tokens *authn.Manager
	S3     *objectstore.Store
	Config *config.Config
	Audit  *audit.Logger
}

// Module serves the Skill Hub API.
type Module struct {
	deps Deps
}

func New(d Deps) *Module { return &Module{deps: d} }

// Routes returns the Skill Hub HTTP handler. Read endpoints accept anonymous
// callers when the skill is public; write endpoints require a Bearer token.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		r.Use(httpapi.OptionalAuth(m.deps.Tokens))
		r.Get("/api/skills-hub/skills", m.listSkills)
		r.Post("/api/skills-hub/skills/list", m.listInstalledSkills)
		r.Post("/api/skills-hub/skills/batch-detail", m.batchDetail)
		r.Get("/api/skills-hub/skills/{name}", m.getSkill)
		r.Get("/api/skills-hub/skills/{name}/files", m.skillFiles)
		r.Get("/api/skills-hub/skills/{name}/file", m.skillFile)
		r.Get("/api/skills-hub/skills/{name}/versions", m.skillVersions)
		r.Get("/api/skills-hub/categories", m.listCategories)
	})

	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.deps.Tokens))
		r.Get("/api/skills-hub/users/published", m.listMyPublished)
		r.Get("/api/skills-hub/users/teams", m.listMyTeams)
		r.Get("/api/skills-hub/users/departments", m.listMyDepartments)
		r.Get("/api/skills-hub/auto-install", m.autoInstall)
		r.Post("/api/skills-hub/skills/publish/init", m.publishInit)
		r.Post("/api/skills-hub/skills/publish/commit", m.publishCommit)
		r.Get("/api/skills-hub/skills/{name}/scan", m.scanStatus)
		r.Get("/api/skills-hub/skills/{name}/download", m.skillDownload)
		r.Patch("/api/skills-hub/skills/{name}", m.patchSkill)
		r.Delete("/api/skills-hub/skills/{name}", m.deleteSkill)
		r.Post("/api/skills-hub/skills/{name}/unpublish", m.unpublishSkill)
		r.Post("/api/skills-hub/skills/{name}/set-visibility", m.setVisibility)
		r.Get("/api/skills-hub/skills/{name}/visibility", m.getVisibility)
	})

	return r
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

type membership struct {
	AccountID    string
	MembershipID string
	Kind         string
	Role         string
	Status       string
	OrgID        string
	OrgSlug      string
	OrgName      string
	Found        bool
}

func (mm membership) active() bool { return mm.Found && mm.Status == "active" }

func (mm membership) orgUUID() any {
	if mm.OrgID == "" {
		return nil
	}
	return mm.OrgID
}

func (m *Module) loadMembership(ctx context.Context, p httpapi.Principal) membership {
	mm := membership{AccountID: p.AccountID, MembershipID: p.MembershipID}
	if p.MembershipID == "" || m.deps.DB == nil {
		return mm
	}
	var orgID, orgSlug, orgName *string
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT m.account_id::text, m.kind, m.role, m.status,
		       m.org_id::text, o.slug::text, COALESCE(m.org_name, o.name)
		FROM auth_memberships m
		LEFT JOIN auth_organizations o ON o.id = m.org_id
		WHERE m.id = $1::uuid`, p.MembershipID).
		Scan(&mm.AccountID, &mm.Kind, &mm.Role, &mm.Status, &orgID, &orgSlug, &orgName)
	if err != nil || mm.AccountID != p.AccountID {
		return membership{AccountID: p.AccountID, MembershipID: p.MembershipID}
	}
	mm.OrgID, mm.OrgSlug, mm.OrgName, mm.Found = deref(orgID), deref(orgSlug), deref(orgName), true
	return mm
}

func viewerAccount(ctx context.Context) string {
	if p, ok := httpapi.PrincipalFrom(ctx); ok {
		if _, err := uuid.Parse(p.AccountID); err == nil {
			return p.AccountID
		}
	}
	return uuid.Nil.String()
}

func requiredAccount(ctx context.Context) string {
	p, ok := httpapi.PrincipalFrom(ctx)
	if !ok || p.AccountID == "" {
		return ""
	}
	return p.AccountID
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (m *Module) auditLog(ctx context.Context, action, targetType, targetID string, after any) {
	if m.deps.Audit == nil {
		return
	}
	m.deps.Audit.Log(ctx, audit.Event{Action: action, TargetType: targetType, TargetID: targetID, After: after})
}

// ---------------------------------------------------------------------------
// Shared constants / helpers
// ---------------------------------------------------------------------------

const (
	maxPageSize          = 100
	defaultPageSize      = 24
	maxSkillArchiveBytes = 200 * 1024 * 1024
	maxFileContentBytes  = 1 * 1024 * 1024
	presignDownloadTTL   = 10 * time.Minute
	presignPublishTTL    = time.Hour
)

var (
	slugPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func parsePage(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	if n > 10000 {
		return 10000
	}
	return n
}

func parsePageSize(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultPageSize
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

func (m *Module) skillsBucket() string {
	if m.deps.Config != nil {
		if m.deps.Config.S3BucketSkills != "" {
			return m.deps.Config.S3BucketSkills
		}
	}
	if m.deps.S3 != nil && m.deps.S3.Skills != "" {
		return m.deps.S3.Skills
	}
	return "cindy-skills"
}

func notFound(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, code, message))
}

func invalidInput(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, code, message))
}

func forbidden(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.Err(http.StatusForbidden, "PERMISSION_DENIED", "permission denied"))
}

func conflict(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusConflict, code, message))
}

func unavailable(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, code, message))
}

func invalidName(w http.ResponseWriter) {
	invalidInput(w, "INVALID_NAME", "invalid skill name")
}

// argsBuilder accumulates positional SQL arguments.
type argsBuilder struct{ args []any }

func (b *argsBuilder) add(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func writeInternal(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.ErrInternal)
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
