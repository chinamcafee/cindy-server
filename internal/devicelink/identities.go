package devicelink

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const identityChallengeTTL = 120 * time.Second

type identityJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type identityRow struct {
	ID           string
	AccountID    string
	MembershipID string
	DeviceID     string
	PublicKey    string
	Thumbprint   string
}

type identityChallengeRow struct {
	ID           string
	AccountID    string
	MembershipID string
	IdentityID   string
	DeviceID     string
	PublicKey    string
	Thumbprint   string
	Message      string
	ExpiresAt    time.Time
}

func (m *Module) accessClaims(r *http.Request) (*authn.Claims, string, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, "", httpapi.ErrUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	claims, err := m.tokens.ParseAccess(token)
	if err != nil {
		return nil, "", httpapi.ErrInvalidToken
	}
	return claims, token, nil
}

func decodePublicKey(raw json.RawMessage) (identityJWK, *ecdsa.PublicKey, string, []byte, error) {
	var key identityJWK
	if len(raw) == 0 || json.Unmarshal(raw, &key) != nil {
		return key, nil, "", nil, errors.New("invalid public key")
	}
	if key.Kty != "EC" || key.Crv != "P-256" || key.X == "" || key.Y == "" {
		return key, nil, "", nil, errors.New("invalid public key")
	}
	x, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(key.X, "="))
	if err != nil {
		return key, nil, "", nil, errors.New("invalid public key")
	}
	y, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(key.Y, "="))
	if err != nil || len(x) != 32 || len(y) != 32 {
		return key, nil, "", nil, errors.New("invalid public key")
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
		return key, nil, "", nil, errors.New("invalid public key")
	}
	canonical := []byte(`{"crv":"P-256","kty":"EC","x":"` + key.X + `","y":"` + key.Y + `"}`)
	sum := sha256.Sum256(canonical)
	thumb := base64.RawURLEncoding.EncodeToString(sum[:])
	return key, pub, thumb, canonical, nil
}

func publicKeyResponse(key identityJWK) map[string]any {
	return map[string]any{"kty": key.Kty, "crv": key.Crv, "x": key.X, "y": key.Y}
}

