package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	// presenceTTL is 60s heartbeat period plus 30s of tolerance.
	presenceTTL = 90 * time.Second
	// maxFieldLen bounds platform/version values stored from untrusted input.
	maxFieldLen = 64
)

type heartbeatRequest struct {
	UID      string `json:"uid"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

// heartbeat records a daily active marker and a short-lived presence key.
// It is fire-and-forget: the client only checks for HTTP 2xx, so
// unparseable uids and storage failures still answer {"ok":true}.
func (m *Module) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}

	uid := strings.TrimSpace(req.UID)
	platform := truncate(strings.TrimSpace(req.Platform), maxFieldLen)
	version := truncate(strings.TrimSpace(req.Version), maxFieldLen)

	if _, err := uuid.Parse(uid); err != nil {
		// Anonymous/legacy ids are tolerated but never persisted.
		httpapi.OK(w, map[string]any{"ok": true})
		return
	}

	m.upsertDaily(r.Context(), uid, platform, version)
	m.touchPresence(r.Context(), uid, platform, version)

	httpapi.OK(w, map[string]any{"ok": true})
}

// upsertDaily maintains one row per account per UTC day.
func (m *Module) upsertDaily(ctx context.Context, uid, platform, version string) {
	if m.deps.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	day := time.Now().UTC().Format("2006-01-02")
	_, err := m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO hb_daily (account_id, day, platform, version)
		VALUES ($1::uuid, $2::date, NULLIF($3, ''), NULLIF($4, ''))
		ON CONFLICT (account_id, day) DO UPDATE SET
			last_seen = now(),
			platform = COALESCE(EXCLUDED.platform, hb_daily.platform),
			version = COALESCE(EXCLUDED.version, hb_daily.version)`,
		uid, day, platform, version)
	if err != nil {
		m.log.Warn("heartbeat_upsert_failed", "error", err)
	}
}

// touchPresence refreshes the short-lived online key, never PG.
func (m *Module) touchPresence(ctx context.Context, uid, platform, version string) {
	if m.deps.Redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	value, err := json.Marshal(map[string]any{
		"platform":   platform,
		"version":    version,
		"lastSeenAt": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	key := fmt.Sprintf("cindy:presence:hb:%s", uid)
	if err := m.deps.Redis.Set(ctx, key, value, presenceTTL).Err(); err != nil {
		m.log.Warn("heartbeat_presence_failed", "error", err)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
