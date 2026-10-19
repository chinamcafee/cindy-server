package modelaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	devGatewayKey      = "dev-gateway-key"
	gatewaySettingsKey = "model_access.gateway"
	gatewayRedisPrefix = "cindy:ma:gateway:"
)

type gatewayCredential struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey"`
}

func (m *Module) gatewayEndpoint() string {
	base := ""
	if m.cfg != nil {
		base = strings.TrimRight(m.cfg.PublicBaseURL, "/")
	}
	if base == "" {
		base = "http://127.0.0.1:19828"
	}
	return base + "/v1"
}

func (m *Module) devMode() bool {
	return m.cfg == nil || m.cfg.IsDev()
}

func (m *Module) storedGatewayKey(ctx context.Context, accountID string) string {
	if m.rdb == nil {
		return ""
	}
	key, err := m.rdb.Get(ctx, gatewayRedisPrefix+accountID).Result()
	if err != nil {
		return ""
	}
	return key
}

func (m *Module) settingsGateway(ctx context.Context) (gatewayCredential, bool) {
	var cred gatewayCredential
	if m.db == nil {
		return cred, false
	}
	var raw []byte
	err := m.db.Pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, gatewaySettingsKey).Scan(&raw)
	if err != nil {
		return cred, false
	}
	if err := json.Unmarshal(raw, &cred); err != nil || cred.APIKey == "" {
		return cred, false
	}
	if cred.Endpoint == "" {
		cred.Endpoint = m.gatewayEndpoint()
	}
	return cred, true
}

func (m *Module) gatewayCredential(ctx context.Context, accountID string) (gatewayCredential, error) {
	cred := gatewayCredential{Endpoint: m.gatewayEndpoint(), APIKey: m.storedGatewayKey(ctx, accountID)}
	if m.devMode() {
		if cred.APIKey == "" {
			cred.APIKey = devGatewayKey
		}
		return cred, nil
	}
	if cred.APIKey != "" {
		return cred, nil
	}
	if stored, ok := m.settingsGateway(ctx); ok {
		return stored, nil
	}
	return gatewayCredential{}, unavailable("MODEL_ACCESS_DISABLED", "model access is not configured")
}

func (m *Module) getCredentials(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	cred, err := m.gatewayCredential(r.Context(), p.AccountID)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.OK(w, cred)
}

func (m *Module) rotateCredentials(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	cred, err := m.gatewayCredential(r.Context(), p.AccountID)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	cred.APIKey = "gw_" + crypto.RandomToken(24)
	if m.rdb != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_ = m.rdb.Set(ctx, gatewayRedisPrefix+p.AccountID, cred.APIKey, 0).Err()
	}
	m.auditEvent(r, "model_access.credentials.rotate", "account", p.AccountID, map[string]any{
		"endpoint": cred.Endpoint,
		"rotated":  true,
	})
	httpapi.OK(w, cred)
}
