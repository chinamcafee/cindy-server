package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makecindy/cindy-server/internal/auth"
	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/mailer"
	"github.com/makecindy/cindy-server/internal/platform/obs"
)

func TestEmailLoginRefreshFlow(t *testing.T) {
	url := os.Getenv("CINDY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CINDY_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Env: "test", Region: "global", DevFixedCode: "123456", EmailDevFixedCode: "123456",
		AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, PublicBaseURL: "http://test",
	}
	logger := obs.NewLogger("test")
	tokens, err := authn.NewManager(filepath.Join(t.TempDir(), "key.pem"), "cindy", cfg.AccessTTL)
	if err != nil {
		t.Fatal(err)
	}
	auditLog := audit.New(database, cfg.Region, logger)
	mail := mailer.New(mailer.Options{FixedCode: cfg.EmailDevFixedCode}, logger)
	svc := auth.NewService(database, nil, tokens, mail, nil, cfg, auditLog)
	module := auth.NewModule(svc, tokens, cfg, auditLog)
	server := httptest.NewServer(module.Routes())
	defer server.Close()

	email := fmt.Sprintf("it-%d@cindy.local", time.Now().UnixNano())
	post := func(path string, body any, token string) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if status, _ := post("/api/auth/email/request-code", map[string]string{"email": email}, ""); status != 200 {
		t.Fatalf("request code status %d", status)
	}
	status, login := post("/api/auth/email/verify-code", map[string]string{
		"email": email, "code": "123456", "deviceId": "it-device", "clientType": "desktop",
	}, "")
	if status != 200 || login["status"] != "ok" {
		t.Fatalf("login failed: %d %v", status, login)
	}
	access, _ := login["accessToken"].(string)
	refresh, _ := login["refreshToken"].(string)
	if access == "" || refresh == "" {
		t.Fatal("missing tokens")
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("me status %d", resp.StatusCode)
	}

	status, refreshed := post("/api/auth/refresh", map[string]string{"refreshToken": refresh, "deviceId": "it-device"}, "")
	if status != 200 || refreshed["accessToken"] == "" {
		t.Fatalf("refresh failed: %d %v", status, refreshed)
	}
	status, _ = post("/api/auth/refresh", map[string]string{"refreshToken": refresh, "deviceId": "it-device"}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("reused refresh token must be rejected, got %d", status)
	}
}
