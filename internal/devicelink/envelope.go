package devicelink

import (
	"encoding/json"
	"time"
)

// Protocol constants. They are append-only and must stay in sync with the
// client protocol package (PROTOCOL_VERSION = 1).
const (
	protocolVersion = 1
)

// Envelope kinds.
const (
	kindHello           = "hello"
	kindHelloAck        = "hello-ack"
	kindPresenceSet     = "presence-set"
	kindPresenceChanged = "presence-changed"
	kindPing            = "ping"
	kindPong            = "pong"
	kindNotify          = "notify"
	kindLinkOpen        = "link-open"
	kindLinkAccept      = "link-accept"
	kindLinkClose       = "link-close"
	kindInvoke          = "invoke"
	kindInvokeResult    = "invoke-result"
	kindPush            = "push"
	kindRelayError      = "relay-error"
)

// routedKinds are forwarded to the device identified by dst.
var routedKinds = map[string]bool{
	kindLinkOpen:     true,
	kindLinkAccept:   true,
	kindLinkClose:    true,
	kindInvoke:       true,
	kindInvokeResult: true,
	kindPush:         true,
}

// controlKinds require the target device to have remote control enabled.
var controlKinds = map[string]bool{
	kindLinkOpen: true,
	kindInvoke:   true,
}

// RelayErrorCode values returned in relay-error payloads.
const (
	relayDeviceOffline   = "DEVICE_OFFLINE"
	relayRemoteDisabled  = "REMOTE_DISABLED"
	relayVersionMismatch = "VERSION_MISMATCH"
	relayPayloadTooLarge = "PAYLOAD_TOO_LARGE"
	relayRateLimited     = "RATE_LIMITED"
	relayBadRequest      = "BAD_REQUEST"
	relayInternal        = "INTERNAL"
)

// WebSocket close codes.
const (
	closeNormal        = 1000
	closeGoingAway     = 1001
	closeProtocolError = 1002
	closeUnsupported   = 1003
	closeTooLarge      = 1009
	closeInternal      = 1011
	closeTryAgain      = 1013
	closeBadFrame      = 4400
	closeUnauthorized  = 4401
	closeReplaced      = 4409
	closeRemoved       = 4410
	closeTooManyConns  = 4429
)

// serverCapabilities is the append-only capability set advertised in hello-ack.
var serverCapabilities = []string{"notify", "shared-task-v2", "provider-share-v1"}

// Envelope is the single frame format of the relay protocol. Payload is kept
// opaque (json.RawMessage) and is never inspected when routing.
type Envelope struct {
	V             int                 `json:"v"`
	Kind          string              `json:"kind"`
	ID            string              `json:"id,omitempty"`
	Src           string              `json:"src,omitempty"`
	Dst           string              `json:"dst,omitempty"`
	SharedTask    *SharedTaskScope    `json:"sharedTask,omitempty"`
	ProviderShare *ProviderShareScope `json:"providerShare,omitempty"`
	Payload       json.RawMessage     `json:"payload,omitempty"`
}

// SharedTaskScope authorises a frame inside a shared task.
type SharedTaskScope struct {
	SharedTaskID string       `json:"sharedTaskId"`
	Target       ScopeTarget  `json:"target"`
	Source       *ScopeSource `json:"source,omitempty"`
}

// ProviderShareScope authorises a frame inside a provider share.
type ProviderShareScope struct {
	ShareID string       `json:"shareId"`
	Target  ScopeTarget  `json:"target"`
	Source  *ScopeSource `json:"source,omitempty"`
}

// ScopeTarget identifies the receiving side of a scoped frame.
type ScopeTarget struct {
	Role     string `json:"role"`
	MemberID string `json:"memberId,omitempty"`
}

// ScopeSource is written by the server (client supplied values are ignored).
type ScopeSource struct {
	Role     string `json:"role"`
	MemberID string `json:"memberId,omitempty"`
}

// HelloPayload is the payload of the first client frame.
type HelloPayload struct {
	DeviceID             string          `json:"deviceId,omitempty"`
	DeviceName           string          `json:"deviceName"`
	Platform             string          `json:"platform"`
	AppVersion           string          `json:"appVersion"`
	RemoteControlEnabled bool            `json:"remoteControlEnabled"`
	Busy                 bool            `json:"busy"`
	DeviceInfo           json.RawMessage `json:"deviceInfo,omitempty"`
	Capabilities         []string        `json:"capabilities,omitempty"`
}

// HelloAckPayload is returned by the server after a successful hello.
type HelloAckPayload struct {
	ServerProtocolVersion int      `json:"serverProtocolVersion"`
	DeviceID              string   `json:"deviceId"`
	UserID                string   `json:"userId"`
	Capabilities          []string `json:"capabilities"`
}

// PresencePayload is broadcast to every connection of the same account.
type PresencePayload struct {
	DeviceID             string    `json:"deviceId"`
	DeviceName           string    `json:"deviceName,omitempty"`
	Platform             string    `json:"platform,omitempty"`
	AppVersion           string    `json:"appVersion,omitempty"`
	Online               bool      `json:"online"`
	Busy                 bool      `json:"busy"`
	RemoteControlEnabled bool      `json:"remoteControlEnabled"`
	LastSeenAt           time.Time `json:"lastSeenAt"`
}

// RelayErrorPayload is carried by relay-error frames.
type RelayErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Dst     string `json:"dst,omitempty"`
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
