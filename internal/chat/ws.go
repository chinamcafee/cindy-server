package chat

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	wsMaxMessage = 256 * 1024
	wsMaxScopes  = 16
	wsQueueSize  = 256
	wsPingPeriod = 30 * time.Second
)

type wsFrame struct {
	Type     string          `json:"type"`
	RoomID   string          `json:"roomId,omitempty"`
	Seq      string          `json:"seq,omitempty"`
	ChangeID string          `json:"changeId,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Scope    string          `json:"scope,omitempty"`
	Cursor   string          `json:"cursor,omitempty"`
	Changes  []wsChange      `json:"changes,omitempty"`
}

type wsChange struct {
	Type     string          `json:"type"`
	EntityID string          `json:"entityId,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

func changeFromEvent(typ string, raw []byte) wsChange {
	change := wsChange{Type: typ, Data: json.RawMessage(raw)}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) == nil {
		for _, key := range []string{"messageId", "id", "actorId", "roomId"} {
			if value, ok := fields[key].(string); ok && value != "" {
				change.EntityID = value
				break
			}
		}
	}
	return change
}

type hub struct {
	mu    sync.RWMutex
	rooms map[string]map[*wsConn]struct{}
}

func newHub() *hub {
	return &hub{rooms: map[string]map[*wsConn]struct{}{}}
}

func (h *hub) add(c *wsConn, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[roomID] == nil {
		h.rooms[roomID] = map[*wsConn]struct{}{}
	}
	h.rooms[roomID][c] = struct{}{}
}

func (h *hub) removeConn(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for roomID, conns := range h.rooms {
		delete(conns, c)
		if len(conns) == 0 {
			delete(h.rooms, roomID)
		}
	}
}

func (h *hub) publishEvent(ev roomEvent) {
	direct, err := json.Marshal(wsFrame{
		Type: ev.Type, RoomID: ev.RoomID, Seq: formatSeq(ev.Seq), ChangeID: formatSeq(ev.Seq),
		Payload: ev.Payload,
	})
	if err != nil {
		return
	}
	changes, err := json.Marshal(wsFrame{
		Type:   "changes",
		Scope:  "conversation:" + ev.RoomID,
		Cursor: formatSeq(ev.Seq),
		Changes: []wsChange{
			changeFromEvent(ev.Type, ev.Payload),
		},
	})
	if err != nil {
		return
	}
	frame := buildFrame(0x1, changes)
	// Older probes consume the direct event; current clients use `changes`
	// and ignore unknown frame types.
	frame = append(frame, buildFrame(0x1, direct)...)
	var overflow []*wsConn
	h.mu.RLock()
	for c := range h.rooms[ev.RoomID] {
		if !c.enqueue(frame) {
			overflow = append(overflow, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range overflow {
		c.close()
	}
}

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (c *wsConn) enqueue(frame []byte) bool {
	select {
	case c.send <- frame:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *wsConn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *wsConn) sendJSON(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return c.enqueue(buildFrame(0x1, b))
}

func (c *wsConn) writePump() {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := c.conn.Write(frame); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := c.conn.Write(buildFrame(0x9, nil)); err != nil {
				c.close()
				return
			}
		}
	}
}

func buildFrame(opcode byte, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+10)
	out = append(out, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		out = append(out, byte(n))
	case n < 65536:
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, payload...)
}

func readFrame(br *bufio.Reader) (fin bool, opcode byte, payload []byte, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(br, head); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		return false, 0, nil, errors.New("reserved bits set")
	}
	opcode = head[0] & 0x0f
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7f)
	switch length {
	case 126:
		buf := make([]byte, 2)
		if _, err = io.ReadFull(br, buf); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(buf))
	case 127:
		buf := make([]byte, 8)
		if _, err = io.ReadFull(br, buf); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(buf))
	}
	if !masked {
		return false, 0, nil, errors.New("unmasked client frame")
	}
	if length < 0 || length > wsMaxMessage {
		return false, 0, nil, errors.New("frame too large")
	}
	mask := make([]byte, 4)
	if _, err = io.ReadFull(br, mask); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

