package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	sessionCookieName  = "admin_session"
	sessionIdleTTL     = 30 * time.Minute
	sessionAbsoluteTTL = 12 * time.Hour
	loginFailureLimit  = 10
	loginFailureWindow = 15 * time.Minute
)

type adminCtxKey struct{}

type adminSession struct {
	SessionID string
	AdminID   string
	Email     string
	Role      string
	CSRF      string
}

func adminFrom(ctx context.Context) adminSession {
	v, _ := ctx.Value(adminCtxKey{}).(adminSession)
	return v
}

type adminJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s adminSession) json() adminJSON {
	return adminJSON{ID: s.AdminID, Email: s.Email, Role: s.Role}
}

// session authenticates the admin cookie, enforces the CSRF header for unsafe
// methods and slides the idle expiry.
func (m *Module) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			m.clearSessionCookie(w)
			httpapi.WriteError(w, httpapi.ErrUnauthorized)
			return
		}
		var s adminSession
		var expiresAt, createdAt time.Time
		err = m.pool.QueryRow(r.Context(), `
			SELECT s.id::text, s.csrf_token, s.expires_at, s.created_at,
			       a.id::text, a.email::text, a.role
			FROM admin_sessions s
			JOIN admin_users a ON a.id = s.admin_id
			WHERE s.token_hash = $1
			  AND s.revoked_at IS NULL
			  AND s.expires_at > now()
			  AND a.status = 'active'`,
			crypto.HashToken(cookie.Value),
		).Scan(&s.SessionID, &s.CSRF, &expiresAt, &createdAt, &s.AdminID, &s.Email, &s.Role)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				m.log.Error("admin_session_lookup_failed", "error", err)
			}
			m.clearSessionCookie(w)
			httpapi.WriteError(w, httpapi.ErrUnauthorized)
			return
		}
		now := time.Now()
		if now.After(createdAt.Add(sessionAbsoluteTTL)) {
			m.clearSessionCookie(w)
			httpapi.WriteError(w, httpapi.ErrUnauthorized)
			return
		}
		if !isSafeMethod(r.Method) {
			token := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
			if token == "" || !crypto.ConstantTimeEqual(token, s.CSRF) {
				httpapi.WriteError(w, httpapi.Err(http.StatusForbidden, "CSRF_INVALID", "missing or invalid CSRF token"))
				return
			}
		}
		if expiresAt.Before(now.Add(sessionIdleTTL / 2)) {
			_, _ = m.pool.Exec(r.Context(),
				`UPDATE admin_sessions SET expires_at = now() + interval '30 minutes' WHERE id = $1::uuid`,
				s.SessionID)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminCtxKey{}, s)))
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	email := normalizeEmail(req.Email)
	ip := clientIP(r)
	if email == "" || req.Password == "" || len(email) > 254 || len(req.Password) > 1024 {
		httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password"))
		return
	}
	if m.loginThrottled(r.Context(), ip) {
		httpapi.WriteError(w, httpapi.ErrRateLimited)
		return
	}

	var adminID, passwordHash, totpSecret, role, status string
	err := m.pool.QueryRow(r.Context(), `
		SELECT id::text, password_hash, COALESCE(totp_secret,''), role, status
		FROM admin_users WHERE email = $1`, email).
		Scan(&adminID, &passwordHash, &totpSecret, &role, &status)
	if err != nil || status != "active" || !crypto.VerifyPassword(req.Password, passwordHash) {
		m.recordLoginFailure(r.Context(), ip)
		m.auditLogin(r, "admin.login.failed", adminID, "failed", "invalid credentials")
		httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password"))
		return
	}
	if totpSecret != "" {
		if strings.TrimSpace(req.TOTP) == "" {
			m.auditLogin(r, "admin.login.failed", adminID, "failed", "totp required")
			httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "TOTP_REQUIRED", "two-factor authentication code required"))
			return
		}
		if !validateTOTP(totpSecret, req.TOTP) {
			m.recordLoginFailure(r.Context(), ip)
			m.auditLogin(r, "admin.login.failed", adminID, "failed", "invalid totp")
			httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "INVALID_TOTP", "invalid two-factor authentication code"))
			return
		}
	}

	token := crypto.RandomToken(32)
	csrf := crypto.RandomToken(32)
	var sessionID string
	if err := m.pool.QueryRow(r.Context(), `
		INSERT INTO admin_sessions (admin_id, token_hash, csrf_token, expires_at, ip, user_agent)
		VALUES ($1::uuid, $2, $3, now() + interval '30 minutes', $4, $5)
		RETURNING id::text`,
		adminID, crypto.HashToken(token), csrf, clientIP(r), r.UserAgent()).Scan(&sessionID); err != nil {
		m.log.Error("admin_login_session_failed", "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	_, _ = m.pool.Exec(r.Context(), `
		UPDATE admin_users SET last_login_at = now(), updated_at = now() WHERE id = $1::uuid`, adminID)
	m.clearLoginFailures(r.Context(), ip)
	m.setSessionCookie(w, token)

	s := adminSession{SessionID: sessionID, AdminID: adminID, Email: email, Role: role, CSRF: csrf}
	if m.audit != nil {
		m.audit.Log(r.Context(), audit.Event{
			ActorType: "admin", ActorID: adminID, IP: clientIP(r), UserAgent: r.UserAgent(),
			Action: "admin.login", TargetType: "admin", TargetID: adminID,
		})
	}
	httpapi.OK(w, map[string]any{"admin": s.json(), "csrfToken": csrf})
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) {
	s := adminFrom(r.Context())
	httpapi.OK(w, map[string]any{"admin": s.json(), "csrfToken": s.CSRF})
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) {
	s := adminFrom(r.Context())
	if _, err := m.pool.Exec(r.Context(),
		`UPDATE admin_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, s.SessionID); err != nil {
		m.log.Error("admin_logout_failed", "error", err)
	}
	m.clearSessionCookie(w)
	m.auditEvent(r, "admin.logout", "admin", s.AdminID, nil, nil, "")
	httpapi.OK(w, map[string]any{"status": "ok"})
}

func (m *Module) isDev() bool {
	return m.cfg != nil && m.cfg.IsDev()
}

func (m *Module) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !m.isDev(),
		MaxAge:   int(sessionAbsoluteTTL.Seconds()),
	})
}

func (m *Module) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !m.isDev(),
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (m *Module) auditLogin(r *http.Request, action, adminID, result, reason string) {
	if m.audit == nil {
		return
	}
	m.audit.Log(r.Context(), audit.Event{
		ActorType: "admin", ActorID: adminID, IP: clientIP(r), UserAgent: r.UserAgent(),
		Action: action, TargetType: "admin", TargetID: adminID, Result: result, Reason: reason,
	})
}

func (m *Module) loginFailureKey(ip string) string {
	return "cindy:" + m.cfg.Env + ":admin:login_fail:" + ip
}

func (m *Module) loginThrottled(ctx context.Context, ip string) bool {
	if m.rdb == nil {
		return false
	}
	n, err := m.rdb.Get(ctx, m.loginFailureKey(ip)).Int()
	if err != nil {
		return false
	}
	return n >= loginFailureLimit
}

func (m *Module) recordLoginFailure(ctx context.Context, ip string) {
	if m.rdb == nil {
		return
	}
	key := m.loginFailureKey(ip)
	n, err := m.rdb.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	if n == 1 {
		m.rdb.Expire(ctx, key, loginFailureWindow)
	}
}

func (m *Module) clearLoginFailures(ctx context.Context, ip string) {
	if m.rdb == nil {
		return
	}
	m.rdb.Del(ctx, m.loginFailureKey(ip))
}
