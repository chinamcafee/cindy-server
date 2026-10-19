package devicelink

import (
	"context"
	"encoding/json"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/cache"
)

const presenceTTL = 90 * time.Second

// presenceEntry mirrors the Redis hash value for
// cindy:{env}:dl:presence:{accountId}.
type presenceEntry struct {
	Online               bool      `json:"online"`
	Busy                 bool      `json:"busy"`
	RemoteControlEnabled bool      `json:"remoteControlEnabled"`
	LastSeenAt           time.Time `json:"lastSeenAt"`
	NodeID               string    `json:"nodeId"`
	ConnID               string    `json:"connId"`
}

type routeEntry struct {
	AccountID string `json:"accountId"`
	NodeID    string `json:"nodeId"`
	ConnID    string `json:"connId"`
}

func (m *Module) presenceKey(accountID string) string {
	return cache.Key(m.env(), "dl", "presence", accountID)
}

func (m *Module) routeKey(deviceID string) string {
	return cache.Key(m.env(), "dl", "route", deviceID)
}

func (m *Module) notifyKey(deviceID string) string {
	return cache.Key(m.env(), "dl", "notify", deviceID)
}

func (c *Conn) presenceEntry(now time.Time) presenceEntry {
	return presenceEntry{
		Online:               true,
		Busy:                 c.busy.Load(),
		RemoteControlEnabled: c.remoteControl.Load(),
		LastSeenAt:           now.UTC(),
		NodeID:               c.hub.nodeID,
		ConnID:               c.connID,
	}
}

// writePresence writes/refreshes the account presence hash and the device
// route key. Both entries expire after presenceTTL and are refreshed by ping.
func (m *Module) writePresence(ctx context.Context, c *Conn) {
	if m.redis == nil {
		return
	}
	entry := c.presenceEntry(time.Now())
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	pipe := m.redis.Pipeline()
	pipe.HSet(ctx, m.presenceKey(c.accountID), c.deviceID, raw)
	pipe.Expire(ctx, m.presenceKey(c.accountID), presenceTTL)
	route, _ := json.Marshal(routeEntry{AccountID: c.accountID, NodeID: c.hub.nodeID, ConnID: c.connID})
	pipe.Set(ctx, m.routeKey(c.deviceID), route, presenceTTL)
	_, _ = pipe.Exec(ctx)
}

func (m *Module) refreshPresence(ctx context.Context, c *Conn) {
	m.writePresence(ctx, c)
}

// clearPresence removes the entries only when they still belong to connID.
func (m *Module) clearPresence(ctx context.Context, accountID, deviceID, connID string) {
	if m.redis == nil {
		return
	}
	presenceKey := m.presenceKey(accountID)
	if raw, err := m.redis.HGet(ctx, presenceKey, deviceID).Bytes(); err == nil {
		var entry presenceEntry
		if json.Unmarshal(raw, &entry) == nil && entry.ConnID == connID {
			m.redis.HDel(ctx, presenceKey, deviceID)
		}
	}
	routeKey := m.routeKey(deviceID)
	if raw, err := m.redis.Get(ctx, routeKey).Bytes(); err == nil {
		var entry routeEntry
		if json.Unmarshal(raw, &entry) == nil && entry.ConnID == connID {
			m.redis.Del(ctx, routeKey)
		}
	}
}

// removeDevicePresence is used by the REST delete endpoint, where no local
// connection ID is known.
func (m *Module) removeDevicePresence(ctx context.Context, accountID, deviceID string) {
	if m.redis == nil {
		return
	}
	presenceKey := m.presenceKey(accountID)
	m.redis.HDel(ctx, presenceKey, deviceID)
	m.redis.Del(ctx, m.routeKey(deviceID))
}

func (m *Module) getPresence(ctx context.Context, accountID string) map[string]presenceEntry {
	out := make(map[string]presenceEntry)
	if m.redis == nil {
		return out
	}
	values, err := m.redis.HGetAll(ctx, m.presenceKey(accountID)).Result()
	if err != nil {
		return out
	}
	for deviceID, raw := range values {
		var entry presenceEntry
		if json.Unmarshal([]byte(raw), &entry) == nil {
			out[deviceID] = entry
		}
	}
	return out
}