func (c *wsConn) readMessage() (byte, []byte, error) {
	var data []byte
	var msgOp byte
	for {
		fin, opcode, payload, err := readFrame(c.br)
		if err != nil {
			return 0, nil, err
		}
		switch opcode {
		case 0x1, 0x2:
			if !fin {
				msgOp = opcode
				data = append([]byte(nil), payload...)
				continue
			}
			return opcode, payload, nil
		case 0x0:
			if msgOp == 0 {
				return 0, nil, errors.New("unexpected continuation")
			}
			data = append(data, payload...)
			if len(data) > wsMaxMessage {
				return 0, nil, errors.New("message too large")
			}
			if fin {
				return msgOp, data, nil
			}
		case 0x8, 0x9, 0xA:
			return opcode, payload, nil
		default:
			return 0, nil, errors.New("unsupported opcode")
		}
	}
}

type wsInbound struct {
	Type     string          `json:"type"`
	Scopes   []string        `json:"scopes"`
	After    json.RawMessage `json:"after"`
	Scope    string          `json:"scope"`
	ChangeID string          `json:"changeId"`
	Token    string          `json:"token"`
}

// parseAfter accepts the legacy per-scope object and the current string form.
func parseAfter(raw json.RawMessage, roomID string) int64 {
	if len(raw) == 0 {
		return 0
	}
	var scalar string
	if json.Unmarshal(raw, &scalar) == nil {
		value, _ := parseSeq(scalar, "after")
		return value
	}
	var perScope map[string]string
	if json.Unmarshal(raw, &perScope) == nil {
		value, _ := parseSeq(perScope[roomID], "after")
		return value
	}
	return 0
}

func readyFrame(actorID string) wsFrame {
	return wsFrame{Type: "ready", Payload: payloadJSON(map[string]any{"actorId": actorID})}
}

func (m *Module) handleWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("access_token"))
	if token == "" {
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		}
	}
	var human actorRow
	if token != "" {
		claims, err := m.deps.Tokens.ParseAccess(token)
		if err != nil {
			if errors.Is(err, authn.ErrExpiredToken) {
				httpapi.WriteError(w, httpapi.ErrTokenExpired)
			} else {
				httpapi.WriteError(w, httpapi.ErrInvalidToken)
			}
			return
		}
		human, err = m.ensureHumanActor(r.Context(), claims.Subject)
		if err != nil {
			m.fail(w, err)
			return
		}
	}
	if !isWebSocketUpgrade(r) {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_INPUT", "websocket upgrade required"))
		return
	}
	conn, rw, err := hijackResponse(w)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}
	accept := base64.StdEncoding.EncodeToString(sha1Sum(r.Header.Get("Sec-WebSocket-Key")))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	ws := &wsConn{conn: conn, br: rw.Reader, send: make(chan []byte, wsQueueSize), done: make(chan struct{})}
	go ws.writePump()
	defer func() {
		ws.close()
		m.hub.removeConn(ws)
	}()

	if token == "" {
		// Current clients send the bearer token in the first `auth` frame.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, payload, err := ws.readMessage()
		if err != nil {
			ws.sendJSON(wsFrame{Type: "error", Payload: payloadJSON(map[string]any{"code": "UNAUTHORIZED", "message": "auth required"})})
			return
		}
		var in wsInbound
		if err := json.Unmarshal(payload, &in); err != nil || in.Type != "auth" || in.Token == "" {
			ws.sendJSON(wsFrame{Type: "error", Payload: payloadJSON(map[string]any{"code": "UNAUTHORIZED", "message": "auth required"})})
			return
		}
		claims, err := m.deps.Tokens.ParseAccess(in.Token)
		if err != nil {
			ws.sendJSON(wsFrame{Type: "error", Payload: payloadJSON(map[string]any{"code": "INVALID_TOKEN", "message": "invalid token"})})
			return
		}
		human, err = m.ensureHumanActor(r.Context(), claims.Subject)
		if err != nil {
			m.fail(w, err)
			return
		}
	}
	ws.sendJSON(readyFrame(human.ID))
	m.wsReadLoop(r.Context(), ws, human)
}

func sha1Sum(v string) []byte {
	sum := sha1.Sum([]byte(v + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return sum[:]
}

// hijackResponse unwraps logging/compression writers that swallow http.Hijacker.
func hijackResponse(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.(http.Hijacker); ok {
		return hj.Hijack()
	}
	if conn, rw, err := http.NewResponseController(w).Hijack(); err == nil {
		return conn, rw, nil
	}
	v := reflect.ValueOf(w)
	for depth := 0; depth < 8 && v.IsValid(); depth++ {
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			break
		}
		var next reflect.Value
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanInterface() {
				continue
			}
			inner, ok := field.Interface().(http.ResponseWriter)
			if !ok {
				continue
			}
			if hj, ok := inner.(http.Hijacker); ok {
				return hj.Hijack()
			}
			next = reflect.ValueOf(inner)
			break
		}
		if !next.IsValid() {
			break
		}
		v = next
	}
	return nil, nil, errors.New("http hijack unsupported")
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if r.Header.Get("Sec-WebSocket-Key") == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}

