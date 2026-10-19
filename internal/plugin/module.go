// Package plugin implements the Cindy Plugin Server public market and member
// publisher APIs. It is mounted under /api/plugins and /api/publisher.
package plugin

import (
	"context"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

// Deps is the set of platform services a plugin module needs.
type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Tokens *authn.Manager
	S3     *objectstore.Store
	Config *config.Config
	Audit  *audit.Logger
}

// Module serves the plugin market and publisher endpoints.
type Module struct {
	deps Deps
}

func New(d Deps) *Module { return &Module{deps: d} }

// Routes returns the module's HTTP handler. Read endpoints accept anonymous
// callers; publisher endpoints require a normal Access Token Bearer.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		r.Use(httpapi.OptionalAuth(m.deps.Tokens))
		r.Get("/api/plugins", m.listPlugins)
		r.Get("/api/plugins/{pluginId}", m.getPlugin)
		r.Get("/api/plugins/{pluginId}/releases/{releaseId}/download", m.downloadRelease)
		r.Post("/api/plugins/{pluginId}/usage-events", m.postUsageEvents)
	})

	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.deps.Tokens))
		r.Post("/api/publisher/uploads", m.prepareUpload)
		r.Post("/api/publisher/uploads/{uploadId}/commit", m.commitUpload)
		r.Get("/api/publisher/uploads/{uploadId}", m.getUpload)
		r.Get("/api/publisher/releases/mine", m.myReleases)
	})

	return r
}

// ---------------------------------------------------------------------------
// Principal / membership helpers
// ---------------------------------------------------------------------------

type membership struct {
	AccountID    string
	MembershipID string
	Kind         string
	Role         string
	Status       string
	OrgID        *string
	OrgSlug      string
	OrgName      string
	PluginPrefix *string
	Found        bool
}

func (mm membership) active() bool { return mm.Found && mm.Status == "active" }

func (mm membership) orgUUID() any {
	if mm.OrgID == nil || *mm.OrgID == "" {
		return nil
	}
	return *mm.OrgID
}

// loadMembership resolves the caller's membership read-only. The JWT subject
// is always authoritative for the account ID; membership facts only add the
// organization context.
func (m *Module) loadMembership(ctx context.Context, p httpapi.Principal) membership {
	mm := membership{AccountID: p.AccountID, MembershipID: p.MembershipID}
	if p.MembershipID == "" || m.deps.DB == nil {
		return mm
	}
	var orgID, orgSlug, orgName, prefix *string
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT m.account_id::text, m.kind, m.role, m.status,
		       m.org_id::text, o.slug::text, m.org_name, o.plugin_prefix
		FROM auth_memberships m
		LEFT JOIN auth_organizations o ON o.id = m.org_id
		WHERE m.id = $1::uuid`, p.MembershipID).
		Scan(&mm.AccountID, &mm.Kind, &mm.Role, &mm.Status, &orgID, &orgSlug, &orgName, &prefix)
	if err != nil {
		return membership{AccountID: p.AccountID, MembershipID: p.MembershipID}
	}
	if mm.AccountID != p.AccountID {
		// A signed membership token must belong to the same account.
		return membership{AccountID: p.AccountID, MembershipID: p.MembershipID}
	}
	mm.OrgID, mm.OrgSlug, mm.OrgName, mm.PluginPrefix, mm.Found = orgID, deref(orgSlug), deref(orgName), prefix, true
	return mm
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
// Plugin resource IDs
//
// The public contract requires IDs matching ^c[a-z0-9]{24}$. The database uses
// uuid primary keys, so the first 15 bytes of the uuid are rendered as 24
// lower-case base32 characters. Lookups decode the prefix and match the
// canonical uuid text prefix, which keeps externally seeded (random) UUIDs
// addressable as well.
// ---------------------------------------------------------------------------

var pluginIDPattern = regexp.MustCompile(`^c[a-z0-9]{24}$`)

var pluginIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func encodePluginID(id string) string {
	u, err := uuid.Parse(id)
	if err != nil {
		return id
	}
	return "c" + strings.ToLower(pluginIDEncoding.EncodeToString(u[:15]))
}

// decodePluginID returns a canonical uuid text prefix (the first 15 bytes,
// including hyphens) suitable for a `id::text LIKE prefix || '%'` probe.
func decodePluginID(id string) (string, bool) {
	if !pluginIDPattern.MatchString(id) {
		return "", false
	}
	raw, err := pluginIDEncoding.DecodeString(strings.ToUpper(id[1:]))
	if err != nil || len(raw) != 15 {
		return "", false
	}
	h := hex.EncodeToString(raw)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:30], true
}

func pluginIDFromPath(raw string) (string, error) {
	prefix, ok := decodePluginID(raw)
	if !ok {
		return "", httpapi.Err(http.StatusNotFound, "NOT_FOUND", "plugin not found")
	}
	return prefix, nil
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

const (
	maxPluginListLimit = 100
	defaultPageLimit   = 100
	presignDownloadTTL = 10 * time.Minute
	presignUploadTTL   = time.Hour
	maxPluginUpload    = 128 * 1024 * 1024
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

func statusPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func encodeCursor(t time.Time, id string) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(raw string) (time.Time, string, bool) {
	if raw == "" || len(raw) > 512 {
		return time.Time{}, "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return time.Time{}, "", false
	}
	return t, parts[1], true
}

func bucketOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (m *Module) pluginsBucket() string {
	if m.deps.Config != nil {
		return bucketOrDefault(m.deps.Config.S3BucketPlugins, "cindy-plugins")
	}
	if m.deps.S3 != nil {
		return bucketOrDefault(m.deps.S3.Plugins, "cindy-plugins")
	}
	return "cindy-plugins"
}

func invalidInput(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, code, message))
}

func notFound(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, code, message))
}

func conflict(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusConflict, code, message))
}

func unavailable(w http.ResponseWriter, code, message string) {
	httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, code, message))
}
