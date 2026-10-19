// Package push delivers device notifications through APNs. The sender is
// disabled unless a .p8 key, key ID, team ID and at least one topic are set.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

type Message struct {
	Token      string
	Platform   string
	Provider   string
	AppVariant string
	APNsEnv    string
	Title      string
	Body       string
	CollapseID string
}

type Sender interface {
	Send(ctx context.Context, msg Message) error
	Available() bool
}

type noopSender struct{}

func (noopSender) Send(context.Context, Message) error { return nil }
func (noopSender) Available() bool                    { return false }

// New returns an APNs sender when all credentials are configured, otherwise a
// no-op sender. Configuration errors are logged and degrade to no-op so a bad
// optional key cannot stop the server.
func New(cfg *config.Config, logger *slog.Logger) Sender {
	if cfg == nil {
		return noopSender{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	keyID := strings.TrimSpace(cfg.APNsKeyID)
	teamID := strings.TrimSpace(cfg.APNsTeamID)
	keyFile := strings.TrimSpace(cfg.APNsPrivateKeyFile)
	topicGlobal := strings.TrimSpace(cfg.APNsTopicGlobal)
	topicCN := strings.TrimSpace(cfg.APNsTopicCN)
	if keyID == "" && teamID == "" && keyFile == "" && topicGlobal == "" && topicCN == "" {
		return noopSender{}
	}
	if keyID == "" || teamID == "" || keyFile == "" || (topicGlobal == "" && topicCN == "") {
		logger.Warn("apns_disabled_incomplete_config")
		return noopSender{}
	}
	key, err := loadPrivateKey(keyFile)
	if err != nil {
		logger.Error("apns_private_key_load_failed", "error", err)
		return noopSender{}
	}
	s := &apnsSender{
		keyID:       keyID,
		teamID:      teamID,
		key:         key,
		topicGlobal: topicGlobal,
		topicCN:     topicCN,
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			Proxy:             http.ProxyFromEnvironment,
		}},
		now: time.Now,
	}
	return s
}

func loadPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("not an EC private key")
		}
		return ec, nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return key, nil
}

type apnsSender struct {
	keyID       string
	teamID      string
	key         *ecdsa.PrivateKey
	topicGlobal string
	topicCN     string
	endpoint    string
	client      *http.Client
	now         func() time.Time

	mu          sync.Mutex
	cachedToken string
	cachedAt    time.Time
}

func (s *apnsSender) Available() bool { return true }

func (s *apnsSender) topic(appVariant string) string {
	if strings.EqualFold(appVariant, "cn") {
		return s.topicCN
	}
	return s.topicGlobal
}

func (s *apnsSender) sendURL(msg Message) string {
	if s.endpoint != "" {
		return s.endpoint
	}
	env := strings.ToLower(strings.TrimSpace(msg.APNsEnv))
	if env == "sandbox" {
		return "https://api.sandbox.push.apple.com"
	}
	return "https://api.push.apple.com"
}

func (s *apnsSender) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(msg.Token) == "" {
		return fmt.Errorf("apns token is empty")
	}
	topic := s.topic(msg.AppVariant)
	if topic == "" {
		return fmt.Errorf("apns topic is not configured for variant %q", msg.AppVariant)
	}
	providerToken, err := s.providerToken()
	if err != nil {
		return err
	}
	payload := map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": truncate(msg.Title, 128), "body": truncate(msg.Body, 1024)},
			"sound": "default",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sendURL(msg)+"/3/device/"+msg.Token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+providerToken)
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	if msg.CollapseID != "" {
		req.Header.Set("apns-collapse-id", truncate(msg.CollapseID, 64))
	}
	req.Header.Set("content-type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("apns request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var reason struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &reason)
	if reason.Reason == "" {
		reason.Reason = strings.TrimSpace(string(raw))
	}
	return &APIError{StatusCode: resp.StatusCode, Reason: reason.Reason}
}

type APIError struct {
	StatusCode int
	Reason     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("apns HTTP %d: %s", e.StatusCode, e.Reason)
}

func (s *apnsSender) providerToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cachedToken != "" && now.Sub(s.cachedAt) < 50*time.Minute {
		return s.cachedToken, nil
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": s.keyID})
	claims, _ := json.Marshal(map[string]any{"iss": s.teamID, "iat": now.Unix()})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("apns sign: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	s.cachedToken = signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	s.cachedAt = now
	return s.cachedToken, nil
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
