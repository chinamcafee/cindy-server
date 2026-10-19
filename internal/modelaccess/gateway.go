package modelaccess

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const gatewayMaxBody = 16 << 20

type gatewayFlushWriter struct {
	w http.ResponseWriter
}

func (f gatewayFlushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func joinGatewayURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		return base + strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

func hopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length":
		return true
	default:
		return false
	}
}

// gatewayProxy forwards managed-model inference to the configured upstream.
// The client's gateway key is resolved separately from resource access tokens;
// upstream credentials never leave this process.
func (m *Module) gatewayProxy(w http.ResponseWriter, r *http.Request) {
	key := ""
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		key = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	expected := ""
	if m.cfg != nil {
		expected = m.cfg.GatewayAPIKey
	}
	if expected == "" && m.cfg != nil && m.cfg.IsDev() {
		expected = devGatewayKey
	}
	if expected == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "MODEL_ACCESS_DISABLED", "managed model gateway is not configured"))
		return
	}
	if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(expected)) != 1 {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, gatewayMaxBody+1))
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	if len(body) > gatewayMaxBody {
		httpapi.WriteError(w, httpapi.Err(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body is too large"))
		return
	}

	anthropic := r.URL.Path == "/v1/messages"
	var base, upstreamKey string
	if m.cfg != nil {
		if anthropic {
			base, upstreamKey = m.cfg.GatewayAnthropicBaseURL, m.cfg.GatewayAnthropicAPIKey
		} else {
			base, upstreamKey = m.cfg.GatewayOpenAIBaseURL, m.cfg.GatewayOpenAIAPIKey
		}
	}
	if base == "" || upstreamKey == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "MODEL_ACCESS_DISABLED", "managed model upstream is not configured"))
		return
	}

	target := joinGatewayURL(base, r.URL.Path)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	for name, values := range r.Header {
		if hopByHopHeader(name) || strings.EqualFold(name, "Authorization") ||
			strings.EqualFold(name, "Accept-Encoding") {
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if anthropic {
		req.Header.Set("x-api-key", upstreamKey)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+upstreamKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadGateway, "UPSTREAM_ERROR", "managed model upstream is unavailable"))
		return
	}
	defer resp.Body.Close()
	for name, values := range resp.Header {
		if hopByHopHeader(name) {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(gatewayFlushWriter{w: w}, resp.Body)
}
