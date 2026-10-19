package skillhub

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

func catalogScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "market":
		return "market"
	case "team":
		return "team"
	default:
		return ""
	}
}

func catalogOrder(sortBy, order string) string {
	direction := "DESC"
	if strings.EqualFold(order, "asc") {
		direction = "ASC"
	}
	var primary string
	switch sortBy {
	case "downloads":
		primary = "(SELECT count(*) FROM sk_download_tickets dt WHERE dt.skill_id = s.id)"
	case "created_at":
		primary = "s.created_at"
	case "trending", "updated_at":
		primary = "s.updated_at"
	default:
		primary = "s.updated_at"
	}
	return primary + " " + direction + ", s.id " + direction
}

func (m *Module) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	filter := skillFilter{
		AccountID: viewerAccount(ctx),
		Scope:     catalogScope(query.Get("scope")),
		Query:     strings.TrimSpace(query.Get("q")),
		Category:  strings.TrimSpace(query.Get("category")),
	}
	page, pageSize := parsePage(query.Get("page")), parsePageSize(query.Get("pageSize"))
	items, total, err := m.fetchSkills(ctx, filter, catalogOrder(query.Get("sort"), query.Get("order")), pageSize, (page-1)*pageSize)
	if err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, skillsListResponse{Items: items, Total: total})
}

func (m *Module) listInstalledSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	var body installedSkillsBody
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if len(body.InstalledSkills) > 500 {
		invalidInput(w, "INVALID_PARAMS", "too many installed skills")
		return
	}
	installed := make(map[string]string, len(body.InstalledSkills))
	slugs := make([]string, 0, len(body.InstalledSkills))
	for _, item := range body.InstalledSkills {
		slug := strings.TrimSpace(item.Slug)
		if !slugPattern.MatchString(slug) {
			continue
		}
		if _, exists := installed[slug]; exists {
			continue
		}
		installed[slug] = item.Version
		slugs = append(slugs, slug)
	}
	filter := skillFilter{
		AccountID: viewerAccount(ctx),
		Scope:     catalogScope(query.Get("scope")),
		Query:     strings.TrimSpace(query.Get("q")),
		Category:  strings.TrimSpace(query.Get("category")),
		Slugs:     slugs,
	}
	page, pageSize := parsePage(query.Get("page")), parsePageSize(query.Get("pageSize"))
	items, total, err := m.fetchSkills(ctx, filter, catalogOrder(query.Get("sort"), query.Get("order")), pageSize, (page-1)*pageSize)
	if err != nil {
		writeInternal(w)
		return
	}
	var available int64
	for _, item := range items {
		if version := installed[item.Slug]; version == "" || version != item.Version {
			available++
		}
	}
	httpapi.OK(w, installedSkillsResponse{Items: items, Total: total, AvailableCount: available})
}

func (m *Module) listMyPublished(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	query := r.URL.Query()
	filter := skillFilter{
		AccountID: account,
		OwnerOnly: true,
		AnyStatus: true,
		Query:     strings.TrimSpace(query.Get("q")),
		Category:  strings.TrimSpace(query.Get("category")),
	}
	page, pageSize := parsePage(query.Get("page")), parsePageSize(query.Get("pageSize"))
	items, total, err := m.fetchSkills(ctx, filter, catalogOrder(query.Get("sort"), query.Get("order")), pageSize, (page-1)*pageSize)
	if err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, skillsListResponse{Items: items, Total: total})
}

func (m *Module) batchDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body batchDetailBody
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	seen := map[string]struct{}{}
	slugs := make([]string, 0, len(body.Slugs))
	for _, slug := range body.Slugs {
		slug = strings.TrimSpace(slug)
		if !slugPattern.MatchString(slug) {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}
	if len(slugs) > 100 {
		invalidInput(w, "INVALID_PARAMS", "at most 100 slugs per request")
		return
	}
	if len(slugs) == 0 {
		httpapi.OK(w, skillsListResponse{Items: []skillItem{}, Total: 0})
		return
	}
	filter := skillFilter{
		AccountID: viewerAccount(ctx),
		Scope:     catalogScope(body.Scope),
		Slugs:     slugs,
	}
	items, _, err := m.fetchSkills(ctx, filter, "s.updated_at DESC, s.id", len(slugs), 0)
	if err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, skillsListResponse{Items: items, Total: int64(len(items))})
}

