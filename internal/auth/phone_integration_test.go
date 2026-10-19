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
	"github.com/makecindy/cindy-server/internal/platform/sms"
)

type fakeSMSSender struct {
	code string
}

func (f *fakeSMSSender) SendVerificationCode(_ context.Context, _ string, code string) error {
	f.code = code
	return nil
}
func (f *fakeSMSSender) Available() bool  { return true }
func (f *fakeSMSSender) Provider() string { return "fake" }

func TestPhoneLoginFlow(t *testing.T) {
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
		Env: "test", Region: "global", AccessTTL: 15 * time.Minute,
		RefreshTTL: time.Hour, PublicBaseURL: "http://test",
	}
	logger := obs.NewLogger("test")
	tokens, err := authn.NewManager(filepath.Join(t.TempDir(), "key.pem"), "cindy", cfg.AccessTTL)
	if err != nil {
		t.Fatal(err)
	}
	auditLog := audit.New(database, cfg.Region, logger)
	mail := mailer.New(mailer.Options{}, logger)
	sender := &fakeSMSSender{}
	svc := auth.NewService(database, nil, tokens, mail, sender, cfg, auditLog)
	module := auth.NewModule(svc, tokens, cfg, auditLog)
	server := httptest.NewServer(module.Routes())
	defer server.Close()

	phone := fmt.Sprintf("+1415%07d", time.Now().UnixNano()%10000000)
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

	status, sent := post("/api/auth/phone/request-code", map[string]string{"phone": phone}, "")
	if status != http.StatusOK || sent["status"] != "sent" {
		t.Fatalf("request code failed: %d %v", status, sent)
	}
	if len(sender.code) != 6 {
		t.Fatalf("expected 6-digit code, got %q", sender.code)
	}

	status, login := post("/api/auth/phone/verify-code", map[string]string{
		"phone": phone, "code": sender.code, "deviceId": "phone-it", "clientType": "mobile",
	}, "")
	if status != http.StatusOK || login["status"] != "ok" {
		t.Fatalf("phone login failed: %d %v", status, login)
	}
	membership, _ := login["membership"].(map[string]any)
	if membership == nil || membership["email"] != nil {
		t.Fatalf("expected phone membership without email: %v", membership)
	}
	access, _ := login["accessToken"].(string)

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status %d", resp.StatusCode)
	}

	status, _ = post("/api/auth/phone/request-code", map[string]string{"phone": "not-a-phone"}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid phone must be rejected, got %d", status)
	}

	// Phone-only accounts must be able to delete via SMS, not a missing email.
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/auth/account/deletion", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var availability map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&availability)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || availability["available"] != true {
		t.Fatalf("deletion availability failed: %d %v", resp.StatusCode, availability)
	}
	verification, _ := availability["verification"].(map[string]any)
	if verification == nil || verification["channel"] != "sms" || verification["maskedTarget"] == "" || verification["maskedTarget"] == nil {
		t.Fatalf("expected sms deletion verification, got %v", availability)
	}

	status, challenge := post("/api/auth/account/deletion/challenge", map[string]any{}, access)
	if status != http.StatusOK || challenge["channel"] != "sms" || challenge["maskedTarget"] == "" {
		t.Fatalf("deletion challenge failed: %d %v", status, challenge)
	}
	status, confirmed := post("/api/auth/account/deletion/confirm", map[string]any{
		"challengeId": challenge["challengeId"], "receiptToken": challenge["receiptToken"],
		"code": sender.code, "acknowledged": true,
	}, access)
	if status != http.StatusOK || confirmed["status"] != "pending" {
		t.Fatalf("deletion confirm failed: %d %v", status, confirmed)
	}
}

var _ sms.Sender = (*fakeSMSSender)(nil)