func randomNonceBytes() ([]byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func digest64(data []byte) string {
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (m *Module) insertIdentityChallenge(
	ctx context.Context,
	id, accountID, membershipID, identityID, deviceID, path, tokenHash, bodyHash, publicKey, thumbprint, purpose string,
	message string,
	expiresAt time.Time,
) (string, error) {
	nonce, err := randomNonceBytes()
	if err != nil {
		return "", err
	}
	_, err = m.db.Pool.Exec(ctx, `
		INSERT INTO dl_identity_challenges
			(id, device_id, path, token_hash, body_hash, nonce, expires_at,
			 account_id, membership_id, identity_id, public_key, thumbprint, message, purpose)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8::uuid,$9::uuid,$10,$11,$12,$13,$14)`,
		id, deviceID, path, nullIfEmpty(tokenHash), nullIfEmpty(bodyHash),
		base64.StdEncoding.EncodeToString(nonce), expiresAt,
		accountID, membershipID, nullIfEmpty(identityID), publicKey, thumbprint, message, purpose)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (m *Module) createIdentityChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicKey json.RawMessage `json:"publicKey"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	_, pub, thumbprint, _, err := decodePublicKey(req.PublicKey)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "CREDENTIAL_INVALID_IDENTITY", err.Error()))
		return
	}
	claims, token, err := m.accessClaims(r)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if claims.MembershipID == "" {
		httpapi.WriteError(w, httpapi.ErrUnauthorized)
		return
	}
	deviceID := claims.DeviceID
	if deviceID == "" {
		deviceID = "mobile"
	}
	challengeID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(identityChallengeTTL)
	nonce, err := randomNonceBytes()
	if err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	message, err := json.Marshal(map[string]any{
		"domain": "cindy.device-identity.enroll.v1", "membershipId": claims.MembershipID,
		"challengeId": challengeID, "thumbprint": thumbprint,
		"expiresAt": expiresAt.UnixMilli(), "nonce": base64.StdEncoding.EncodeToString(nonce),
	})
	if err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	if _, err := m.insertIdentityChallenge(r.Context(), challengeID, claims.Subject, claims.MembershipID, "", deviceID,
		"/identities", "", "", string(req.PublicKey), thumbprint, "enroll",
		string(message), expiresAt); err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	_ = pub
	_ = token
	httpapi.OK(w, map[string]any{
		"challengeId": challengeID, "version": 1, "membershipId": claims.MembershipID,
		"thumbprint": thumbprint, "expiresAt": expiresAt.UnixMilli(),
		"message": base64.StdEncoding.EncodeToString(message),
	})
}

func (m *Module) loadIdentity(ctx context.Context, accountID, identityID string) (*identityRow, error) {
	var row identityRow
	err := m.db.Pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, COALESCE(membership_id::text,''), COALESCE(device_id,''),
		       public_key, COALESCE(thumbprint,'')
		FROM dl_device_identities
		WHERE id=$1::uuid AND account_id=$2::uuid AND revoked_at IS NULL`, identityID, accountID).
		Scan(&row.ID, &row.AccountID, &row.MembershipID, &row.DeviceID, &row.PublicKey, &row.Thumbprint)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func identityResponse(row *identityRow, region string) map[string]any {
	var key identityJWK
	_ = json.Unmarshal([]byte(row.PublicKey), &key)
	return map[string]any{
		"id": row.ID, "userId": row.AccountID, "membershipId": row.MembershipID,
		"realm": region, "revokedAt": nil, "thumbprint": row.Thumbprint,
		"publicKey": publicKeyResponse(key),
	}
}

func (m *Module) registerIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChallengeID string `json:"challengeId"`
		Signature   string `json:"signature"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	claims, _, err := m.accessClaims(r)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if !validUUID(req.ChallengeID) || req.Signature == "" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "CHALLENGE_INVALID", "invalid challenge"))
		return
	}
	var ch identityChallengeRow
	err = m.db.Pool.QueryRow(r.Context(), `
		SELECT id::text, COALESCE(account_id::text,''), COALESCE(membership_id::text,''),
		       COALESCE(identity_id::text,''), device_id, COALESCE(public_key,''),
		       COALESCE(thumbprint,''), COALESCE(message,''), expires_at
		FROM dl_identity_challenges
		WHERE id=$1::uuid AND account_id=$2::uuid AND purpose='enroll'
		  AND consumed_at IS NULL AND expires_at > now()`, req.ChallengeID, claims.Subject).
		Scan(&ch.ID, &ch.AccountID, &ch.MembershipID, &ch.IdentityID, &ch.DeviceID,
			&ch.PublicKey, &ch.Thumbprint, &ch.Message, &ch.ExpiresAt)
	if err != nil {
		m.log.Warn("identity_challenge_lookup_failed", "error", err, "challenge", req.ChallengeID, "account", claims.Subject)
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "CHALLENGE_INVALID", "challenge expired or invalid"))
		return
	}
	if err := verifyIdentitySignature(ch.PublicKey, []byte(ch.Message), req.Signature); err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "CHALLENGE_INVALID", "signature verification failed"))
		return
	}
	if _, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_identity_challenges SET consumed_at=now() WHERE id=$1::uuid`, ch.ID); err != nil {
		m.internalError(w, "register_identity", err)
		return
	}

	var key identityJWK
	_ = json.Unmarshal([]byte(ch.PublicKey), &key)
	var identityID string
	err = m.db.Pool.QueryRow(r.Context(), `
		SELECT id::text FROM dl_device_identities WHERE device_id=$1`, ch.DeviceID).Scan(&identityID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		identityID = uuid.NewString()
		_, err = m.db.Pool.Exec(r.Context(), `
			INSERT INTO dl_device_identities (id, account_id, device_id, public_key, algorithm, membership_id, thumbprint)
			VALUES ($1::uuid,$2::uuid,$3,$4,'ES256',$5::uuid,$6)`,
			identityID, ch.AccountID, ch.DeviceID, ch.PublicKey, ch.MembershipID, ch.Thumbprint)
	case err == nil:
		_, err = m.db.Pool.Exec(r.Context(), `
			UPDATE dl_device_identities
			SET account_id=$2::uuid, public_key=$3, membership_id=$4::uuid, thumbprint=$5,
			    revoked_at=NULL, created_at=now()
			WHERE id=$1::uuid`, identityID, ch.AccountID, ch.PublicKey, ch.MembershipID, ch.Thumbprint)
	}
	if err != nil {
		m.internalError(w, "register_identity", err)
		return
	}
	m.auditEvent(r.Context(), "devicelink.identity.register", "device", ch.DeviceID, map[string]any{"algorithm": "ES256"})
	row := &identityRow{ID: identityID, AccountID: ch.AccountID, MembershipID: ch.MembershipID,
		DeviceID: ch.DeviceID, PublicKey: ch.PublicKey, Thumbprint: ch.Thumbprint}
	httpapi.OK(w, map[string]any{"identity": identityResponse(row, m.region())})
}

