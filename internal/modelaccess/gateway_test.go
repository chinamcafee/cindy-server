package modelaccess

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

func TestGatewayOpenAIProxy(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"ok\":true}\n\n")
	}))
	defer upstream.Close()

	m := &Module{cfg: &config.Config{
		Env:                  "dev",
		GatewayAPIKey:        "gateway-key",
		GatewayOpenAIBaseURL: upstream.URL,
		GatewayOpenAIAPIKey:  "upstream-key",
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?beta=1", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer gateway-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	m.gatewayProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" || gotQuery != "beta=1" {
		t.Fatalf("upstream path/query = %q %q", gotPath, gotQuery)
	}
	if gotAuth != "Bearer upstream-key" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if gotBody != `{"model":"gpt-test"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `{"ok":true}`) {
		t.Fatalf("response body = %q", rec.Body.String())
	}
}

func TestGatewayAnthropicProxy(t *testing.T) {
	var gotAPIKey, gotVersion, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	m := &Module{cfg: &config.Config{
		Env:                     "dev",
		GatewayAPIKey:           "gateway-key",
		GatewayAnthropicBaseURL: upstream.URL + "/v1",
		GatewayAnthropicAPIKey:  "anthropic-key",
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req.Header.Set("Authorization", "Bearer gateway-key")
	rec := httptest.NewRecorder()
	m.gatewayProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAPIKey != "anthropic-key" || gotVersion == "" {
		t.Fatalf("upstream anthropic headers = %q %q", gotAPIKey, gotVersion)
	}
}

func TestGatewayRejectsBadKeyAndMissingUpstream(t *testing.T) {
	m := &Module{cfg: &config.Config{Env: "prod", GatewayAPIKey: "gateway-key"}}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	m.gatewayProxy(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer gateway-key")
	rec = httptest.NewRecorder()
	m.gatewayProxy(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing upstream status = %d", rec.Code)
	}
}
