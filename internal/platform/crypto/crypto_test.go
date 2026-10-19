package crypto

import "testing"

func TestPassword(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("secret", hash) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatal("invalid password accepted")
	}
}

func TestBox(t *testing.T) {
	box := NewBox("master")
	ciphertext, nonce, err := box.Encrypt([]byte("hello"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Decrypt(ciphertext, nonce, "aad")
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "hello" {
		t.Fatalf("got %q", plain)
	}
	if _, err := box.Decrypt(ciphertext, nonce, "other"); err == nil {
		t.Fatal("aad mismatch must fail")
	}
}

func TestTokenHash(t *testing.T) {
	token := RandomToken(32)
	if len(token) < 40 {
		t.Fatalf("token too short: %d", len(token))
	}
	if HashToken(token) == HashToken(token+"x") {
		t.Fatal("hash collision")
	}
}
