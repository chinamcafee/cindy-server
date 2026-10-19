package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/push"
)

const (
	handshakeTimeout = 15 * time.Second
	readIdleTimeout  = 120 * time.Second
	writeQueueSize   = 256
	writeQueueBytes  = 4 << 20

	maxMessageBytes      = 2 << 20 // protocol payload limit, JSON frames only
	notifyLimitPerMinute = 10
)

// Conn is one authenticated relay connection.
type Conn struct {
	m   *Module
	hub *Hub
	ws  *wsConn

	accountID string
	deviceID  string
	connID    string

	deviceName   string
	platform     string
	appVersion   string
	deviceInfo   json.RawMessage
	capabilities []string

	remoteControl atomic.Bool
	busy          atomic.Bool

	out      chan []byte
	outBytes atomic.Int64
	done     chan struct{}

	closeOnce sync.Once
	helloDone bool
}

func (c *Conn) run() {
	go c.writePump()
	c.readLoop()
	c.cleanup()
}

func (c *Conn) writePump() {
	for {
		select {
		case frame := <-c.out:
			c.outBytes.Add(-int64(len(frame)))
			if err := c.ws.writeFrame(opText, frame); err != nil {
				c.close(closeInternal, "write failed")
				return
			}
		case <-c.done:
			return
		}
	}
}

// enqueue never blocks; a full/slow client is disconnected with 1013.
func (c *Conn) enqueue(frame []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	if c.outBytes.Load()+int64(len(frame)) > writeQueueBytes {
		c.close(closeTryAgain, "relay congested")
		return false
	}
	select {
	case c.out <- frame:
		c.outBytes.Add(int64(len(frame)))
		return true
	default:
		c.close(closeTryAgain, "relay congested")
		return false
	}
}

func (c *Conn) close(code int, reason string) {
	c.closeOnce.Do(func() {
		c.ws.Close(code, reason)
		close(c.done)
	})
}

func (c *Conn) readLoop() {
	_ = c.ws.SetReadDeadline(time.Now().Add(handshakeTimeout))

	for {
		frame, err := c.ws.ReadMessage()
		if err != nil {
			if !c.helloDone {
				c.close(closeBadFrame, "hello required within 15s")
			}
			switch {
			case errors.Is(err, errWSProtocol):
				c.close(closeBadFrame, "invalid frame")
			case errors.Is(err, errWSTooLarge):
				c.close(closeTooLarge, "frame too large")
			}
			return
		}
		if len(frame) > maxMessageBytes {
			c.close(closeBadFrame, "frame too large")
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(readIdleTimeout))

		if !c.helloDone {
			if !c.handleHello(frame) {
				return
			}
			continue
		}
		c.handleEnvelope(frame)
	}
}

func (c *Conn) handleHello(frame []byte) bool {
	var env Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		c.close(closeBadFrame, "invalid frame")
		return false
	}
	if env.V != protocolVersion {
		c.close(closeBadFrame, "protocol version mismatch")
		return false
	}
	if env.Kind != kindHello {
		c.close(closeBadFrame, "hello required")
		return false
	}
	var hello HelloPayload
	if len(env.Payload) == 0 {
		c.close(closeBadFrame, "missing hello payload")
		return false
	}
	if err := json.Unmarshal(env.Payload, &hello); err != nil {
		c.close(closeBadFrame, "invalid hello payload")
		return false
	}

	hello.DeviceName = strings.TrimSpace(hello.DeviceName)
	hello.Platform = strings.TrimSpace(hello.Platform)
	hello.AppVersion = strings.TrimSpace(hello.AppVersion)
	if hello.DeviceName == "" || len(hello.DeviceName) > 128 ||
		len(hello.Platform) > 32 || len(hello.AppVersion) > 64 {
		c.close(closeBadFrame, "invalid hello fields")
		return false
	}
	info := hello.DeviceInfo
	if len(info) == 0 || string(info) == "null" || !json.Valid(info) {
		info = json.RawMessage(`{}`)
	}
	if len(hello.Capabilities) > 32 {
		hello.Capabilities = hello.Capabilities[:32]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	deviceID, removed, err := c.m.registerDevice(ctx, c.accountID, &hello, info)
	cancel()
	if err != nil {
		c.m.log.Error("devicelink_register_device_failed", "account_id", c.accountID, "error", err)
		c.close(closeInternal, "register failed")
		return false
	}
	if removed {
		c.close(closeRemoved, "device removed")
		return false
	}

	c.deviceID = deviceID
	c.deviceName = hello.DeviceName
	c.platform = hello.Platform
	c.appVersion = hello.AppVersion
	c.deviceInfo = info
	c.capabilities = append([]string(nil), hello.Capabilities...)
	c.remoteControl.Store(hello.RemoteControlEnabled)
	c.busy.Store(hello.Busy)

	old, err := c.hub.register(c)
	if err != nil {
		c.close(closeTooManyConns, "connection limit reached")
		return false
	}
	c.helloDone = true
	if old != nil {
		old.close(closeReplaced, "replaced by a newer connection")
	}

	ack := Envelope{
		V:    protocolVersion,
		Kind: kindHelloAck,
		ID:   env.ID,
		Payload: mustJSON(HelloAckPayload{
			ServerProtocolVersion: protocolVersion,
			DeviceID:              c.deviceID,
			UserID:                c.accountID,
			Capabilities:          serverCapabilities,
		}),
	}
	if !c.enqueue(mustJSON(ack)) {
		return false
	}

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	c.m.writePresence(ctx, c)
	cancel()
	c.broadcastPresence(true)
	return true
}

