package plugin

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

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type uploadSession struct {
	ID           string
	AccountID    string
	OrgID        string
	SizeBytes    int64
	SHA256       string
	ObjectKey    string
	Status       string
	PutExpiresAt time.Time
	FailureCode  string
	PluginID     string
	ReleaseID    string
	GhostID      string
	Version      string
	ReviewStatus string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s uploadSession) reviewStatusPtr() *string {
	if s.Status != "succeeded" {
		return nil
	}
	return statusPtr(s.ReviewStatus)
}

func (m *Module) loadUpload(ctx context.Context, uploadID, accountID string) (*uploadSession, error) {
	if _, err := uuid.Parse(uploadID); err != nil {
		return nil, pgx.ErrNoRows
	}
	var s uploadSession
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, COALESCE(org_id::text,''), size_bytes, sha256::text,
		       object_key, status, put_url_expires_at, COALESCE(failure_code,''),
		       COALESCE(plugin_id::text,''), COALESCE(release_id::text,''),
		       COALESCE(ghost_id,''), COALESCE(version,''), COALESCE(review_status,''),
		       created_at, updated_at
		FROM plugin_upload_sessions
		WHERE id = $1::uuid AND account_id = $2::uuid`, uploadID, accountID).
		Scan(&s.ID, &s.AccountID, &s.OrgID, &s.SizeBytes, &s.SHA256, &s.ObjectKey, &s.Status,
			&s.PutExpiresAt, &s.FailureCode, &s.PluginID, &s.ReleaseID, &s.GhostID,
			&s.Version, &s.ReviewStatus, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *Module) prepareUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	mm := m.loadMembership(ctx, p)
	if mm.Found && !mm.active() {
		httpapi.WriteError(w, httpapi.Err(http.StatusForbidden, "MEMBERSHIP_INACTIVE", "membership is not active"))
		return
	}
	var req struct {
		SizeBytes int64  `json:"sizeBytes"`
		SHA256    string `json:"sha256"`
	}
	if err := httpapi.DecodeJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if req.SizeBytes <= 0 || req.SizeBytes > maxPluginUpload {
		invalidInput(w, "INVALID_PARAMS", "sizeBytes must be between 1 and 134217728")
		return
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA256))
	if !sha256Pattern.MatchString(sha) {
		invalidInput(w, "INVALID_PARAMS", "sha256 must be 64 lower-case hex characters")
		return
	}
	if m.deps.S3 == nil {
		unavailable(w, "STORAGE_UNAVAILABLE", "object storage unavailable")
		return
	}
	uploadID := uuid.NewString()
	objectKey := "plugins/uploads/" + uploadID + ".cindy"
	putURL, err := m.deps.S3.PresignPut(ctx, m.pluginsBucket(), objectKey, presignUploadTTL, "application/octet-stream")
	if err != nil {
		unavailable(w, "STORAGE_UNAVAILABLE", "object storage unavailable")
		return
	}
	expires := time.Now().UTC().Add(presignUploadTTL)
	if _, err := m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO plugin_upload_sessions
		(id, account_id, org_id, size_bytes, sha256, object_key, status, put_url_expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'awaiting_upload', $7)`,
		uploadID, p.AccountID, mm.orgUUID(), req.SizeBytes, sha, objectKey, expires); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	m.auditLog(ctx, "plugin.publish.prepare", "plugin_upload", uploadID, nil)
	httpapi.Created(w, prepareUploadResponse{
		UploadID:  uploadID,
		PutURL:    putURL,
		Headers:   map[string]string{},
		ExpiresAt: isoMillis(expires),
		Status:    "awaiting_upload",
	})
}

