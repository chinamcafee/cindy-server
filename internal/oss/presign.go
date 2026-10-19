package oss

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	sceneAvatar = "avatar"

	// avatarMaxBytes mirrors AVATAR_MAX_BYTES in the desktop client.
	avatarMaxBytes int64 = 5 << 20
	// avatarPresignTTL is the presigned PUT lifetime for avatars.
	avatarPresignTTL = 10 * time.Minute
)

// avatarContentTypes is the only accepted upload whitelist.
var avatarContentTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

type presignPutRequest struct {
	Scene       string `json:"scene"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type presignPutResponse struct {
	PutURL    string            `json:"putUrl"`
	PublicURL string            `json:"publicUrl"`
	Key       string            `json:"key"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// presignPut issues a short-lived PUT URL for a public avatar object and
// records the object in file_objects so it can be audited and reclaimed.
func (m *Module) presignPut(w http.ResponseWriter, r *http.Request) {
	if m.deps.Store == nil || m.deps.DB == nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}

	var req presignPutRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}

	if strings.TrimSpace(req.Scene) != sceneAvatar {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "unsupported scene"))
		return
	}

	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(req.ContentType))
	if err != nil || mediaType == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid contentType"))
		return
	}
	ext, ok := avatarContentTypes[mediaType]
	if !ok {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "unsupported contentType"))
		return
	}
	if req.Size <= 0 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid size"))
		return
	}
	if req.Size > avatarMaxBytes {
		httpapi.WriteError(w, httpapi.Err(http.StatusRequestEntityTooLarge, "AVATAR_TOO_LARGE", "avatar exceeds 5 MiB"))
		return
	}

	principal := httpapi.MustPrincipal(r.Context())
	key := fmt.Sprintf("public/avatars/%s/%s.%s", principal.AccountID, uuid.NewString(), ext)

	putURL, err := m.deps.Store.PresignPut(r.Context(), m.deps.Store.Public, key, avatarPresignTTL, mediaType)
	if err != nil {
		m.log.Error("oss_presign_failed", "scene", sceneAvatar, "error", err)
		httpapi.WriteError(w, httpapi.Err(http.StatusInternalServerError, "PRESIGN_FAILED", "could not sign upload url"))
		return
	}

	if err := m.registerObject(r.Context(), principal.AccountID, key, mediaType, req.Size); err != nil {
		m.log.Error("oss_register_failed", "scene", sceneAvatar, "error", err)
		httpapi.WriteError(w, httpapi.Err(http.StatusInternalServerError, "PRESIGN_FAILED", "could not register object"))
		return
	}

	if m.deps.Audit != nil {
		m.deps.Audit.Log(r.Context(), auditEventFromRequest(r, principal.AccountID, key, mediaType, req.Size))
	}

	httpapi.OK(w, presignPutResponse{
		PutURL:    putURL,
		PublicURL: m.publicURL(key),
		Key:       key,
		Headers:   map[string]string{"Content-Type": mediaType},
		ExpiresAt: time.Now().UTC().Add(avatarPresignTTL),
	})
}

// publicURL resolves the anonymous read URL of a public object. There is no
// dedicated CDN setting yet, so the configured S3 endpoint is the best source.
func (m *Module) publicURL(key string) string {
	if m.deps.Config != nil && strings.TrimSpace(m.deps.Config.S3Endpoint) != "" {
		base := strings.TrimRight(m.deps.Config.S3Endpoint, "/")
		return base + "/" + path.Join(m.deps.Store.Public, key)
	}
	return m.deps.Store.PublicURL(key)
}

// registerObject records the pending avatar object. Inserts are idempotent on
// (bucket, key); keys embed a fresh UUID so retries create distinct rows.
func (m *Module) registerObject(ctx context.Context, accountID, key, mediaType string, size int64) error {
	var owner any
	if _, err := uuid.Parse(accountID); err == nil {
		owner = accountID
	}
	_, err := m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO file_objects (bucket, key, owner_account_id, scene, mime, size_bytes, acl, status)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, 'public-read', 'active')
		ON CONFLICT (bucket, key) DO UPDATE SET
			owner_account_id = EXCLUDED.owner_account_id,
			scene = EXCLUDED.scene,
			mime = EXCLUDED.mime,
			size_bytes = EXCLUDED.size_bytes,
			acl = EXCLUDED.acl,
			status = 'active'`,
		m.deps.Store.Public, key, owner, sceneAvatar, mediaType, size)
	if err != nil {
		return fmt.Errorf("insert file object: %w", err)
	}
	return nil
}

func auditEventFromRequest(r *http.Request, accountID, key, mediaType string, size int64) audit.Event {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	return audit.Event{
		ActorType:  "account",
		ActorID:    accountID,
		IP:         ip,
		UserAgent:  r.UserAgent(),
		Action:     "oss.presign.put",
		TargetType: "file_object",
		TargetID:   key,
		After:      map[string]any{"scene": sceneAvatar, "mime": mediaType, "size": size},
	}
}
