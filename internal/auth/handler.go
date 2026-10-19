package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/mailer"
	"github.com/makecindy/cindy-server/internal/platform/obs"
	"github.com/makecindy/cindy-server/internal/platform/sms"
)

type Module struct {
	svc    *Service
	tokens *authn.Manager
	cfg    *config.Config
	audit  *audit.Logger
}

func NewModule(svc *Service, tokens *authn.Manager, cfg *config.Config, auditLog *audit.Logger) *Module {
	return &Module{svc: svc, tokens: tokens, cfg: cfg, audit: auditLog}
}

type accountCtxKey struct{}

func accountIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(accountCtxKey{}).(string)
	return v
}

func (m *Module) accountAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			httpapi.WriteError(w, httpapi.ErrUnauthorized)
			return
		}
		claims, err := m.tokens.ParseAccount(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			httpapi.WriteError(w, httpapi.ErrInvalidToken)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountCtxKey{}, claims.Subject)))
	})
}

func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		httpapi.OK(w, m.tokens.JWKS())
	})

	r.Get("/captcha/turnstile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Cindy verification</title></head>
<body style="font-family:system-ui;padding:32px"><h2>Verification is not required in this deployment</h2>
<p>You can close this window and continue.</p></body></html>`))
	})

	r.Get("/desktop/login-callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		status := r.URL.Query().Get("status")
		msg := "Login complete. You can close this window and return to Cindy."
		if status == "error" {
			msg = "Login failed. Return to Cindy and try again."
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Cindy Login</title></head>
<body style="font-family:system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#0b0d12;color:#e5e7eb">
<div style="text-align:center"><h2>Cindy</h2><p>` + msg + `</p></div></body></html>`))
	})

	r.Get("/api/auth/providers", func(w http.ResponseWriter, r *http.Request) {
		httpapi.OK(w, m.svc.Providers())
	})

	r.Post("/api/auth/discovery", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email string `json:"email"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if !strings.Contains(email, "@") || len(email) > 254 {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid email"))
			return
		}
		httpapi.OK(w, map[string]any{"methods": m.svc.DiscoveryByEmail(r.Context(), email)})
	})

	r.Post("/api/auth/sso/discovery", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Org string `json:"org"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		res, err := m.svc.SSODiscovery(r.Context(), req.Org)
		if err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "ORG_SSO_NOT_FOUND", "organization SSO not found"))
			return
		}
		httpapi.OK(w, res)
	})

	r.Post("/api/auth/email/request-code", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email        string `json:"email"`
			Locale       string `json:"locale"`
			CaptchaToken string `json:"captchaToken"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if !strings.Contains(email, "@") || len(email) > 254 {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid email"))
			return
		}
		if err := m.svc.SendEmailCode(r.Context(), email); err != nil {
			switch {
			case err.Error() == "cooldown":
				w.Header().Set("Retry-After", "60")
				httpapi.WriteError(w, httpapi.ErrRateLimited)
			case errors.Is(err, mailer.ErrNotConfigured):
				httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "EMAIL_NOT_CONFIGURED", "email verification is not configured"))
			default:
				slog.Error("email_send_failed", "request_id", obs.RequestID(r.Context()), "error", err)
				httpapi.WriteError(w, httpapi.Err(http.StatusBadGateway, "EMAIL_SEND_FAILED", "failed to send verification code"))
			}
			return
		}
		httpapi.OK(w, map[string]any{"status": "sent"})
	})

	r.Post("/api/auth/phone/request-code", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Phone        string `json:"phone"`
			Locale       string `json:"locale"`
			CaptchaToken string `json:"captchaToken"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		phone, err := NormalizePhone(req.Phone)
		if err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid phone number"))
			return
		}
		if err := m.svc.SendPhoneCode(r.Context(), phone, clientIP(r)); err != nil {
			switch {
			case errors.Is(err, ErrPhoneRateLimited):
				w.Header().Set("Retry-After", "60")
				httpapi.WriteError(w, httpapi.ErrRateLimited)
			case errors.Is(err, sms.ErrNotConfigured):
				httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SMS_NOT_CONFIGURED", "SMS verification is not configured"))
			default:
				slog.Error("sms_send_failed", "request_id", obs.RequestID(r.Context()), "phone", sms.MaskPhone(phone), "error", err)
				httpapi.WriteError(w, httpapi.Err(http.StatusBadGateway, "SMS_SEND_FAILED", "failed to send verification code"))
			}
			return
		}
		httpapi.OK(w, map[string]any{"status": "sent"})
	})

	r.Post("/api/auth/email/verify-code", m.emailVerify)
	r.Post("/api/auth/phone/verify-code", m.phoneVerify)

	r.Post("/api/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refreshToken"`
			DeviceID     string `json:"deviceId"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		res, err := m.svc.Refresh(r.Context(), req.RefreshToken, req.DeviceID)
		if err != nil {
			code := "INVALID_REFRESH_TOKEN"
			if err.Error() == "refresh token expired" {
				code = "REFRESH_TOKEN_EXPIRED"
			} else if err.Error() == "device mismatch" {
				code = "DEVICE_MISMATCH"
			}
			httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, code, err.Error()))
			return
		}
		httpapi.OK(w, res)
	})

	r.Post("/api/auth/select-account", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			LoginTicket string `json:"loginTicket"`
			AccountID   string `json:"accountId"`
			DeviceID    string `json:"deviceId"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		res, err := m.svc.SelectAccount(r.Context(), req.LoginTicket, req.AccountID, req.DeviceID, clientIP(r), r.UserAgent())
		if err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "INVALID_LOGIN_TICKET", "invalid login ticket"))
			return
		}
		httpapi.OK(w, res)
	})

	r.Post("/api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_AUTH_CODE", "authorization code flow is not enabled"))
	})

	r.Get("/api/auth/social/{provider}/authorize", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SOCIAL_PROVIDER_UNAVAILABLE", "social login is not configured"))
	})
	r.Get("/api/auth/sso/{connectionId}/authorize", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SSO_PROVIDER_UNAVAILABLE", "SSO is not configured"))
	})
	r.Post("/api/auth/social/{provider}", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SOCIAL_PROVIDER_UNAVAILABLE", "social login is not configured"))
	})
	// Binding and SSO step-up verification are provider-dependent. The routes
	// stay in the self-hosted contract so clients get structured errors rather
	// than a 404 when a future provider starts issuing these tickets.
	bindingUnavailable := func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "BINDING_UNAVAILABLE", "account binding is not configured"))
	}
	ssoVerificationUnavailable := func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SSO_VERIFICATION_UNAVAILABLE", "SSO verification is not configured"))
	}
	r.Post("/api/auth/binding/request-code", bindingUnavailable)
	r.Post("/api/auth/binding/verify", bindingUnavailable)
	r.Post("/api/auth/sso/verification/request-code", ssoVerificationUnavailable)
	r.Post("/api/auth/sso/verification/verify", ssoVerificationUnavailable)

	r.Post("/api/auth/desktop/callback/poll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PollSecret string `json:"pollSecret"`
			DeviceID   string `json:"deviceId"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		res, err := m.svc.DesktopPoll(r.Context(), req.PollSecret)
		if err != nil {
			httpapi.WriteError(w, err)
			return
		}
		httpapi.OK(w, res)
	})

	r.Post("/api/auth/account/deletion/status", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ReceiptToken string `json:"receiptToken"`
		}
		if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		res, err := m.svc.DeletionStatusByReceipt(r.Context(), req.ReceiptToken)
		if err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "ACCOUNT_DELETION_RECEIPT_INVALID", "invalid receipt"))
			return
		}
		httpapi.OK(w, res)
	})

	// Resource-token authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(httpapi.Auth(m.tokens))
		r.Get("/api/me", m.me)
		r.Patch("/api/me/profile", m.updateProfile)
		r.Post("/api/me/identity-card", m.identityCard)
		r.Post("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			p := httpapi.MustPrincipal(r.Context())
			_ = m.svc.Logout(r.Context(), p.MembershipID)
			m.audit.Log(r.Context(), audit.Event{Action: "auth.logout", TargetType: "membership", TargetID: p.MembershipID, IP: clientIP(r), UserAgent: r.UserAgent()})
			httpapi.OK(w, map[string]any{"status": "ok"})
		})
		r.Post("/api/auth/connections/token", m.connectionToken)
		r.Get("/api/auth/account/deletion", func(w http.ResponseWriter, r *http.Request) {
			p := httpapi.MustPrincipal(r.Context())
			res, err := m.svc.DeletionAvailability(r.Context(), p.MembershipID)
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			httpapi.OK(w, res)
		})
		r.Post("/api/auth/account/deletion/challenge", func(w http.ResponseWriter, r *http.Request) {
			p := httpapi.MustPrincipal(r.Context())
			res, err := m.svc.StartDeletion(r.Context(), p.AccountID, p.MembershipID)
			if err != nil {
				if errors.Is(err, sms.ErrNotConfigured) {
					httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "SMS_NOT_CONFIGURED", "SMS verification is not configured"))
					return
				}
				httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "ACCOUNT_DELETION_PENDING", "deletion already pending"))
				return
			}
			httpapi.OK(w, res)
		})
		r.Post("/api/auth/account/deletion/confirm", func(w http.ResponseWriter, r *http.Request) {
			p := httpapi.MustPrincipal(r.Context())
			var req struct {
				ChallengeID  string `json:"challengeId"`
				ReceiptToken string `json:"receiptToken"`
				Code         string `json:"code"`
				Acknowledged bool   `json:"acknowledged"`
			}
			if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			res, err := m.svc.ConfirmDeletion(r.Context(), p.AccountID, req.ChallengeID, req.ReceiptToken, req.Code)
			if err != nil {
				httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "ACCOUNT_DELETION_CHALLENGE_INVALID", err.Error()))
				return
			}
			httpapi.OK(w, res)
		})
	})

	// Account-token authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(m.accountAuth)
		r.Get("/api/auth/account", func(w http.ResponseWriter, r *http.Request) {
			members, err := m.svc.Memberships(r.Context(), accountIDFrom(r.Context()))
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			httpapi.OK(w, map[string]any{"memberships": members})
		})
		r.Post("/api/auth/account/exchange", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				MembershipID string `json:"membershipId"`
			}
			if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			res, err := m.svc.ExchangeMembership(r.Context(), accountIDFrom(r.Context()), req.MembershipID, r.Header.Get("X-Device-Id"), clientIP(r), r.UserAgent())
			if err != nil {
				httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "membership not found"))
				return
			}
			httpapi.OK(w, res)
		})
		r.Post("/api/auth/account/refresh", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				AccountRefreshToken string `json:"accountRefreshToken"`
				DeviceID            string `json:"deviceId"`
			}
			if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			token, refresh, err := m.svc.RefreshAccount(r.Context(), req.AccountRefreshToken, req.DeviceID)
			if err != nil {
				httpapi.WriteError(w, httpapi.Err(http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", err.Error()))
				return
			}
			httpapi.OK(w, map[string]any{"accountToken": token, "accountRefreshToken": refresh})
		})
		r.Post("/api/auth/account/logout", func(w http.ResponseWriter, r *http.Request) {
			_ = m.svc.LogoutAccount(r.Context(), accountIDFrom(r.Context()))
			httpapi.OK(w, map[string]any{"status": "ok"})
		})
	})

	return r
}

