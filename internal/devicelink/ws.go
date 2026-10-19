package devicelink

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The repository does not vendor gorilla/websocket (adding a module dependency
// is out of scope for this package), so the small subset of RFC 6455 needed by
// the relay lives here. Only text frames, fragmentation, ping/pong and close
// are supported.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	wsHardLimit = 4 << 20 // transport limit enforced before JSON parsing
)

var (
	errWSProtocol = errors.New("websocket protocol error")
	errWSTooLarge = errors.New("websocket frame too large")
	errWSTimeout  = errors.New("websocket handshake timeout")
)

var wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu   sync.Mutex
	closeOnce sync.Once
}

// upgradeWebSocket performs the RFC 6455 opening handshake. The request must
// carry a Bearer token that was validated by the caller beforehand.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("websocket upgrade requires GET")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") ||
		!headerHasToken(r.Header, "Upgrade", "websocket") {
		return nil, errors.New("not a websocket upgrade request")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")), "13") {
		w.Header().Set("Sec-WebSocket-Version", "13")
		return nil, errors.New("unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	conn, brw, err := hijack(w)
	if err != nil {
		return nil, err
	}

	accept := wsAccept(key)
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(response)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetWriteDeadline(time.Time{})

	ws := &wsConn{conn: conn, br: brw.Reader}
	return ws, nil
}

func hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.(http.Hijacker); ok {
		return hj.Hijack()
	}
	// ResponseController unwraps known wrappers (ListenAndServe / HTTP/2 is not
	// supported, which is fine for a WSS relay endpoint).
	return http.NewResponseController(w).Hijack()
}

func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func (w *wsConn) SetReadDeadline(t time.Time) error { return w.conn.SetReadDeadline(t) }

func (w *wsConn) RemoteAddr() string {
	if w.conn.RemoteAddr() == nil {
		return ""
	}
	return w.conn.RemoteAddr().String()
}

// ReadMessage reads the next complete text message. Control frames are handled
// transparently. Binary messages are rejected (the protocol is JSON only).
func (w *wsConn) ReadMessage() ([]byte, error) {
	var message []byte
	var fragmenting bool

	for {
		fin, opcode, payload, err := w.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := w.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
			// Ignore; any inbound frame already refreshed the read deadline.
		case opClose:
			_ = w.writeFrame(opClose, payload)
			w.shutdown()
			return nil, io.EOF
		case opText:
			if fragmenting {
				return nil, errWSProtocol
			}
			if fin {
				return payload, nil
			}
			message = append(message, payload...)
			fragmenting = true
		case opContinuation:
			if !fragmenting {
				return nil, errWSProtocol
			}
			message = append(message, payload...)
			if len(message) > wsHardLimit {
				return nil, errWSTooLarge
			}
			if fin {
				return message, nil
			}
		case opBinary:
			return nil, errWSProtocol
		default:
			return nil, errWSProtocol
		}
	}
}

func (w *wsConn) readFrame() (bool, byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(w.br, header[:]); err != nil {
		return false, 0, nil, err
	}
	fin := header[0]&0x80 != 0
	if header[0]&0x70 != 0 {
		return false, 0, nil, errWSProtocol // RSV bits must be zero (no extensions)
	}
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	if !masked {
		return false, 0, nil, errWSProtocol // client frames must be masked
	}

	length := int64(header[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		u := binary.BigEndian.Uint64(ext[:])
		if u > uint64(wsHardLimit) {
			return false, 0, nil, errWSTooLarge
		}
		length = int64(u)
	}
	if length < 0 || length > wsHardLimit {
		return false, 0, nil, errWSTooLarge
	}

	var mask [4]byte
	if _, err := io.ReadFull(w.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return false, 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	isControl := opcode >= 0x8
	if isControl && (!fin || length > 125) {
		return false, 0, nil, errWSProtocol
	}
	return fin, opcode, payload, nil
}

func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	header := make([]byte, 0, 10)
	header = append(header, 0x80|opcode)
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	frame := make([]byte, 0, len(header)+length)
	frame = append(frame, header...)
	frame = append(frame, payload...)

	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := w.conn.Write(frame)
	return err
}

// Close sends a close frame and shuts down the transport.
func (w *wsConn) Close(code int, reason string) {
	w.closeOnce.Do(func() {
		payload := make([]byte, 2+len(reason))
		binary.BigEndian.PutUint16(payload[:2], uint16(code))
		copy(payload[2:], reason)
		_ = w.writeFrame(opClose, payload)
		_ = w.conn.Close()
	})
}

// shutdown aborts the transport without a close handshake.
func (w *wsConn) shutdown() {
	w.closeOnce.Do(func() {
		_ = w.conn.Close()
	})
}

func (w *wsConn) String() string { return fmt.Sprintf("ws(%s)", w.RemoteAddr()) }
