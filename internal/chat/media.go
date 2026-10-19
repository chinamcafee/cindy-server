package chat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	maxMediaBytes  = 100 << 20
	mediaUploadTTL = 15 * time.Minute
	mediaReadTTL   = 10 * time.Minute
)

type mediaPrepareRequest struct {
	OperationID  string `json:"operationId"`
	RoomID       string `json:"roomId"`
	Mime         string `json:"mime"`
	SizeBytes    int64  `json:"sizeBytes"`
	OriginalName string `json:"originalName"`
}

type mediaCompleteRequest struct {
	OperationID string `json:"operationId"`
}

type mediaMeta struct {
	ID           string
	RoomID       string
	UploaderID   string
	OSSKey       string
	Mime         string
	OriginalName string
	SizeBytes    int64
	Status       string
	CreatedAt    time.Time
}

func (m *Module) loadMedia(ctx context.Context, mediaID string) (mediaMeta, error) {
	if !isUUID(mediaID) {
		return mediaMeta{}, errMediaNotFound
	}
	var meta mediaMeta
	err := m.deps.DB.Pool.QueryRow(ctx, `SELECT id, room_id, uploader_actor_id, oss_key, mime, COALESCE(original_name,''), size_bytes, status, created_at
		FROM chat_media WHERE id=$1`, mediaID).
		Scan(&meta.ID, &meta.RoomID, &meta.UploaderID, &meta.OSSKey, &meta.Mime, &meta.OriginalName, &meta.SizeBytes, &meta.Status, &meta.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return mediaMeta{}, errMediaNotFound
	}
	return meta, err
}

func (m *Module) handleMediaPrepare(w http.ResponseWriter, r *http.Request) {
	var req mediaPrepareRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.prepareMedia(w, r, req)
}

func (m *Module) prepareMedia(w http.ResponseWriter, r *http.Request, req mediaPrepareRequest) {
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	human, err := m.ensureHumanActor(r.Context(), accountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if m.deps.S3 == nil {
			return 0, nil, httpapi.ErrUnavailable
		}
		if !isUUID(req.RoomID) {
			return 0, nil, errConversationNotFound
		}
		mime := strings.TrimSpace(req.Mime)
		name := strings.TrimSpace(req.OriginalName)
		if mime == "" || len(mime) > 255 || len(name) > 255 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid media metadata")
		}
		if req.SizeBytes <= 0 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_ATTACHMENT", "invalid media size")
		}
		if req.SizeBytes > maxMediaBytes {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "ATTACHMENT_TOO_LARGE", "attachment exceeds 100 MiB")
		}
		acting, err := m.resolveActingActor(r, human)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := m.requireJoined(ctx, req.RoomID, acting.ID); err != nil {
			return 0, nil, err
		}
		key := "media/" + accountID + "/" + req.RoomID + "/" + uuid.NewString()
		var mediaID string
		if err := m.deps.DB.Pool.QueryRow(ctx, `INSERT INTO chat_media (room_id, uploader_actor_id, oss_key, mime, original_name, size_bytes, status)
			VALUES ($1,$2,$3,$4,$5,$6,'pending') RETURNING id`, req.RoomID, acting.ID, key, mime, name, req.SizeBytes).Scan(&mediaID); err != nil {
			return 0, nil, err
		}
		uploadURL, err := m.deps.S3.PresignPut(ctx, m.deps.S3.Private, key, mediaUploadTTL, mime)
		if err != nil {
			return 0, nil, httpapi.ErrUnavailable
		}
		expiresAt := time.Now().UTC().Add(mediaUploadTTL)
		return http.StatusOK, map[string]any{
			"id":           mediaID,
			"mediaId":      mediaID,
			"uploadUrl":    uploadURL,
			"headers":      map[string]string{"Content-Type": mime},
			"expiresAt":    expiresAt,
			"originalName": name,
		}, nil
	})
}

func (m *Module) handleMediaComplete(w http.ResponseWriter, r *http.Request) {
	var req mediaCompleteRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	mediaID := chi.URLParam(r, "mediaId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if m.deps.S3 == nil {
			return 0, nil, httpapi.ErrUnavailable
		}
		meta, err := m.loadMedia(ctx, mediaID)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := m.requireJoined(ctx, meta.RoomID, human.ID); err != nil {
			return 0, nil, err
		}
		if meta.Status == "sealed" {
			return http.StatusOK, map[string]any{"state": "sealed"}, nil
		}
		if time.Since(meta.CreatedAt) > mediaUploadTTL {
			return 0, nil, errUploadExpired
		}
		info, err := m.deps.S3.Stat(ctx, m.deps.S3.Private, meta.OSSKey)
		if err != nil {
			return 0, nil, errMediaNotFound
		}
		if info.Size <= 0 || info.Size > maxMediaBytes || info.Size != meta.SizeBytes {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_ATTACHMENT", "uploaded object does not match declared size")
		}
		if _, err := m.deps.DB.Pool.Exec(ctx, `UPDATE chat_media SET status='sealed' WHERE id=$1`, meta.ID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"state": "sealed"}, nil
	})
}

func (m *Module) handleMediaGet(w http.ResponseWriter, r *http.Request) {
	mediaID := chi.URLParam(r, "mediaId")
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	meta, err := m.loadMedia(r.Context(), mediaID)
	if err != nil {
		m.fail(w, err)
		return
	}
	if _, _, err := m.requireJoined(r.Context(), meta.RoomID, human.ID); err != nil {
		m.fail(w, err)
		return
	}
	if m.deps.S3 == nil {
		m.fail(w, httpapi.ErrUnavailable)
		return
	}
	url, err := m.deps.S3.PresignGet(r.Context(), m.deps.S3.Private, meta.OSSKey, mediaReadTTL)
	if err != nil {
		m.fail(w, httpapi.ErrUnavailable)
		return
	}
	httpapi.OK(w, map[string]any{
		"id":        meta.ID,
		"name":      meta.OriginalName,
		"type":      meta.Mime,
		"mime":      meta.Mime,
		"size":      meta.SizeBytes,
		"url":       url,
		"expiresAt": time.Now().UTC().Add(mediaReadTTL),
	})
}

func (m *Module) handleConversationMediaPrepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OperationID string `json:"operationId"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Size        int64  `json:"size"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	roomID := chi.URLParam(r, "roomId")
	body := mediaPrepareRequest{
		OperationID:  req.OperationID,
		RoomID:       roomID,
		Mime:         req.Type,
		SizeBytes:    req.Size,
		OriginalName: req.Name,
	}
	m.prepareMedia(w, r, body)
}
