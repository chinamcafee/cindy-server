package updates

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// endpointManifest mirrors schemaVersion 1 of the desktop/mobile endpoint
// manifest. Empty strings mean "feature unavailable" to the clients.
type endpointManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Region        string `json:"region"`

	AuthAPIBaseURL         string `json:"authApiBaseUrl"`
	AuthDesktopCallbackURL string `json:"authDesktopCallbackUrl"`
	ChatAPIBaseURL         string `json:"chatApiBaseUrl"`
	DeviceLinkAPIBaseURL   string `json:"deviceLinkApiBaseUrl"`
	OAuthBrokerAPIBaseURL  string `json:"oauthBrokerApiBaseUrl"`
	OSSAPIBaseURL          string `json:"ossApiBaseUrl"`
	HeartbeatURL           string `json:"heartbeatUrl"`

	TelegramHookWSURL string `json:"telegramHookWsUrl"`
	XHookWSURL        string `json:"xHookWsUrl"`
	SlackHookWSURL    string `json:"slackHookWsUrl"`

	WebsiteURL            string `json:"websiteUrl"`
	ModelAccessAPIBaseURL string `json:"modelAccessApiBaseUrl"`
	VoiceAPIBaseURL       string `json:"voiceApiBaseUrl"`
	GitHubAPIBaseURL      string `json:"githubApiBaseUrl"`

	SkillHubAPIBaseURL      string `json:"skillhubApiBaseUrl"`
	CindySkillHubAPIBaseURL string `json:"cindySkillHubApiBaseUrl"`
	PluginAPIBaseURL        string `json:"pluginApiBaseUrl"`

	CDNBaseURL          string `json:"cdnBaseUrl"`
	MobileUpdateBaseURL string `json:"mobileUpdateBaseUrl"`
	Review              string `json:"review"`
}

// endpointJSON returns the newest published manifest for the region, or a
// local default derived from configuration when nothing was published yet.
func (m *Module) endpointJSON(w http.ResponseWriter, r *http.Request) {
	setNoCache(w)

	if m.deps.DB != nil {
		var payload []byte
		err := m.deps.DB.Pool.QueryRow(r.Context(), `
			SELECT payload
			FROM upd_endpoint_manifests
			WHERE status = 'published' AND region = $1
			ORDER BY published_at DESC NULLS LAST, version DESC
			LIMIT 1`, m.region()).Scan(&payload)
		switch {
		case err == nil && len(payload) > 0:
			writeRawJSON(w, http.StatusOK, payload)
			return
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			m.log.Warn("endpoint_manifest_lookup_failed", "region", m.region(), "error", err)
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, m.defaultManifest())
}

// defaultManifest builds a fully local endpoint manifest for development and
// first boot. Every URL points back at this server unless configured otherwise.
func (m *Module) defaultManifest() endpointManifest {
	base := "http://127.0.0.1:19828"
	region := m.region()
	if m.deps.Config != nil {
		if m.deps.Config.PublicBaseURL != "" {
			base = strings.TrimRight(m.deps.Config.PublicBaseURL, "/")
		}
		if m.deps.Config.Region != "" {
			region = m.deps.Config.Region
		}
	}
	hookWS := wsEndpoint(base, "/hook/v1/ws")
	// cdnBaseUrl/websiteUrl are HTTPS-only in the client schema and point at
	// static assets rather than the API backend. Keep them on the public CDN so
	// self-hosted API deployments do not lose update/agent asset downloads.
	assetBase := "https://hotfix.cindy.app/cindy"
	website := "https://cindy.app"
	if region == "cn" {
		assetBase = "https://hotfix.cindy.com.cn/cindy"
		website = "https://cindy.com.cn"
	}
	if m.deps.Config != nil {
		if v := strings.TrimRight(m.deps.Config.AssetCDNBaseURL, "/"); v != "" {
			assetBase = v
		}
		if v := strings.TrimRight(m.deps.Config.WebsiteURL, "/"); v != "" {
			website = v
		}
	}

	return endpointManifest{
		SchemaVersion:           1,
		Region:                  region,
		AuthAPIBaseURL:          base,
		AuthDesktopCallbackURL:  "",
		ChatAPIBaseURL:          base,
		DeviceLinkAPIBaseURL:    base,
		OAuthBrokerAPIBaseURL:   base,
		OSSAPIBaseURL:           base,
		HeartbeatURL:            base,
		TelegramHookWSURL:       hookWS,
		XHookWSURL:              hookWS,
		SlackHookWSURL:          hookWS,
		WebsiteURL:              website,
		ModelAccessAPIBaseURL:   base,
		VoiceAPIBaseURL:         base,
		GitHubAPIBaseURL:        base,
		SkillHubAPIBaseURL:      base,
		CindySkillHubAPIBaseURL: base,
		PluginAPIBaseURL:        base,
		CDNBaseURL:              assetBase,
		MobileUpdateBaseURL:     strings.TrimRight(base, "/") + "/api/mobile-update",
		Review:                  "",
	}
}

// wsEndpoint converts an http(s) base URL into a ws(s) URL for the hook lane.
func wsEndpoint(base, route string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path.Join(u.Path, route)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func setNoCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, no-store, max-age=0")
}

func writeRawJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