func (m *Module) commitUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	uploadID := chi.URLParam(r, "uploadId")
	sess, err := m.loadUpload(ctx, uploadID, p.AccountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w, "UPLOAD_NOT_FOUND", "upload not found")
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	// Commit carries no body; ignore an empty JSON object if a client sends it.

	if sess.Status == "awaiting_upload" && time.Now().UTC().After(sess.PutExpiresAt) {
		_, _ = m.deps.DB.Pool.Exec(ctx, `
			UPDATE plugin_upload_sessions SET status='expired', updated_at=now()
			WHERE id=$1::uuid AND status='awaiting_upload'`, sess.ID)
		sess.Status = "expired"
	}
	if sess.Status != "awaiting_upload" {
		httpapi.WriteJSON(w, http.StatusAccepted, commitUploadResponse{UploadID: sess.ID, Status: sess.Status})
		return
	}

	tag, err := m.deps.DB.Pool.Exec(ctx, `
		UPDATE plugin_upload_sessions SET status='validating', updated_at=now()
		WHERE id=$1::uuid AND account_id=$2::uuid AND status='awaiting_upload'`,
		sess.ID, p.AccountID)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if tag.RowsAffected() == 0 {
		if latest, err := m.loadUpload(ctx, uploadID, p.AccountID); err == nil {
			httpapi.WriteJSON(w, http.StatusAccepted, commitUploadResponse{UploadID: latest.ID, Status: latest.Status})
			return
		}
		conflict(w, "UPLOAD_CONFLICT", "upload is being processed")
		return
	}

	mm := m.loadMembership(ctx, p)
	pluginID, releaseID, ghostID, version, failureCode := m.processUpload(ctx, sess, mm)
	if failureCode != "" {
		_, _ = m.deps.DB.Pool.Exec(ctx, `
			UPDATE plugin_upload_sessions SET status='failed', failure_code=$2, updated_at=now()
			WHERE id=$1::uuid`, sess.ID, failureCode)
		m.auditLog(ctx, "plugin.publish.failed", "plugin_upload", sess.ID, map[string]any{"code": failureCode})
		httpapi.WriteJSON(w, http.StatusAccepted, commitUploadResponse{
			UploadID: sess.ID,
			Status:   "failed",
			Failure:  &uploadFailure{Code: failureCode, Message: failureMessage(failureCode)},
		})
		return
	}
	m.auditLog(ctx, "plugin.publish.succeeded", "plugin_release", releaseID, map[string]any{
		"uploadId": sess.ID, "pluginId": pluginID, "ghostId": ghostID, "version": version,
	})
	httpapi.WriteJSON(w, http.StatusAccepted, commitUploadResponse{UploadID: sess.ID, Status: "succeeded"})
}

