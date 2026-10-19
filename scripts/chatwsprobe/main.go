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
	a1 := createActor(token, "cw-a", "Chat WS A")
	a2 := createActor(token, "cw-b", "Chat WS B")
	room := post(token, "/v1/conversations", map[string]any{
		"operationId": fmt.Sprintf("cw-room-%d", time.Now().UnixNano()),
		"kind":        "group", "name": "Chat WS Probe", "participants": []string{a1, a2}, "sourceId": fmt.Sprintf("cw-src-%d", time.Now().UnixNano()),
	})["id"].(string)

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:19828/v1/ws", header)
	if err != nil {
		fail("ws dial: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"type": "subscribe", "scopes": []string{room}, "after": map[string]string{}}); err != nil {
		fail("subscribe: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	msg := post(token, "/v1/conversations/"+room+"/messages", map[string]any{
		"operationId": fmt.Sprintf("cw-msg-%d", time.Now().UnixNano()),
		"content":     []map[string]any{{"type": "text", "text": "ws hello"}},
	})
	fmt.Println("message sent:", msg["id"], "seq", msg["seq"])

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			fail("read event: %v", err)
		}
		if frame["type"] == "message.created" && frame["roomId"] == room {
			fmt.Println("received message.created event; chat WSS OK")
			return
		}
	}
}

func login() (string, error) {
	email := fmt.Sprintf("chatws-%d@cindy.local", time.Now().UnixNano())
	if _, err := postResp("/api/auth/email/request-code", map[string]string{"email": email}); err != nil {
		return "", err
	}
	out, err := postResp("/api/auth/email/verify-code", map[string]any{
		"email": email, "code": "123456", "deviceId": "chatws", "clientType": "desktop",
	})
	if err != nil {
		return "", err
	}
	token, _ := out["accessToken"].(string)
	if token == "" {
		return "", fmt.Errorf("no token")
	}
	return token, nil
}

func createActor(token, externalID, name string) string {
	out := post(token, "/v1/actors", map[string]any{
		"operationId": "cw-actor-" + externalID, "kind": "bot", "externalId": externalID, "name": name,
	})
	return out["id"].(string)
}

func post(token, path string, payload any) map[string]any {
	out, err := postResp(path, payload, token)
	if err != nil {
		fail("%s: %v", path, err)
	}
	return out
}

func postResp(path string, payload any, token ...string) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if len(token) > 0 {
		req.Header.Set("Authorization", "Bearer "+token[0])
	}
	resp, err := http.DefaultClient.Do(req)
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
