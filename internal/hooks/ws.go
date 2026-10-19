package hooks

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// Minimal RFC 6455 server implementation.
//
// gorilla/websocket is the project-level choice for websocket transport, but
// it is not a module requirement in go.mod and this task must not change
// go.mod, so the small subset needed by the hook lane lives here: upgrade,
// text frames, fragmentation, ping/pong, and application close codes.

const (
	wsMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA

	// maxFrameBytes mirrors the protocol-wide OOM guard.
	maxFrameBytes int64 = 48 << 20
	writeTimeout        = 10 * time.Second
)

var (
	errWSClosed        = errors.New("websocket closed")
	errProtocol        = errors.New("websocket protocol error")
	errPayloadTooLarge = errors.New("websocket payload too large")
	errUpgradeAborted  = errors.New("websocket upgrade aborted")
)

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer

	writeMu   sync.Mutex
	closed    atomic.Bool
	closeSent atomic.Bool

	maxPayload  int64
	idleTimeout time.Duration
}

// upgradeWebSocket validates the handshake, hijacks the connection and writes
// the 101 response. Errors returned before hijacking are API errors and may be
// rendered as JSON; errUpgradeAborted means the socket is already gone.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") || !headerHasToken(r.Header.Get("Upgrade"), "websocket") {
		return nil, httpapi.Err(http.StatusBadRequest, "UPGRADE_REQUIRED", "websocket upgrade required")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return nil, httpapi.Err(http.StatusBadRequest, "UPGRADE_REQUIRED", "unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if !validWSKey(key) {
		return nil, httpapi.Err(http.StatusBadRequest, "UPGRADE_REQUIRED", "invalid websocket key")
	}

	conn, brw, err := hijackWriter(w)
	if err != nil {
		return nil, httpapi.Err(http.StatusBadRequest, "UPGRADE_REQUIRED", "connection cannot be upgraded")
	}

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n" +
		"\r\n"
	if _, err := brw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, errUpgradeAborted
	}
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return nil, errUpgradeAborted
	}

	return &wsConn{
		conn:       conn,
		br:         brw.Reader,
		bw:         brw.Writer,
		maxPayload: maxFrameBytes,
	}, nil
}

// hijackWriter unwraps middleware response-writer decorators until it reaches
// an http.Hijacker. Some shared middleware embeds http.ResponseWriter without
// exposing Hijack/Unwrap, so reflection over the exported field is required.
func hijackWriter(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	current := w
	for depth := 0; depth < 8; depth++ {
		if unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter }); ok {
			next := unwrapper.Unwrap()
			if next == nil || next == current {
				break
			}
			current = next
			continue
		}
		if hijacker, ok := current.(http.Hijacker); ok {
			return hijacker.Hijack()
		}
		next := unwrapResponseWriter(current)
		if next == nil || next == current {
			break
		}
		current = next
	}
	return http.NewResponseController(current).Hijack()
}

func unwrapResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	value := reflect.ValueOf(w)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	elem := value.Elem()
	if elem.Kind() != reflect.Struct {
		return nil
	}
	field := elem.FieldByName("ResponseWriter")
	if !field.IsValid() || !field.CanInterface() {
		return nil
	}
	inner, ok := field.Interface().(http.ResponseWriter)
	if !ok {
		return nil
	}
	return inner
}

func (c *wsConn) setIdleTimeout(timeout time.Duration) { c.idleTimeout = timeout }

