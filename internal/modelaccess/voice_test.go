package modelaccess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

func TestVoiceSessionShape(t *testing.T) {
	m := &Module{cfg: &config.Config{
		Env:                  "dev",
		VoiceASRWebSocketURL: "wss://asr.example.test/socket",
		VoiceASRProvider:     "default-asr",
		VoiceASRProtocol:     "qwen-asr-server-vad",
		VoiceASRSampleRate:   16000,
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/voice/sessions",
		strings.NewReader(`{"mode":"dictation","asrProvider":"client-asr"}`))
	req = req.WithContext(httpapi.WithPrincipal(req.Context(), httpapi.Principal{
		AccountID:    "11111111-1111-1111-1111-111111111111",
		MembershipID: "22222222-2222-2222-2222-222222222222",
		Region:       "global",
	}))
	rec := httptest.NewRecorder()
	m.createVoiceSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ASR struct {
			Provider        string `json:"provider"`
			WebSocketURL    string `json:"websocketUrl"`
			ProtocolProfile string `json:"protocolProfile"`
			SampleRate      int    `json:"sampleRate"`
		} `json:"asr"`
		Refiner struct {
			Enabled bool `json:"enabled"`
		} `json:"refiner"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ASR.Provider != "client-asr" || body.ASR.WebSocketURL != "wss://asr.example.test/socket" ||
		body.ASR.ProtocolProfile != "qwen-asr-server-vad" || body.ASR.SampleRate != 16000 {
		t.Fatalf("unexpected asr: %+v", body.ASR)
	}
	if body.Refiner.Enabled {
		t.Fatal("refiner must be disabled by default")
	}
}

func TestVoiceSessionUnavailableWithoutUpstream(t *testing.T) {
	m := &Module{cfg: &config.Config{Env: "dev"}}
	req := httptest.NewRequest(http.MethodPost, "/api/voice/sessions", strings.NewReader(`{"mode":"dictation"}`))
	req = req.WithContext(httpapi.WithPrincipal(req.Context(), httpapi.Principal{AccountID: "a", MembershipID: "m", Region: "global"}))
	rec := httptest.NewRecorder()
	m.createVoiceSession(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "VOICE_NOT_CONFIGURED") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
