package devicelink

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// iceServers issues short-lived TURN REST credentials:
// username = <expiryUnix>:<accountId>, credential = base64(HMAC-SHA1(secret, username)).
func (m *Module) iceServers(w http.ResponseWriter, r *http.Request) {
	const ttl = 10 * time.Minute
	expiresAt := time.Now().UTC().Add(ttl)
	username := fmt.Sprintf("%d:%s", expiresAt.Unix(), accountID(r))

	secret := ""
	var urls []string
	if m.cfg != nil {
		secret = m.cfg.TurnSecret
		urls = m.cfg.TurnURLs
	}
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if len(urls) > 4 {
		urls = urls[:4]
	}
	servers := []iceServer{}
	if len(urls) > 0 {
		servers = append(servers, iceServer{URLs: urls, Username: username, Credential: credential})
	}
	httpapi.OK(w, map[string]any{
		"iceServers": servers,
		"expiresAt":  expiresAt.UTC(),
	})
}
