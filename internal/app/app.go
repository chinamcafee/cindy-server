package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/admin"
	"github.com/makecindy/cindy-server/internal/auth"
	"github.com/makecindy/cindy-server/internal/chat"
	"github.com/makecindy/cindy-server/internal/devicelink"
	"github.com/makecindy/cindy-server/internal/githubissue"
	"github.com/makecindy/cindy-server/internal/heartbeat"
	"github.com/makecindy/cindy-server/internal/hooks"
	"github.com/makecindy/cindy-server/internal/modelaccess"
	"github.com/makecindy/cindy-server/internal/oss"
	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/cache"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/idempotency"
	"github.com/makecindy/cindy-server/internal/platform/mailer"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
	"github.com/makecindy/cindy-server/internal/platform/obs"
	"github.com/makecindy/cindy-server/internal/platform/sms"
	"github.com/makecindy/cindy-server/internal/plugin"
	"github.com/makecindy/cindy-server/internal/skillhub"
	"github.com/makecindy/cindy-server/internal/updates"
)

type App struct {
	Config  *config.Config
	Logger  *slog.Logger
	DB      *db.DB
	Redis   *redis.Client
	S3      *objectstore.Store
	Tokens  *authn.Manager
	Idem    *idempotency.Store
	Audit   *audit.Logger
	Mail    *mailer.Mailer
	Box     *crypto.Box
	Metrics *obs.Metrics
	Router  http.Handler
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	rdb, err := cache.New(ctx, cfg.RedisURL)
	if err != nil {
		database.Close()
		return nil, err
	}
	s3, err := objectstore.New(ctx, cfg)
	if err != nil {
		database.Close()
		_ = rdb.Close()
		return nil, err
	}
	tokens, err := authn.NewManager(cfg.JWTPrivateKeyFile, "cindy", cfg.AccessTTL)
	if err != nil {
		return nil, err
	}
	auditLog := audit.New(database, cfg.Region, logger)
	mail := mailer.New(mailer.Options{
		Addr: cfg.SMTPAddr, From: cfg.SMTPFrom, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
		TLS: cfg.SMTPTLS, Auth: cfg.SMTPAuth, FixedCode: cfg.EmailDevFixedCode,
	}, logger)
	box := crypto.NewBox(cfg.MasterKey)
	mail.SetSettingsLoader(func(ctx context.Context) (mailer.Options, bool, error) {
		return mailer.LoadSettings(ctx, database.Pool, box)
	})
	metrics := obs.NewMetrics()
	idem := idempotency.New(rdb)

	smsSender, err := sms.New(cfg, logger)
	if err != nil {
		return nil, err
	}
	authSvc := auth.NewService(database, rdb, tokens, mail, smsSender, cfg, auditLog)
	authModule := auth.NewModule(authSvc, tokens, cfg, auditLog)
	chatModule := chat.New(chat.Deps{DB: database, Redis: rdb, Tokens: tokens, Idem: idem, Audit: auditLog, S3: s3, Config: cfg})
	dlModule := devicelink.New(devicelink.Deps{DB: database, Redis: rdb, Tokens: tokens, S3: s3, Config: cfg, Audit: auditLog})
	modelModule := modelaccess.New(modelaccess.Deps{DB: database, Redis: rdb, Tokens: tokens, S3: s3, Config: cfg, Audit: auditLog, Box: box})
	adminModule := admin.New(admin.Deps{DB: database, Redis: rdb, Config: cfg, Audit: auditLog, Logger: logger, Box: box})
	ossModule := oss.New(oss.Deps{DB: database, Store: s3, Tokens: tokens, Config: cfg, Logger: logger, Audit: auditLog})
	updateModule := updates.New(updates.Deps{DB: database, Config: cfg, Logger: logger})
	heartbeatModule := heartbeat.New(heartbeat.Deps{DB: database, Redis: rdb, Config: cfg, Logger: logger})
	githubModule := githubissue.New(githubissue.Deps{DB: database, Config: cfg, Tokens: tokens, Logger: logger, Audit: auditLog})
	pluginModule := plugin.New(plugin.Deps{DB: database, Redis: rdb, Tokens: tokens, S3: s3, Config: cfg, Audit: auditLog})
	skillModule := skillhub.New(skillhub.Deps{DB: database, Redis: rdb, Tokens: tokens, S3: s3, Config: cfg, Audit: auditLog})
	hooksModule := hooks.New(hooks.Deps{DB: database, Config: cfg, Tokens: tokens, Logger: logger})

	r := chi.NewRouter()
	r.Use(obs.RequestIDMiddleware)
	r.Use(middleware.RealIP)
	r.Use(func(next http.Handler) http.Handler { return obs.Recoverer(logger, next) })
	r.Use(func(next http.Handler) http.Handler { return obs.AccessLog(logger, metrics, next) })
	compress := middleware.Compress(5)
	r.Use(func(next http.Handler) http.Handler {
		compressed := compress(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	})
	r.Use(rateLimitFor(rdb))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpapi.OK(w, map[string]any{"ok": true, "region": cfg.Region})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": err.Error()})
			return
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "redis": err.Error()})
			return
		}
		httpapi.OK(w, map[string]any{"ok": true, "region": cfg.Region})
	})
	r.Handle("/metrics", metrics.Handler())

	groups := []routeGroup{
		{prefix: "/admin/api/v1/", handler: adminModule.Routes()},
		{prefix: "/api/auth/", handler: authModule.Routes()},
		{prefix: "/api/me", handler: authModule.Routes()},
		{prefix: "/.well-known/", handler: authModule.Routes()},
		{prefix: "/captcha/", handler: authModule.Routes()},
		{prefix: "/desktop/", handler: authModule.Routes()},
		{prefix: "/api/model-catalog/", handler: modelModule.Routes()},
		{prefix: "/api/model-access/", handler: modelModule.Routes()},
		{prefix: "/api/billing/", handler: modelModule.Routes()},
		{prefix: "/api/voice/", handler: modelModule.Routes()},
		{prefix: "/api/user/", handler: modelModule.Routes()},
		{prefix: "/api/integrations/", handler: modelModule.Routes()},
		{prefix: "/api/device-link/", handler: dlModule.Routes()},
		{prefix: "/api/plugins/", handler: pluginModule.Routes()},
		{prefix: "/api/plugins", handler: pluginModule.Routes()},
		{prefix: "/api/publisher/", handler: pluginModule.Routes()},
		{prefix: "/api/skills-hub/", handler: skillModule.Routes()},
		{prefix: "/api/oss/", handler: ossModule.Routes()},
		{prefix: "/api/mobile-update/", handler: updateModule.Routes()},
		{prefix: "/endpoint.json", handler: updateModule.Routes()},
		{prefix: "/api/endpoint.json", handler: updateModule.Routes()},
		{prefix: "/api/release-notes/", handler: updateModule.Routes()},
		{prefix: "/api/announcements", handler: updateModule.Routes()},
		{prefix: "/api/github/", handler: githubModule.Routes()},
		{prefix: "/v1/messages", handler: modelModule.Routes()},
		{prefix: "/v1/responses", handler: modelModule.Routes()},
		{prefix: "/v1/chat/completions", handler: modelModule.Routes()},
		{prefix: "/v1/embeddings", handler: modelModule.Routes()},
		{prefix: "/v1/", handler: chatModule.Routes()},
		{prefix: "/heartbeat", handler: heartbeatModule.Routes()},
		{prefix: "/hook/", handler: hooksModule.Routes()},
		{prefix: "/ws", handler: hooksModule.Routes()},
	}
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, group := range groups {
			if strings.HasPrefix(r.URL.Path, group.prefix) {
				group.handler.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))

	return &App{
		Config: cfg, Logger: logger, DB: database, Redis: rdb, S3: s3, Tokens: tokens,
		Idem: idem, Audit: auditLog, Mail: mail, Box: box, Metrics: metrics, Router: r,
	}, nil
}

func (a *App) Close() {
	if a.Redis != nil {
		_ = a.Redis.Close()
	}
	if a.DB != nil {
		a.DB.Close()
	}
}

type routeGroup struct {
	prefix  string
	handler http.Handler
}

func rateLimitFor(rdb *redis.Client) func(http.Handler) http.Handler {
	logged := httpapi.RateLimit(rdb, httpapi.RateLimitConfig{Limit: 600, Window: time.Minute, Prefix: "global"})
	return logged
}
