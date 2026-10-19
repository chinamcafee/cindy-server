package hooks

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// handshakeTimeout bounds the wait for the first hello frame.
	handshakeTimeout = 15 * time.Second
	// idleTimeout closes lanes that have been silent for 90s.
	idleTimeout = 90 * time.Second
	// serverPingInterval keeps NAT mappings alive and surfaces dead peers.
	serverPingInterval = 30 * time.Second
	// closeCodeDeviceBusy is the application close code for a superseded lane.
	closeCodeDeviceBusy = 4000

	maxRememberedAcks = 512
)

type session struct {
	module    *Module
	ws        *wsConn
	accountID string

	deviceID string
	features []string

	closed atomic.Bool

	// seen replays task.ack for duplicate dispatches. Only the reader
	// goroutine touches these fields.
	seen      map[string][]byte
	seenOrder []string
}

func newSession(module *Module, ws *wsConn, accountID string) *session {
	return &session{
		module:    module,
		ws:        ws,
		accountID: accountID,
		seen:      make(map[string][]byte),
	}
}

func (s *session) isClosed() bool { return s.closed.Load() }

// run drives the lane: hello handshake, then the frame loop until error/close.
func (s *session) run() {
	s.ws.setIdleTimeout(handshakeTimeout)

	opcode, frame, err := s.ws.readMessage()
	if err != nil {
		return
	}
	if opcode != opText {
		s.ws.closeWith(1008, "hello required")
		return
	}
	var env envelope
	if err := json.Unmarshal(frame, &env); err != nil || env.Type != TypeHello || (env.V != 0 && env.V != protocolVersion) {
		s.ws.closeWith(1008, "hello required")
		return
	}
	if err := s.handleHello(env.Payload); err != nil {
		code, reason := closeForHelloError(err)
		s.ws.closeWith(code, reason)
		return
	}
	s.module.log.Debug("hook_lane_ready", "device_id", s.deviceID, "features", len(s.features))

	s.ws.setIdleTimeout(idleTimeout)
	done := make(chan struct{})
	defer close(done)
	go s.pingLoop(done)

	for {
		opcode, frame, err := s.ws.readMessage()
		if err != nil {
			return
		}
		if opcode != opText {
			continue
		}
		if !s.handleFrame(frame) {
			return
		}
	}
}

// close releases the device slot and the socket. It is idempotent.
func (s *session) close() {
	if s.closed.Swap(true) {
		return
	}
	if s.deviceID != "" {
		s.module.hub.Unregister(s.accountID, s.deviceID, s)
	}
	_ = s.ws.Close()
}

func (s *session) pingLoop(done <-chan struct{}) {
	ticker := time.NewTicker(serverPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := s.ws.writePing(); err != nil {
				_ = s.ws.Close()
				return
			}
		}
	}
}

// handleHello validates the handshake payload, claims the device slot and
// answers with welcome. Re-sent hello frames are accepted as the newest truth.
func (s *session) handleHello(raw json.RawMessage) error {
	var hello helloPayload
	if err := json.Unmarshal(raw, &hello); err != nil {
		return errHelloInvalid
	}
	if hello.ProtocolVersion != 0 && hello.ProtocolVersion != protocolVersion {
		return errHelloInvalid
	}
	deviceID := strings.TrimSpace(hello.DeviceID)
	if deviceID == "" || len(deviceID) > 128 {
		return errHelloInvalid
	}
	features := sanitizeFeatures(hello.Features)

	if s.deviceID != deviceID {
		if s.deviceID != "" {
			s.module.hub.Unregister(s.accountID, s.deviceID, s)
		}
		if !s.module.hub.Register(s.accountID, deviceID, s) {
			return errDeviceBusy
		}
		s.deviceID = deviceID
	}
	s.features = features

	return s.send(newEnvelope(TypeWelcome, welcomePayload{
		ServerName: serverName,
		Features:   negotiateFeatures(features),
	}))
}

// handleFrame returns false when the lane must stop.
func (s *session) handleFrame(frame []byte) bool {
	var env envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		// A single malformed frame never breaks the lane.
		s.module.log.Debug("hook_frame_dropped", "reason", "invalid_envelope")
		return true
	}
	if env.V != 0 && env.V != protocolVersion {
		s.module.log.Debug("hook_frame_dropped", "reason", "protocol_version", "type", string(env.Type))
		return true
	}

	switch env.Type {
	case TypeHello:
		if err := s.handleHello(env.Payload); err != nil {
			code, reason := closeForHelloError(err)
			s.ws.closeWith(code, reason)
			return false
		}
	case TypePing:
		if isEmptyPayload(env.Payload) {
			if err := s.send(newEmptyEnvelope(TypePong)); err != nil {
				return false
			}
		}
	case TypePong:
		// Liveness only; the read deadline already moved.
	case TypeTaskDispatch:
		s.handleDispatch(env.Payload)
	case TypeTaskAck, TypeTurnEnd, TypeTurnProgress, TypeTurnReopen, TypeMsgOpResult:
		// Accepted from the desktop side; this minimal server has no turn
		// outbox, so they are acknowledged as no-ops.
	default:
		// Unknown or not-yet-implemented types are dropped without closing.
		s.module.log.Debug("hook_frame_dropped", "reason", "unsupported_type", "type", string(env.Type))
	}
	return true
}

// handleDispatch acknowledges a dispatch and closes the skeleton turn.
func (s *session) handleDispatch(raw json.RawMessage) {
	var dispatch dispatchPayload
	if err := json.Unmarshal(raw, &dispatch); err != nil {
		return
	}
	requestID := strings.TrimSpace(dispatch.RequestID)
	if requestID == "" || len(requestID) > 128 {
		return
	}
	if previous, ok := s.seen[requestID]; ok {
		_ = s.ws.writeText(previous)
		return
	}

	sessionID := ""
	if dispatch.SessionID != nil {
		sessionID = strings.TrimSpace(*dispatch.SessionID)
	}
	if sessionID == "" {
		// Skeleton sessions are named after the request; a real lane maps the
		// externalKey to a desktop session.
		sessionID = "hook-" + requestID
	}

	ack := newEnvelope(TypeTaskAck, taskAckPayload{
		RequestID: requestID,
		Result:    "accepted",
		SessionID: sessionID,
	})
	frame, err := json.Marshal(ack)
	if err != nil {
		return
	}
	if err := s.ws.writeText(frame); err != nil {
		return
	}
	s.remember(requestID, frame)

	// The skeleton cannot run an agent, so the turn is closed immediately to
	// keep the dispatch -> ack -> turn.end loop observable to clients.
	_ = s.send(newEnvelope(TypeTurnEnd, turnEndPayload{
		RequestID:   requestID,
		ExternalKey: dispatch.ExternalKey,
		SessionID:   sessionID,
		Status:      "ok",
		FinalText:   "",
		Usage:       turnUsage{DurationMS: 0},
	}))
}

func (s *session) remember(requestID string, frame []byte) {
	if _, exists := s.seen[requestID]; exists {
		return
	}
	s.seen[requestID] = frame
	s.seenOrder = append(s.seenOrder, requestID)
	if len(s.seenOrder) > maxRememberedAcks {
		oldest := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seen, oldest)
	}
}

func (s *session) send(env envelope) error {
	frame, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return s.ws.writeText(frame)
}

func closeForHelloError(err error) (int, string) {
	if err == errDeviceBusy {
		return closeCodeDeviceBusy, "device already connected"
	}
	return 1008, "invalid hello"
}
