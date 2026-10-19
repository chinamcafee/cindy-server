package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type pluginListRow struct {
	ID             string
	GhostID        string
	Scope          string
	OrgID          string
	Namespace      string
	DefaultInstall bool
	CreatedAt      time.Time
	ReleaseID      string
	Version        string
	Name           string
	Description    string
	Author         string
	SHA256         string
	SizeBytes      int64
	PublishedAt    time.Time
}

func (m *Module) listPlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := httpapi.PrincipalFrom(ctx)
	mm := m.loadMembership(ctx, p)

	query := r.URL.Query()
	scope := strings.ToLower(strings.TrimSpace(query.Get("scope")))
	if scope == "" {
		scope = "all"
	}
	switch scope {
	case "all", "public", "organization", "personal":
	default:
		invalidInput(w, "INVALID_PARAMS", "invalid scope")
		return
	}
	limit := parseLimit(query.Get("limit"), defaultPageLimit, maxPluginListLimit)
	search := strings.TrimSpace(query.Get("query"))
	if len(search) > 128 {
		search = search[:128]
	}

	args := make([]any, 0, 8)
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	visibility := []string{"p.scope = 'public'"}
	if mm.OrgID != nil && *mm.OrgID != "" {
		visibility = append(visibility, "p.scope = 'organization' AND p.org_id = "+next(*mm.OrgID)+"::uuid")
	}
	// Personal plugins have no owner column in plugin_plugins, so they are not
	// exposed in a mixed catalog (avoids leaking another account's package).
	where := []string{"p.status = 'active'", "(" + strings.Join(visibility, " OR ") + ")"}
	if scope != "all" {
		where = append(where, "p.scope = "+next(scope))
	}
	if search != "" {
		where = append(where, "strpos(lower(concat_ws(' ', p.name, p.description, p.author, p.ghost_id)), lower("+next(search)+")) > 0")
	}
	if raw := query.Get("cursor"); raw != "" {
		cursorAt, cursorID, ok := decodeCursor(raw)
		if !ok {
			invalidInput(w, "PLUGIN_LIST_CHANGED", "invalid cursor")
			return
		}
		where = append(where, "(p.created_at, p.id) < ("+next(cursorAt)+"::timestamptz, "+next(cursorID)+"::uuid)")
	}

	sql := `
		SELECT p.id::text, p.ghost_id, p.scope, COALESCE(p.org_id::text,''), COALESCE(p.namespace,''),
		       p.default_install, p.created_at,
		       r.id::text, r.version, COALESCE(r.manifest->>'name', p.name),
		       COALESCE(r.manifest->>'description',''), COALESCE(r.manifest->>'author',''),
		       r.sha256, r.size_bytes, COALESCE(r.published_at, r.created_at)
		FROM plugin_plugins p
		JOIN LATERAL (
			SELECT rr.* FROM plugin_releases rr
			WHERE rr.plugin_id = p.id
			  AND rr.review_status = 'approved'
			  AND rr.status = 'active'
			  AND rr.published_at IS NOT NULL
			ORDER BY rr.published_at DESC, rr.created_at DESC
			LIMIT 1
		) r ON true
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT ` + next(limit+1)

	rows, err := m.deps.DB.Pool.Query(ctx, sql, args...)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	defer rows.Close()

	items := make([]pluginSummary, 0, limit)
	pageIDs := make(map[string]struct{}, limit)
	var lastTime time.Time
	var lastID string
	hasMore := false
	for rows.Next() {
		var row pluginListRow
		if err := rows.Scan(&row.ID, &row.GhostID, &row.Scope, &row.OrgID, &row.Namespace,
			&row.DefaultInstall, &row.CreatedAt, &row.ReleaseID, &row.Version, &row.Name,
			&row.Description, &row.Author, &row.SHA256, &row.SizeBytes, &row.PublishedAt); err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		if len(items) >= limit {
			hasMore = true
			break
		}
		lastTime, lastID = row.CreatedAt, row.ID
		pageIDs[row.ID] = struct{}{}
		items = append(items, summaryFromRow(row, mm))
	}
	if err := rows.Err(); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	removals := m.loadRemovals(ctx, mm, pageIDs)
	var nextCursor *string
	if hasMore {
		c := encodeCursor(lastTime, lastID)
		nextCursor = &c
	}

	httpapi.OK(w, listPluginsResponse{
		SchemaVersion:       2,
		Plugins:             items,
		NextCursor:          nextCursor,
		Removals:            removals,
		CurrentOrganization: currentOrganization(mm),
	})
}

