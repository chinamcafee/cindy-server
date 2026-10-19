package modelaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const voiceSessionTTL = 10 * time.Minute

type voiceSessionASR struct {
	Provider        string `json:"provider"`
	WebSocketURL    string `json:"websocketUrl"`
	ProtocolProfile string `json:"protocolProfile"`
	SampleRate      int    `json:"sampleRate"`
	Model           string `json:"model,omitempty"`
}

type voiceSessionRefiner struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
}

type voiceSessionResponse struct {
	SessionID string              `json:"sessionId"`
	Ticket    string              `json:"ticket"`
	WSURL     string              `json:"wsUrl"`
	ExpiresAt string              `json:"expiresAt"`
	ASR       voiceSessionASR     `json:"asr"`
	Refiner   voiceSessionRefiner `json:"refiner"`
}

func (m *Module) createVoiceSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode            string `json:"mode"`
		Language        string `json:"language"`
		Client          string `json:"client"`
		ClientVersion   string `json:"clientVersion"`
		ASRProvider     string `json:"asrProvider"`
		RefinerProvider string `json:"refinerProvider"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if req.Mode != "dictation" {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "mode must be dictation"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	wsURL := ""
	provider := strings.TrimSpace(req.ASRProvider)
	protocol := "qwen-asr-server-vad"
	sampleRate := 16000
	model := ""
	refinerEnabled := false
	if m.cfg != nil {
		wsURL = strings.TrimSpace(m.cfg.VoiceASRWebSocketURL)
		if provider == "" {
			provider = m.cfg.VoiceASRProvider
		}
		if m.cfg.VoiceASRProtocol != "" {
			protocol = m.cfg.VoiceASRProtocol
		}
		if m.cfg.VoiceASRSampleRate > 0 {
			sampleRate = m.cfg.VoiceASRSampleRate
		}
		model = m.cfg.VoiceASRModel
		refinerEnabled = m.cfg.VoiceRefinerEnabled
	}
	if wsURL == "" || (!strings.HasPrefix(wsURL, "wss://") && !strings.HasPrefix(wsURL, "ws://")) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "VOICE_NOT_CONFIGURED", "voice ASR upstream is not configured"))
		return
	}
	switch protocol {
	case "volcengine-sauc-duration", "qwen-asr-server-vad", "openai-transcription-manual":
	default:
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	sessionID := uuid.NewString()
	ticket := crypto.RandomToken(32)
	now := time.Now().UTC()
	if m.now != nil {
		now = m.now()
	}
	expires := now.Add(5 * time.Minute)

	if m.rdb != nil {
		payload, _ := json.Marshal(map[string]any{
			"accountId":   p.AccountID,
			"asrProvider": req.ASRProvider,
			"ticketHash":  crypto.HashToken(ticket),
			"expiresAt":   expires,
		})
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_ = m.rdb.Set(ctx, "cindy:voice:session:"+sessionID, payload, voiceSessionTTL).Err()
	}
	m.auditEvent(r, "voice.session.create", "voice_session", sessionID, map[string]any{
		"asrProvider": req.ASRProvider,
	})

	httpapi.OK(w, voiceSessionResponse{
		SessionID: sessionID,
		Ticket:    ticket,
		WSURL:     wsURL,
		ExpiresAt: isoMillis(expires),
		ASR: voiceSessionASR{
			Provider: provider, WebSocketURL: wsURL,
			ProtocolProfile: protocol, SampleRate: sampleRate, Model: model,
		},
		Refiner: voiceSessionRefiner{Enabled: refinerEnabled},
	})
}

func (m *Module) voiceWebSocketURL(ticket string) string {
	base := "http://127.0.0.1:19828"
	if m.cfg != nil && m.cfg.PublicBaseURL != "" {
		base = strings.TrimRight(m.cfg.PublicBaseURL, "/")
	}
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + "/v1/voice/asr?ticket=" + ticket
}

func (m *Module) voiceRefine(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteError(w, notImplemented("NOT_IMPLEMENTED", "voice refinement is not available"))
}

func (m *Module) voiceRefineWarmup(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
