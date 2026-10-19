package devicelink

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// handleWS authenticates the handshake with the same Bearer access token used
// by the REST API, then hijacks the connection and runs the relay pump.
//
// ProviderShareGuest authentication is not implemented yet; such requests get
// 401 as allowed by the contract.
func (m *Module) handleWS(w http.ResponseWriter, r *http.Request) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	claims, err := m.tokens.ParseAccess(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
	if err != nil {
		if errors.Is(err, authn.ErrExpiredToken) {
			httpapi.WriteError(w, httpapi.ErrTokenExpired)
		} else {
			httpapi.WriteError(w, httpapi.ErrInvalidToken)
		}
		return
	}

	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusUpgradeRequired, "UPGRADE_REQUIRED", err.Error()))
		return
	}

	c := &Conn{
		m:         m,
		hub:       m.hub,
		ws:        ws,
		accountID: claims.Subject,
		connID:    uuid.NewString(),
		out:       make(chan []byte, writeQueueSize),
		done:      make(chan struct{}),
	}
	c.run()
}