func summaryFromRow(row pluginListRow, mm membership) pluginSummary {
	namespaceValue := row.Namespace
	if row.Scope == "organization" && namespaceValue == "" {
		namespaceValue = mm.OrgSlug
	}
	var namespace *string
	if row.Scope == "organization" && namespaceValue != "" {
		namespace = stringPtr(namespaceValue)
	}
	summary := pluginSummary{
		ID:             encodePluginID(row.ID),
		GhostID:        row.GhostID,
		Name:           row.Name,
		Description:    stringPtr(row.Description),
		Author:         stringPtr(row.Author),
		Scope:          row.Scope,
		Namespace:      namespace,
		DefaultInstall: row.DefaultInstall,
		CurrentRelease: releaseSummary{
			ID:          row.ReleaseID,
			Version:     row.Version,
			SHA256:      row.SHA256,
			SizeBytes:   row.SizeBytes,
			PublishedAt: isoMillis(row.PublishedAt),
			Icon:        nil,
		},
	}
	if row.Scope == "organization" {
		organizationID := row.OrgID
		if organizationID == "" && mm.OrgID != nil {
			organizationID = *mm.OrgID
		}
		summary.OrganizationID = stringPtr(organizationID)
	}
	return summary
}

func currentOrganization(mm membership) map[string]any {
	if mm.OrgID == nil || *mm.OrgID == "" {
		return nil
	}
	org := map[string]any{
		"organizationId": *mm.OrgID,
		"pluginPrefix":   nil,
	}
	if mm.OrgSlug != "" {
		org["orgSlug"] = mm.OrgSlug
	}
	if mm.PluginPrefix != nil && *mm.PluginPrefix != "" {
		org["pluginPrefix"] = *mm.PluginPrefix
	}
	return org
}

