package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/obs"
)

type Logger struct {
	db     *db.DB
	region string
	log    *slog.Logger
}

type Event struct {
	ActorType  string
	ActorID    string
	IP         string
	UserAgent  string
	Action     string
	TargetType string
	TargetID   string
	Before     any
	After      any
	Result     string
	Reason     string
}

func New(database *db.DB, region string, logger *slog.Logger) *Logger {
	return &Logger{db: database, region: region, log: logger}
}

func (l *Logger) Log(ctx context.Context, e Event) {
	if e.ActorType == "" {
		e.ActorType = "system"
	}
	if e.Result == "" {
		e.Result = "ok"
	}
	if p, ok := httpapi.PrincipalFrom(ctx); ok && e.ActorID == "" {
		e.ActorType = "account"
		e.ActorID = p.AccountID
	}
	_, err := l.db.Pool.Exec(ctx, `
		INSERT INTO audit_events (occurred_at, region, actor_type, actor_id, actor_ip, user_agent, action, target_type, target_id, before, after, request_id, result, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		time.Now().UTC(), l.region, e.ActorType, nullString(e.ActorID), nullString(e.IP), nullString(e.UserAgent),
		e.Action, nullString(e.TargetType), nullString(e.TargetID), e.Before, e.After, nullString(obs.RequestID(ctx)), e.Result, nullString(e.Reason),
	)
	if err != nil {
		l.log.Error("audit_write_failed", "action", e.Action, "error", err)
	}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
