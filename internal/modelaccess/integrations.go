package modelaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type featureFlagsResponse struct {
	IsCanary          bool `json:"isCanary"`
	DefaultEnableBeta bool `json:"defaultEnableBeta"`
}

func boolValue(raw []byte) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var wrapper struct {
		Value *bool `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Value != nil {
		return *wrapper.Value, true
	}
	return false, false
}

func (m *Module) featureFlags(w http.ResponseWriter, r *http.Request) {
	res := featureFlagsResponse{}
	p := httpapi.MustPrincipal(r.Context())
	if m.db == nil {
		httpapi.OK(w, res)
		return
	}
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT key, default_value FROM feature_flags
		WHERE key IN ('isCanary','defaultEnableBeta')`)
	if err == nil {
		for rows.Next() {
			var key string
			var raw []byte
			if err := rows.Scan(&key, &raw); err != nil {
				break
			}
			if v, ok := boolValue(raw); ok {
				switch key {
				case "isCanary":
					res.IsCanary = v
				case "defaultEnableBeta":
					res.DefaultEnableBeta = v
				}
			}
		}
		rows.Close()
	}

	orgID := m.membershipOrg(r.Context(), p.MembershipID)
	overrides, err := m.db.Pool.Query(r.Context(), `
		SELECT flag_key, value FROM feature_flag_overrides
		WHERE (scope = 'account' AND target = $1) OR (scope = 'org' AND target = $2)
		ORDER BY (scope = 'account')`, p.AccountID, orgID)
	if err == nil {
		for overrides.Next() {
			var key string
			var raw []byte
			if err := overrides.Scan(&key, &raw); err != nil {
				break
			}
			if v, ok := boolValue(raw); ok {
				switch key {
				case "isCanary":
					res.IsCanary = v
				case "defaultEnableBeta":
					res.DefaultEnableBeta = v
				}
			}
		}
		overrides.Close()
	}
	httpapi.OK(w, res)
}

type oauthIntegrationConfig struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	TokenURL     string `json:"tokenUrl"`
	Scopes       string `json:"scopes"`
}

func oauthSlugAllowed(slug string) bool {
	return slug == "feishu" || slug == "jira"
}

func (m *Module) oauthConfig(ctx context.Context, slug string) (*oauthIntegrationConfig, bool) {
	if m.db == nil {
		return nil, false
	}
	var raw []byte
	if err := m.db.Pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, "oauth."+slug).Scan(&raw); err != nil {
		return nil, false
	}
	var cfg oauthIntegrationConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false
	}
	if cfg.TokenURL == "" || cfg.ClientID == "" {
		return nil, false
	}
	return &cfg, true
}

type oauthExchangeRequest struct {
	Code         string `json:"code"`
	RedirectURI  string `json:"redirectUri"`
	CodeVerifier string `json:"codeVerifier"`
}

type oauthRefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type oauthTokenPayload struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    json.Number `json:"expires_in"`
	Scope        string      `json:"scope"`
	Error        string      `json:"error"`
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func (m *Module) oauthExchange(w http.ResponseWriter, r *http.Request) {
	slug := chiURLParam(r, "slug")
	if !oauthSlugAllowed(slug) {
		httpapi.WriteError(w, notFound("NOT_FOUND", "unsupported integration"))
		return
	}
	var req oauthExchangeRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if strings.TrimSpace(req.Code) == "" || len(req.Code) > 4096 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "code is required"))
		return
	}
	cfg, ok := m.oauthConfig(r.Context(), slug)
	if !ok {
		httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "integration is not configured"))
		return
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", req.Code)
	form.Set("redirect_uri", req.RedirectURI)
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	if req.CodeVerifier != "" {
		form.Set("code_verifier", req.CodeVerifier)
	}
	m.oauthTokenCall(w, r, slug, cfg, form)
}

func (m *Module) oauthRefresh(w http.ResponseWriter, r *http.Request) {
	slug := chiURLParam(r, "slug")
	if !oauthSlugAllowed(slug) {
		httpapi.WriteError(w, notFound("NOT_FOUND", "unsupported integration"))
		return
	}
	var req oauthRefreshRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" || len(req.RefreshToken) > 8192 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "refreshToken is required"))
		return
	}
	cfg, ok := m.oauthConfig(r.Context(), slug)
	if !ok {
		httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "integration is not configured"))
		return
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", req.RefreshToken)
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	m.oauthTokenCall(w, r, slug, cfg, form)
}

func (m *Module) oauthTokenCall(w http.ResponseWriter, r *http.Request, slug string, cfg *oauthIntegrationConfig, form url.Values) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "oauth upstream unavailable"))
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "oauth upstream unavailable"))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var payload oauthTokenPayload
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil || payload.AccessToken == "" {
			httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "oauth upstream response invalid"))
			return
		}
		out := map[string]any{"accessToken": payload.AccessToken}
		if payload.RefreshToken != "" {
			out["refreshToken"] = payload.RefreshToken
		}
		if payload.ExpiresIn != "" {
			if n, err := payload.ExpiresIn.Int64(); err == nil && n > 0 {
				out["expiresIn"] = n
			}
		}
		if payload.Scope != "" {
			out["scope"] = payload.Scope
		}
		httpapi.OK(w, out)
		return
	}

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		var payload oauthTokenPayload
		_ = json.Unmarshal(body, &payload)
		if payload.Error == "invalid_grant" {
			code := "FEISHU_OAUTH_FAILED"
			if slug == "jira" {
				code = "JIRA_OAUTH_FAILED"
			}
			httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, code, "oauth upstream rejected the grant"))
			return
		}
	}
	httpapi.WriteError(w, unavailable("SERVICE_UNAVAILABLE", "oauth upstream unavailable"))
}

var bouncePathRe = regexp.MustCompile(`^/[A-Za-z0-9_\-./]*$`)

func (m *Module) oauthBounce(w http.ResponseWriter, r *http.Request) {
	slug := chiURLParam(r, "slug")
	if !oauthSlugAllowed(slug) {
		httpapi.WriteError(w, notFound("NOT_FOUND", "unsupported integration"))
		return
	}
	query := r.URL.Query()
	port, err := strconv.Atoi(query.Get("redirectPort"))
	if err != nil || port < 1 || port > 65535 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "redirectPort is required"))
		return
	}
	path := query.Get("callbackPath")
	if path == "" {
		path = query.Get("path")
	}
	if path == "" {
		path = "/oauth/callback"
	}
	if len(path) > 128 || !bouncePathRe.MatchString(path) {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "callbackPath is invalid"))
		return
	}
	values := url.Values{}
	state := query.Get("state")
	if len(state) > 512 {
		state = ""
	}
	if state != "" {
		values.Set("state", state)
	}
	if code := query.Get("code"); code != "" && len(code) <= 4096 {
		values.Set("code", code)
	}
	if errCode := query.Get("error"); errCode != "" && len(errCode) <= 256 {
		values.Set("error", errCode)
	}
	location := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	if encoded := values.Encode(); encoded != "" {
		location += "?" + encoded
	}
	http.Redirect(w, r, location, http.StatusFound)
}

func (m *Module) embeddings(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteError(w, notImplemented("EMBEDDINGS_UNAVAILABLE", "embedding gateway is not configured"))
}
