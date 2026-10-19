package skillhub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type versionRecord struct {
	ID         string
	Version    string
	ScanStatus string
	ZipSHA256  string
	FolderHash string
	FileSize   int64
}

func safeVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	for _, r := range version {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '-' || r == '+' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (m *Module) loadVersion(ctx context.Context, skillID, version string) (*versionRecord, error) {
	var record versionRecord
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT id::text, version, COALESCE(scan_status,'pending'), COALESCE(zip_sha256,''),
		       COALESCE(folder_hash,''), file_size
		FROM sk_versions WHERE skill_id = $1::uuid AND version = $2`, skillID, version).
		Scan(&record.ID, &record.Version, &record.ScanStatus, &record.ZipSHA256, &record.FolderHash, &record.FileSize)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (m *Module) resolveVersion(ctx context.Context, base *skillBase, requested string) (*versionRecord, error) {
	version := requested
	if version == "" {
		version = base.LatestVersion
	}
	if !safeVersion(version) {
		return nil, notFoundError("NOT_FOUND", "version not found")
	}
	record, err := m.loadVersion(ctx, base.ID, version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundError("NOT_FOUND", "version not found")
		}
		return nil, err
	}
	return record, nil
}

// objectKeyForVersion prefers the publish upload record (the exact key the
// client PUT to) and falls back to the content-addressed convention.
func (m *Module) objectKeyForVersion(ctx context.Context, slug string, record *versionRecord) (string, error) {
	var key string
	err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT oss_key FROM sk_publish_uploads
		WHERE slug = $1 AND version = $2 AND status = 'published'
		ORDER BY created_at DESC LIMIT 1`, slug, record.Version).Scan(&key)
	if err == nil && key != "" {
		return key, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) && err != nil {
		return "", err
	}
	if record.ZipSHA256 != "" {
		return "skills/" + slug + "/" + record.Version + "/" + record.ZipSHA256 + ".zip", nil
	}
	return "", pgx.ErrNoRows
}

func (m *Module) fetchSkillZip(ctx context.Context, key string) (string, error) {
	if m.deps.S3 == nil {
		return "", errors.New("object storage unavailable")
	}
	bucket := m.skillsBucket()
	if _, err := m.deps.S3.Stat(ctx, bucket, key); err != nil {
		return "", err
	}
	object, err := m.deps.S3.Client().GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", err
	}
	defer object.Close()
	file, err := os.CreateTemp("", "cindy-skill-*.zip")
	if err != nil {
		return "", err
	}
	path := file.Name()
	written, err := io.Copy(file, io.LimitReader(object, maxSkillArchiveBytes+1))
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if closeErr != nil {
		os.Remove(path)
		return "", closeErr
	}
	if written > maxSkillArchiveBytes {
		os.Remove(path)
		return "", errors.New("skill archive too large")
	}
	return path, nil
}

func isMissingObject(err error) bool {
	if err == nil {
		return false
	}
	response := minio.ToErrorResponse(err)
	return response.Code == "NoSuchKey" || response.Code == "NotFound" || response.StatusCode == http.StatusNotFound
}

func (m *Module) skillFiles(w http.ResponseWriter, r *http.Request) {
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
	includeHashes := r.URL.Query().Get("includeHashes") == "1" ||
		r.URL.Query().Get("includeHashes") == "true"

	files, err := m.filesFromDB(ctx, record, includeHashes)
	if err != nil {
		writeInternal(w)
		return
	}
	if files == nil {
		key, keyErr := m.objectKeyForVersion(ctx, slug, record)
		if keyErr != nil {
			notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
			return
		}
		path, fetchErr := m.fetchSkillZip(ctx, key)
		if fetchErr != nil {
			if isMissingObject(fetchErr) {
				notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
				return
			}
			unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
			return
		}
		defer os.Remove(path)
		var entries []zipFileInfo
		if includeHashes {
			if hashed, hashErr := inspectSkillZip(path); hashErr == nil {
				entries = hashed
			}
		}
		if entries == nil {
			var zipErr error
			entries, zipErr = listSkillZip(path)
			if zipErr != nil {
				invalidInput(w, "MANIFEST_INVALID", "skill package is invalid")
				return
			}
		}
		files = make([]fileEntry, 0, len(entries))
		for _, entry := range entries {
			item := fileEntry{Path: entry.Path, Size: entry.Size, Language: entry.Language, Truncated: entry.Truncated}
			if includeHashes {
				item.SHA256 = entry.SHA256
			}
			files = append(files, item)
		}
	}
	httpapi.OK(w, filesResponse{Slug: slug, Version: record.Version, Files: files})
}