func (m *Module) lookupIdentity(w http.ResponseWriter, r *http.Request) {
	claims, _, err := m.accessClaims(r)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !validUUID(id) {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	row, err := m.loadIdentity(r.Context(), claims.Subject, id)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	httpapi.OK(w, map[string]any{"identity": identityResponse(row, m.region())})
}

func (m *Module) createRelayChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IdentityID string `json:"identityId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	m.issueIdentityBoundChallenge(w, r, req.IdentityID, "/api/device-link/ws", "", "")
}

func (m *Module) createPushChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IdentityID string            `json:"identityId"`
		Method     string            `json:"method"`
		Body       map[string]string `json:"body"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method != "PUT" && method != "DELETE" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid method"))
		return
	}
	body, err := json.Marshal(req.Body)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	m.issueIdentityBoundChallenge(w, r, req.IdentityID, "/api/device-link/push-token", method, string(body))
}

func (m *Module) issueIdentityBoundChallenge(w http.ResponseWriter, r *http.Request, identityID, path, method, body string) {
	claims, token, err := m.accessClaims(r)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if !validUUID(identityID) {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	row, err := m.loadIdentity(r.Context(), claims.Subject, identityID)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	authDevice := claims.DeviceID
	if authDevice == "" {
		authDevice = row.DeviceID
	}
	challengeID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(identityChallengeTTL)
	nonce, err := randomNonceBytes()
	if err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	messageFields := map[string]any{
		"domain": "cindy.device-identity.relay.v1", "membershipId": claims.MembershipID,
		"challengeId": challengeID, "thumbprint": row.Thumbprint,
		"expiresAt": expiresAt.UnixMilli(), "nonce": base64.StdEncoding.EncodeToString(nonce),
		"identityId": identityID, "authDeviceId": authDevice,
		"path": path, "tokenHash": digest64([]byte(token)),
	}
	if method != "" {
		messageFields["domain"] = "cindy.device-identity.push.v1"
		messageFields["method"] = method
		messageFields["bodyHash"] = digest64([]byte(body))
	}
	message, err := json.Marshal(messageFields)
	if err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	purpose := "relay"
	if method != "" {
		purpose = "push"
	}
	if _, err := m.insertIdentityChallenge(r.Context(), challengeID, claims.Subject, claims.MembershipID, identityID,
		authDevice, path, digest64([]byte(token)), digest64([]byte(body)), row.PublicKey,
		row.Thumbprint, purpose, string(message), expiresAt); err != nil {
		m.internalError(w, "identity_challenge", err)
		return
	}
	httpapi.OK(w, map[string]any{
		"challengeId": challengeID, "version": 1, "membershipId": claims.MembershipID,
		"identityId": identityID, "thumbprint": row.Thumbprint,
		"expiresAt": expiresAt.UnixMilli(), "message": base64.StdEncoding.EncodeToString(message),
	})
}

func verifyIdentitySignature(publicKeyJSON string, message []byte, signature string) error {
	var key identityJWK
	if err := json.Unmarshal([]byte(publicKeyJSON), &key); err != nil {
		return err
	}
	_, pub, _, _, err := decodePublicKey(json.RawMessage(publicKeyJSON))
	if err != nil {
		return err
	}
	signingInput := message
	sigText := signature
	if parts := strings.Split(signature, "."); len(parts) == 3 {
		signingInput = []byte(parts[0] + "." + parts[1])
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || string(payload) != string(message) {
			return errors.New("jws payload mismatch")
		}
		sigText = parts[2]
	}
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sigText, "="))
	if err != nil || len(sig) != 64 {
		return errors.New("invalid signature")
	}
	rInt := new(big.Int).SetBytes(sig[:32])
	sInt := new(big.Int).SetBytes(sig[32:])
	sum := sha256.Sum256(signingInput)
	if !ecdsa.Verify(pub, sum[:], rInt, sInt) {
		return errors.New("invalid signature")
	}
	return nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
