package skillhub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// skillFilter describes a catalog/detail query. AccountID is always a valid
// uuid string (uuid.Nil when anonymous) so visibility predicates never need
// null handling.
type skillFilter struct {
	AccountID      string
	Scope          string // market | team | "" (native/all)
	Query          string
	Category       string
	Slugs          []string
	OwnerOnly      bool
	AnyStatus      bool // owner-only listings include unpublished but not deleted
	IncludeDeleted bool
	SkipVisibility bool
}

// visibilityClause builds the viewer-specific part of the WHERE clause.
func (f skillFilter) visibilityClause(b *argsBuilder) string {
	if f.SkipVisibility {
		return "TRUE"
	}
	if f.OwnerOnly {
		return "s.owner_account_id = " + b.add(f.AccountID) + "::uuid"
	}
	switch f.Scope {
	case "market":
		return "s.visibility = 'public'"
	case "team":
		account := b.add(f.AccountID)
		shared := "s.visibility = 'shared' AND EXISTS (" +
			"SELECT 1 FROM sk_visibility_shares vs " +
			"JOIN sk_team_members tm ON tm.team_id = vs.team_id " +
			"WHERE vs.skill_id = s.id AND tm.account_id = " + account + "::uuid)"
		return "(s.owner_account_id = " + account + "::uuid OR (" + shared + "))"
	default:
		account := b.add(f.AccountID)
		shared := "s.visibility = 'shared' AND EXISTS (" +
			"SELECT 1 FROM sk_visibility_shares vs " +
			"JOIN sk_team_members tm ON tm.team_id = vs.team_id " +
			"WHERE vs.skill_id = s.id AND tm.account_id = " + account + "::uuid)"
		return "(s.visibility = 'public' OR s.owner_account_id = " + account + "::uuid OR (" + shared + "))"
	}
}

func (f skillFilter) where(b *argsBuilder) string {
	clauses := []string{"(" + f.visibilityClause(b) + ")"}
	switch {
	case f.IncludeDeleted:
	case f.AnyStatus:
		clauses = append(clauses, "s.status <> 'deleted'")
	default:
		clauses = append(clauses, "s.status = 'active'")
	}
	if f.Query != "" {
		clauses = append(clauses, "strpos(lower(concat_ws(' ', s.display_name, s.summary, s.description, s.slug)), lower("+b.add(f.Query)+")) > 0")
	}
	if f.Category != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM sk_skill_categories sc JOIN sk_categories c ON c.id = sc.category_id WHERE sc.skill_id = s.id AND c.slug = "+b.add(f.Category)+")")
	}
	if len(f.Slugs) > 0 {
		clauses = append(clauses, "s.slug = ANY("+b.add(f.Slugs)+"::text[])")
	}
	return strings.Join(clauses, " AND ")
}

const skillSelectColumns = `
	s.id::text, s.slug, s.display_name, COALESCE(s.summary,''), COALESCE(s.description,''),
	COALESCE(s.latest_version,''), s.visibility, s.status, COALESCE(s.icon_key,''),
	COALESCE(s.github_url,''), s.owner_account_id::text, s.owner_type, s.updated_at,
	COALESCE(v.folder_hash,''), COALESCE(v.zip_sha256,''), COALESCE(v.scan_status,''),
	COALESCE(own.display_name,''), COALESCE(own.org_slug,''), COALESCE(own.org_name,''),
	COALESCE(pv.version,''), COALESCE(pv.scan_status,''),
	(SELECT count(*) FROM sk_download_tickets dt WHERE dt.skill_id = s.id)`

const skillSelectFrom = `
	FROM sk_skills s
	LEFT JOIN sk_versions v ON v.skill_id = s.id AND v.version = s.latest_version
	LEFT JOIN LATERAL (
		SELECT m.display_name, COALESCE(o.slug::text,'') AS org_slug,
		       COALESCE(NULLIF(m.org_name,''), m.display_name, o.name) AS org_name
		FROM auth_memberships m
		LEFT JOIN auth_organizations o ON o.id = m.org_id
		WHERE m.account_id = s.owner_account_id AND m.status = 'active'
		ORDER BY (m.kind = 'org') DESC, m.created_at
		LIMIT 1
	) own ON true
	LEFT JOIN LATERAL (
		SELECT pv.version, pv.scan_status FROM sk_versions pv
		WHERE pv.skill_id = s.id
		  AND pv.version <> COALESCE(s.latest_version,'')
		  AND pv.scan_status IN ('pending','scanning','machine_reviewing')
		ORDER BY pv.created_at DESC
		LIMIT 1
	) pv ON true`

type skillRow struct {
	ID            string
	Slug          string
	DisplayName   string
	Summary       string
	Description   string
	LatestVersion string
	Visibility    string
	Status        string
	IconKey       string
	GitHubURL     string
	OwnerAccount  string
	OwnerType     string
	UpdatedAt     time.Time
	FolderHash    string
	ZipSHA256     string
	ScanStatus    string
	OwnerDisplay  string
	OwnerOrgSlug  string
	OwnerOrgName  string
	PendingVer    string
	PendingScan   string
	Downloads     int64
}