func (m *Module) emailVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email      string `json:"email"`
		Code       string `json:"code"`
		DeviceID   string `json:"deviceId"`
		ClientType string `json:"clientType"`
		Locale     string `json:"locale"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	res, err := m.svc.VerifyEmailCode(r.Context(), strings.ToLower(strings.TrimSpace(req.Email)), req.Code, req.DeviceID, clientIP(r), r.UserAgent())
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_CODE", err.Error()))
		return
	}
	if res.Membership != nil {
		m.audit.Log(r.Context(), audit.Event{ActorID: res.Membership.ID, ActorType: "account", Action: "auth.login.email", TargetType: "membership", TargetID: res.Membership.ID, IP: clientIP(r), UserAgent: r.UserAgent()})
	}
	httpapi.OK(w, res)
}

func (m *Module) phoneVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone      string `json:"phone"`
		Code       string `json:"code"`
		DeviceID   string `json:"deviceId"`
		ClientType string `json:"clientType"`
		Locale     string `json:"locale"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	phone, err := NormalizePhone(req.Phone)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid phone number"))
		return
	}
	res, err := m.svc.VerifyPhoneCode(r.Context(), phone, req.Code, req.DeviceID, clientIP(r), r.UserAgent())
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_CODE", err.Error()))
		return
	}
	httpapi.OK(w, res)
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	res, err := m.svc.Me(r.Context(), p.AccountID, p.MembershipID)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.OK(w, res)
}