// processUpload performs the synchronous validation and release creation the
// task asks for. It returns a stable failure code instead of propagating
// internal errors to the client.
func (m *Module) processUpload(ctx context.Context, sess *uploadSession, mm membership) (pluginID, releaseID, ghostID, version, failureCode string) {
	if mm.Found && !mm.active() {
		return "", "", "", "", "MEMBERSHIP_INACTIVE"
	}
	if m.deps.S3 == nil {
		return "", "", "", "", "PUBLISH_STORAGE_UNAVAILABLE"
	}
	bucket := m.pluginsBucket()

	info, err := m.deps.S3.Stat(ctx, bucket, sess.ObjectKey)
	if err != nil {
		if isMissingObject(err) {
			return "", "", "", "", "UPLOAD_OBJECT_MISSING"
		}
		return "", "", "", "", "PUBLISH_STORAGE_UNAVAILABLE"
	}
	if info.Size != sess.SizeBytes {
		return "", "", "", "", "UPLOAD_SIZE_MISMATCH"
	}

	tmpPath, size, actualSHA, err := m.downloadObject(ctx, bucket, sess.ObjectKey)
	if err != nil {
		if isMissingObject(err) {
			return "", "", "", "", "UPLOAD_OBJECT_MISSING"
		}
		return "", "", "", "", "PUBLISH_STORAGE_UNAVAILABLE"
	}
	defer os.Remove(tmpPath)
	if size != sess.SizeBytes {
		return "", "", "", "", "UPLOAD_SIZE_MISMATCH"
	}
	if actualSHA != sess.SHA256 {
		return "", "", "", "", "UPLOAD_SHA256_MISMATCH"
	}

	namespace := ""
	orgPrefix := ""
	if sess.OrgID != "" {
		prefix, slug, err := m.organization(ctx, sess.OrgID)
		if err != nil {
			return "", "", "", "", "PLUGIN_NAMESPACE_CONFIG_REQUIRED"
		}
		orgPrefix = prefix
		namespace = slug
	}

	pkg, err := readPluginPackage(tmpPath, namespace)
	if err != nil {
		return "", "", "", "", "PLUGIN_PACKAGE_INVALID"
	}
	ghostID, version = pkg.ID, pkg.Version
	if orgPrefix != "" && !strings.HasPrefix(ghostID, orgPrefix+"-") {
		return "", "", "", "", "PUBLISH_GHOST_ID_PREFIX_MISMATCH"
	}

	scope := "personal"
	if sess.OrgID != "" {
		scope = "organization"
	}

	var existingID, existingNamespace string
	lookupErr := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, COALESCE(namespace,'') FROM plugin_plugins
		WHERE ghost_id = $1 AND scope = $2 AND org_id IS NOT DISTINCT FROM $3::uuid
		LIMIT 1`, ghostID, scope, nullableUUID(sess.OrgID)).
		Scan(&existingID, &existingNamespace)
	switch {
	case lookupErr == nil:
		pluginID = existingID
		var dup string
		err = m.deps.DB.Pool.QueryRow(ctx, `
			SELECT id::text FROM plugin_releases WHERE plugin_id = $1::uuid AND version = $2`,
			pluginID, version).Scan(&dup)
		if err == nil {
			return "", "", "", "", "PUBLISH_VERSION_EXISTS"
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", "", "PUBLISH_INTERNAL_ERROR"
		}
	case errors.Is(lookupErr, pgx.ErrNoRows):
		pluginID = uuid.NewString()
	default:
		return "", "", "", "", "PUBLISH_INTERNAL_ERROR"
	}
	releaseID = uuid.NewString()

	destKey := "plugins/" + pluginID + "/" + releaseID + ".cindy"
	if _, err := m.deps.S3.Client().CopyObject(ctx,
		minio.CopyDestOptions{Bucket: bucket, Object: destKey},
		minio.CopySrcOptions{Bucket: bucket, Object: sess.ObjectKey}); err != nil {
		return "", "", "", "", "PUBLISH_STORAGE_UNAVAILABLE"
	}

	txErr := m.deps.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if existingID == "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO plugin_plugins
				(id, ghost_id, scope, org_id, namespace, name, description, author, status, default_install)
				VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8, 'active', false)`,
				pluginID, ghostID, scope, nullableUUID(sess.OrgID), nullableString(namespace),
				pkg.Name, nullableString(pkg.Description), nullableString(pkg.Author)); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE plugin_plugins
				SET name=$2, description=$3, author=$4, namespace=$5, updated_at=now()
				WHERE id=$1::uuid`,
				pluginID, pkg.Name, nullableString(pkg.Description), nullableString(pkg.Author),
				nullableString(namespace)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_releases
			(id, plugin_id, version, manifest, sha256, size_bytes, status, review_status, published_at)
			VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6, 'active', 'pending', now())`,
			releaseID, pluginID, version, string(pkg.Manifest), actualSHA, size); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE plugin_upload_sessions
			SET status='succeeded', plugin_id=$2::uuid, release_id=$3::uuid, ghost_id=$4,
			    version=$5, review_status='pending', updated_at=now()
			WHERE id=$1::uuid`, sess.ID, pluginID, releaseID, ghostID, version); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		_ = m.deps.S3.Remove(ctx, bucket, destKey)
		var pgErr *pgconn.PgError
		if errors.As(txErr, &pgErr) && pgErr.Code == "23505" {
			return "", "", "", "", "PUBLISH_VERSION_EXISTS"
		}
		return "", "", "", "", "PUBLISH_INTERNAL_ERROR"
	}
	return pluginID, releaseID, ghostID, version, ""
}

func (m *Module) downloadObject(ctx context.Context, bucket, key string) (string, int64, string, error) {
	object, err := m.deps.S3.Client().GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", 0, "", err
	}
	defer object.Close()
	file, err := os.CreateTemp("", "cindy-plugin-*.zip")
	if err != nil {
		return "", 0, "", err
	}
	path := file.Name()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(object, maxPluginUpload+1))
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return "", 0, "", err
	}
	if closeErr != nil {
		os.Remove(path)
		return "", 0, "", closeErr
	}
	if written > maxPluginUpload {
		os.Remove(path)
		return "", 0, "", errors.New("object too large")
	}
	return path, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (m *Module) organization(ctx context.Context, orgID string) (prefix, slug string, err error) {
	var prefixPtr, slugPtr *string
	err = m.deps.DB.Pool.QueryRow(ctx, `
		SELECT plugin_prefix, slug::text FROM auth_organizations WHERE id = $1::uuid`, orgID).
		Scan(&prefixPtr, &slugPtr)
	if err != nil {
		return "", "", err
	}
	return deref(prefixPtr), deref(slugPtr), nil
}

func isMissingObject(err error) bool {
	if err == nil {
		return false
	}
	response := minio.ToErrorResponse(err)
	return response.Code == "NoSuchKey" || response.Code == "NotFound" || response.StatusCode == http.StatusNotFound
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableUUID(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (m *Module) getUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	sess, err := m.loadUpload(ctx, chi.URLParam(r, "uploadId"), p.AccountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w, "UPLOAD_NOT_FOUND", "upload not found")
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if sess.Status == "awaiting_upload" && time.Now().UTC().After(sess.PutExpiresAt) {
		sess.Status = "expired"
	}
	httpapi.OK(w, statusFromSession(*sess))
}

func statusFromSession(sess uploadSession) uploadStatusResponse {
	out := uploadStatusResponse{
		UploadID: sess.ID,
		Status:   sess.Status,
	}
	if sess.Status == "succeeded" {
		out.PluginID = statusPtr(encodePluginID(sess.PluginID))
		out.ReleaseID = statusPtr(sess.ReleaseID)
		out.GhostID = statusPtr(sess.GhostID)
		out.Version = statusPtr(sess.Version)
		out.ReviewStatus = sess.reviewStatusPtr()
	}
	if sess.Status == "failed" {
		code := sess.FailureCode
		if code == "" {
			code = "PUBLISH_INTERNAL_ERROR"
		}
		out.Failure = &uploadFailure{Code: code, Message: failureMessage(code)}
	}
	return out
}

func failureMessage(code string) string {
	if message, ok := uploadFailureMessages[code]; ok {
		return message
	}
	return "publishing failed"
}

var uploadFailureMessages = map[string]string{
	"UPLOAD_OBJECT_MISSING":            "uploaded package was not found; please upload it again",
	"UPLOAD_SIZE_MISMATCH":             "uploaded package size does not match the prepared size",
	"UPLOAD_SHA256_MISMATCH":           "uploaded package checksum does not match",
	"PLUGIN_PACKAGE_INVALID":           "the plugin package or its ghost.json is invalid",
	"MEMBERSHIP_INACTIVE":              "your organization membership is not active",
	"PUBLISH_NOT_AUTHORIZED":           "you are not allowed to publish this plugin",
	"PLUGIN_GHOST_ID_CONFLICT":         "the plugin id is already in use",
	"PUBLISH_PREFIX_UNREGISTERED":      "your organization has no registered plugin prefix",
	"PUBLISH_GHOST_ID_PREFIX_MISMATCH": "the plugin id does not match your organization prefix",
	"PUBLISH_GHOST_ID_INVALID":         "the plugin id is invalid",
	"PLUGIN_GHOST_ID_RESERVED":         "the plugin id is reserved",
	"PLUGIN_NAMESPACE_CONFIG_REQUIRED": "organization namespace configuration is unavailable",
	"PUBLISH_VERSION_EXISTS":           "this plugin version already exists; bump the version",
	"PUBLISH_STORAGE_UNAVAILABLE":      "object storage is unavailable; please retry",
	"PUBLISH_INTERNAL_ERROR":           "publishing failed; please retry",
}

func (m *Module) myReleases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpapi.MustPrincipal(ctx)
	limit := 50
	args := []any{p.AccountID}
	where := "u.account_id = $1::uuid AND u.release_id IS NOT NULL"
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		at, id, ok := decodeCursor(raw)
		if !ok {
			invalidInput(w, "INVALID_PARAMS", "invalid cursor")
			return
		}
		args = append(args, at, id)
		where += " AND (u.created_at, u.id) < ($2::timestamptz, $3::uuid)"
	}
	args = append(args, limit+1)
	sql := `
		SELECT id::text, account_id::text, COALESCE(org_id::text,''), size_bytes, sha256::text,
		       object_key, status, put_url_expires_at, COALESCE(failure_code,''),
		       COALESCE(plugin_id::text,''), COALESCE(release_id::text,''),
		       COALESCE(ghost_id,''), COALESCE(version,''), COALESCE(review_status,''),
		       created_at, updated_at
		FROM plugin_upload_sessions u
		WHERE ` + where + `
		ORDER BY u.created_at DESC, u.id DESC
		LIMIT $` + itoaArgs(len(args))
	rows, err := m.deps.DB.Pool.Query(ctx, sql, args...)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	defer rows.Close()

	items := make([]myReleaseItem, 0, limit)
	var lastAt time.Time
	var lastID string
	hasMore := false
	for rows.Next() {
		var sess uploadSession
		if err := rows.Scan(&sess.ID, &sess.AccountID, &sess.OrgID, &sess.SizeBytes, &sess.SHA256,
			&sess.ObjectKey, &sess.Status, &sess.PutExpiresAt, &sess.FailureCode, &sess.PluginID,
			&sess.ReleaseID, &sess.GhostID, &sess.Version, &sess.ReviewStatus,
			&sess.CreatedAt, &sess.UpdatedAt); err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		if len(items) >= limit {
			hasMore = true
			break
		}
		lastAt, lastID = sess.CreatedAt, sess.ID
		items = append(items, myReleaseItem{
			uploadStatusResponse: statusFromSession(sess),
			CreatedAt:            isoMillis(sess.CreatedAt),
			UpdatedAt:            isoMillis(sess.UpdatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	var next *string
	if hasMore {
		cursor := encodeCursor(lastAt, lastID)
		next = &cursor
	}
	httpapi.OK(w, myReleasesResponse{Releases: items, NextCursor: next})
}

func itoaArgs(n int) string {
	if n <= 0 {
		return "1"
	}
	return strconv.Itoa(n)
}
