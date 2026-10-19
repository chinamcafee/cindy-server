package skillhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/minio/minio-go/v7"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

var tagSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func bumpPatch(version string) string {
	trimmed := strings.TrimSpace(version)
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) < 3 {
		return "1.0.0"
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch < 0 {
		return "1.0.0"
	}
	return parts[0] + "." + parts[1] + "." + strconv.Itoa(patch+1)
}

func (m *Module) publishInit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	var body struct {
		Slug    string `json:"slug"`
		Version string `json:"version"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	slug := strings.TrimSpace(body.Slug)
	if !slugPattern.MatchString(slug) {
		invalidName(w)
		return
	}
	if body.Version != "" && !versionPattern.MatchString(body.Version) {
		invalidInput(w, "INVALID_PARAMS", "invalid version")
		return
	}

	var skillID, owner, latest string
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, owner_account_id::text, COALESCE(latest_version,'')
		FROM sk_skills WHERE slug = $1`, slug).Scan(&skillID, &owner, &latest)
	switch {
	case err == nil:
		if owner != account {
			conflict(w, "NAME_TAKEN", "skill name is already taken")
			return
		}
	case isNoRows(err):
	case err != nil:
		writeInternal(w)
		return
	}

	next := strings.TrimSpace(body.Version)
	if next == "" {
		next = bumpPatch(latest)
	} else if skillID != "" {
		var exists bool
		if err := m.deps.DB.Pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM sk_versions WHERE skill_id = $1::uuid AND version = $2)`,
			skillID, next).Scan(&exists); err == nil && exists {
			next = bumpPatch(latest)
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		var exists bool
		if err := m.deps.DB.Pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM sk_versions WHERE skill_id = $1::uuid AND version = $2)`,
			nullableString(skillID), next).Scan(&exists); err != nil {
			// A missing skill row is fine: no version can exist yet.
			break
		}
		if !exists {
			break
		}
		next = bumpPatch(next)
	}
	if !versionPattern.MatchString(next) {
		invalidInput(w, "INVALID_PARAMS", "unable to determine the next version")
		return
	}
	if m.deps.S3 == nil {
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	ossKey := "skills/" + slug + "/" + next + "/" + randomHex(16) + ".zip"
	expires := time.Now().UTC().Add(presignPublishTTL)
	uploadURL, err := m.deps.S3.PresignPut(ctx, m.skillsBucket(), ossKey, presignPublishTTL, "application/zip")
	if err != nil {
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	if _, err := m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO sk_publish_uploads (account_id, slug, version, oss_key, status)
		VALUES ($1::uuid, $2, $3, $4, 'pending')`, account, slug, next, ossKey); err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, publishInitResponse{
		NextVersion: next,
		OSSKey:      ossKey,
		UploadURL:   uploadURL,
		ExpiresAt:   isoMillis(expires),
	})
}

func (m *Module) publishCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	var body publishCommitBody
	if err := httpapi.DecodeJSONLoose(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	slug := strings.TrimSpace(body.Slug)
	version := strings.TrimSpace(body.Version)
	ossKey := strings.TrimSpace(body.OSSKey)
	if !slugPattern.MatchString(slug) {
		invalidName(w)
		return
	}
	if !versionPattern.MatchString(version) {
		invalidInput(w, "INVALID_PARAMS", "invalid version")
		return
	}
	if !isSafeRelativePath(ossKey) || !strings.HasPrefix(ossKey, "skills/"+slug+"/"+version+"/") {
		invalidInput(w, "INVALID_PARAMS", "invalid ossKey")
		return
	}
	visibility := strings.TrimSpace(body.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	if !validVisibility(visibility) {
		invalidInput(w, "INVALID_VISIBILITY", "invalid visibility")
		return
	}
	tags, err := normalizeTags(body.Tags)
	if err != nil {
		invalidInput(w, "INVALID_PARAMS", "invalid tags")
		return
	}

	var uploadID string
	if err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text FROM sk_publish_uploads
		WHERE account_id = $1::uuid AND slug = $2 AND version = $3 AND oss_key = $4
		ORDER BY created_at DESC LIMIT 1`, account, slug, version, ossKey).Scan(&uploadID); err != nil {
		if isNoRows(err) {
			invalidInput(w, "INVALID_PARAMS", "unknown upload")
			return
		}
		writeInternal(w)
		return
	}

	var skillID, owner, latest string
	err = m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, owner_account_id::text, COALESCE(latest_version,'')
		FROM sk_skills WHERE slug = $1`, slug).Scan(&skillID, &owner, &latest)
	newSkill := false
	switch {
	case err == nil:
		if owner != account {
			forbidden(w)
			return
		}
	case isNoRows(err):
		newSkill = true
		skillID = uuid.NewString()
	default:
		writeInternal(w)
		return
	}

	if m.deps.S3 == nil {
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	bucket := m.skillsBucket()
	info, err := m.deps.S3.Stat(ctx, bucket, ossKey)
	if err != nil {
		if isMissingObject(err) {
			notFound(w, "OSS_OBJECT_NOT_FOUND", "uploaded skill package not found")
			return
		}
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	if info.Size > maxSkillArchiveBytes {
		httpapi.WriteError(w, httpapi.Err(http.StatusRequestEntityTooLarge, "SKILL_FILE_TOO_LARGE", "skill package is too large"))
		return
	}
	zipPath, size, digest, err := m.downloadSkillObject(ctx, bucket, ossKey)
	if err != nil {
		if isMissingObject(err) {
			notFound(w, "OSS_OBJECT_NOT_FOUND", "uploaded skill package not found")
			return
		}
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	defer os.Remove(zipPath)
	files, err := inspectSkillZip(zipPath)
	if err != nil {
		invalidInput(w, "MANIFEST_INVALID", "skill package is invalid")
		return
	}
	folderHash := computeFolderHash(files)

	finalKey := "skills/" + slug + "/" + version + "/" + digest + ".zip"
	if _, err := m.deps.S3.Client().CopyObject(ctx,
		minio.CopyDestOptions{Bucket: bucket, Object: finalKey},
		minio.CopySrcOptions{Bucket: bucket, Object: ossKey}); err != nil {
		unavailable(w, "OSS_PUT_FAILED", "failed to finalize the skill package")
		return
	}

	mm := m.loadMembership(ctx, httpapi.MustPrincipal(ctx))
	ownerType := "personal"
	if mm.Kind == "org" && mm.OrgID != "" {
		ownerType = "organization"
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" && newSkill {
		displayName = slug
	}
	summary := strings.TrimSpace(body.Summary)
	description := strings.TrimSpace(body.Description)

	versionID := uuid.NewString()
	txErr := m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if newSkill {
			if _, err := tx.Exec(ctx, `
				INSERT INTO sk_skills
				(id, slug, owner_account_id, owner_type, display_name, summary, description,
				 visibility, status, latest_version)
				VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8, 'active', $9)`,
				skillID, slug, account, ownerType, displayName, nullableString(summary),
				nullableString(description), visibility, version); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE sk_skills
				SET display_name = COALESCE(NULLIF($2,''), display_name),
				    summary = COALESCE(NULLIF($3,''), summary),
				    description = COALESCE(NULLIF($4,''), description),
				    latest_version = $5, updated_at = now()
				WHERE id = $1::uuid`,
				skillID, displayName, summary, description, version); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sk_versions
			(id, skill_id, version, folder_hash, zip_sha256, file_size, status, scan_status)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, 'pending', 'pending')`,
			versionID, skillID, version, folderHash, digest, size); err != nil {
			return err
		}
		for _, file := range files {
			if _, err := tx.Exec(ctx, `
				INSERT INTO sk_files (version_id, path, size_bytes, language, sha256, truncated)
				VALUES ($1::uuid, $2, $3, $4, $5, $6)`,
				versionID, file.Path, file.Size, file.Language, nullableString(file.SHA256), file.Truncated); err != nil {
				return err
			}
		}
		if newSkill {
			if err := applyTags(ctx, tx, skillID, tags); err != nil {
				return err
			}
			if err := m.replaceTeamShares(ctx, tx, skillID, commitTeamSlugs(body)); err != nil {
				return err
			}
		} else if len(tags) > 0 {
			if err := applyTags(ctx, tx, skillID, tags); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE sk_publish_uploads SET status = 'published', oss_key = $2
			WHERE id = $1::uuid`, uploadID, finalKey); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(txErr, &pgErr) && pgErr.Code == "23505" {
			conflict(w, "VERSION_RACE", "this version already exists")
			return
		}
		_ = m.deps.S3.Remove(ctx, bucket, finalKey)
		writeInternal(w)
		return
	}
	m.auditLog(ctx, "skill.publish.commit", "skill", skillID, map[string]any{"slug": slug, "version": version})
	httpapi.OK(w, publishCommitResponse{Status: "pending", SkillID: skillID, Version: version})
}

