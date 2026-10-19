package devicelink

import (
	"context"
	"errors"
	"sync"
)

const maxConnsPerAccount = 20

var errTooManyConns = errors.New("account connection limit reached")

// RelayBus is the seam for multi-instance fan-out. The single-instance build
// uses localRelayBus; a Redis pub/sub implementation can be plugged in later
// without touching the hub or connection code.
type RelayBus interface {
	PublishEnvelope(ctx context.Context, accountID string, frame []byte) error
	PublishPresence(ctx context.Context, accountID string, frame []byte) error
}

type localRelayBus struct{}

func (localRelayBus) PublishEnvelope(context.Context, string, []byte) error { return nil }
func (localRelayBus) PublishPresence(context.Context, string, []byte) error { return nil }

// Hub owns the local connection table: accountID -> deviceID -> *Conn.
type Hub struct {
	mu       sync.RWMutex
	accounts map[string]map[string]*Conn
	nodeID   string
	bus      RelayBus
}

func NewHub(nodeID string, bus RelayBus) *Hub {
	if bus == nil {
		bus = localRelayBus{}
	}
	return &Hub{
		accounts: make(map[string]map[string]*Conn),
		nodeID:   nodeID,
		bus:      bus,
	}
}

// register inserts c and returns the previous connection for the same device,
// which the caller must close with 4409.
func (h *Hub) register(c *Conn) (*Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	account := h.accounts[c.accountID]
	if account == nil {
		account = make(map[string]*Conn)
		h.accounts[c.accountID] = account
	}
	old := account[c.deviceID]
	if old == nil && len(account) >= maxConnsPerAccount {
		return nil, errTooManyConns
	}
	account[c.deviceID] = c
	return old, nil
}

// unregister removes c only if it is still the current connection, so a
// replaced connection can never tear down the new one's presence.
func (h *Hub) unregister(c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	account := h.accounts[c.accountID]
	if account == nil || account[c.deviceID] != c {
		return false
	}
	delete(account, c.deviceID)
	if len(account) == 0 {
		delete(h.accounts, c.accountID)
	}
	return true
}

func (h *Hub) route(accountID, deviceID string) *Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if account := h.accounts[accountID]; account != nil {
		return account[deviceID]
	}
	return nil
}

func (h *Hub) broadcast(accountID string, frame []byte) {
	h.mu.RLock()
	account := h.accounts[accountID]
	conns := make([]*Conn, 0, len(account))
	for _, c := range account {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, c := range conns {
		c.enqueue(frame)
	}
}

// kick closes the local connection for a device (used when the device is
// removed through the REST API).
func (h *Hub) kick(accountID, deviceID string, code int, reason string) {
	if c := h.route(accountID, deviceID); c != nil {
		c.close(code, reason)
	}
}

func (h *Hub) count(accountID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.accounts[accountID])
}