func (m *Module) updateProfile(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	var req struct {
		DisplayName *string         `json:"displayName"`
		AvatarURL   json.RawMessage `json:"avatarUrl"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	var avatar *string
	clear := false
	if len(req.AvatarURL) > 0 {
		if string(req.AvatarURL) == "null" {
			clear = true
		} else {
			var s string
			if err := json.Unmarshal(req.AvatarURL, &s); err != nil {
				httpapi.WriteError(w, httpapi.ErrInvalidInput)
				return
			}
			avatar = &s
		}
	}
	mem, err := m.svc.UpdateProfile(r.Context(), p.MembershipID, req.DisplayName, avatar, clear)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.OK(w, map[string]any{"membership": mem})
}

func (m *Module) identityCard(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	var req struct {
		Audience string `json:"audience"`
		Purpose  string `json:"purpose"`
		Nonce    string `json:"nonce"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if len(req.Nonce) == 0 || len(req.Nonce) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid nonce"))
		return
	}
	card, exp, err := m.svc.IdentityCard(r.Context(), p.MembershipID, req.Purpose, req.Nonce)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.OK(w, map[string]any{"card": card, "expiresAt": exp.Format(time.RFC3339)})
}

func (m *Module) connectionToken(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	var req struct {
		Audience string `json:"audience"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if req.Audience == "" || len(req.Audience) > 64 {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	token, expiresIn, err := m.svc.ConnectionToken(r.Context(), p.MembershipID, req.Audience)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	httpapi.OK(w, map[string]any{"token": token, "expiresIn": expiresIn, "audience": req.Audience})
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
