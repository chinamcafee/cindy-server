package modelaccess

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
)

func TestSmokeRoutes(t *testing.T) {
	mgr, err := authn.NewManager(filepath.Join(t.TempDir(), "k.pem"), "cindy", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.SignAccess("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "global")
	if err != nil {
		t.Fatal(err)
	}
	mod := New(Deps{Tokens: mgr, Config: &config.Config{
		Env: "dev", PublicBaseURL: "http://127.0.0.1:8080",
		VoiceASRWebSocketURL: "ws://voice.example.test/asr",
		VoiceASRProvider:     "x", VoiceASRProtocol: "qwen-asr-server-vad", VoiceASRSampleRate: 16000,
	}})
	srv := httptest.NewServer(mod.Routes())
	defer srv.Close()

	authReq := func(method, path, body string) *http.Request {
		var r *http.Request
		if body == "" {
			r, _ = http.NewRequest(method, srv.URL+path, nil)
		} else {
			r, _ = http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		}
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	do := func(r *http.Request) (*http.Response, []byte) {
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	// catalog
	resp, body := do(mustReq(http.MethodGet, srv.URL+"/api/model-catalog/catalog?registrySchemaVersion=5&registryMedia=1&registryLocalRuntimes=1"))
	if resp.StatusCode != 200 {
		t.Fatalf("catalog status %d %s", resp.StatusCode, body)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 4 {
		t.Fatalf("catalog top-level keys = %d: %s", len(top), body)
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=60" || resp.Header.Get("ETag") == "" {
		t.Fatalf("catalog headers %v", resp.Header)
	}
	req := mustReq(http.MethodGet, srv.URL+"/api/model-catalog/catalog?registrySchemaVersion=5&registryMedia=1&registryLocalRuntimes=1")
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	resp2, _ := do(req)
	if resp2.StatusCode != 304 {
		t.Fatalf("expected 304, got %d", resp2.StatusCode)
	}

	// models
	resp, body = do(authReq(http.MethodGet, "/api/model-access/models?schemaVersion=5", ""))
	if resp.StatusCode != 200 {
		t.Fatalf("models %d %s", resp.StatusCode, body)
	}
	var models struct {
		SchemaVersion int    `json:"schemaVersion"`
		AccountTier   string `json:"accountTier"`
		Models        []any  `json:"models"`
	}
	_ = json.Unmarshal(body, &models)
	if models.SchemaVersion != 5 || models.AccountTier != "free" || models.Models == nil {
		t.Fatalf("models body %s", body)
	}
	resp, _ = do(authReq(http.MethodGet, "/api/model-access/models?schemaVersion=4", ""))
	if resp.StatusCode != 400 {
		t.Fatalf("models v4 expected 400, got %d", resp.StatusCode)
	}
	resp, _ = do(mustReq(http.MethodGet, srv.URL+"/api/model-access/models?schemaVersion=5"))
	if resp.StatusCode != 401 {
		t.Fatalf("models unauthenticated expected 401, got %d", resp.StatusCode)
	}

	// credentials
	resp, body = do(authReq(http.MethodGet, "/api/model-access/credentials", ""))
	if resp.StatusCode != 200 || !strings.Contains(string(body), "dev-gateway-key") {
		t.Fatalf("credentials %d %s", resp.StatusCode, body)
	}
	resp, body = do(authReq(http.MethodPost, "/api/model-access/credentials/rotate", ""))
	if resp.StatusCode != 200 || strings.Contains(string(body), "dev-gateway-key") {
		t.Fatalf("rotate %d %s", resp.StatusCode, body)
	}

	// quota / balance / credit usage
	for _, path := range []string{"/api/model-access/quota", "/api/model-access/balance", "/api/model-access/credit-usage", "/api/model-access/byok/providers?schemaVersion=1", "/api/model-access/byok/credentials?schemaVersion=1"} {
		resp, body = do(authReq(http.MethodGet, path, ""))
		if resp.StatusCode != 200 {
			t.Fatalf("%s %d %s", path, resp.StatusCode, body)
		}
	}
	resp, body = do(authReq(http.MethodGet, "/api/model-access/quota", ""))
	var quota map[string]any
	_ = json.Unmarshal(body, &quota)
	if quota["maxBudget"] != nil || quota["currency"] != "USD" {
		t.Fatalf("quota %s", body)
	}
	resp, body = do(authReq(http.MethodGet, "/api/model-access/credit-usage", ""))
	if !strings.Contains(string(body), `"promotionalGrantConsistency":"OBSERVED"`) {
		t.Fatalf("credit usage %s", body)
	}

	// billing catalog + order without DB
	resp, body = do(authReq(http.MethodGet, "/api/billing/catalog", ""))
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"products":[]`) {
		t.Fatalf("billing catalog %d %s", resp.StatusCode, body)
	}
	resp, body = do(authReq(http.MethodPost, "/api/billing/credit-topup/orders", `{"offerCode":"gold","purchaseOptionId":"x"}`))
	if resp.StatusCode != 404 {
		t.Fatalf("topup expected 404 without DB, got %d %s", resp.StatusCode, body)
	}
	resp, body = do(authReq(http.MethodGet, "/api/billing/subscription", ""))
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"subscription":null`) {
		t.Fatalf("subscription %d %s", resp.StatusCode, body)
	}

	// voice
	resp, body = do(authReq(http.MethodPost, "/api/voice/sessions", `{"mode":"dictation","asrProvider":"x"}`))
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"websocketUrl":"ws://`) {
		t.Fatalf("voice %d %s", resp.StatusCode, body)
	}
	resp, _ = do(authReq(http.MethodPost, "/api/voice/sessions", `{"mode":"other"}`))
	if resp.StatusCode != 400 {
		t.Fatalf("voice mode expected 400, got %d", resp.StatusCode)
	}
	resp, _ = do(authReq(http.MethodPost, "/api/voice/refine", `{}`))
	if resp.StatusCode != 501 {
		t.Fatalf("refine expected 501, got %d", resp.StatusCode)
	}

	// flags / oauth / embeddings
	resp, body = do(authReq(http.MethodGet, "/api/user/feature-flags", ""))
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"isCanary":false`) {
		t.Fatalf("flags %d %s", resp.StatusCode, body)
	}
	resp, body = do(authReq(http.MethodPost, "/api/integrations/feishu/oauth/exchange", `{"code":"x"}`))
	if resp.StatusCode != 503 || !strings.Contains(string(body), "SERVICE_UNAVAILABLE") {
		t.Fatalf("exchange %d %s", resp.StatusCode, body)
	}
	resp, body = do(authReq(http.MethodPost, "/api/integrations/slack/oauth/exchange", `{"code":"x"}`))
	if resp.StatusCode != 404 {
		t.Fatalf("slack exchange %d %s", resp.StatusCode, body)
	}
	resp, _ = do(authReq(http.MethodPost, "/v1/embeddings", `{}`))
	// /v1/* is the managed-model gateway and uses its own gateway key; a
	// resource access token is intentionally not accepted there.
	if resp.StatusCode != 401 {
		t.Fatalf("embeddings expected gateway auth 401, got %d", resp.StatusCode)
	}

	// bounce
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = func() (*http.Response, []byte) {
		resp, err := noRedirect.Do(mustReq(http.MethodGet, srv.URL+"/api/integrations/oauth/jira/bounce?redirectPort=4567&state=abc&code=c1"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "http://127.0.0.1:4567/oauth/callback?code=c1&state=abc" {
		t.Fatalf("bounce %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// guides 404 without DB
	resp, body = do(authReq(http.MethodGet, "/api/model-access/invocation-guide?modelId=x", ""))
	if resp.StatusCode != 404 || !strings.Contains(string(body), "MEDIA_INVOCATION_GUIDE_NOT_FOUND") {
		t.Fatalf("guide %d %s", resp.StatusCode, body)
	}
}

func mustReq(method, url string) *http.Request {
	r, _ := http.NewRequest(method, url, nil)
	return r
}
