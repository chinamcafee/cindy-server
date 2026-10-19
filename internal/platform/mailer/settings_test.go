package mailer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makecindy/cindy-server/internal/platform/crypto"
)

func TestSettingsRoundTrip(t *testing.T) {
	dsn := os.Getenv("CINDY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CINDY_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box := crypto.NewBox("mailer-settings-test-master")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, SettingsKey)
	})

	opts, err := SaveSettings(ctx, pool, box, SettingsInput{
		Addr: "smtp.example.com:465", From: "no-reply@example.com",
		Username: "mailer", Password: "secret-code", TLS: "ssl", Auth: "plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Password != "secret-code" {
		t.Fatal("save did not return the password")
	}
	loaded, ok, err := LoadSettings(ctx, pool, box)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.Addr != opts.Addr || loaded.From != opts.From || loaded.Username != opts.Username ||
		loaded.Password != "secret-code" || loaded.TLS != "ssl" || loaded.Auth != "plain" {
		t.Fatalf("unexpected loaded settings: %+v", loaded)
	}
	view, err := LoadSettingsView(ctx, pool, box, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "database" || !view.PasswordSet || view.UpdatedAt == nil {
		t.Fatalf("unexpected view: %+v", view)
	}

	// A blank password on update keeps the stored one.
	if _, err := SaveSettings(ctx, pool, box, SettingsInput{
		Addr: "smtp.example.com:587", From: "no-reply@example.com",
		Username: "mailer", TLS: "starttls", Auth: "login",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = LoadSettings(ctx, pool, box)
	if err != nil || loaded.Password != "secret-code" {
		t.Fatalf("password not preserved: %+v err=%v", loaded, err)
	}
}
