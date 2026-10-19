package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

func testSender(t *testing.T, handler http.HandlerFunc) (*apnsSender, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &apnsSender{
		keyID: "KEYID12345", teamID: "TEAMID6789", key: key,
		topicGlobal: "app.cindy.global", topicCN: "app.cindy.cn",
		endpoint: server.URL, client: server.Client(), now: time.Now,
	}, server
}

func TestAPNsSendRequestAndJWT(t *testing.T) {
	var (
		path   string
		header http.Header
		body   []byte
	)
	sender, _ := testSender(t, func(w http.ResponseWriter, r *http.Request) {
		path, header = r.URL.Path, r.Header.Clone()
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	err := sender.Send(context.Background(), Message{
		Token: "device-token", Platform: "ios", Provider: "apns", AppVariant: "global",
		APNsEnv: "sandbox", Title: "Task done", Body: "Your task finished", CollapseID: "collapse-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/3/device/device-token" {
		t.Fatalf("path = %q", path)
	}
	if header.Get("apns-topic") != "app.cindy.global" ||
		header.Get("apns-push-type") != "alert" ||
		header.Get("apns-priority") != "10" ||
		header.Get("apns-collapse-id") != "collapse-1" {
		t.Fatalf("unexpected headers: %+v", header)
	}
	var payload struct {
		Aps struct {
			Alert map[string]string `json:"alert"`
			Sound string            `json:"sound"`
		} `json:"aps"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Aps.Alert["title"] != "Task done" || payload.Aps.Alert["body"] != "Your task finished" || payload.Aps.Sound != "default" {
		t.Fatalf("unexpected payload: %s", body)
	}

	auth := strings.TrimPrefix(header.Get("authorization"), "bearer ")
	parts := strings.Split(auth, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT: %q", auth)
	}
	headerJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var jwtHeader map[string]string
	var claims map[string]any
	_ = json.Unmarshal(headerJSON, &jwtHeader)
	_ = json.Unmarshal(claimsJSON, &claims)
	if jwtHeader["alg"] != "ES256" || jwtHeader["kid"] != "KEYID12345" || claims["iss"] != "TEAMID6789" {
		t.Fatalf("unexpected JWT header/claims: %s %s", headerJSON, claimsJSON)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("invalid ES256 signature: %v %d", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&sender.key.PublicKey, digest[:], r, s) {
		t.Fatal("JWT signature does not verify")
	}
}

func TestAPNsErrorReason(t *testing.T) {
	sender, _ := testSender(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, `{"reason":"Unregistered"}`)
	})
	err := sender.Send(context.Background(), Message{
		Token: "device-token", AppVariant: "cn", APNsEnv: "prod", Title: "t", Body: "b",
	})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusGone || apiErr.Reason != "Unregistered" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestNoopWhenUnconfigured(t *testing.T) {
	if New(nil, nil).Available() {
		t.Fatal("nil config must be unavailable")
	}
	if New(&config.Config{}, nil).Available() {
		t.Fatal("empty config must be unavailable")
	}
}