func (c *Conn) handleEnvelope(frame []byte) {
	var env Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		c.close(closeBadFrame, "invalid frame")
		return
	}
	if env.V != protocolVersion {
		c.close(closeBadFrame, "protocol version mismatch")
		return
	}

	switch env.Kind {
	case kindPing:
		c.refreshAsync()
		c.enqueue(mustJSON(Envelope{
			V:       protocolVersion,
			Kind:    kindPong,
			ID:      env.ID,
			Payload: env.Payload,
		}))
	case kindPong:
		c.refreshAsync()
	case kindPresenceSet:
		c.handlePresenceSet(&env)
	case kindNotify:
		c.handleNotify(&env)
	case kindHello, kindHelloAck, kindPresenceChanged, kindRelayError:
		// Duplicate hello and server-originated kinds are ignored.
	default:
		// Unknown kinds are silently dropped so capabilities can evolve.
		if routedKinds[env.Kind] {
			c.routeEnvelope(&env)
		}
	}
}

func (c *Conn) refreshAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.m.refreshPresence(ctx, c)
	}()
}

func (c *Conn) handlePresenceSet(env *Envelope) {
	var p struct {
		Busy                 *bool   `json:"busy"`
		RemoteControlEnabled *bool   `json:"remoteControlEnabled"`
		DeviceName           *string `json:"deviceName"`
	}
	if len(env.Payload) == 0 || json.Unmarshal(env.Payload, &p) != nil {
		c.sendRelayError(env.ID, env.Dst, relayBadRequest, "invalid presence-set payload")
		return
	}
	if p.Busy == nil && p.RemoteControlEnabled == nil && p.DeviceName == nil {
		return
	}
	if p.DeviceName != nil {
		name := strings.TrimSpace(*p.DeviceName)
		if name == "" || len(name) > 128 {
			c.sendRelayError(env.ID, env.Dst, relayBadRequest, "invalid deviceName")
			return
		}
		c.deviceName = name
	}
	if p.Busy != nil {
		c.busy.Store(*p.Busy)
	}
	if p.RemoteControlEnabled != nil {
		c.remoteControl.Store(*p.RemoteControlEnabled)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.m.db.Pool.Exec(ctx, `
		UPDATE dl_devices
		SET busy = $3, remote_control_enabled = $4,
		    name = CASE WHEN $5 = '' THEN name ELSE $5 END,
		    last_seen_at = now(), updated_at = now()
		WHERE id = $1 AND account_id = $2::uuid AND removed_at IS NULL`,
		c.deviceID, c.accountID, c.busy.Load(), c.remoteControl.Load(), c.deviceName,
	); err != nil {
		c.m.log.Warn("devicelink_presence_update_failed", "device_id", c.deviceID, "error", err)
	}

	c.m.writePresence(ctx, c)
	c.broadcastPresence(true)
}

func (c *Conn) handleNotify(env *Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	allowed := c.notifyAllowed(ctx)
	cancel()
	if !allowed {
		c.sendRelayError(env.ID, env.Dst, relayRateLimited, "notify rate limit exceeded")
		return
	}

	var p struct {
		TargetDeviceID string `json:"targetDeviceId"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		CollapseID     string `json:"collapseId"`
		Sender         struct {
			DisplayName string `json:"displayName"`
			AvatarBytes []byte `json:"avatarBytes"`
		} `json:"sender"`
	}
	if len(env.Payload) == 0 || json.Unmarshal(env.Payload, &p) != nil {
		c.sendRelayError(env.ID, env.Dst, relayBadRequest, "invalid notify payload")
		return
	}
	if len(p.Title) > 128 || len(p.Body) > 1024 || len(p.CollapseID) > 128 ||
		len(p.Sender.DisplayName) > 64 || len(p.Sender.AvatarBytes) > 2048 {
		c.sendRelayError(env.ID, env.Dst, relayBadRequest, "notify field too large")
		return
	}

	// Notify content is never persisted and never relayed.
	if c.m.cfg != nil && c.m.cfg.IsDev() {
		c.m.log.Info("devicelink_notify",
			"account_id", c.accountID,
			"device_id", c.deviceID,
			"target_device_id", p.TargetDeviceID,
			"title_len", len(p.Title),
			"body_len", len(p.Body),
			"collapse_id", p.CollapseID,
		)
	}
	if c.m.push == nil || !c.m.push.Available() {
		return
	}
	target := strings.TrimSpace(p.TargetDeviceID)
	if target == "" {
		target = c.deviceID
	}
	c.deliverPush(target, p.Title, p.Body, p.CollapseID)
}

// deliverPush fans a notify out to the target device's registered APNs tokens.
// Tokens rejected as unregistered are removed; other failures are logged and
// retried by the next notify.
func (c *Conn) deliverPush(targetDeviceID, title, body, collapseID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rows, err := c.m.db.Pool.Query(ctx, `
		SELECT token, platform, provider, app_variant, apns_env
		FROM dl_push_tokens
		WHERE account_id = $1::uuid AND device_id = $2`, c.accountID, targetDeviceID)
	if err != nil {
		c.m.log.Warn("devicelink_push_lookup_failed", "device_id", targetDeviceID, "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var token, platform, provider, appVariant, apnsEnv string
		if err := rows.Scan(&token, &platform, &provider, &appVariant, &apnsEnv); err != nil {
			c.m.log.Warn("devicelink_push_scan_failed", "error", err)
			return
		}
		if provider != "apns" {
			continue
		}
		err := c.m.push.Send(ctx, push.Message{
			Token: token, Platform: platform, Provider: provider, AppVariant: appVariant,
			APNsEnv: apnsEnv, Title: title, Body: body, CollapseID: collapseID,
		})
		if err == nil {
			continue
		}
		var apiErr *push.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusGone || apiErr.Reason == "BadDeviceToken" || apiErr.Reason == "Unregistered") {
			_, _ = c.m.db.Pool.Exec(ctx, `DELETE FROM dl_push_tokens WHERE token = $1 AND account_id = $2::uuid`, token, c.accountID)
			continue
		}
		c.m.log.Warn("devicelink_push_failed", "device_id", targetDeviceID, "error", err)
	}
	if err := rows.Err(); err != nil {
		c.m.log.Warn("devicelink_push_rows_failed", "error", err)
	}
}

func (c *Conn) notifyAllowed(ctx context.Context) bool {
	if c.m.redis == nil {
		return true
	}
	key := c.m.notifyKey(c.deviceID)
	count, err := c.m.redis.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	if count == 1 {
		c.m.redis.Expire(ctx, key, time.Minute)
	}
	return count <= notifyLimitPerMinute
}

func (c *Conn) routeEnvelope(env *Envelope) {
	dst := strings.TrimSpace(env.Dst)
	if dst == "" {
		c.sendRelayError(env.ID, "", relayBadRequest, "missing dst")
		return
	}
	if dst == c.deviceID {
		c.sendRelayError(env.ID, dst, relayBadRequest, "cannot route to self")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := *env
	out.V = protocolVersion
	out.Src = c.deviceID // always overwrite client supplied src
	if out.ID == "" {
		out.ID = uuid.NewString()
	}

	target := c.hub.route(c.accountID, dst)
	if target == nil {
		// Multi-instance hook: a Redis-backed RelayBus would publish here; the
		// owning node either delivers the frame or answers DEVICE_OFFLINE.
		_ = c.hub.bus.PublishEnvelope(ctx, c.accountID, mustJSON(out))
		c.sendRelayError(env.ID, dst, relayDeviceOffline, "device offline")
		return
	}

	if controlKinds[env.Kind] && !target.remoteControl.Load() {
		c.sendRelayError(env.ID, dst, relayRemoteDisabled, "remote control disabled on target")
		return
	}
	if failure := c.prepareScopes(ctx, env); failure != nil {
		c.sendRelayError(env.ID, dst, failure.code, failure.message)
		return
	}

	if !target.enqueue(mustJSON(out)) {
		c.sendRelayError(env.ID, dst, relayInternal, "target unavailable")
	}
}

type relayFailure struct {
	code    string
	message string
}

func badRequest(message string) *relayFailure {
	return &relayFailure{code: relayBadRequest, message: message}
}

func offlineFailure(message string) *relayFailure {
	return &relayFailure{code: relayDeviceOffline, message: message}
}

// prepareScopes validates sharedTask/providerShare scopes and rewrites source.
func (c *Conn) prepareScopes(ctx context.Context, env *Envelope) *relayFailure {
	if env.SharedTask != nil && env.ProviderShare != nil {
		return badRequest("only one scope is allowed")
	}
	if env.SharedTask != nil {
		scope := env.SharedTask
		if scope.SharedTaskID == "" || scope.Target.Role == "" {
			return badRequest("invalid sharedTask scope")
		}
		role, memberID, hostDeviceID, status, err := c.m.sharedTaskRole(ctx, scope.SharedTaskID, c.accountID)
		if err != nil || status != "active" {
			return offlineFailure("shared task unavailable")
		}
		switch scope.Target.Role {
		case "host":
			if role == "guest" && env.Dst != hostDeviceID {
				return offlineFailure("shared task target unavailable")
			}
		case "guest":
			if scope.Target.MemberID == "" {
				return badRequest("missing target memberId")
			}
			ok, err := c.m.sharedTaskGuestActive(ctx, scope.SharedTaskID, scope.Target.MemberID)
			if err != nil || !ok {
				return offlineFailure("shared task target unavailable")
			}
		default:
			return badRequest("invalid sharedTask target role")
		}
		scope.Source = &ScopeSource{Role: role, MemberID: memberID}
	}
	if env.ProviderShare != nil {
		scope := env.ProviderShare
		if scope.ShareID == "" || scope.Target.Role == "" {
			return badRequest("invalid providerShare scope")
		}
		role, memberID, hostDeviceID, status, err := c.m.providerShareRole(ctx, scope.ShareID, c.accountID)
		if err != nil || status != "active" {
			return offlineFailure("provider share unavailable")
		}
		switch scope.Target.Role {
		case "host":
			if role == "guest" && env.Dst != hostDeviceID {
				return offlineFailure("provider share target unavailable")
			}
		case "guest":
			if scope.Target.MemberID == "" {
				return badRequest("missing target memberId")
			}
			ok, err := c.m.providerShareMemberActive(ctx, scope.ShareID, scope.Target.MemberID)
			if err != nil || !ok {
				return offlineFailure("provider share target unavailable")
			}
		default:
			return badRequest("invalid providerShare target role")
		}
		scope.Source = &ScopeSource{Role: role, MemberID: memberID}
	}
	return nil
}

func (c *Conn) sendRelayError(id, dst, code, message string) {
	payload := mustJSON(RelayErrorPayload{Code: code, Message: message, Dst: dst})
	c.enqueue(mustJSON(Envelope{
		V:       protocolVersion,
		Kind:    kindRelayError,
		ID:      id,
		Src:     "server",
		Payload: payload,
	}))
}

func (c *Conn) broadcastPresence(online bool) {
	payload := PresencePayload{
		DeviceID:             c.deviceID,
		DeviceName:           c.deviceName,
		Platform:             c.platform,
		AppVersion:           c.appVersion,
		Online:               online,
		Busy:                 c.busy.Load(),
		RemoteControlEnabled: c.remoteControl.Load(),
		LastSeenAt:           time.Now().UTC(),
	}
	frame := mustJSON(Envelope{
		V:       protocolVersion,
		Kind:    kindPresenceChanged,
		Src:     "server",
		Payload: mustJSON(payload),
	})
	c.hub.broadcast(c.accountID, frame)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.hub.bus.PublishPresence(ctx, c.accountID, frame)
}

func (c *Conn) cleanup() {
	if c.deviceID == "" {
		c.close(closeNormal, "")
		return
	}

	current := c.hub.unregister(c)
	c.close(closeNormal, "bye")
	if !current {
		// A newer connection replaced this one; do not touch its state.
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.m.clearPresence(ctx, c.accountID, c.deviceID, c.connID)
	if _, err := c.m.db.Pool.Exec(ctx,
		`UPDATE dl_devices SET last_seen_at = now(), updated_at = now() WHERE id = $1 AND account_id = $2::uuid`,
		c.deviceID, c.accountID,
	); err != nil && !errors.Is(err, context.Canceled) {
		c.m.log.Warn("devicelink_last_seen_failed", "device_id", c.deviceID, "error", err)
	}
	c.broadcastPresence(false)
}
