package devicelink

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	mediaMaxSize = int64(2) << 30 // 2 GiB
	mediaPutTTL  = 15 * time.Minute
	mediaGetTTL  = 15 * time.Minute
)

func (m *Module) presignPut(w http.ResponseWriter, r *http.Request) {
	if m.s3 == nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}
	var req struct {
		Size         int64  `json:"size"`
		Ext          string `json:"ext"`
		ContentType  string `json:"contentType"`
		SharedTaskID string `json:"sharedTaskId"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if req.Size <= 0 || req.Size > mediaMaxSize {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "size must be between 1 byte and 2 GiB"))
		return
	}
	ext := sanitizeExt(req.Ext)
	if ext == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid ext"))
		return
	}
	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(contentType) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid contentType"))
		return
	}
	if req.SharedTaskID != "" {
		if !validUUID(req.SharedTaskID) {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid sharedTaskId"))
			return
		}
		allowed, err := m.sharedTaskAccess(r.Context(), req.SharedTaskID, accountID(r))
		if err != nil {
			m.internalError(w, "presign_put_shared_task", err)
			return
		}
		if !allowed {
			httpapi.WriteError(w, httpapi.ErrForbidden)
			return
		}
	}

	now := time.Now().UTC()
	key := fmt.Sprintf("media/%s/%04d/%02d/%s.%s", accountID(r), now.Year(), int(now.Month()), uuid.NewString(), ext)
	putURL, err := m.s3.PresignPut(r.Context(), m.s3.Private, key, mediaPutTTL, contentType)
	if err != nil {
		m.internalError(w, "presign_put", err)
		return
	}
	httpapi.OK(w, map[string]any{
		"putUrl":    putURL,
		"key":       key,
		"expiresAt": now.Add(mediaPutTTL),
	})
}

func (m *Module) presignGet(w http.ResponseWriter, r *http.Request) {
	if m.s3 == nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	key := strings.TrimSpace(req.Key)
	allowed, err := m.mediaAllowed(r.Context(), accountID(r), key)
	if err != nil {
		m.internalError(w, "presign_get", err)
		return
	}
	if !allowed {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}

	now := time.Now().UTC()
	getURL, err := m.s3.PresignGet(r.Context(), m.s3.Private, key, mediaGetTTL)
	if err != nil {
		m.internalError(w, "presign_get", err)
		return
	}
	httpapi.OK(w, map[string]any{
		"getUrl":    getURL,
		"expiresAt": now.Add(mediaGetTTL),
	})
}

func (m *Module) deleteMedia(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	key := strings.TrimSpace(req.Key)
	allowed, err := m.mediaAllowed(r.Context(), accountID(r), key)
	if err != nil {
		m.internalError(w, "delete_media", err)
		return
	}
	if !allowed {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	if m.s3 != nil {
		if err := m.s3.Remove(r.Context(), m.s3.Private, key); err != nil {
			// Deletion is best effort; lifecycle rules are the safety net.
			m.log.Warn("devicelink_media_delete_failed", "key", key, "error", err)
		}
	}
	httpapi.OK(w, map[string]any{"deleted": true})
}

// mediaAllowed verifies that the key belongs to the account or to a shared
// task the account participates in.
func (m *Module) mediaAllowed(ctx context.Context, account, key string) (bool, error) {
	if key == "" || len(key) > 1024 || strings.Contains(key, "..") || !strings.HasPrefix(key, "media/") {
		return false, nil
	}
	if strings.HasPrefix(key, "media/"+account+"/") {
		return true, nil
	}

	rows, err := m.db.Pool.Query(ctx, `
		SELECT t.id::text
		FROM dl_shared_tasks t
		LEFT JOIN dl_shared_task_guests g
		       ON g.shared_task_id = t.id AND g.account_id = $1::uuid AND g.removed_at IS NULL
		WHERE (t.owner_account_id = $1::uuid OR g.id IS NOT NULL)
		  AND t.status = 'active'`, account)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return false, err
		}
		if strings.Contains(key, taskID) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func sanitizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" || len(ext) > 10 {
		return ""
	}
	for i := 0; i < len(ext); i++ {
		ch := ext[i]
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return ""
		}
	}
	return ext
}