// ReadMessage returns the next complete text/binary message. Control frames are
// handled inline; a close frame is echoed before returning errWSClosed.
func (c *wsConn) readMessage() (byte, []byte, error) {
	var (
		messageOp byte
		message   []byte
	)
	for {
		if c.idleTimeout > 0 {
			_ = c.conn.SetReadDeadline(time.Now().Add(c.idleTimeout))
		}
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			switch {
			case errors.Is(err, errProtocol):
				_ = c.writeClose(1002, "protocol error")
			case errors.Is(err, errPayloadTooLarge):
				_ = c.writeClose(1009, "message too big")
			}
			return 0, nil, err
		}

		switch opcode {
		case opClose:
			code, _ := parseClosePayload(payload)
			_ = c.writeClose(code, "")
			return 0, nil, errWSClosed
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opText, opBinary:
			if messageOp != 0 {
				return 0, nil, errProtocol
			}
			messageOp = opcode
			message = payload
			if fin {
				return messageOp, message, nil
			}
		case opContinuation:
			if messageOp == 0 {
				return 0, nil, errProtocol
			}
			if int64(len(message))+int64(len(payload)) > c.maxPayload {
				return 0, nil, errPayloadTooLarge
			}
			message = append(message, payload...)
			if fin {
				return messageOp, message, nil
			}
		default:
			return 0, nil, errProtocol
		}
	}
}

func (c *wsConn) readFrame() (bool, byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.br, header[:]); err != nil {
		return false, 0, nil, err
	}
	fin := header[0]&0x80 != 0
	if header[0]&0x70 != 0 {
		return false, 0, nil, errProtocol
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7f)

	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.br, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.br, extended[:]); err != nil {
			return false, 0, nil, err
		}
		unsigned := binary.BigEndian.Uint64(extended[:])
		if unsigned > uint64(c.maxPayload) {
			return false, 0, nil, errPayloadTooLarge
		}
		length = int64(unsigned)
	}

	if opcode >= 0x8 {
		if length > 125 || !fin {
			return false, 0, nil, errProtocol
		}
	} else if length > c.maxPayload {
		return false, 0, nil, errPayloadTooLarge
	}
	if !masked {
		// RFC 6455 requires every client frame to be masked.
		return false, 0, nil, errProtocol
	}

	var mask [4]byte
	if _, err := io.ReadFull(c.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

func (c *wsConn) writeText(payload []byte) error { return c.writeFrame(opText, payload) }

func (c *wsConn) writePing() error { return c.writeFrame(opPing, nil) }

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return errWSClosed
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	var header [10]byte
	header[0] = 0x80 | opcode
	headerLen := 2
	switch {
	case len(payload) < 126:
		header[1] = byte(len(payload))
	case len(payload) <= 65535:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:], uint16(len(payload)))
		headerLen = 4
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(len(payload)))
		headerLen = 10
	}
	if _, err := c.bw.Write(header[:headerLen]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.bw.Write(payload); err != nil {
			return err
		}
	}
	return c.bw.Flush()
}

// writeClose sends at most one close frame. A zero code is normalised to 1000.
func (c *wsConn) writeClose(code int, reason string) error {
	if c.closeSent.Swap(true) {
		return nil
	}
	if code < 1000 || code > 4999 || (code >= 1004 && code <= 1006) || (code >= 1015 && code <= 2999) {
		code = 1000
	}
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], uint16(code))
	copy(payload[2:], reason)
	return c.writeFrame(opClose, payload)
}

// closeWith sends a close frame (best effort) and tears the socket down.
func (c *wsConn) closeWith(code int, reason string) {
	_ = c.writeClose(code, reason)
	_ = c.Close()
}

// Close terminates the underlying connection. It is safe to call repeatedly.
func (c *wsConn) Close() error {
	c.closed.Store(true)
	return c.conn.Close()
}

func parseClosePayload(payload []byte) (int, string) {
	if len(payload) < 2 {
		return 1000, ""
	}
	return int(binary.BigEndian.Uint16(payload[:2])), string(payload[2:])
}

func headerHasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func validWSKey(key string) bool {
	if key == "" {
		return false
	}
	if raw, err := base64.StdEncoding.DecodeString(key); err == nil {
		return len(raw) == 16
	}
	if raw, err := base64.RawStdEncoding.DecodeString(key); err == nil {
		return len(raw) == 16
	}
	return false
}

func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsMagic))
	return base64.StdEncoding.EncodeToString(sum[:])
}
