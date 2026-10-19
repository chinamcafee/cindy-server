package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/mailer"
)

func (m *Module) envMailOptions() mailer.Options {
	return mailer.Options{
		Addr: m.cfg.SMTPAddr, From: m.cfg.SMTPFrom, Username: m.cfg.SMTPUsername,
		Password: m.cfg.SMTPPassword, TLS: m.cfg.SMTPTLS, Auth: m.cfg.SMTPAuth,
	}
}

func (m *Module) getEmailSettings(w http.ResponseWriter, r *http.Request) {
	view, err := mailer.LoadSettingsView(r.Context(), m.pool, m.box, m.envMailOptions())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, map[string]any{"settings": view})
}

func (m *Module) putEmailSettings(w http.ResponseWriter, r *http.Request) {
	var req mailer.SettingsInput
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	before, _ := mailer.LoadSettingsView(r.Context(), m.pool, m.box, m.envMailOptions())
	opts, err := mailer.SaveSettings(r.Context(), m.pool, m.box, req)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", err.Error()))
		return
	}
	view, err := mailer.LoadSettingsView(r.Context(), m.pool, m.box, m.envMailOptions())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.auditEvent(r, "email.settings.update", "app_setting", mailer.SettingsKey,
		map[string]any{"addr": before.Addr, "from": before.From, "username": before.Username, "tls": before.TLS, "auth": before.Auth},
		map[string]any{"addr": opts.Addr, "from": opts.From, "username": opts.Username, "tls": opts.TLS, "auth": opts.Auth, "passwordChanged": req.Password != ""},
		"")
	httpapi.OK(w, map[string]any{"settings": view})
}

func (m *Module) testEmailSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	to := strings.TrimSpace(req.To)
	if !strings.Contains(to, "@") || len(to) > 254 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid recipient email"))
		return
	}
	mail := mailer.New(m.envMailOptions(), m.log)
	mail.SetSettingsLoader(func(ctx context.Context) (mailer.Options, bool, error) {
		return mailer.LoadSettings(ctx, m.pool, m.box)
	})
	if err := mail.SendTest(r.Context(), to); err != nil {
		if errors.Is(err, mailer.ErrNotConfigured) {
			httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "EMAIL_NOT_CONFIGURED", "email delivery is not configured"))
			return
		}
		m.log.Warn("admin_email_test_failed", "error", err)
		httpapi.WriteError(w, httpapi.Err(http.StatusBadGateway, "EMAIL_SEND_FAILED", "failed to send test email"))
		return
	}
	m.auditEvent(r, "email.settings.test", "app_setting", mailer.SettingsKey, nil, map[string]any{"to": to}, "")
	httpapi.OK(w, map[string]any{"status": "sent"})
}