func (m *Module) getSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "name")
	if !slugPattern.MatchString(slug) {
		invalidName(w)
		return
	}
	account := viewerAccount(ctx)
	if _, err := m.loadVisibleSkill(ctx, slug, account); err != nil {
		if err == errSkillDeleted {
			httpapi.OK(w, map[string]any{"deleted": true})
			return
		}
		writeAPIError(w, err)
		return
	}
	items, _, err := m.fetchSkills(ctx, skillFilter{
		AccountID:      account,
		Slugs:          []string{slug},
		SkipVisibility: true,
		IncludeDeleted: true,
	}, "s.updated_at DESC, s.id", 1, 0)
	if err != nil {
		writeInternal(w)
		return
	}
	if len(items) == 0 {
		notFound(w, "NOT_FOUND", "skill not found")
		return
	}
	httpapi.OK(w, items[0])
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

type categoryRow struct {
	ID       string
	Slug     string
	Name     string
	ParentID string
	Source   string
	Count    int64
	MyCount  int64
}

func (m *Module) listCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	scope := catalogScope(query.Get("scope"))
	includeEmpty := !strings.EqualFold(query.Get("includeEmpty"), "false")
	account := viewerAccount(ctx)

	filter := skillFilter{AccountID: account, Scope: scope}
	builder := &argsBuilder{}
	visibility := filter.where(builder)
	myPlaceholder := builder.add(account)

	sql := `
		SELECT c.id::text, c.slug, c.name, COALESCE(c.parent_id::text,''), COALESCE(c.source,'system'),
		       (SELECT count(*) FROM sk_skill_categories sc
		          JOIN sk_skills s ON s.id = sc.skill_id
		         WHERE sc.category_id = c.id AND ` + visibility + `),
		       (SELECT count(*) FROM sk_skill_categories sc
		          JOIN sk_skills s ON s.id = sc.skill_id
		         WHERE sc.category_id = c.id AND ` + visibility + `
		           AND s.owner_account_id = ` + myPlaceholder + `::uuid)
		FROM sk_categories c
		ORDER BY c.name`
	rows, err := m.deps.DB.Pool.Query(ctx, sql, builder.args...)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	all := make([]categoryRow, 0)
	for rows.Next() {
		var row categoryRow
		if err := rows.Scan(&row.ID, &row.Slug, &row.Name, &row.ParentID, &row.Source, &row.Count, &row.MyCount); err != nil {
			writeInternal(w)
			return
		}
		all = append(all, row)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}

	byID := make(map[string]*categoryNode, len(all))
	for _, row := range all {
		byID[row.ID] = &categoryNode{
			Slug:         row.Slug,
			Name:         row.Name,
			Source:       row.Source,
			SkillCount:   row.Count,
			MySkillCount: row.MyCount,
			Children:     []categoryNode{},
		}
	}
	roots := make([]categoryNode, 0)
	for _, row := range all {
		if row.ParentID == "" {
			continue
		}
		node := byID[row.ID]
		if parent, ok := byID[row.ParentID]; ok {
			parent.Children = append(parent.Children, *node)
			parent.SkillCount += node.SkillCount
			parent.MySkillCount += node.MySkillCount
		}
	}
	for _, row := range all {
		if row.ParentID == "" {
			roots = append(roots, *byID[row.ID])
		}
	}
	if !includeEmpty {
		filtered := roots[:0]
		for _, root := range roots {
			children := root.Children[:0]
			for _, child := range root.Children {
				if child.SkillCount > 0 || child.MySkillCount > 0 {
					children = append(children, child)
				}
			}
			root.Children = children
			if root.SkillCount > 0 || root.MySkillCount > 0 || len(root.Children) > 0 {
				filtered = append(filtered, root)
			}
		}
		roots = filtered
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })
	httpapi.OK(w, roots)
}

