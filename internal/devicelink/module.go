package devicelink

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
	"github.com/makecindy/cindy-server/internal/platform/push"
)

// Deps are the platform dependencies required by the Device-Link module.
type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Tokens *authn.Manager
	S3     *objectstore.Store
	Config *config.Config
	Audit  *audit.Logger
}

// Module implements the Device-Link relay: device directory, media tickets,
// ICE credentials, shared tasks, provider shares, device identities and the
// WebSocket relay itself.
type Module struct {
	db     *db.DB
	redis  *redis.Client
	tokens *authn.Manager
	s3     *objectstore.Store
	cfg    *config.Config
	audit  *audit.Logger
	push   push.Sender

	hub    *Hub
	nodeID string
	log    *slog.Logger
}

func New(d Deps) *Module {
	m := &Module{
		db:     d.DB,
		redis:  d.Redis,
		tokens: d.Tokens,
		s3:     d.S3,
		cfg:    d.Config,
		audit:  d.Audit,
		push:   push.New(d.Config, slog.Default()),
		nodeID: uuid.NewString(),
		log:    slog.Default(),
	}
	m.hub = NewHub(m.nodeID, localRelayBus{})
	return m
}

// Routes mounts every Device-Link endpoint. REST calls require a Bearer access
// token; the WebSocket endpoint performs the same check before upgrading.
func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/device-link", func(r chi.Router) {
		r.Get("/ws", m.handleWS)

		r.Group(func(r chi.Router) {
			r.Use(httpapi.Auth(m.tokens))

			r.Get("/devices", m.listDevices)
			r.Patch("/devices/{id}", m.renameDevice)
			r.Delete("/devices/{id}", m.removeDevice)

			r.Put("/push-token", m.putPushToken)
			r.Delete("/push-token", m.deletePushToken)

			r.Get("/ice-servers", m.iceServers)

			r.Post("/media/presign-put", m.presignPut)
			r.Post("/media/presign-get", m.presignGet)
			r.Delete("/media", m.deleteMedia)

			r.Get("/shared-tasks", m.listSharedTasks)
			r.Post("/shared-tasks", m.createSharedTask)
			r.Post("/shared-tasks/join", m.joinSharedTask)
			r.Get("/shared-tasks/{id}", m.getSharedTask)
			r.Post("/shared-tasks/{id}/invite", m.inviteSharedTask)
			r.Post("/shared-tasks/{id}/invites", m.inviteSharedTask)
			r.Get("/shared-tasks/{id}/members", m.sharedTaskMembersHandler)
			r.Delete("/shared-tasks/{id}/members/{memberId}", m.removeSharedTaskMember)
			r.Post("/shared-tasks/{id}/members/{memberId}/remove", m.removeSharedTaskMember)
			r.Post("/shared-tasks/{id}/leave", m.leaveSharedTask)
			r.Post("/shared-tasks/{id}/close", m.closeSharedTask)

			r.Get("/provider-shares", m.listProviderShares)
			r.Post("/provider-shares", m.createProviderShare)
			r.Post("/provider-shares/links", m.createProviderShareLink)
			r.Post("/provider-shares/preview", m.previewProviderShareLink)
			r.Post("/provider-shares/requests", m.createProviderShareRequestByLink)
			r.Get("/provider-shares/requests/{requestId}", m.providerShareRequestState)
			r.Post("/provider-shares/requests/{requestId}/withdraw", m.withdrawProviderShareRequest)
			r.Post("/provider-shares/requests/{requestId}/approve", func(w http.ResponseWriter, r *http.Request) { m.decideProviderShareRequestAlias(w, r, true) })
			r.Post("/provider-shares/requests/{requestId}/reject", func(w http.ResponseWriter, r *http.Request) { m.decideProviderShareRequestAlias(w, r, false) })
			r.Post("/provider-shares/members/{memberId}/{action}", m.setProviderShareMemberState)
			r.Post("/provider-shares/received/{memberId}/leave", m.leaveReceivedProviderShare)
			r.Get("/provider-shares/received", m.listReceivedProviderShares)
			r.Get("/provider-shares/x/*", m.providerShareCrossRegion)
			r.Post("/provider-shares/x/*", m.providerShareCrossRegion)
			r.Get("/provider-shares/{id}", m.getProviderShare)
			r.Patch("/provider-shares/{id}", m.updateProviderShare)
			r.Delete("/provider-shares/{id}", m.deleteProviderShare)
			r.Post("/provider-shares/{id}/requests", m.createProviderShareRequest)
			r.Post("/provider-shares/{id}/requests/{requestId}/approve", m.approveProviderShareRequest)
			r.Post("/provider-shares/{id}/requests/{requestId}/reject", m.rejectProviderShareRequest)

			r.Post("/identities/challenges", m.createIdentityChallenge)
			r.Post("/identities", m.registerIdentity)
			r.Get("/identities/{id}", m.lookupIdentity)
			r.Post("/identities/relay-challenges", m.createRelayChallenge)
			r.Post("/identities/push-challenges", m.createPushChallenge)
		})
	})
	return r
}

func (m *Module) env() string {
	if m.cfg != nil && m.cfg.Env != "" {
		return m.cfg.Env
	}
	return "dev"
}

func accountID(r *http.Request) string {
	return httpapi.MustPrincipal(r.Context()).AccountID
}

func decodeBody(r *http.Request, dst any) error {
	return httpapi.DecodeJSONLoose(nil, r, dst)
}

// deviceIDFromRequest returns the caller's device hint, used to compute isSelf
// and as a default hostDeviceId. It is advisory only.
func deviceIDFromRequest(r *http.Request, fallback string) string {
	if v := strings.TrimSpace(r.Header.Get("X-Device-Id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("deviceId")); v != "" {
		return v
	}
	return fallback
}

func (m *Module) internalError(w http.ResponseWriter, op string, err error) {
	m.log.Error("devicelink_internal_error", "op", op, "error", err)
	httpapi.WriteError(w, httpapi.ErrInternal)
}

func (m *Module) notFound(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.ErrNotFound)
}

func (m *Module) auditEvent(ctx context.Context, action, targetType, targetID string, after any) {
	if m.audit == nil {
		return
	}
	m.audit.Log(ctx, audit.Event{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		After:      after,
	})
}

func validUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}
