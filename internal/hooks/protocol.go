package hooks

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Wire constants of the hook protocol.
const (
	protocolVersion = 1
	serverName      = "cindy-hook"

	// capabilityTaskDispatch is the only optional capability this minimal
	// server can honestly negotiate; v1 dispatch itself is always available.
	capabilityTaskDispatch = "task-dispatch-v1"
)

// MessageType is the open set of hook frame types.
type MessageType string

// The 42 message types of the current desktop protocol. Only the connection
// and dispatch subset is wired in this minimal server; the remaining
// constants document the full namespace and are used for parsing/dispatch
// lookups as features are added.
const (
	TypeHello        MessageType = "hello"         // #1  d→s
	TypeWelcome      MessageType = "welcome"       // #2  s→d
	TypePing         MessageType = "ping"          // #3  both
	TypePong         MessageType = "pong"          // #4  both
	TypeTaskDispatch MessageType = "task.dispatch" // #5  s→d
	TypeTaskAck      MessageType = "task.ack"      // #6  d→s
	TypeTurnEnd      MessageType = "turn.end"      // #7  d→s
	TypeTurnDelivery MessageType = "turn.delivery" // #8  s→d
	TypeTurnProgress MessageType = "turn.progress" // #9  d→s
	TypeTurnReopen   MessageType = "turn.reopen"   // #10 d→s
	TypeMsgOp        MessageType = "msg.op"        // #11 d→s
	TypeMsgOpResult  MessageType = "msg.op.result" // #12 s→d

	TypeBindStart             MessageType = "bind.start"              // #13 d→s
	TypeBindUpdate            MessageType = "bind.update"             // #14 s→d
	TypeBindRevoke            MessageType = "bind.revoke"             // #15 d→s
	TypeProviderBindStart     MessageType = "provider.bind.start"     // #16 d→s
	TypeProviderBindCancel    MessageType = "provider.bind.cancel"    // #17 d→s
	TypeProviderBindRevoke    MessageType = "provider.bind.revoke"    // #18 d→s
	TypeProviderBindUpdate    MessageType = "provider.bind.update"    // #19 s→d
	TypeProviderBindState     MessageType = "provider.bind.state"     // #20 s→d
	TypeQueryRequest          MessageType = "query.request"           // #21 s→d
	TypeQueryResponse         MessageType = "query.response"          // #22 d→s
	TypeTaskCancel            MessageType = "task.cancel"             // #23 s→d
	TypeSessionArchive        MessageType = "session.archive"         // #24 s→d
	TypeInteractionRequest    MessageType = "interaction.request"     // #25 d→s
	TypeInteractionDecision   MessageType = "interaction.decision"    // #26 s→d
	TypeInteractionCancel     MessageType = "interaction.cancel"      // #27 d→s
	TypePrefsGet              MessageType = "prefs.get"               // #28 d→s
	TypePrefsSet              MessageType = "prefs.set"               // #29 d→s
	TypePrefsState            MessageType = "prefs.state"             // #30 s→d
	TypeProviderPrefsGet      MessageType = "provider.prefs.get"      // #31 d→s
	TypeProviderPrefsSet      MessageType = "provider.prefs.set"      // #32 d→s
	TypeProviderPrefsState    MessageType = "provider.prefs.state"    // #33 s→d
	TypeToolRequest           MessageType = "tool.request"            // #34 d→s
	TypeToolResponse          MessageType = "tool.response"           // #35 s→d
	TypeBindState             MessageType = "bind.state"              // #36 s→d
	TypeGroupMessage          MessageType = "group.message"           // #37 s→d
	TypeLifecyclePreference   MessageType = "lifecycle.preference"    // #38 d→s
	TypeProviderBehaviorGet   MessageType = "provider.behavior.get"   // #39 d→s
	TypeProviderBehaviorSet   MessageType = "provider.behavior.set"   // #40 d→s
	TypeProviderBehaviorState MessageType = "provider.behavior.state" // #41 s→d
	TypeProviderCommandsSet   MessageType = "provider.commands.set"   // #42 d→s
)

// knownMessageTypes is the canonical 42-entry set.
var knownMessageTypes = map[MessageType]struct{}{
	TypeHello: {}, TypeWelcome: {}, TypePing: {}, TypePong: {},
	TypeTaskDispatch: {}, TypeTaskAck: {}, TypeTurnEnd: {}, TypeTurnDelivery: {},
	TypeTurnProgress: {}, TypeTurnReopen: {}, TypeMsgOp: {}, TypeMsgOpResult: {},
	TypeBindStart: {}, TypeBindUpdate: {}, TypeBindRevoke: {},
	TypeProviderBindStart: {}, TypeProviderBindCancel: {}, TypeProviderBindRevoke: {},
	TypeProviderBindUpdate: {}, TypeProviderBindState: {}, TypeQueryRequest: {}, TypeQueryResponse: {},
	TypeTaskCancel: {}, TypeSessionArchive: {},
	TypeInteractionRequest: {}, TypeInteractionDecision: {}, TypeInteractionCancel: {},
	TypePrefsGet: {}, TypePrefsSet: {}, TypePrefsState: {},
	TypeProviderPrefsGet: {}, TypeProviderPrefsSet: {}, TypeProviderPrefsState: {},
	TypeToolRequest: {}, TypeToolResponse: {}, TypeBindState: {}, TypeGroupMessage: {},
	TypeLifecyclePreference: {}, TypeProviderBehaviorGet: {}, TypeProviderBehaviorSet: {},
	TypeProviderBehaviorState: {}, TypeProviderCommandsSet: {},
}

