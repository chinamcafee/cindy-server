package modelaccess

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/idempotency"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Tokens *authn.Manager
	S3     *objectstore.Store
	Config *config.Config
	Audit  *audit.Logger
	Box    *crypto.Box
}

type Module struct {
	db     *db.DB
	rdb    *redis.Client
	tokens *authn.Manager
	s3     *objectstore.Store
	cfg    *config.Config
	audit  *audit.Logger
	box    *crypto.Box
	idem   *idempotency.Store
	now    func() time.Time
}

func New(d Deps) *Module {
	m := &Module{
		db:     d.DB,
		rdb:    d.Redis,
		tokens: d.Tokens,
		s3:     d.S3,
		cfg:    d.Config,
		audit:  d.Audit,
		box:    d.Box,
		now:    func() time.Time { return time.Now().UTC() },
	}
	if d.Redis != nil {
		m.idem = idempotency.New(d.Redis)
	}
	return m
}

func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/api/model-catalog/catalog", m.catalog)
	r.Get("/api/model-catalog/ready", m.catalogReady)
	r.Get("/api/integrations/oauth/{slug}/bounce", m.oauthBounce)
	r.Post("/v1/messages", m.gatewayProxy)
	r.Post("/v1/responses", m.gatewayProxy)
	r.Post("/v1/chat/completions", m.gatewayProxy)
	r.Post("/v1/embeddings", m.gatewayProxy)

	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.tokens))
		r.Use(noStore)

		r.Get("/api/model-access/models", m.listModels)
		r.Get("/api/model-access/credentials", m.getCredentials)
		r.Post("/api/model-access/credentials/rotate", m.rotateCredentials)
		r.Get("/api/model-access/invocation-guide", m.invocationGuide)
		r.Get("/api/model-access/invocation-guides", m.invocationGuides)
		r.Get("/api/model-access/quota", m.quota)
		r.Get("/api/model-access/balance", m.balance)
		r.Get("/api/model-access/credit-usage", m.creditUsage)
		r.Get("/api/model-access/byok/providers", m.byokProviders)
		r.Get("/api/model-access/byok/credentials", m.byokCredentials)
		r.Post("/api/model-access/byok/credentials", m.byokCreateCredential)
		r.Delete("/api/model-access/byok/credentials/{id}", m.byokRevokeCredential)

		r.Get("/api/billing/catalog", m.billingCatalog)
		r.Get("/api/billing/orders", m.billingListOrders)
		r.Post("/api/billing/credit-topup/orders", m.createTopupOrder)
		r.Get("/api/billing/credit-topup/orders/{id}", m.getOrder)
		r.Get("/api/billing/orders/{id}", m.getOrder)
		r.Post("/api/billing/orders/{id}/{action}", m.billingOrderAction)
		r.Get("/api/billing/subscriptions", m.listSubscriptions)
		r.Post("/api/billing/subscriptions", m.createSubscription)
		r.Get("/api/billing/subscription", m.currentSubscription)
		r.Delete("/api/billing/subscription", m.cancelCurrentSubscription)
		r.Post("/api/billing/subscription/resume", m.resumeSubscription)
		r.Post("/api/billing/subscriptions/purchases/{id}/refresh", m.refreshSubscriptionPurchase)
		r.Post("/api/billing/subscription/plan-changes/{id}/confirm", m.planChangeAction)
		r.Post("/api/billing/subscription/plan-changes/{id}/refresh", m.planChangeAction)
		r.Delete("/api/billing/subscription/plan-changes/{id}", m.planChangeAction)
		r.Post("/api/billing/subscription/plan-change-quotes", m.planChangeQuote)
		r.Post("/api/billing/plan-change-quotes", m.planChangeQuote)
		r.Post("/api/billing/subscription/portal", m.billingPortal)
		r.Post("/api/billing/portal", m.billingPortal)

		r.Post("/api/voice/sessions", m.createVoiceSession)
		r.Post("/api/voice/refine", m.voiceRefine)
		r.Post("/api/voice/sessions/{sessionId}/refine", m.voiceRefine)
		r.Post("/api/voice/sessions/{sessionId}/refine-warmup", m.voiceRefineWarmup)

		r.Get("/api/user/feature-flags", m.featureFlags)
		r.Post("/api/integrations/{slug}/oauth/exchange", m.oauthExchange)
		r.Post("/api/integrations/{slug}/oauth/refresh", m.oauthRefresh)
		// Inference gateway is authenticated by its own gateway key, not by a
		// resource access token. It is mounted outside this auth group.
	})

	return r
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (m *Module) auditEvent(r *http.Request, action, targetType, targetID string, after any) {
	if m.audit == nil || m.db == nil {
		return
	}
	m.audit.Log(r.Context(), audit.Event{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		After:      after,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
	})
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}
