package skillhub

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

func (m *Module) loadOwnedSkill(ctx context.Context, slug, account string) (*skillBase, error) {
	if !slugPattern.MatchString(slug) {
		return nil, notFoundError("NOT_FOUND", "skill not found")
	}
	base, err := m.loadSkillBase(ctx, slug)
	if err != nil {
		if isNoRows(err) {
			return nil, notFoundError("NOT_FOUND", "skill not found")
		}
		return nil, err
	}
	if base.OwnerAccount != account {
		return nil, &apiError{status: http.StatusForbidden, code: "PERMISSION_DENIED", message: "permission denied"}
	}
	if base.Status == "deleted" {
		return nil, notFoundError("SKILL_DELETED", "skill is deleted")
	}
	return base, nil
}

func (m *Module) patchSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	base, err := m.loadOwnedSkill(ctx, chi.URLParam(r, "name"), account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var body struct {
		DisplayName   *string         `json:"displayName"`
		Summary       *string         `json:"summary"`
		Description   *string         `json:"description"`
		Tags          json.RawMessage `json:"tags"`
		ContentLocale string          `json:"contentLocale"`
		Visibility    *string         `json:"visibility"`
		TeamSlug      *string         `json:"teamSlug"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if body.Visibility != nil && !validVisibility(*body.Visibility) {
		invalidInput(w, "INVALID_VISIBILITY", "invalid visibility")
		return
	}
	if body.DisplayName != nil {
		value := strings.TrimSpace(*body.DisplayName)
		if value == "" || len([]rune(value)) > 128 {
			invalidInput(w, "INVALID_PARAMS", "invalid displayName")
			return
		}
		body.DisplayName = &value
	}
	if body.Summary != nil && len([]rune(*body.Summary)) > 300 {
		invalidInput(w, "INVALID_PARAMS", "summary is too long")
		return
	}
	if body.Description != nil && len([]rune(*body.Description)) > 4000 {
		invalidInput(w, "INVALID_PARAMS", "description is too long")
		return
	}

	var tags []taxonomyRef
	replaceTags := body.Tags != nil
	if replaceTags {
		var rawTags []string
		if string(body.Tags) != "null" {
			if err := json.Unmarshal(body.Tags, &rawTags); err != nil {
				invalidInput(w, "INVALID_PARAMS", "invalid tags")
				return
			}
		}
		normalized, err := normalizeTags(rawTags)
		if err != nil {
			invalidInput(w, "INVALID_PARAMS", "invalid tags")
			return
		}
		tags = normalized
	}

	txErr := m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{base.ID}
		add := func(column string, value any) {
			args = append(args, value)
			sets = append(sets, column+" = $"+strconv.Itoa(len(args)))
		}
		if body.DisplayName != nil {
			add("display_name", *body.DisplayName)
		}
		if body.Summary != nil {
			add("summary", *body.Summary)
		}
		if body.Description != nil {
			add("description", *body.Description)
		}
		if body.Visibility != nil {
			add("visibility", *body.Visibility)
		}
		if _, err := tx.Exec(ctx, `UPDATE sk_skills SET `+strings.Join(sets, ", ")+` WHERE id = $1::uuid`, args...); err != nil {
			return err
		}
		if replaceTags {
			if err := applyTags(ctx, tx, base.ID, tags); err != nil {
				return err
			}
		}
		if body.TeamSlug != nil {
			slugs := []string{}
			if strings.TrimSpace(*body.TeamSlug) != "" {
				slugs = append(slugs, strings.TrimSpace(*body.TeamSlug))
			}
			if err := m.replaceTeamShares(ctx, tx, base.ID, slugs); err != nil {
				return err
			}
		} else if body.Visibility != nil && *body.Visibility != "shared" {
			if err := m.replaceTeamShares(ctx, tx, base.ID, []string{}); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		writeInternal(w)
		return
	}
	m.auditLog(ctx, "skill.update", "skill", base.ID, nil)
	httpapi.OK(w, map[string]any{"slug": base.Slug, "status": "updated"})
}

func (m *Module) deleteSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	base, err := m.loadOwnedSkill(ctx, chi.URLParam(r, "name"), account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if _, err := m.deps.DB.Pool.Exec(ctx, `
		UPDATE sk_skills SET status = 'deleted', updated_at = now() WHERE id = $1::uuid`, base.ID); err != nil {
		writeInternal(w)
		return
	}
	m.auditLog(ctx, "skill.delete", "skill", base.ID, nil)
	httpapi.OK(w, map[string]any{"slug": base.Slug, "status": "deleted"})
}

func (m *Module) unpublishSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	base, err := m.loadOwnedSkill(ctx, chi.URLParam(r, "name"), account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if _, err := m.deps.DB.Pool.Exec(ctx, `
		UPDATE sk_skills SET status = 'unpublished', updated_at = now() WHERE id = $1::uuid`, base.ID); err != nil {
		writeInternal(w)
		return
	}
	m.auditLog(ctx, "skill.unpublish", "skill", base.ID, nil)
	httpapi.OK(w, map[string]any{"slug": base.Slug, "status": "unpublished"})
}

func (m *Module) setVisibility(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	base, err := m.loadOwnedSkill(ctx, chi.URLParam(r, "name"), account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var body struct {
		Visibility   string   `json:"visibility"`
		TeamSlug     string   `json:"teamSlug"`
		VisibleSlugs []string `json:"visibleSlugs"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	visibility := strings.TrimSpace(body.Visibility)
	if !validVisibility(visibility) {
		invalidInput(w, "INVALID_VISIBILITY", "invalid visibility")
		return
	}
	slugs := make([]string, 0, 1+len(body.VisibleSlugs))
	seen := map[string]struct{}{}
	add := func(slug string) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			return
		}
		if _, dup := seen[slug]; dup {
			return
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}
	if visibility == "shared" {
		add(body.TeamSlug)
		for _, slug := range body.VisibleSlugs {
			add(slug)
		}
	}
	txErr := m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE sk_skills SET visibility = $2, updated_at = now() WHERE id = $1::uuid`,
			base.ID, visibility); err != nil {
			return err
		}
		return m.replaceTeamShares(ctx, tx, base.ID, slugs)
	})
	if txErr != nil {
		writeInternal(w)
		return
	}
	m.auditLog(ctx, "skill.set_visibility", "skill", base.ID, map[string]any{"visibility": visibility})
	httpapi.OK(w, map[string]any{"slug": base.Slug, "visibility": visibility})
}

func (m *Module) getVisibility(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	base, err := m.loadOwnedSkill(ctx, chi.URLParam(r, "name"), account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	resp := visibilityResponse{
		SharedTeams:  []sharedTeamRef{},
		VisibleDepts: []string{},
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT vs.team_id::text, t.slug, t.name
		FROM sk_visibility_shares vs
		JOIN sk_teams t ON t.id = vs.team_id
		WHERE vs.skill_id = $1::uuid
		ORDER BY t.name`, base.ID)
	if err != nil {
		writeInternal(w)
		return
	}
	for rows.Next() {
		var team sharedTeamRef
		if err := rows.Scan(&team.ID, &team.Slug, &team.Name); err != nil {
			rows.Close()
			writeInternal(w)
			return
		}
		resp.SharedTeams = append(resp.SharedTeams, team)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}
	rows, err = m.deps.DB.Pool.Query(ctx, `
		SELECT dept_id::text FROM sk_visibility_shares
		WHERE skill_id = $1::uuid AND dept_id IS NOT NULL`, base.ID)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var deptID string
		if err := rows.Scan(&deptID); err != nil {
			writeInternal(w)
			return
		}
		resp.VisibleDepts = append(resp.VisibleDepts, deptID)
	}
	httpapi.OK(w, resp)
}