func (m *Module) wsReadLoop(ctx context.Context, ws *wsConn, human actorRow) {
	subscribed := map[string]bool{}
	for {
		_ = ws.conn.SetReadDeadline(time.Now().Add(3 * wsPingPeriod))
		opcode, payload, err := ws.readMessage()
		if err != nil {
			return
		}
		switch opcode {
		case 0x8:
			return
		case 0x9:
			if !ws.enqueue(buildFrame(0xA, payload)) {
				return
			}
			continue
		case 0xA:
			continue
		}
		var in wsInbound
		if err := json.Unmarshal(payload, &in); err != nil {
			ws.sendJSON(wsFrame{Type: "error", Payload: payloadJSON(map[string]any{"code": "INVALID_INPUT", "message": "invalid json"})})
			return
		}
		switch in.Type {
		case "auth":
			// Credentials sent in the first frame were already verified.
			continue
		case "subscribe":
			scopes := in.Scopes
			if len(scopes) == 0 && in.Scope != "" {
				scopes = []string{in.Scope}
			}
			for _, scope := range scopes {
				scope = strings.TrimSpace(scope)
				if strings.HasPrefix(scope, "actor:") {
					// Personal list changes are pulled by the client after `ready`;
					// acknowledge the scope so it does not look like a failed subscribe.
					ws.sendJSON(wsFrame{Type: "changes", Scope: scope, Cursor: "0", Changes: []wsChange{}})
					continue
				}
				roomID := strings.TrimPrefix(scope, "conversation:")
				if !isUUID(roomID) {
					ws.sendJSON(wsFrame{Type: "scope_error", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "INVALID_INPUT"})})
					continue
				}
				if !subscribed[roomID] && len(subscribed) >= wsMaxScopes {
					ws.sendJSON(wsFrame{Type: "scope_error", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "TOO_MANY_SCOPES"})})
					continue
				}
				if _, _, err := m.requireJoined(ctx, roomID, human.ID); err != nil {
					ws.sendJSON(wsFrame{Type: "scope_error", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "NOT_MEMBER"})})
					continue
				}
				subscribed[roomID] = true
				m.hub.add(ws, roomID)
				m.wsBackfill(ctx, ws, roomID, parseAfter(in.After, roomID))
			}
		case "ack":
			// Cursors are client-owned; ack only advances the in-memory subscription.
		default:
			ws.sendJSON(wsFrame{Type: "error", Payload: payloadJSON(map[string]any{"code": "INVALID_INPUT", "message": "unknown type"})})
		}
	}
}

func (m *Module) wsBackfill(ctx context.Context, ws *wsConn, roomID string, after int64) {
	for batch := 0; batch < 10; batch++ {
		rows, err := m.deps.DB.Pool.Query(ctx, `SELECT seq, type, payload FROM chat_room_events
			WHERE room_id=$1 AND seq > $2 ORDER BY seq ASC LIMIT 100`, roomID, after)
		if err != nil {
			ws.sendJSON(wsFrame{Type: "error", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "UNAVAILABLE"})})
			return
		}
		count := 0
		changes := []wsChange{}
		direct := []wsFrame{}
		for rows.Next() {
			var seq int64
			var typ string
			var payload []byte
			if err := rows.Scan(&seq, &typ, &payload); err != nil {
				rows.Close()
				ws.sendJSON(wsFrame{Type: "error", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "UNAVAILABLE"})})
				return
			}
			count++
			after = seq
			changes = append(changes, changeFromEvent(typ, payload))
			direct = append(direct, wsFrame{Type: typ, RoomID: roomID, Seq: formatSeq(seq), ChangeID: formatSeq(seq), Payload: json.RawMessage(payload)})
		}
		rows.Close()
		if err := ws.sendJSON(wsFrame{Type: "changes", Scope: "conversation:" + roomID, Cursor: formatSeq(after), Changes: changes}); !err {
			ws.close()
			return
		}
		for i := range direct {
			if !ws.sendJSON(direct[i]) {
				ws.close()
				return
			}
		}
		if count < 100 {
			return
		}
	}
	ws.sendJSON(wsFrame{Type: "change.reset", RoomID: roomID, Payload: payloadJSON(map[string]any{"code": "RESET_REQUIRED"})})
}

func payloadJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
