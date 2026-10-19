package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/authn"
)

type principalKey struct{}

type Principal struct {
	AccountID    string
	MembershipID string
	Region       string
}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func MustPrincipal(ctx context.Context) Principal {
	p, _ := PrincipalFrom(ctx)
	return p
}

func Auth(m *authn.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				WriteError(w, ErrUnauthorized)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			claims, err := m.ParseAccess(token)
			if err != nil {
				if errors.Is(err, authn.ErrExpiredToken) {
					WriteError(w, ErrTokenExpired)
				} else {
					WriteError(w, ErrInvalidToken)
				}
				return
			}
			p := Principal{AccountID: claims.Subject, MembershipID: claims.MembershipID, Region: claims.Region}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func OptionalAuth(m *authn.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if strings.HasPrefix(header, "Bearer ") {
				token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
				if claims, err := m.ParseAccess(token); err == nil {
					p := Principal{AccountID: claims.Subject, MembershipID: claims.MembershipID, Region: claims.Region}
					r = r.WithContext(WithPrincipal(r.Context(), p))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type RateLimitConfig struct {
	Limit  int64
	Window time.Duration
	Prefix string
}

func RateLimit(rdb *redis.Client, cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rdb == nil || cfg.Limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			subject := ""
			if p, ok := PrincipalFrom(r.Context()); ok {
				subject = p.AccountID
			}
			if subject == "" {
				subject = clientIP(r)
			}
			window := time.Now().Unix() / int64(cfg.Window.Seconds())
			key := fmt.Sprintf("cindy:rl:%s:%s:%d", cfg.Prefix, subject, window)
			ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
			defer cancel()
			count, err := rdb.Incr(ctx, key).Result()
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if count == 1 {
				rdb.Expire(ctx, key, cfg.Window)
			}
			remaining := cfg.Limit - count
			if remaining < 0 {
				remaining = 0
			}
			w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(cfg.Limit, 10))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
			reset := (window + 1) * int64(cfg.Window.Seconds())
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			if count > cfg.Limit {
				retry := time.Until(time.Unix(reset, 0))
				if retry < time.Second {
					retry = time.Second
				}
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				WriteError(w, ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		return strings.TrimSpace(parts[0])
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