// IsKnownMessageType reports whether t belongs to the 42-type namespace.
func IsKnownMessageType(t MessageType) bool {
	_, ok := knownMessageTypes[t]
	return ok
}

// KnownMessageTypes returns a copy of the full type namespace.
func KnownMessageTypes() []MessageType {
	return []MessageType{
		TypeHello, TypeWelcome, TypePing, TypePong,
		TypeTaskDispatch, TypeTaskAck, TypeTurnEnd, TypeTurnDelivery,
		TypeTurnProgress, TypeTurnReopen, TypeMsgOp, TypeMsgOpResult,
		TypeBindStart, TypeBindUpdate, TypeBindRevoke,
		TypeProviderBindStart, TypeProviderBindCancel, TypeProviderBindRevoke,
		TypeProviderBindUpdate, TypeProviderBindState, TypeQueryRequest, TypeQueryResponse,
		TypeTaskCancel, TypeSessionArchive,
		TypeInteractionRequest, TypeInteractionDecision, TypeInteractionCancel,
		TypePrefsGet, TypePrefsSet, TypePrefsState,
		TypeProviderPrefsGet, TypeProviderPrefsSet, TypeProviderPrefsState,
		TypeToolRequest, TypeToolResponse, TypeBindState, TypeGroupMessage,
		TypeLifecyclePreference, TypeProviderBehaviorGet, TypeProviderBehaviorSet,
		TypeProviderBehaviorState, TypeProviderCommandsSet,
	}
}

// envelope is the protocol frame header shared by every message type.
type envelope struct {
	V       int             `json:"v"`
	Type    MessageType     `json:"type"`
	ID      string          `json:"id"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload"`
}

func newEnvelope(t MessageType, payload any) envelope {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	return envelope{
		V:       protocolVersion,
		Type:    t,
		ID:      uuid.NewString(),
		TS:      time.Now().UnixMilli(),
		Payload: raw,
	}
}

func newEmptyEnvelope(t MessageType) envelope {
	return envelope{
		V:       protocolVersion,
		Type:    t,
		ID:      uuid.NewString(),
		TS:      time.Now().UnixMilli(),
		Payload: json.RawMessage(`{}`),
	}
}

type helloPayload struct {
	ProtocolVersion       int      `json:"protocolVersion"`
	DeviceID              string   `json:"deviceId"`
	DeviceName            string   `json:"deviceName"`
	Workspaces            []string `json:"workspaces"`
	DefaultWorkspace      *string  `json:"defaultWorkspace"`
	Agents                []any    `json:"agents"`
	Features              []string `json:"features"`
	LifecycleAnnouncement *bool    `json:"lifecycleAnnouncement"`
}

type welcomePayload struct {
	ServerName string   `json:"serverName"`
	Features   []string `json:"features"`
}

type dispatchPayload struct {
	RequestID   string          `json:"requestId"`
	ExternalKey string          `json:"externalKey"`
	Workspace   *string         `json:"workspace"`
	SessionID   *string         `json:"sessionId"`
	Prompt      string          `json:"prompt"`
	Options     json.RawMessage `json:"options"`
	Attachments json.RawMessage `json:"attachments"`
	Source      json.RawMessage `json:"source"`
}

type taskAckPayload struct {
	RequestID     string  `json:"requestId"`
	Result        string  `json:"result"`
	Reason        *string `json:"reason"`
	SessionID     string  `json:"sessionId"`
	QueuePosition *int    `json:"queuePosition"`
}

type turnEndPayload struct {
	RequestID    string    `json:"requestId"`
	ExternalKey  string    `json:"externalKey"`
	SessionID    string    `json:"sessionId"`
	Status       string    `json:"status"`
	FinalText    string    `json:"finalText"`
	ErrorMessage *string   `json:"errorMessage"`
	Usage        turnUsage `json:"usage"`
}

type turnUsage struct {
	DurationMS int64 `json:"durationMs"`
}

var (
	errHelloInvalid = errors.New("invalid hello")
	errDeviceBusy   = errors.New("device already connected")
)

// negotiateFeatures returns the server capability subset of the client list.
func negotiateFeatures(client []string) []string {
	declared := make(map[string]struct{}, len(client))
	for _, f := range client {
		declared[f] = struct{}{}
	}
	out := make([]string, 0, 1)
	for _, f := range []string{capabilityTaskDispatch} {
		if _, ok := declared[f]; ok {
			out = append(out, f)
		}
	}
	return out
}

// sanitizeFeatures bounds untrusted feature strings.
func sanitizeFeatures(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" || len(f) > 64 {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
		if len(out) >= 64 {
			break
		}
	}
	return out
}

// isEmptyPayload accepts the protocol's "ping/pong payload must be {}" rule.
func isEmptyPayload(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "{}"
}