// ---------------------------------------------------------------------------
// Teams / departments
// ---------------------------------------------------------------------------

func (m *Module) listMyTeams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT t.slug, t.name, t.type, COALESCE(t.source,'system'), t.is_personal, tm.role
		FROM sk_team_members tm
		JOIN sk_teams t ON t.id = tm.team_id
		WHERE tm.account_id = $1::uuid
		ORDER BY t.name`, account)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	teams := make([]teamRef, 0)
	for rows.Next() {
		var team teamRef
		if err := rows.Scan(&team.Slug, &team.Name, &team.Type, &team.Source, &team.IsPersonal, &team.MyRole); err != nil {
			writeInternal(w)
			return
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, teams)
}

func (m *Module) listMyDepartments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	mm := m.loadMembership(ctx, p)
	resp := departmentsResponse{
		Departments:     []departmentRef{},
		FirstLevelDepts: []departmentRef{},
		AllDeptIDs:      []string{},
	}
	if mm.OrgID == "" {
		httpapi.OK(w, resp)
		return
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT id::text, slug, name, COALESCE(parent_id::text,'')
		FROM sk_departments WHERE org_id = $1::uuid ORDER BY name`, mm.OrgID)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	type deptRow struct{ id, slug, name, parent string }
	all := make([]deptRow, 0)
	names := map[string]string{}
	parents := map[string]string{}
	for rows.Next() {
		var row deptRow
		if err := rows.Scan(&row.id, &row.slug, &row.name, &row.parent); err != nil {
			writeInternal(w)
			return
		}
		all = append(all, row)
		names[row.id] = row.name
		parents[row.id] = row.parent
	}
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}
	for _, row := range all {
		pathParts := []string{row.name}
		parent := row.parent
		for depth := 0; parent != "" && depth < 16; depth++ {
			if name, ok := names[parent]; ok {
				pathParts = append([]string{name}, pathParts...)
			}
			parent = parents[parent]
		}
		resp.Departments = append(resp.Departments, departmentRef{
			DeptID: row.id,
			Name:   row.name,
			Path:   strings.Join(pathParts, " / "),
		})
		resp.AllDeptIDs = append(resp.AllDeptIDs, row.id)
		if row.parent == "" {
			resp.FirstLevelDepts = append(resp.FirstLevelDepts, departmentRef{DeptID: row.id, Name: row.name})
		}
	}
	httpapi.OK(w, resp)
}

// ---------------------------------------------------------------------------
// Organization auto-install
// ---------------------------------------------------------------------------

func (m *Module) autoInstall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	mm := m.loadMembership(ctx, p)
	resp := autoInstallResponse{Skills: []autoInstallSkill{}, NextCursor: nil}
	if mm.OrgID == "" {
		httpapi.OK(w, resp)
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor != "" && !slugPattern.MatchString(cursor) {
		invalidInput(w, "INVALID_PARAMS", "invalid cursor")
		return
	}
	limit := 100
	args := []any{mm.OrgID, cursor, limit + 1}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT s.slug, v.version
		FROM sk_auto_install ai
		JOIN sk_skills s ON s.id = ai.skill_id
		JOIN sk_versions v ON v.skill_id = s.id AND v.version = ai.version
		WHERE ai.org_id = $1::uuid
		  AND s.status = 'active'
		  AND s.visibility IN ('public','shared')
		  AND s.slug ~ '^[a-z0-9][a-z0-9-]{0,127}$'
		  AND v.version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'
		  AND ($2 = '' OR s.slug > $2)
		ORDER BY s.slug
		LIMIT $3`, args...)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	skills := make([]autoInstallSkill, 0, limit)
	var last string
	hasMore := false
	for rows.Next() {
		var name, version string
		if err := rows.Scan(&name, &version); err != nil {
			writeInternal(w)
			return
		}
		if len(skills) >= limit {
			hasMore = true
			break
		}
		last = name
		skills = append(skills, autoInstallSkill{Name: name, Version: version, CatalogScope: "team"})
	}
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}
	if hasMore {
		next := last
		resp.NextCursor = &next
	}
	resp.Skills = skills
	httpapi.OK(w, resp)
}