func (m *Module) filesFromDB(ctx context.Context, record *versionRecord, includeHashes bool) ([]fileEntry, error) {
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT path, size_bytes, COALESCE(language,''), truncated, COALESCE(sha256,'')
		FROM sk_files WHERE version_id = $1::uuid ORDER BY path`, record.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]fileEntry, 0)
	for rows.Next() {
		var item fileEntry
		var digest string
		if err := rows.Scan(&item.Path, &item.Size, &item.Language, &item.Truncated, &digest); err != nil {
			return nil, err
		}
		if includeHashes {
			item.SHA256 = digest
		}
		files = append(files, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	return files, nil
}

func (m *Module) skillFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "name")
	account := viewerAccount(ctx)
	base, err := m.loadVisibleSkill(ctx, slug, account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	target := r.URL.Query().Get("path")
	if !isSafeRelativePath(target) {
		invalidInput(w, "INVALID_PARAMS", "invalid file path")
		return
	}
	record, err := m.resolveVersion(ctx, base, r.URL.Query().Get("version"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	key, keyErr := m.objectKeyForVersion(ctx, slug, record)
	if keyErr != nil {
		notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
		return
	}
	path, fetchErr := m.fetchSkillZip(ctx, key)
	if fetchErr != nil {
		if isMissingObject(fetchErr) {
			notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
			return
		}
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	defer os.Remove(path)

	content, size, truncated, readErr := readSkillZipFile(path, target)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			notFound(w, "FILE_NOT_FOUND", "file not found")
			return
		}
		invalidInput(w, "MANIFEST_INVALID", "skill package is invalid")
		return
	}
	httpapi.OK(w, fileContentResponse{
		Path:      target,
		Size:      size,
		Language:  languageFor(target),
		Truncated: truncated,
		Content:   content,
	})
}

func (m *Module) skillVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "name")
	base, err := m.loadVisibleSkill(ctx, slug, viewerAccount(ctx))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT v.version, v.created_at, COALESCE(v.scan_status,'pending'), v.file_size,
		       COALESCE(v.zip_sha256,''),
		       (SELECT count(*) FROM sk_download_tickets d WHERE d.version_id = v.id)
		FROM sk_versions v
		WHERE v.skill_id = $1::uuid
		ORDER BY v.created_at DESC`, base.ID)
	if err != nil {
		writeInternal(w)
		return
	}
	defer rows.Close()
	versions := make([]versionItem, 0)
	for rows.Next() {
		var item versionItem
		var created time.Time
		var status string
		if err := rows.Scan(&item.Version, &created, &status, &item.Size, &item.SHA256, &item.Downloads); err != nil {
			writeInternal(w)
			return
		}
		item.CreatedAt = isoMillis(created)
		item.Status = normalizeModeration(status)
		versions = append(versions, item)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w)
		return
	}
	httpapi.OK(w, versions)
}

func (m *Module) skillDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "name")
	account := requiredAccount(ctx)
	if account == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
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
	if m.deps.S3 == nil {
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	key, keyErr := m.objectKeyForVersion(ctx, slug, record)
	if keyErr != nil {
		notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
		return
	}
	if _, err := m.deps.S3.Stat(ctx, m.skillsBucket(), key); err != nil {
		if isMissingObject(err) {
			notFound(w, "OSS_OBJECT_NOT_FOUND", "skill package not found")
			return
		}
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	expires := time.Now().UTC().Add(presignDownloadTTL)
	url, err := m.deps.S3.PresignGet(ctx, m.skillsBucket(), key, presignDownloadTTL)
	if err != nil {
		unavailable(w, "SERVICE_UNAVAILABLE", "object storage unavailable")
		return
	}
	_, _ = m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO sk_download_tickets (skill_id, version_id, account_id, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`, base.ID, record.ID, account, expires)

	fileHash := record.FolderHash
	if fileHash == "" {
		fileHash = record.ZipSHA256
	}
	httpapi.OK(w, downloadSkillResponse{
		URL:       url,
		ExpiresAt: isoMillis(expires),
		FileHash:  fileHash,
		FileSize:  record.FileSize,
		ZipSHA256: record.ZipSHA256,
	})
}
