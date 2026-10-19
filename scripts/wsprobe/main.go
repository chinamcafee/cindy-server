package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

const base = "http://127.0.0.1:19828"

func main() {
	token, err := login()
	if err != nil {
		fail("login: %v", err)
	}
	a, err := dial(token, "Probe A", true)
	if err != nil {
		fail("dial A: %v", err)
	}
	defer a.Close()
	b, err := dial(token, "Probe B", true)
	if err != nil {
		fail("dial B: %v", err)
	}
	defer b.Close()

	idA, err := handshake(a, "Probe A")
	if err != nil {
		fail("hello A: %v", err)
	}
	idB, err := handshake(b, "Probe B")
	if err != nil {
		fail("hello B: %v", err)
	}
	fmt.Println("device ids:", idA, idB)

	if err := a.WriteJSON(map[string]any{
		"v": 1, "kind": "link-open", "id": "open-1", "dst": idB,
		"payload": map[string]any{"controllerName": "Probe A", "protocolVersion": 1, "appVersion": "probe"},
	}); err != nil {
		fail("link-open write: %v", err)
	}
	env, err := readKind(b, "link-open", 5*time.Second)
	if err != nil {
		fail("link-open receive: %v", err)
	}
	if env["src"] != idA {
		fail("unexpected link-open: %v", env)
	}
	fmt.Println("link-open routed to target, src rewritten:", env["src"])

	if err := b.WriteJSON(map[string]any{
		"v": 1, "kind": "link-accept", "id": "open-1", "dst": idA,
		"payload": map[string]any{"appVersion": "probe", "allowlistHash": "deadbeef", "capabilities": []string{"reliable-transport-v1"}},
	}); err != nil {
		fail("link-accept write: %v", err)
	}
	if env, err = readKind(a, "link-accept", 5*time.Second); err != nil {
		fail("link-accept receive: %v %v", env, err)
	}
	fmt.Println("link-accept routed back")

	if err := a.WriteJSON(map[string]any{
		"v": 1, "kind": "invoke", "id": "invoke-1", "dst": idB,
		"payload": map[string]any{"channel": "device-link:ping", "args": map[string]any{}},
	}); err != nil {
		fail("invoke write: %v", err)
	}
	if env, err = readKind(b, "invoke", 5*time.Second); err != nil {
		fail("invoke receive: %v %v", env, err)
	}
	fmt.Println("invoke routed")

	if err := b.WriteJSON(map[string]any{
		"v": 1, "kind": "invoke-result", "id": "invoke-1", "dst": idA,
		"payload": map[string]any{"ok": true, "result": map[string]any{"pong": true}},
	}); err != nil {
		fail("invoke-result write: %v", err)
	}
	if env, err = readKind(a, "invoke-result", 5*time.Second); err != nil {
		fail("invoke-result receive: %v %v", env, err)
	}
	fmt.Println("invoke-result routed; device-link relay OK")
}

func login() (string, error) {
	email := fmt.Sprintf("wsprobe-%d@cindy.local", time.Now().UnixNano())
	if err := post("/api/auth/email/request-code", map[string]string{"email": email}); err != nil {
		return "", err
	}
	body, err := postResp("/api/auth/email/verify-code", map[string]any{
		"email": email, "code": "123456", "deviceId": "ws-probe", "clientType": "desktop",
	})
	if err != nil {
		return "", err
	}
	token, _ := body["accessToken"].(string)
	if token == "" {
		return "", fmt.Errorf("no access token: %v", body)
	}
	return token, nil
}

func dial(token, name string, remote bool) (*websocket.Conn, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:19828/api/device-link/ws", header)
	return conn, err
}

func handshake(conn *websocket.Conn, name string) (string, error) {
	if err := conn.WriteJSON(map[string]any{
		"v": 1, "kind": "hello",
		"payload": map[string]any{
			"deviceName": name, "platform": "darwin", "appVersion": "probe",
			"remoteControlEnabled": true, "busy": false,
			"deviceInfo":   map[string]any{"osVersion": "probe"},
			"capabilities": []string{"shared-task-v2", "provider-share-v1"},
		},
	}); err != nil {
		return "", err
	}
	env, err := readEnvelope(conn, 5*time.Second)
	if err != nil {
		return "", err
	}
	if env["kind"] != "hello-ack" {
		return "", fmt.Errorf("expected hello-ack, got %v", env)
	}
	payload, _ := env["payload"].(map[string]any)
	id, _ := payload["deviceId"].(string)
	if id == "" {
		return "", fmt.Errorf("missing deviceId: %v", env)
	}
	return id, nil
}

func readKind(conn *websocket.Conn, kind string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		env, err := readEnvelope(conn, time.Until(deadline))
		if err != nil {
			return nil, err
		}
		if env["kind"] == kind {
			return env, nil
		}
	}
	return nil, fmt.Errorf("timed out waiting for %s", kind)
}

func readEnvelope(conn *websocket.Conn, timeout time.Duration) (map[string]any, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var env map[string]any
	if err := conn.ReadJSON(&env); err != nil {
		return nil, err
	}
	return env, nil
}

func post(path string, payload any) error {
	_, err := postResp(path, payload)
	return err
}

func postResp(path string, payload any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %v", resp.Status, out)
	}
	return out, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
