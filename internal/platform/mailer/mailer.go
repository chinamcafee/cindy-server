package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/wneessen/go-mail"
)

var ErrNotConfigured = errors.New("email delivery is not configured")

type Options struct {
	Addr      string
	From      string
	Username  string
	Password  string
	TLS       string // auto | starttls | ssl | none
	Auth      string // auto | plain | login | none
	FixedCode string
}

type SettingsLoader func(context.Context) (Options, bool, error)

type Mailer struct {
	opts   Options
	log    *slog.Logger
	loader SettingsLoader
}

func New(opts Options, logger *slog.Logger) *Mailer {
	return &Mailer{opts: opts, log: logger}
}

// SetSettingsLoader makes the mailer read admin-managed settings on every
// send, so saving in the console takes effect without restarting the server.
func (m *Mailer) SetSettingsLoader(loader SettingsLoader) { m.loader = loader }

func (m *Mailer) effectiveOptions(ctx context.Context) (Options, error) {
	if m.loader == nil {
		return m.opts, nil
	}
	loaded, ok, err := m.loader(ctx)
	if err != nil {
		return Options{}, err
	}
	if !ok {
		return m.opts, nil
	}
	loaded.FixedCode = m.opts.FixedCode
	return loaded, nil
}

func (m *Mailer) DevCode() string { return m.opts.FixedCode }

func (m *Mailer) Available() bool {
	if m.loader != nil {
		return true
	}
	return m.opts.FixedCode != "" ||
		(strings.TrimSpace(m.opts.Addr) != "" && strings.TrimSpace(m.opts.From) != "")
}

func (m *Mailer) SendCode(ctx context.Context, to, code string) error {
	opts, err := m.effectiveOptions(ctx)
	if err != nil {
		return err
	}
	if opts.FixedCode != "" {
		m.log.Info("verification_code_dev", "to", to, "code", opts.FixedCode)
		return nil
	}
	return m.send(ctx, opts, to, "Your Cindy verification code",
		fmt.Sprintf("Your verification code is %s. It expires in 10 minutes.", code))
}

// SendTest bypasses the dev fixed-code shortcut and always performs a real SMTP
// delivery, so the admin console can verify credentials immediately.
func (m *Mailer) SendTest(ctx context.Context, to string) error {
	opts, err := m.effectiveOptions(ctx)
	if err != nil {
		return err
	}
	return m.send(ctx, opts, to, "Cindy SMTP test",
		"This is a test email from the Cindy admin console. Your SMTP settings are working.")
}

func (m *Mailer) send(ctx context.Context, opts Options, to, subject, body string) error {
	if strings.TrimSpace(opts.Addr) == "" || strings.TrimSpace(opts.From) == "" {
		return ErrNotConfigured
	}
	msg := mail.NewMsg()
	if err := msg.From(strings.TrimSpace(opts.From)); err != nil {
		return err
	}
	if err := msg.To(to); err != nil {
		return err
	}
	msg.Subject(subject)
	msg.SetBodyString(mail.TypeTextPlain, body)

	addr := strings.TrimSpace(opts.Addr)
	host, port, splitErr := net.SplitHostPort(addr)
	clientOpts := m.clientOptions(opts)
	if splitErr == nil {
		if portNum, convErr := strconv.Atoi(port); convErr == nil {
			clientOpts = append(clientOpts, mail.WithPort(portNum))
		}
	} else {
		host = addr
	}
	client, err := mail.NewClient(host, clientOpts...)
	if err != nil {
		return err
	}
	return client.DialAndSendWithContext(ctx, msg)
}

func (m *Mailer) clientOptions(settings Options) []mail.Option {
	opts := make([]mail.Option, 0, 6)
	username := strings.TrimSpace(settings.Username)
	auth := strings.ToLower(strings.TrimSpace(settings.Auth))
	if auth == "" || auth == "auto" {
		if username != "" {
			auth = "plain"
		} else {
			auth = "none"
		}
	}
	switch auth {
	case "plain":
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain))
	case "login":
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthLogin))
	}
	if username != "" {
		opts = append(opts, mail.WithUsername(username), mail.WithPassword(settings.Password))
	}

	tlsMode := strings.ToLower(strings.TrimSpace(settings.TLS))
	if tlsMode == "" || tlsMode == "auto" {
		tlsMode = autoTLSMode(settings.Addr)
	}
	switch tlsMode {
	case "ssl":
		opts = append(opts, mail.WithSSL())
	case "none":
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	default:
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	return opts
}

func autoTLSMode(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "starttls"
	}
	switch port {
	case "465":
		return "ssl"
	case "1025", "25":
		return "none"
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return "none"
	}
	return "starttls"
}
