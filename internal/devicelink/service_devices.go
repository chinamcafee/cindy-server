package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// DeviceView is the device directory entry returned to clients.
type DeviceView struct {
	ID                   string          `json:"id"`
	DeviceID             string          `json:"deviceId"`
	Name                 string          `json:"name"`
	SelfName             string          `json:"selfName,omitempty"`
	Platform             string          `json:"platform"`
	AppVersion           string          `json:"appVersion"`
	LastSeenAt           *string         `json:"lastSeenAt"`
	Online               bool            `json:"online"`
	Busy                 bool            `json:"busy"`
	RemoteControlEnabled bool            `json:"remoteControlEnabled"`
	IsSelf               bool            `json:"isSelf"`
	DeviceInfo           json.RawMessage `json:"deviceInfo"`
}

// registerDevice resolves and upserts the device row for a hello. The returned
// removed flag means the requested device was removed and the socket must be
// closed with 4410.
func (m *Module) registerDevice(ctx context.Context, accountID string, hello *HelloPayload, info json.RawMessage) (string, bool, error) {
	deviceID := ""
	requested := strings.TrimSpace(hello.DeviceID)
	if requested != "" && len(requested) <= 128 {
		var owner string
		var removedAt *time.Time
		err := m.db.Pool.QueryRow(ctx,
			`SELECT account_id::text, removed_at FROM dl_devices WHERE id = $1`, requested,
		).Scan(&owner, &removedAt)
		switch {
		case err == nil && owner == accountID:
			if removedAt != nil {
				return "", true, nil
			}
			deviceID = requested
		case err == nil:
			// The id belongs to another account: never reuse it.
		case errors.Is(err, pgx.ErrNoRows):
			// Fall through to the attribute lookup below.
		default:
			return "", false, err
		}
	}

	// Reuse the server-assigned id of a matching physical device when the
	// client did not present one (or presented an unknown one).
	if deviceID == "" {
		var existing string
		var removedAt *time.Time
		err := m.db.Pool.QueryRow(ctx, `
			SELECT id, removed_at FROM dl_devices
			WHERE account_id = $1::uuid
			  AND name = $2
			  AND COALESCE(platform, '') = $3
			  AND COALESCE(app_version, '') = $4
			ORDER BY (removed_at IS NULL) DESC, COALESCE(last_seen_at, created_at) DESC
			LIMIT 1`,
			accountID, hello.DeviceName, hello.Platform, hello.AppVersion).Scan(&existing, &removedAt)
		switch {
		case err == nil && removedAt != nil:
			return "", true, nil
		case err == nil:
			deviceID = existing
		case errors.Is(err, pgx.ErrNoRows):
			deviceID = uuid.NewString()
		default:
			return "", false, err
		}
	}

	_, err := m.db.Pool.Exec(ctx, `
		INSERT INTO dl_devices (id, account_id, name, platform, app_version, device_info, remote_control_enabled, busy, last_seen_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::jsonb, $7, $8, now())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			platform = EXCLUDED.platform,
			app_version = EXCLUDED.app_version,
			device_info = EXCLUDED.device_info,
			remote_control_enabled = EXCLUDED.remote_control_enabled,
			busy = EXCLUDED.busy,
			last_seen_at = now(),
			updated_at = now()`,
		deviceID, accountID, hello.DeviceName, hello.Platform, hello.AppVersion,
		string(info), hello.RemoteControlEnabled, hello.Busy,
	)
	if err != nil {
		return "", false, err
	}
	return deviceID, false, nil
}

func (m *Module) listDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountID(r)
	self := deviceIDFromRequest(r, "")

	rows, err := m.db.Pool.Query(ctx, `
		SELECT id, name, COALESCE(platform, ''), COALESCE(app_version, ''), device_info,
		       remote_control_enabled, busy, last_seen_at
		FROM dl_devices
		WHERE account_id = $1::uuid AND removed_at IS NULL
		ORDER BY COALESCE(last_seen_at, created_at) DESC`, account)
	if err != nil {
		m.internalError(w, "list_devices", err)
		return
	}
	defer rows.Close()

	presence := m.getPresence(ctx, account)
	out := make([]DeviceView, 0, 8)
	for rows.Next() {
		var (
			view     DeviceView
			info     []byte
			lastSeen *time.Time
		)
		if err := rows.Scan(&view.ID, &view.Name, &view.Platform, &view.AppVersion, &info,
			&view.RemoteControlEnabled, &view.Busy, &lastSeen); err != nil {
			m.internalError(w, "scan_device", err)
			return
		}
		if len(info) == 0 || !json.Valid(info) {
			info = []byte(`{}`)
		}
		view.DeviceInfo = json.RawMessage(info)
		view.DeviceID = view.ID
		view.SelfName = view.Name
		view.LastSeenAt = isoTimePtr(lastSeen)
		view.IsSelf = self != "" && view.ID == self

		if entry, ok := presence[view.ID]; ok {
			view.Online = entry.Online
			view.Busy = entry.Busy
			view.RemoteControlEnabled = entry.RemoteControlEnabled
			if !entry.LastSeenAt.IsZero() {
				view.LastSeenAt = isoTimePtr(&entry.LastSeenAt)
			}
		}
		out = append(out, view)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, "list_devices", err)
		return
	}
	httpapi.OK(w, map[string]any{"devices": out})
}

func isoTimePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func (m *Module) renameDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid device name"))
		return
	}

	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_devices SET name = $3, updated_at = now()
		WHERE id = $1 AND account_id = $2::uuid AND removed_at IS NULL`,
		id, accountID(r), name)
	if err != nil {
		m.internalError(w, "rename_device", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	m.auditEvent(r.Context(), "devicelink.device.rename", "device", id, map[string]any{"name": name})
	httpapi.OK(w, map[string]any{"status": "ok", "deviceId": id, "name": name})
}

func (m *Module) removeDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	account := accountID(r)

	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_devices SET removed_at = now(), updated_at = now()
		WHERE id = $1 AND account_id = $2::uuid AND removed_at IS NULL`, id, account)
	if err != nil {
		m.internalError(w, "remove_device", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}

	m.hub.kick(account, id, closeRemoved, "device removed")
	m.removeDevicePresence(r.Context(), account, id)
	m.auditEvent(r.Context(), "devicelink.device.remove", "device", id, nil)
	httpapi.OK(w, map[string]any{"status": "ok", "deviceId": id})
}

type pushTokenRequest struct {
	Token      string `json:"token"`
	DeviceID   string `json:"deviceId"`
	Platform   string `json:"platform"`
	Provider   string `json:"provider"`
	AppVariant string `json:"appVariant"`
	APNsEnv    string `json:"apnsEnv"`
}

func (m *Module) putPushToken(w http.ResponseWriter, r *http.Request) {
	var req pushTokenRequest
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Platform = strings.TrimSpace(req.Platform)
	if req.Token == "" || len(req.Token) > 4096 || req.Platform == "" || len(req.Platform) > 32 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid push token"))
		return
	}
	if req.Provider == "" {
		req.Provider = "apns"
	}
	if req.AppVariant == "" {
		req.AppVariant = "global"
	}
	if req.APNsEnv == "" {
		req.APNsEnv = "prod"
	}
	if len(req.Provider) > 32 || len(req.AppVariant) > 32 || len(req.APNsEnv) > 32 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid push token metadata"))
		return
	}
	deviceID := deviceIDFromRequest(r, strings.TrimSpace(req.DeviceID))

	_, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_push_tokens (account_id, device_id, token, platform, provider, app_variant, apns_env)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (token) DO UPDATE SET
			account_id = EXCLUDED.account_id,
			device_id = EXCLUDED.device_id,
			platform = EXCLUDED.platform,
			provider = EXCLUDED.provider,
			app_variant = EXCLUDED.app_variant,
			apns_env = EXCLUDED.apns_env,
			updated_at = now()`,
		accountID(r), deviceID, req.Token, req.Platform, req.Provider, req.AppVariant, req.APNsEnv)
	if err != nil {
		m.internalError(w, "put_push_token", err)
		return
	}
	httpapi.OK(w, map[string]any{"status": "ok"})
}

func (m *Module) deletePushToken(w http.ResponseWriter, r *http.Request) {
	var req pushTokenRequest
	if r.ContentLength > 0 {
		_ = decodeBody(r, &req)
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(req.Token)
	}
	deviceID := deviceIDFromRequest(r, strings.TrimSpace(req.DeviceID))
	if token == "" && deviceID == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "token or deviceId required"))
		return
	}

	var (
		tag pgconn.CommandTag
		err error
	)
	if token != "" {
		tag, err = m.db.Pool.Exec(r.Context(),
			`DELETE FROM dl_push_tokens WHERE token = $1 AND account_id = $2::uuid`, token, accountID(r))
	} else {
		tag, err = m.db.Pool.Exec(r.Context(),
			`DELETE FROM dl_push_tokens WHERE device_id = $1 AND account_id = $2::uuid`, deviceID, accountID(r))
	}
	if err != nil {
		m.internalError(w, "delete_push_token", err)
		return
	}
	httpapi.OK(w, map[string]any{"status": "ok", "deleted": tag.RowsAffected()})
}