func (m *Module) fetchSkills(ctx context.Context, filter skillFilter, order string, limit, offset int) ([]skillItem, int64, error) {
	countArgs := &argsBuilder{}
	countWhere := filter.where(countArgs)
	var total int64
	if err := m.deps.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM sk_skills s WHERE "+countWhere, countArgs.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 || limit <= 0 {
		return []skillItem{}, total, nil
	}

	args := &argsBuilder{}
	where := filter.where(args)
	limitPlaceholder := args.add(limit)
	offsetPlaceholder := args.add(offset)
	sql := "SELECT " + skillSelectColumns + skillSelectFrom +
		" WHERE " + where + " ORDER BY " + orderClause(order) + " LIMIT " + limitPlaceholder + " OFFSET " + offsetPlaceholder
	rows, err := m.deps.DB.Pool.Query(ctx, sql, args.args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	rowsOut := make([]skillRow, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var row skillRow
		if err := rows.Scan(&row.ID, &row.Slug, &row.DisplayName, &row.Summary, &row.Description,
			&row.LatestVersion, &row.Visibility, &row.Status, &row.IconKey, &row.GitHubURL,
			&row.OwnerAccount, &row.OwnerType, &row.UpdatedAt, &row.FolderHash, &row.ZipSHA256,
			&row.ScanStatus, &row.OwnerDisplay, &row.OwnerOrgSlug, &row.OwnerOrgName,
			&row.PendingVer, &row.PendingScan, &row.Downloads); err != nil {
			return nil, 0, err
		}
		rowsOut = append(rowsOut, row)
		ids = append(ids, row.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	categories, tags, err := m.loadTaxonomy(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	items := make([]skillItem, 0, len(rowsOut))
	for _, row := range rowsOut {
		items = append(items, m.skillItemFromRow(ctx, row, filter.AccountID, categories[row.ID], tags[row.ID]))
	}
	return items, total, nil
}

func orderClause(order string) string {
	switch order {
	case "trending":
		return "s.updated_at DESC, s.id"
	case "downloads":
		return "(SELECT count(*) FROM sk_download_tickets dt WHERE dt.skill_id = s.id) DESC, s.updated_at DESC"
	case "updated_at":
		return "s.updated_at DESC, s.id"
	case "created_at":
		return "s.created_at DESC, s.id"
	default:
		return "s.updated_at DESC, s.id"
	}
}

func normalizeModeration(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "published", "passed", "pass", "approved", "active":
		return "published"
	case "pending", "machine_reviewing":
		return "pending"
	case "scanning":
		return "scanning"
	case "quarantine", "warning", "warn", "manual_reviewing":
		return "quarantine"
	case "rejected", "failed", "fail", "blocked":
		return "rejected"
	default:
		return "pending"
	}
}

func (m *Module) skillItemFromRow(ctx context.Context, row skillRow, viewer string, categories, tags []taxonomyRef) skillItem {
	ownerType := row.OwnerType
	if ownerType == "" {
		ownerType = "personal"
	}
	ownerName := row.OwnerOrgName
	ownerSlug := row.OwnerOrgSlug
	if ownerType != "organization" || ownerSlug == "" {
		ownerType = "personal"
		ownerSlug = row.OwnerAccount
		ownerName = row.OwnerDisplay
	}
	if ownerName == "" {
		ownerName = ownerSlug
	}
	publisherName := ownerName
	if row.OwnerDisplay != "" {
		publisherName = row.OwnerDisplay
	}
	isMine := viewer != "" && viewer != uuidNil && viewer == row.OwnerAccount
	item := skillItem{
		Slug:             row.Slug,
		Icon:             m.iconURL(ctx, row.IconKey),
		DisplayName:      row.DisplayName,
		Summary:          stringPtr(row.Summary),
		Description:      stringPtr(row.Description),
		Version:          row.LatestVersion,
		MarketVersion:    row.LatestVersion,
		FolderHash:       row.FolderHash,
		FileHash:         row.ZipSHA256,
		Owner:            ownerRef{Type: ownerType, Slug: ownerSlug, Name: ownerName},
		Publisher:        publisherRef{Name: publisherName},
		Visibility:       row.Visibility,
		ModerationStatus: normalizeModeration(row.ScanStatus),
		UpdatedAt:        isoMillis(row.UpdatedAt),
		IsMine:           isMine,
		IsCreator:        isMine,
		CanManage:        isMine,
		Categories:       categories,
		Tags:             tags,
		GitHubURL:        stringPtr(row.GitHubURL),
		Stats:            skillStats{Downloads: row.Downloads},
	}
	if row.PendingVer != "" {
		item.PendingVersion = &pendingVersionRef{Version: row.PendingVer, Status: normalizeModeration(row.PendingScan)}
	}
	if item.Categories == nil {
		item.Categories = []taxonomyRef{}
	}
	if item.Tags == nil {
		item.Tags = []taxonomyRef{}
	}
	return item
}

const uuidNil = "00000000-0000-0000-0000-000000000000"

func (m *Module) iconURL(ctx context.Context, key string) *string {
	if key == "" || m.deps.S3 == nil {
		return nil
	}
	url, err := m.deps.S3.PresignGet(ctx, m.skillsBucket(), key, 30*time.Minute)
	if err != nil || url == "" {
		return nil
	}
	return &url
}

func (m *Module) loadTaxonomy(ctx context.Context, ids []string) (map[string][]taxonomyRef, map[string][]taxonomyRef, error) {
	categories := map[string][]taxonomyRef{}
	tags := map[string][]taxonomyRef{}
	if len(ids) == 0 {
		return categories, tags, nil
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT sc.skill_id::text, c.slug, c.name, COALESCE(c.source,'system')
		FROM sk_skill_categories sc
		JOIN sk_categories c ON c.id = sc.category_id
		WHERE sc.skill_id::text = ANY($1::text[])
		ORDER BY c.name`, ids)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var skillID string
		var ref taxonomyRef
		if err := rows.Scan(&skillID, &ref.Slug, &ref.Name, &ref.Source); err != nil {
			rows.Close()
			return nil, nil, err
		}
		categories[skillID] = append(categories[skillID], ref)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = m.deps.DB.Pool.Query(ctx, `
		SELECT st.skill_id::text, t.slug, t.name, COALESCE(t.source,'system')
		FROM sk_skill_tags st
		JOIN sk_tags t ON t.id = st.tag_id
		WHERE st.skill_id::text = ANY($1::text[])
		ORDER BY t.name`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var skillID string
		var ref taxonomyRef
		if err := rows.Scan(&skillID, &ref.Slug, &ref.Name, &ref.Source); err != nil {
			return nil, nil, err
		}
		tags[skillID] = append(tags[skillID], ref)
	}
	return categories, tags, rows.Err()
}

// ---------------------------------------------------------------------------
// Single skill access
// ---------------------------------------------------------------------------

type skillBase struct {
	ID            string
	Slug          string
	LatestVersion string
	Visibility    string
	Status        string
	OwnerAccount  string
	OwnerType     string
}

func (m *Module) loadSkillBase(ctx context.Context, slug string) (*skillBase, error) {
	var base skillBase
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, slug, COALESCE(latest_version,''), visibility, status, owner_account_id::text, owner_type
		FROM sk_skills WHERE slug = $1`, slug).
		Scan(&base.ID, &base.Slug, &base.LatestVersion, &base.Visibility, &base.Status, &base.OwnerAccount, &base.OwnerType)
	if err != nil {
		return nil, err
	}
	return &base, nil
}

func (m *Module) canViewSkill(ctx context.Context, base *skillBase, account string) bool {
	if account != "" && account != uuidNil && base.OwnerAccount == account {
		return true
	}
	if base.Status != "active" {
		return false
	}
	switch base.Visibility {
	case "public":
		return true
	case "shared":
		if account == "" || account == uuidNil {
			return false
		}
		var visible bool
		err := m.deps.DB.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM sk_visibility_shares vs
				JOIN sk_team_members tm ON tm.team_id = vs.team_id
				WHERE vs.skill_id = $1::uuid AND tm.account_id = $2::uuid)`,
			base.ID, account).Scan(&visible)
		return err == nil && visible
	default:
		return false
	}
}

// loadVisibleSkill loads a skill and enforces visibility. It returns HTTP
// errors with the stable business codes.
func (m *Module) loadVisibleSkill(ctx context.Context, slug, account string) (*skillBase, error) {
	if !slugPattern.MatchString(slug) {
		return nil, notFoundError("NOT_FOUND", "skill not found")
	}
	base, err := m.loadSkillBase(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundError("NOT_FOUND", "skill not found")
		}
		return nil, err
	}
	if base.Status == "deleted" {
		return base, errSkillDeleted
	}
	if !m.canViewSkill(ctx, base, account) {
		if base.Status == "unpublished" {
			return nil, notFoundError("SKILL_UNPUBLISHED", "skill is unpublished")
		}
		return nil, notFoundError("NOT_FOUND", "skill not found")
	}
	return base, nil
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func notFoundError(code, message string) error {
	return &apiError{status: 404, code: code, message: message}
}

var errSkillDeleted = &apiError{status: 404, code: "SKILL_DELETED", message: "skill is deleted"}

func writeAPIError(w http.ResponseWriter, err error) {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		httpapi.WriteError(w, httpapi.Err(apiErr.status, apiErr.code, apiErr.message))
		return
	}
	writeInternal(w)
}
