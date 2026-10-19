package authn

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSignAndParse(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(filepath.Join(dir, "key.pem"), "cindy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.SignAccess("acc-1", "mem-1", "global")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "acc-1" || claims.MembershipID != "mem-1" || claims.Region != "global" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, err := m.ParseAccount(token); err == nil {
		t.Fatal("access token must not parse as account token")
	}
}

func TestAccountToken(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "key.pem"), "cindy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.SignAccount("acc-2")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.ParseAccount(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "acc-2" || claims.Type != "account" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, err := m.ParseAccess(token); err == nil {
		t.Fatal("account token must not parse as access token")
	}
}

func TestExpiredToken(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "key.pem"), "cindy", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.SignAccess("acc-3", "mem-3", "global")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ParseAccess(token); err == nil {
		t.Fatal("expected expired token error")
	}
}

func TestJWKS(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "key.pem"), "cindy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	jwks := m.JWKS()
	keys, ok := jwks["keys"].([]map[string]any)
	if !ok || len(keys) != 1 || keys[0]["crv"] != "Ed25519" {
		t.Fatalf("unexpected jwks: %+v", jwks)
	}
}