func commitTeamSlugs(body publishCommitBody) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1+len(body.VisibleSlugs))
	for _, slug := range append([]string{body.TeamSlug, body.DeptTeamSlug}, body.VisibleSlugs...) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out
}

func (m *Module) downloadSkillObject(ctx context.Context, bucket, key string) (string, int64, string, error) {
	object, err := m.deps.S3.Client().GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", 0, "", err
	}
	defer object.Close()
	file, err := os.CreateTemp("", "cindy-skill-upload-*.zip")
	if err != nil {
		return "", 0, "", err
	}
	path := file.Name()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(object, maxSkillArchiveBytes+1))
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return "", 0, "", err
	}
	if closeErr != nil {
		os.Remove(path)
		return "", 0, "", closeErr
	}
	if written > maxSkillArchiveBytes {
		os.Remove(path)
		return "", 0, "", errors.New("skill archive too large")
	}
	return path, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func validVisibility(value string) bool {
	return value == "private" || value == "shared" || value == "public"
}

func normalizeTags(raw []string) ([]taxonomyRef, error) {
	out := make([]taxonomyRef, 0, len(raw))
	seen := map[string]struct{}{}
	for _, value := range raw {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if len([]rune(name)) > 64 {
			return nil, errors.New("tag is too long")
		}
		slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		if !tagSlugPattern.MatchString(slug) {
			return nil, errors.New("invalid tag")
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, taxonomyRef{Slug: slug, Name: name, Source: "platform"})
	}
	if len(out) > 20 {
		return nil, errors.New("too many tags")
	}
	return out, nil
}

