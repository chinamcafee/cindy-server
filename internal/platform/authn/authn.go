package authn

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	MembershipID string `json:"mid"`
	Region       string `json:"region"`
	DeviceID     string `json:"did,omitempty"`
	Type         string `json:"typ"`
	jwt.RegisteredClaims
}

type Manager struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	kid     string
	issuer  string
	ttl     time.Duration
	aud     string
}

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

func NewManager(keyFile, issuer string, ttl time.Duration) (*Manager, error) {
	priv, err := loadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	return &Manager{
		private: priv,
		public:  pub,
		kid:     base64.RawURLEncoding.EncodeToString(sum[:8]),
		issuer:  issuer,
		ttl:     ttl,
		aud:     "cindy-client",
	}, nil
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("invalid key file %s", path)
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ed, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is not ed25519")
		}
		return ed, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

func (m *Manager) SignAccess(accountID, membershipID, region string) (string, error) {
	return m.SignAccessDevice(accountID, membershipID, region, "")
}

func (m *Manager) SignAccessDevice(accountID, membershipID, region, deviceID string) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		MembershipID: membershipID,
		Region:       region,
		DeviceID:     deviceID,
		Type:         "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Audience:  jwt.ClaimStrings{m.aud},
			Subject:   accountID,
			ID:        randomID(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = m.kid
	return tok.SignedString(m.private)
}

func (m *Manager) SignAccount(accountID string) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		Type: "account",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Audience:  jwt.ClaimStrings{"cindy-account"},
			Subject:   accountID,
			ID:        randomID(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = m.kid
	return tok.SignedString(m.private)
}

func (m *Manager) ParseAccount(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return m.public, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithAudience("cindy-account"), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid || claims.Type != "account" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (m *Manager) SignMap(claims jwt.MapClaims) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = m.kid
	return tok.SignedString(m.private)
}

func (m *Manager) SignCustom(claims jwt.Claims) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = m.kid
	return tok.SignedString(m.private)
}

func (m *Manager) ParseAccess(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, fmt.Errorf("unexpected signing method %s", t.Method.Alg())
		}
		return m.public, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithAudience(m.aud), jwt.WithExpirationRequired())
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !parsed.Valid || claims.Type != "access" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (m *Manager) ParseCard(token string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return m.public, nil
	}, jwt.WithAudience("cindy-provider-share"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (m *Manager) JWKS() map[string]any {
	return map[string]any{
		"keys": []map[string]any{{
			"kty": "OKP",
			"crv": "Ed25519",
			"x":   base64.RawURLEncoding.EncodeToString(m.public),
			"kid": m.kid,
			"use": "sig",
			"alg": "EdDSA",
		}},
	}
}

func (m *Manager) PublicKey() ed25519.PublicKey { return m.public }

func (m *Manager) TTL() time.Duration { return m.ttl }

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