func (m *Module) loadRemovals(ctx context.Context, mm membership, pageIDs map[string]struct{}) []removalNotice {
	removals := make([]removalNotice, 0)
	if mm.OrgID == nil || *mm.OrgID == "" {
		return removals
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT plugin_id::text, ghost_id, scope, COALESCE(org_id::text,''), COALESCE(namespace,''), action, removed_at
		FROM plugin_removals
		WHERE org_id = $1::uuid AND scope = 'organization'
		ORDER BY removed_at DESC`, *mm.OrgID)
	if err != nil {
		return removals
	}
	defer rows.Close()
	for rows.Next() {
		var id, ghostID, scope, orgID, namespace, action string
		var removedAt time.Time
		if err := rows.Scan(&id, &ghostID, &scope, &orgID, &namespace, &action, &removedAt); err != nil {
			return removals
		}
		if _, listed := pageIDs[id]; listed {
			continue
		}
		noticeNamespace := namespace
		if noticeNamespace == "" {
			noticeNamespace = mm.OrgSlug
		}
		noticeOrgID := orgID
		if noticeOrgID == "" && mm.OrgID != nil {
			noticeOrgID = *mm.OrgID
		}
		removals = append(removals, removalNotice{
			PluginID:       encodePluginID(id),
			GhostID:        ghostID,
			Scope:          scope,
			Namespace:      stringPtr(noticeNamespace),
			OrganizationID: stringPtr(noticeOrgID),
			Action:         action,
			RemovedAt:      isoMillis(removedAt),
		})
	}
	return removals
}

// ---------------------------------------------------------------------------
// Detail
// ---------------------------------------------------------------------------

func (m *Module) getPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := httpapi.PrincipalFrom(ctx)
	mm := m.loadMembership(ctx, p)
	prefix, err := pluginIDFromPath(chi.URLParam(r, "pluginId"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}

	args := []any{prefix}
	visibility := []string{"p.scope = 'public'"}
	if mm.OrgID != nil && *mm.OrgID != "" {
		args = append(args, *mm.OrgID)
		visibility = append(visibility, "p.scope = 'organization' AND p.org_id = $2::uuid")
	}
	sql := `
		SELECT p.id::text, p.ghost_id, p.scope, COALESCE(p.org_id::text,''), COALESCE(p.namespace,''),
		       p.default_install, r.id::text, r.version, r.sha256, r.size_bytes,
		       COALESCE(r.published_at, r.created_at), r.manifest::text
		FROM plugin_plugins p
		JOIN LATERAL (
			SELECT rr.* FROM plugin_releases rr
			WHERE rr.plugin_id = p.id
			  AND rr.review_status = 'approved'
			  AND rr.status = 'active'
			  AND rr.published_at IS NOT NULL
			ORDER BY rr.published_at DESC, rr.created_at DESC
			LIMIT 1
		) r ON true
		WHERE p.status = 'active' AND p.id::text LIKE $1 || '%'
		  AND (` + strings.Join(visibility, " OR ") + `)
		LIMIT 1`

	var (
		id, ghostID, scope, orgID, namespace string
		defaultInstall                       bool
		releaseID, version, sha256, manifest string
		sizeBytes                            int64
		publishedAt                          time.Time
	)
	if err := m.deps.DB.Pool.QueryRow(ctx, sql, args...).Scan(&id, &ghostID, &scope, &orgID, &namespace,
		&defaultInstall, &releaseID, &version, &sha256, &sizeBytes, &publishedAt, &manifest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w, "NOT_FOUND", "plugin not found")
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	if scope == "organization" && namespace == "" {
		namespace = mm.OrgSlug
	}
	sanitized, name, description, author := sanitizeManifest(manifest, scope, namespace)
	summary := pluginSummary{
		ID:             encodePluginID(id),
		GhostID:        ghostID,
		Name:           name,
		Description:    stringPtr(description),
		Author:         stringPtr(author),
		Scope:          scope,
		DefaultInstall: defaultInstall,
	}
	if scope == "organization" {
		if namespace != "" {
			summary.Namespace = stringPtr(namespace)
		}
		organizationID := orgID
		if organizationID == "" && mm.OrgID != nil {
			organizationID = *mm.OrgID
		}
		summary.OrganizationID = stringPtr(organizationID)
	}
	httpapi.OK(w, getPluginResponse{
		SchemaVersion: 2,
		Plugin: pluginDetail{
			pluginSummary: summary,
			CurrentRelease: releaseDetail{
				releaseSummary: releaseSummary{
					ID:          releaseID,
					Version:     version,
					SHA256:      sha256,
					SizeBytes:   sizeBytes,
					PublishedAt: isoMillis(publishedAt),
					Icon:        nil,
				},
				Manifest: sanitized,
			},
		},
	})
}

// sanitizeManifest injects the server-owned namespace for organization
// releases and strips any author-declared namespace elsewhere.
func sanitizeManifest(raw, scope, namespace string) (json.RawMessage, string, string, string) {
	decoded := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		decoded = map[string]json.RawMessage{}
	}
	if scope == "organization" && namespace != "" {
		if encoded, err := json.Marshal(namespace); err == nil {
			decoded["namespace"] = encoded
		}
	} else {
		delete(decoded, "namespace")
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		out = []byte("{}")
	}
	var name, description, author string
	if v, ok := decoded["name"]; ok {
		_ = json.Unmarshal(v, &name)
	}
	if v, ok := decoded["description"]; ok {
		_ = json.Unmarshal(v, &description)
	}
	if v, ok := decoded["author"]; ok {
		_ = json.Unmarshal(v, &author)
	}
	return out, name, description, author
}

// ---------------------------------------------------------------------------
// Download
// ---------------------------------------------------------------------------

func (m *Module) downloadRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := httpapi.PrincipalFrom(ctx)
	mm := m.loadMembership(ctx, p)
	prefix, err := pluginIDFromPath(chi.URLParam(r, "pluginId"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	releaseID := chi.URLParam(r, "releaseId")
	if _, err := uuid.Parse(releaseID); err != nil {
		notFound(w, "NOT_FOUND", "release not found")
		return
	}

	args := []any{prefix, releaseID}
	visibility := []string{"p.scope = 'public'"}
	if mm.OrgID != nil && *mm.OrgID != "" {
		args = append(args, *mm.OrgID)
		visibility = append(visibility, "p.scope = 'organization' AND p.org_id = $3::uuid")
	}
	sql := `
		SELECT p.id::text, p.ghost_id, p.scope, COALESCE(p.namespace,''), r.id::text, r.sha256, r.size_bytes
		FROM plugin_releases r
		JOIN plugin_plugins p ON p.id = r.plugin_id
		WHERE p.status = 'active' AND p.id::text LIKE $1 || '%' AND r.id = $2::uuid
		  AND r.review_status = 'approved' AND r.status = 'active'
		  AND (` + strings.Join(visibility, " OR ") + `)
		LIMIT 1`
	var (
		pluginID, ghostID, scope, namespace, foundRelease, sha256 string
		sizeBytes                                                 int64
	)
	if err := m.deps.DB.Pool.QueryRow(ctx, sql, args...).Scan(
		&pluginID, &ghostID, &scope, &namespace, &foundRelease, &sha256, &sizeBytes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w, "NOT_FOUND", "release not found")
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	if m.deps.S3 == nil {
		unavailable(w, "STORAGE_UNAVAILABLE", "object storage unavailable")
		return
	}
	bucket := m.pluginsBucket()
	key := "plugins/" + pluginID + "/" + foundRelease + ".cindy"
	if _, err := m.deps.S3.Stat(ctx, bucket, key); err != nil {
		notFound(w, "NOT_FOUND", "release object not found")
		return
	}
	expires := time.Now().UTC().Add(presignDownloadTTL)
	url, err := m.deps.S3.PresignGet(ctx, bucket, key, presignDownloadTTL)
	if err != nil {
		unavailable(w, "STORAGE_UNAVAILABLE", "object storage unavailable")
		return
	}

	if scope == "organization" && namespace == "" {
		namespace = mm.OrgSlug
	}
	var namespaceValue *string
	if scope == "organization" && namespace != "" {
		namespaceValue = stringPtr(namespace)
	}
	_, _ = m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO plugin_download_tickets (plugin_id, release_id, account_id, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`,
		pluginID, foundRelease, ticketAccount(p.AccountID), expires)

	httpapi.OK(w, downloadResponse{
		PluginID:  encodePluginID(pluginID),
		ReleaseID: foundRelease,
		GhostID:   ghostID,
		Namespace: namespaceValue,
		URL:       url,
		ExpiresAt: isoMillis(expires),
		SHA256:    sha256,
		SizeBytes: sizeBytes,
	})
}

func ticketAccount(accountID string) string {
	if _, err := uuid.Parse(accountID); err != nil {
		return uuid.Nil.String()
	}
	return accountID
}