func applyTags(ctx context.Context, tx pgx.Tx, skillID string, tags []taxonomyRef) error {
	if _, err := tx.Exec(ctx, `DELETE FROM sk_skill_tags WHERE skill_id = $1::uuid`, skillID); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sk_tags (slug, name, source) VALUES ($1, $2, 'platform')
			ON CONFLICT (slug) DO NOTHING`, tag.Slug, tag.Name); err != nil {
			return err
		}
		var tagID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM sk_tags WHERE slug = $1`, tag.Slug).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sk_skill_tags (skill_id, tag_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT DO NOTHING`, skillID, tagID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) replaceTeamShares(ctx context.Context, tx pgx.Tx, skillID string, teamSlugs []string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM sk_visibility_shares WHERE skill_id = $1::uuid AND team_id IS NOT NULL`, skillID); err != nil {
		return err
	}
	for _, slug := range teamSlugs {
		var teamID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM sk_teams WHERE slug = $1`, slug).Scan(&teamID); err != nil {
			if isNoRows(err) {
				continue
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sk_visibility_shares (skill_id, team_id) VALUES ($1::uuid, $2::uuid)`,
			skillID, teamID); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scan / moderation status
// ---------------------------------------------------------------------------

func (m *Module) scanStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "name")
	account := viewerAccount(ctx)
	base, err := m.loadVisibleSkill(ctx, slug, account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	record, err := m.resolveVersion(ctx, base, r.URL.Query().Get("version"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var rejectionReason string
	_ = m.deps.DB.Pool.QueryRow(ctx, `
		SELECT COALESCE(rejection_reason,'') FROM sk_versions WHERE id = $1::uuid`, record.ID).
		Scan(&rejectionReason)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	resp := scanResponse{
		Status:    normalizeModeration(record.ScanStatus),
		Gates:     []any{},
		Scorecard: map[string]any{},
	}
	if resp.Status == "rejected" {
		resp.RejectionReason = rejectionReason
	}
	httpapi.OK(w, resp)
}
