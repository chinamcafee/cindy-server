package hooks

import "sync"

// hub is the in-process connection registry keyed by account and device id.
// Device slots are first-wins: a second live connection for the same key is
// rejected with close code 4000 by the caller.
type hub struct {
	mu    sync.Mutex
	conns map[string]*session
}

func newHub() *hub {
	return &hub{conns: make(map[string]*session)}
}

func hubKey(accountID, deviceID string) string {
	return accountID + "\x00" + deviceID
}

// Register claims the device slot. It returns false when another live session
// already holds it.
func (h *hub) Register(accountID, deviceID string, s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := hubKey(accountID, deviceID)
	if existing, ok := h.conns[key]; ok && existing != nil && !existing.isClosed() {
		return false
	}
	h.conns[key] = s
	return true
}

// Unregister releases the slot only if it still belongs to s.
func (h *hub) Unregister(accountID, deviceID string, s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := hubKey(accountID, deviceID)
	if h.conns[key] == s {
		delete(h.conns, key)
	}
}
