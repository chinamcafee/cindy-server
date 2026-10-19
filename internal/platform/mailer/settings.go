package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
)

const SettingsKey = "email.smtp"

type SettingsInput struct {
	Addr     string `json:"addr"`
	From     string `json:"from"`
	Username string `json:"username"`
	Password string `json:"password"`
	TLS      string `json:"tls"`
	Auth     string `json:"auth"`
}

type StoredSettings struct {
	Addr               string    `json:"addr"`
	From               string    `json:"from"`
	Username           string    `json:"username"`
	TLS                string    `json:"tls"`
	Auth               string    `json:"auth"`
	PasswordCiphertext []byte    `json:"passwordCiphertext,omitempty"`
	PasswordNonce      []byte    `json:"passwordNonce,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt,omitempty"`
}

// LoadSettings reads the admin-managed SMTP settings. A missing row means the
// server keeps using the environment/default configuration.
func LoadSettings(ctx context.Context, pool *pgxpool.Pool, box *crypto.Box) (Options, bool, error) {
	if pool == nil {
		return Options{}, false, nil
	}
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, SettingsKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Options{}, false, nil
	}
	if err != nil {
		return Options{}, false, err
	}
	var stored StoredSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return Options{}, false, fmt.Errorf("decode email settings: %w", err)
	}
	password, err := decryptPassword(box, stored)
	if err != nil {
		return Options{}, false, err
	}
	return Options{
		Addr: stored.Addr, From: stored.From, Username: stored.Username,
		Password: password, TLS: stored.TLS, Auth: stored.Auth,
	}, true, nil
}

func SaveSettings(ctx context.Context, pool *pgxpool.Pool, box *crypto.Box, input SettingsInput) (Options, error) {
	if pool == nil {
		return Options{}, errors.New("database is not configured")
	}
	addr := strings.TrimSpace(input.Addr)
	from := strings.TrimSpace(input.From)
	username := strings.TrimSpace(input.Username)
	tlsMode := strings.ToLower(strings.TrimSpace(input.TLS))
	authMode := strings.ToLower(strings.TrimSpace(input.Auth))
	if tlsMode == "" {
		tlsMode = "auto"
	}
	if authMode == "" {
		authMode = "auto"
	}
	if err := validateAddr(addr); err != nil {
		return Options{}, err
	}
	if from == "" {
		return Options{}, errors.New("from address is required")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return Options{}, errors.New("from address is invalid")
	}
	switch tlsMode {
	case "auto", "starttls", "ssl", "none":
	default:
		return Options{}, errors.New("tls must be auto, starttls, ssl or none")
	}
	switch authMode {
	case "auto", "plain", "login", "none":
	default:
		return Options{}, errors.New("auth must be auto, plain, login or none")
	}

	existing, err := loadStored(ctx, pool)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Options{}, err
	}
	password := input.Password
	if password == "" && existing != nil {
		if password, err = decryptPassword(box, *existing); err != nil {
			return Options{}, err
		}
	}
	stored := StoredSettings{
		Addr: addr, From: from, Username: username, TLS: tlsMode, Auth: authMode,
		UpdatedAt: time.Now().UTC(),
	}
	if password != "" {
		if box == nil {
			return Options{}, errors.New("master key is required to store the SMTP password")
		}
		ciphertext, nonce, err := box.Encrypt([]byte(password), SettingsKey)
		if err != nil {
			return Options{}, err
		}
		stored.PasswordCiphertext, stored.PasswordNonce = ciphertext, nonce
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return Options{}, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		SettingsKey, raw); err != nil {
		return Options{}, err
	}
	return Options{Addr: addr, From: from, Username: username, Password: password, TLS: tlsMode, Auth: authMode}, nil
}

func loadStored(ctx context.Context, pool *pgxpool.Pool) (*StoredSettings, error) {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, SettingsKey).Scan(&raw); err != nil {
		return nil, err
	}
	var stored StoredSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

func decryptPassword(box *crypto.Box, stored StoredSettings) (string, error) {
	if len(stored.PasswordCiphertext) == 0 && len(stored.PasswordNonce) == 0 {
		return "", nil
	}
	if box == nil {
		return "", errors.New("master key is missing; cannot decrypt SMTP password")
	}
	plain, err := box.Decrypt(stored.PasswordCiphertext, stored.PasswordNonce, SettingsKey)
	if err != nil {
		return "", fmt.Errorf("decrypt SMTP password: %w", err)
	}
	return string(plain), nil
}

func validateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("SMTP server must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("SMTP port must be 1..65535")
	}
	return nil
}

// SettingsView is the admin-safe projection; it never contains the password.
type SettingsView struct {
	Source      string     `json:"source"`
	Addr        string     `json:"addr"`
	From        string     `json:"from"`
	Username    string     `json:"username"`
	TLS         string     `json:"tls"`
	Auth        string     `json:"auth"`
	PasswordSet bool       `json:"passwordSet"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
}

func LoadSettingsView(ctx context.Context, pool *pgxpool.Pool, _ *crypto.Box, fallback Options) (SettingsView, error) {
	stored, err := loadStored(ctx, pool)
	if errors.Is(err, pgx.ErrNoRows) {
		return SettingsView{
			Source: "env", Addr: fallback.Addr, From: fallback.From, Username: fallback.Username,
			TLS: fallback.TLS, Auth: fallback.Auth, PasswordSet: fallback.Password != "",
		}, nil
	}
	if err != nil {
		return SettingsView{}, err
	}
	// The view never needs the plaintext password. Do not block the admin page
	// when an old ciphertext was written with a different master key; the
	// operator can enter a new authorization code and save over it.
	updated := stored.UpdatedAt
	return SettingsView{
		Source: "database", Addr: stored.Addr, From: stored.From, Username: stored.Username,
		TLS: stored.TLS, Auth: stored.Auth,
		PasswordSet: len(stored.PasswordCiphertext) > 0, UpdatedAt: &updated,
	}, nil
}
