package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/crypto"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/mailer"
	"github.com/makecindy/cindy-server/internal/platform/sms"
)

type Service struct {
	db     *db.DB
	rdb    *redis.Client
	tokens *authn.Manager
	mail   *mailer.Mailer
	sms    sms.Sender
	cfg    *config.Config
	audit  *audit.Logger
}

func NewService(database *db.DB, rdb *redis.Client, tokens *authn.Manager, mail *mailer.Mailer, smsSender sms.Sender, cfg *config.Config, auditLog *audit.Logger) *Service {
	return &Service{db: database, rdb: rdb, tokens: tokens, mail: mail, sms: smsSender, cfg: cfg, audit: auditLog}
}

type Membership struct {
	ID          string  `json:"id"`
	PassportID  string  `json:"passportId,omitempty"`
	Kind        string  `json:"kind"`
	Role        string  `json:"role"`
	DisplayName string  `json:"displayName"`
	AvatarURL   *string `json:"avatarUrl"`
	Email       *string `json:"email"`
	OrgID       *string `json:"orgId"`
	OrgName     *string `json:"orgName"`
	OrgLogoURL  *string `json:"orgLogoUrl"`
}

type AccountMembership struct {
	Membership
	OrgSlug *string `json:"orgSlug"`
}

type LoginResult struct {
	Status                  string       `json:"status"`
	AccessToken             string       `json:"accessToken,omitempty"`
	RefreshToken            string       `json:"refreshToken,omitempty"`
	Membership              *Membership  `json:"membership,omitempty"`
	AccountToken            string       `json:"accountToken,omitempty"`
	AccountRefreshToken     string       `json:"accountRefreshToken,omitempty"`
	LoginTicket             string       `json:"loginTicket,omitempty"`
	Accounts                []Membership `json:"accounts,omitempty"`
	AccountDeletionRestored bool         `json:"accountDeletionRestored,omitempty"`
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) loadMembership(ctx context.Context, membershipID string) (*Membership, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT id::text, kind, role, display_name,
		       COALESCE(avatar_url,''), COALESCE(email::text,''),
		       COALESCE(org_id::text,''), COALESCE(org_name,''), COALESCE(org_logo_url,'')
		FROM auth_memberships WHERE id = $1::uuid`, membershipID)
	var m Membership
	var avatar, email, orgID, orgName, logo string
	if err := row.Scan(&m.ID, &m.Kind, &m.Role, &m.DisplayName, &avatar, &email, &orgID, &orgName, &logo); err != nil {
		return nil, err
	}
	m.AvatarURL, m.Email, m.OrgID, m.OrgName, m.OrgLogoURL = ptr(avatar), ptr(email), ptr(orgID), ptr(orgName), ptr(logo)
	return &m, nil
}

func (s *Service) accountMemberships(ctx context.Context, accountID string) ([]AccountMembership, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT m.id::text, m.kind, m.role, m.display_name,
		       COALESCE(m.avatar_url,''), COALESCE(m.email::text,''),
		       COALESCE(m.org_id::text,''), COALESCE(m.org_name,''), COALESCE(m.org_logo_url,''),
		       COALESCE(o.slug::text,'')
		FROM auth_memberships m
		LEFT JOIN auth_organizations o ON o.id = m.org_id
		WHERE m.account_id = $1::uuid AND m.status = 'active'
		ORDER BY m.created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountMembership
	for rows.Next() {
		var m AccountMembership
		var avatar, email, orgID, orgName, logo, slug string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Role, &m.DisplayName, &avatar, &email, &orgID, &orgName, &logo, &slug); err != nil {
			return nil, err
		}
		m.AvatarURL, m.Email, m.OrgID, m.OrgName, m.OrgLogoURL, m.OrgSlug = ptr(avatar), ptr(email), ptr(orgID), ptr(orgName), ptr(logo), ptr(slug)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) findOrCreateIdentity(ctx context.Context, kind, identifier, displayName string) (accountID, membershipID string, restored bool, err error) {
	// An already-linked identity always wins, including after switching from
	// single-user mode back to multi-account mode.
	err = s.db.Pool.QueryRow(ctx, `
		SELECT a.id::text, m.id::text
		FROM auth_identities i
		JOIN auth_accounts a ON a.id = i.account_id
		JOIN auth_memberships m ON m.account_id = a.id AND m.kind = 'personal'
		WHERE i.type = $1 AND i.identifier = $2`, kind, identifier).Scan(&accountID, &membershipID)
	if err == nil {
		if kind == "email" {
			if _, err := s.db.Pool.Exec(ctx, `
				UPDATE auth_memberships SET email=$2::citext, updated_at=now()
				WHERE id=$1::uuid AND (email IS NULL OR email::text <> $2::text)`, membershipID, identifier); err != nil {
				return "", "", false, err
			}
		}
		restored, _ = s.cancelDeletionOnLogin(ctx, accountID)
		return accountID, membershipID, restored, nil
	}
	if err != pgx.ErrNoRows {
		return "", "", false, err
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return "", "", false, err
	}
	defer tx.Rollback(ctx)

	if s.cfg.SingleUserMode {
		lockKey := "single-user:" + s.cfg.Region
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return "", "", false, err
		}
		// Recheck inside the lock: a parallel email/phone login may have linked
		// this identity while we waited.
		err = tx.QueryRow(ctx, `
			SELECT a.id::text, m.id::text
			FROM auth_identities i
			JOIN auth_accounts a ON a.id = i.account_id
			JOIN auth_memberships m ON m.account_id = a.id AND m.kind = 'personal'
			WHERE i.type = $1 AND i.identifier = $2`, kind, identifier).Scan(&accountID, &membershipID)
		if err == nil {
			if kind == "email" {
				if _, err = tx.Exec(ctx, `
					UPDATE auth_memberships SET email=$2::citext, updated_at=now()
					WHERE id=$1::uuid AND (email IS NULL OR email::text <> $2::text)`, membershipID, identifier); err != nil {
					return "", "", false, err
				}
			}
			if err = tx.Commit(ctx); err != nil {
				return "", "", false, err
			}
			restored, _ = s.cancelDeletionOnLogin(ctx, accountID)
			return accountID, membershipID, restored, nil
		}
		if err != pgx.ErrNoRows {
			return "", "", false, err
		}
		if s.cfg.SingleUserAccountID != "" {
			if _, parseErr := uuid.Parse(s.cfg.SingleUserAccountID); parseErr != nil {
				return "", "", false, fmt.Errorf("invalid single-user account id: %w", parseErr)
			}
			err = tx.QueryRow(ctx, `
				SELECT a.id::text, COALESCE(m.id::text, '')
				FROM auth_accounts a
				LEFT JOIN auth_memberships m ON m.account_id = a.id AND m.kind = 'personal'
				WHERE a.id = $1::uuid
				LIMIT 1`, s.cfg.SingleUserAccountID).Scan(&accountID, &membershipID)
			if err != nil {
				return "", "", false, fmt.Errorf("single-user account not found: %w", err)
			}
			if membershipID == "" {
				membershipID, err = insertPersonalMembership(ctx, tx, accountID, kind, identifier, displayName)
				if err != nil {
					return "", "", false, err
				}
			}
		} else {
			err = tx.QueryRow(ctx, `
				SELECT a.id::text, m.id::text
				FROM auth_accounts a
				JOIN auth_memberships m ON m.account_id = a.id AND m.kind = 'personal'
				WHERE a.region = $1
				ORDER BY a.created_at ASC
				LIMIT 1`, s.cfg.Region).Scan(&accountID, &membershipID)
			if errors.Is(err, pgx.ErrNoRows) {
				accountID = uuid.NewString()
				if _, err = tx.Exec(ctx, `INSERT INTO auth_accounts (id, region) VALUES ($1::uuid, $2)`, accountID, s.cfg.Region); err != nil {
					return "", "", false, err
				}
				membershipID, err = insertPersonalMembership(ctx, tx, accountID, kind, identifier, displayName)
				if err != nil {
					return "", "", false, err
				}
			} else if err != nil {
				return "", "", false, err
			}
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO auth_identities (account_id, type, identifier, verified_at)
			VALUES ($1::uuid, $2, $3, now())
			ON CONFLICT (type, identifier) DO NOTHING`, accountID, kind, identifier); err != nil {
			return "", "", false, err
		}
		if kind == "email" {
			// In personal single-user mode the latest verified email becomes the
			// profile email, so Settings shows the address used to sign in.
			if _, err = tx.Exec(ctx, `
				UPDATE auth_memberships SET email=$2::citext, updated_at=now()
				WHERE id=$1::uuid`, membershipID, identifier); err != nil {
				return "", "", false, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return "", "", false, err
		}
		restored, _ = s.cancelDeletionOnLogin(ctx, accountID)
		return accountID, membershipID, restored, nil
	}

	accountID = uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO auth_accounts (id, region) VALUES ($1::uuid, $2)`, accountID, s.cfg.Region); err != nil {
		return "", "", false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_identities (account_id, type, identifier, verified_at) VALUES ($1::uuid, $2, $3, now())`, accountID, kind, identifier); err != nil {
		return "", "", false, err
	}
	membershipID, err = insertPersonalMembership(ctx, tx, accountID, kind, identifier, displayName)
	if err != nil {
		return "", "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", false, err
	}
	return accountID, membershipID, false, nil
}

func insertPersonalMembership(ctx context.Context, tx pgx.Tx, accountID, kind, identifier, displayName string) (string, error) {
	membershipID := uuid.NewString()
	var email any
	if kind == "email" {
		email = identifier
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships (id, account_id, kind, role, display_name, email)
		VALUES ($1::uuid, $2::uuid, 'personal', 'owner', $3, $4)`,
		membershipID, accountID, displayName, email); err != nil {
		return "", err
	}
	return membershipID, nil
}

func (s *Service) cancelDeletionOnLogin(ctx context.Context, accountID string) (bool, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE auth_account_deletions SET status='cancelled', restored_at=now(), cancelled_at=now()
		WHERE account_id=$1::uuid AND status='pending'`, accountID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func randomCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

func (s *Service) SendEmailCode(ctx context.Context, email string) error {
	if !s.mail.Available() {
		return mailer.ErrNotConfigured
	}
	code := s.cfg.EmailDevFixedCode
	if code == "" {
		code = randomCode()
	}
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, "cindy:auth:code_cooldown:"+email, "1", time.Minute).Result()
		if err == nil && !ok {
			return fmt.Errorf("cooldown")
		}
	}
	var codeID string
	if err := s.db.Pool.QueryRow(ctx, `
		INSERT INTO auth_verification_codes (channel, identifier, purpose, code_hash, expires_at)
		VALUES ('email', $1, 'email_login', $2, now() + interval '10 minutes')
		RETURNING id::text`, email, crypto.HashToken(code)).Scan(&codeID); err != nil {
		return err
	}
	if err := s.mail.SendCode(ctx, email, code); err != nil {
		_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_verification_codes SET consumed_at=now() WHERE id=$1::uuid`, codeID)
		return err
	}
	return nil
}

func (s *Service) VerifyEmailCode(ctx context.Context, email, code, deviceID, ip, ua string) (*LoginResult, error) {
	return s.verifyCode(ctx, "email", email, code, deviceID, ip, ua)
}

func (s *Service) SendPhoneCode(ctx context.Context, rawPhone, ip string) error {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return err
	}
	if err := s.phoneRateLimit(ctx, phone, ip); err != nil {
		return err
	}
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, "cindy:auth:phone_cooldown:"+phone, "1", time.Minute).Result()
		if err == nil && !ok {
			return ErrPhoneRateLimited
		}
	}
	code := s.cfg.DevFixedCode
	if code == "" {
		code = randomCode()
	}
	var codeID string
	err = s.db.Pool.QueryRow(ctx, `
		INSERT INTO auth_verification_codes (channel, identifier, purpose, code_hash, expires_at)
		VALUES ('phone', $1, 'phone_login', $2, now() + interval '10 minutes')
		RETURNING id::text`, phone, crypto.HashToken(code)).Scan(&codeID)
	if err != nil {
		if s.rdb != nil {
			s.rdb.Del(ctx, "cindy:auth:phone_cooldown:"+phone)
		}
		return err
	}
	if s.sms == nil {
		s.cleanupPhoneCode(ctx, codeID, phone)
		return sms.ErrNotConfigured
	}
	if err := s.sms.SendVerificationCode(ctx, phone, code); err != nil {
		s.cleanupPhoneCode(ctx, codeID, phone)
		return fmt.Errorf("send sms: %w", err)
	}
	return nil
}

func (s *Service) phoneRateLimit(ctx context.Context, phone, ip string) error {
	if s.rdb == nil {
		return nil
	}
	now := time.Now().UTC()
	ipKey := "cindy:auth:phone_ip:" + now.Format("200601021504") + ":" + ip
	if count, err := s.rdb.Incr(ctx, ipKey).Result(); err == nil {
		if count == 1 {
			s.rdb.Expire(ctx, ipKey, 2*time.Minute)
		}
		if count > 10 {
			return ErrPhoneRateLimited
		}
	}
	hourKey := "cindy:auth:phone_hour:" + now.Format("2006010215") + ":" + phone
	if count, err := s.rdb.Incr(ctx, hourKey).Result(); err == nil {
		if count == 1 {
			s.rdb.Expire(ctx, hourKey, time.Hour)
		}
		if count > 5 {
			return ErrPhoneRateLimited
		}
	}
	return nil
}

func (s *Service) cleanupPhoneCode(ctx context.Context, codeID, phone string) {
	_, _ = s.db.Pool.Exec(ctx, `DELETE FROM auth_verification_codes WHERE id=$1::uuid`, codeID)
	if s.rdb != nil {
		s.rdb.Del(ctx, "cindy:auth:phone_cooldown:"+phone)
	}
}

func (s *Service) VerifyPhoneCode(ctx context.Context, rawPhone, code, deviceID, ip, ua string) (*LoginResult, error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return nil, err
	}
	return s.verifyCode(ctx, "phone", phone, code, deviceID, ip, ua)
}

func (s *Service) DesktopPoll(ctx context.Context, pollSecret string) (map[string]any, error) {
	var status, code, errCode string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT status, COALESCE(code,''), COALESCE(error_code,'')
		FROM auth_desktop_authorizations WHERE poll_secret_hash=$1`, crypto.HashToken(pollSecret)).
		Scan(&status, &code, &errCode)
	if err != nil {
		return map[string]any{"status": "pending"}, nil
	}
	switch status {
	case "ok":
		return map[string]any{"status": "ok", "code": code}, nil
	case "error":
		return map[string]any{"status": "error", "error": map[string]any{"code": errCode}}, nil
	case "expired":
		return map[string]any{"status": "expired"}, nil
	default:
		return map[string]any{"status": "pending"}, nil
	}
}

func (s *Service) ExchangeMembership(ctx context.Context, accountID, membershipID, deviceID, ip, ua string) (*LoginResult, error) {
	var owned int
	if err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_memberships WHERE id=$1::uuid AND account_id=$2::uuid AND status='active'`, membershipID, accountID).Scan(&owned); err != nil || owned == 0 {
		return nil, fmt.Errorf("membership not found")
	}
	access, refresh, err := s.issueResourceTokens(ctx, accountID, membershipID, deviceID, ip, ua)
	if err != nil {
		return nil, err
	}
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Status: "ok", AccessToken: access, RefreshToken: refresh, Membership: m}, nil
}

func (s *Service) RefreshAccount(ctx context.Context, refreshToken, deviceID string) (string, string, error) {
	var sessionID, accountID string
	var expiresAt time.Time
	var revokedAt *time.Time
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, expires_at, revoked_at FROM auth_account_sessions WHERE token_hash=$1`,
		crypto.HashToken(refreshToken)).Scan(&sessionID, &accountID, &expiresAt, &revokedAt)
	if err != nil || revokedAt != nil || time.Now().After(expiresAt) {
		return "", "", fmt.Errorf("invalid account refresh token")
	}
	token, err := s.tokens.SignAccount(accountID)
	if err != nil {
		return "", "", err
	}
	newRefresh, err := s.issueAccountSession(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at=now() WHERE id=$1::uuid`, sessionID)
	return token, newRefresh, nil
}

func (s *Service) LogoutAccount(ctx context.Context, accountID string) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE auth_account_sessions SET revoked_at=now() WHERE account_id=$1::uuid AND revoked_at IS NULL`, accountID)
	return err
}

func (s *Service) verifyCode(ctx context.Context, channel, identifier, code, deviceID, ip, ua string) (*LoginResult, error) {
	var codeID, codeHash string
	var attempts int
	var expiresAt time.Time
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id::text, code_hash, attempts, expires_at FROM auth_verification_codes
		WHERE identifier=$1 AND purpose=$2 AND consumed_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, identifier, channel+"_login").Scan(&codeID, &codeHash, &attempts, &expiresAt)
	if err != nil {
		return nil, fmt.Errorf("invalid code")
	}
	if attempts >= 5 {
		return nil, fmt.Errorf("code attempts exceeded")
	}
	if time.Now().After(expiresAt) {
		return nil, fmt.Errorf("code expired")
	}
	if !crypto.ConstantTimeEqual(crypto.HashToken(code), codeHash) {
		_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_verification_codes SET attempts=attempts+1 WHERE id=$1::uuid`, codeID)
		return nil, fmt.Errorf("invalid code")
	}
	_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_verification_codes SET consumed_at=now() WHERE id=$1::uuid`, codeID)

	display := identifier
	if channel == "email" {
		if i := strings.Index(identifier, "@"); i > 0 {
			display = identifier[:i]
		}
	} else if len(identifier) > 4 {
		display = "User " + identifier[len(identifier)-4:]
	}
	accountID, membershipID, restored, err := s.findOrCreateIdentity(ctx, channel, identifier, display)
	if err != nil {
		return nil, err
	}
	return s.loginOutcome(ctx, accountID, membershipID, deviceID, ip, ua, restored)
}

func (s *Service) loginOutcome(ctx context.Context, accountID, membershipID, deviceID, ip, ua string, restored bool) (*LoginResult, error) {
	members, err := s.accountMemberships(ctx, accountID)
	if err != nil {
		return nil, err
	}
	accountToken, err := s.tokens.SignAccount(accountID)
	if err != nil {
		return nil, err
	}
	accountRefresh, err := s.issueAccountSession(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(members) > 1 {
		ticket := uuid.NewString()
		if _, err := s.db.Pool.Exec(ctx, `INSERT INTO auth_login_tickets (id, account_id, purpose, expires_at) VALUES ($1::uuid, $2::uuid, 'select_account', now() + interval '5 minutes')`, ticket, accountID); err != nil {
			return nil, err
		}
		accounts := make([]Membership, 0, len(members))
		for _, m := range members {
			accounts = append(accounts, m.Membership)
		}
		return &LoginResult{Status: "select_account", LoginTicket: ticket, Accounts: accounts, AccountToken: accountToken, AccountRefreshToken: accountRefresh}, nil
	}
	access, refresh, err := s.issueResourceTokens(ctx, accountID, membershipID, deviceID, ip, ua)
	if err != nil {
		return nil, err
	}
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	res := &LoginResult{
		Status: "ok", AccessToken: access, RefreshToken: refresh, Membership: m,
		AccountToken: accountToken, AccountRefreshToken: accountRefresh,
		AccountDeletionRestored: restored,
	}
	return res, nil
}

func (s *Service) issueResourceTokens(ctx context.Context, accountID, membershipID, deviceID, ip, ua string) (string, string, error) {
	access, err := s.tokens.SignAccessDevice(accountID, membershipID, s.cfg.Region, deviceID)
	if err != nil {
		return "", "", err
	}
	refresh := crypto.RandomToken(32)
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO auth_refresh_sessions (account_id, membership_id, device_id, token_hash, expires_at, ip, user_agent)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`,
		accountID, membershipID, nullIfEmpty(deviceID), crypto.HashToken(refresh), time.Now().Add(s.cfg.RefreshTTL), nullIfEmpty(ip), nullIfEmpty(ua))
	if err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

func (s *Service) issueAccountSession(ctx context.Context, accountID string) (string, error) {
	token := crypto.RandomToken(32)
	_, err := s.db.Pool.Exec(ctx, `INSERT INTO auth_account_sessions (account_id, token_hash, expires_at) VALUES ($1::uuid,$2,$3)`,
		accountID, crypto.HashToken(token), time.Now().Add(s.cfg.RefreshTTL))
	return token, err
}

func (s *Service) Refresh(ctx context.Context, refreshToken, deviceID string) (*LoginResult, error) {
	var sessionID, accountID, membershipID, storedDevice, familyID string
	var expiresAt time.Time
	var revokedAt *time.Time
	var rotatedFrom *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, membership_id::text, COALESCE(device_id,''), family_id::text,
		       expires_at, revoked_at, rotated_from::text
		FROM auth_refresh_sessions WHERE token_hash=$1`, crypto.HashToken(refreshToken)).
		Scan(&sessionID, &accountID, &membershipID, &storedDevice, &familyID, &expiresAt, &revokedAt, &rotatedFrom)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token")
	}
	if revokedAt != nil || rotatedFrom != nil {
		_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at=now() WHERE family_id=$1::uuid AND revoked_at IS NULL`, familyID)
		return nil, fmt.Errorf("invalid refresh token")
	}
	if time.Now().After(expiresAt) {
		return nil, fmt.Errorf("refresh token expired")
	}
	if storedDevice != "" && deviceID != "" && storedDevice != deviceID {
		return nil, fmt.Errorf("device mismatch")
	}
	access, newRefresh, err := s.issueResourceTokens(ctx, accountID, membershipID, deviceID, "", "")
	if err != nil {
		return nil, err
	}
	_, _ = s.db.Pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at=now(), rotated_from=$2::uuid WHERE id=$1::uuid`, sessionID, sessionID)
	// Link new session to the same family for reuse detection.
	if _, err := s.db.Pool.Exec(ctx, `UPDATE auth_refresh_sessions SET family_id=$1::uuid WHERE token_hash=$2 AND revoked_at IS NULL`, familyID, crypto.HashToken(newRefresh)); err != nil {
		return nil, err
	}
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Status: "ok", AccessToken: access, RefreshToken: newRefresh, Membership: m}, nil
}

func (s *Service) Memberships(ctx context.Context, accountID string) ([]AccountMembership, error) {
	return s.accountMemberships(ctx, accountID)
}

func (s *Service) Me(ctx context.Context, accountID, membershipID string) (map[string]any, error) {
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	all, err := s.accountMemberships(ctx, accountID)
	if err != nil {
		return nil, err
	}
	identities := make([]Membership, 0, len(all))
	for _, item := range all {
		identities = append(identities, item.Membership)
	}
	return map[string]any{"membership": m, "passportId": accountID, "identities": identities}, nil
}

func (s *Service) UpdateProfile(ctx context.Context, membershipID string, displayName *string, avatarURL *string, clearAvatar bool) (*Membership, error) {
	if displayName != nil {
		if _, err := s.db.Pool.Exec(ctx, `UPDATE auth_memberships SET display_name=$2, updated_at=now() WHERE id=$1::uuid`, membershipID, *displayName); err != nil {
			return nil, err
		}
	}
	if avatarURL != nil {
		if _, err := s.db.Pool.Exec(ctx, `UPDATE auth_memberships SET avatar_url=$2, updated_at=now() WHERE id=$1::uuid`, membershipID, *avatarURL); err != nil {
			return nil, err
		}
	}
	if clearAvatar {
		if _, err := s.db.Pool.Exec(ctx, `UPDATE auth_memberships SET avatar_url=NULL, updated_at=now() WHERE id=$1::uuid`, membershipID); err != nil {
			return nil, err
		}
	}
	return s.loadMembership(ctx, membershipID)
}

func (s *Service) SelectAccount(ctx context.Context, ticket, membershipID, deviceID, ip, ua string) (*LoginResult, error) {
	var ticketAccount, memberAccount string
	var expiresAt time.Time
	var consumedAt *time.Time
	err := s.db.Pool.QueryRow(ctx, `
		SELECT t.account_id::text, m.account_id::text, t.expires_at, t.consumed_at
		FROM auth_login_tickets t, auth_memberships m
		WHERE t.id=$1::uuid AND t.purpose='select_account' AND m.id=$2::uuid`, ticket, membershipID).
		Scan(&ticketAccount, &memberAccount, &expiresAt, &consumedAt)
	if err != nil || consumedAt != nil || time.Now().After(expiresAt) || ticketAccount != memberAccount {
		return nil, fmt.Errorf("invalid login ticket")
	}
	if _, err := s.db.Pool.Exec(ctx, `UPDATE auth_login_tickets SET consumed_at=now() WHERE id=$1::uuid`, ticket); err != nil {
		return nil, err
	}
	access, refresh, err := s.issueResourceTokens(ctx, memberAccount, membershipID, deviceID, ip, ua)
	if err != nil {
		return nil, err
	}
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Status: "ok", AccessToken: access, RefreshToken: refresh, Membership: m}, nil
}

// deletionTarget resolves the verification channel from the account's verified
// identities. Phone-only accounts must not be forced through email.
func (s *Service) deletionTarget(ctx context.Context, membershipID string) (channel, target, masked string, ok bool) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT i.type, i.identifier::text
		FROM auth_memberships m
		JOIN auth_identities i ON i.account_id = m.account_id
		WHERE m.id = $1::uuid AND i.verified_at IS NOT NULL
		ORDER BY CASE i.type WHEN 'email' THEN 0 WHEN 'phone' THEN 1 ELSE 2 END, i.created_at`,
		membershipID)
	if err != nil {
		return "", "", "", false
	}
	defer rows.Close()
	for rows.Next() {
		var kind, identifier string
		if err := rows.Scan(&kind, &identifier); err != nil {
			return "", "", "", false
		}
		switch kind {
		case "email":
			return "email", identifier, maskEmail(identifier), true
		case "phone":
			return "sms", identifier, sms.MaskPhone(identifier), true
		}
	}
	return "", "", "", false
}

func (s *Service) DeletionAvailability(ctx context.Context, membershipID string) (map[string]any, error) {
	channel, _, masked, ok := s.deletionTarget(ctx, membershipID)
	if !ok {
		return map[string]any{
			"available":                     false,
			"manualAppleRevocationRequired": false,
		}, nil
	}
	return map[string]any{
		"available":                     true,
		"verification":                  map[string]any{"channel": channel, "maskedTarget": masked},
		"manualAppleRevocationRequired": false,
	}, nil
}

func (s *Service) StartDeletion(ctx context.Context, accountID, membershipID string) (map[string]any, error) {
	channel, target, masked, ok := s.deletionTarget(ctx, membershipID)
	if !ok {
		return nil, fmt.Errorf("no verified deletion channel")
	}
	var pending int
	_ = s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_account_deletions WHERE account_id=$1::uuid AND status IN ('pending','processing')`, accountID).Scan(&pending)
	if pending > 0 {
		return nil, fmt.Errorf("deletion pending")
	}
	code := s.cfg.DevFixedCode
	if channel == "email" {
		code = s.cfg.EmailDevFixedCode
	}
	if code == "" {
		code = randomCode()
	}
	challengeID := uuid.NewString()
	receipt := crypto.RandomToken(32)
	expires := time.Now().Add(10 * time.Minute)
	switch channel {
	case "email":
		if err := s.mail.SendCode(ctx, target, code); err != nil {
			return nil, err
		}
	case "sms":
		if s.sms == nil || !s.sms.Available() {
			return nil, sms.ErrNotConfigured
		}
		if err := s.sms.SendVerificationCode(ctx, target, code); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported deletion channel")
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO auth_account_deletions (id, account_id, status, challenge_hash, receipt_hash, channel, masked_target, scheduled_at)
		VALUES ($1::uuid,$2::uuid,'pending',$3,$4,$5,$6,$7)`,
		challengeID, accountID, crypto.HashToken(code), crypto.HashToken(receipt), channel, masked, expires)
	if err != nil {
		return nil, err
	}
	return map[string]any{"challengeId": challengeID, "receiptToken": receipt, "channel": channel, "maskedTarget": masked, "expiresAt": expires.UTC()}, nil
}

func (s *Service) ConfirmDeletion(ctx context.Context, accountID, challengeID, receipt, code string) (map[string]any, error) {
	var challengeHash, receiptHash, status string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT challenge_hash, receipt_hash, status FROM auth_account_deletions
		WHERE id=$1::uuid AND account_id=$2::uuid`, challengeID, accountID).Scan(&challengeHash, &receiptHash, &status)
	if err != nil || status != "pending" {
		return nil, fmt.Errorf("invalid challenge")
	}
	if !crypto.ConstantTimeEqual(crypto.HashToken(code), challengeHash) || !crypto.ConstantTimeEqual(crypto.HashToken(receipt), receiptHash) {
		return nil, fmt.Errorf("invalid code")
	}
	_, err = s.db.Pool.Exec(ctx, `
		UPDATE auth_account_deletions SET scheduled_at=now() + interval '30 days' WHERE id=$1::uuid`, challengeID)
	if err != nil {
		return nil, err
	}
	return s.deletionStatus(ctx, challengeID)
}

func (s *Service) DeletionStatusByReceipt(ctx context.Context, receipt string) (map[string]any, error) {
	var id string
	err := s.db.Pool.QueryRow(ctx, `SELECT id::text FROM auth_account_deletions WHERE receipt_hash=$1`, crypto.HashToken(receipt)).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("invalid receipt")
	}
	return s.deletionStatus(ctx, id)
}

func (s *Service) deletionStatus(ctx context.Context, id string) (map[string]any, error) {
	var status string
	var requestedAt time.Time
	var deleteAfter, executedAt, cancelledAt *time.Time
	err := s.db.Pool.QueryRow(ctx, `
		SELECT status, created_at, scheduled_at, executed_at, cancelled_at
		FROM auth_account_deletions WHERE id=$1::uuid`, id).
		Scan(&status, &requestedAt, &deleteAfter, &executedAt, &cancelledAt)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"status": status, "requestedAt": requestedAt.UTC()}
	if deleteAfter != nil {
		res["deleteAfter"] = deleteAfter.UTC()
	}
	if executedAt != nil {
		res["processingAt"] = executedAt.UTC()
		if status == "completed" {
			res["completedAt"] = executedAt.UTC()
		}
	}
	if cancelledAt != nil {
		res["cancelledAt"] = cancelledAt.UTC()
	}
	return res, nil
}

func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

func (s *Service) Logout(ctx context.Context, membershipID string) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE auth_refresh_sessions SET revoked_at=now() WHERE membership_id=$1::uuid AND revoked_at IS NULL`, membershipID)
	return err
}

func (s *Service) SSODiscovery(ctx context.Context, org string) (map[string]any, error) {
	var orgID, orgName string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT o.id::text, o.name FROM auth_organizations o
		LEFT JOIN auth_org_domains d ON d.org_id=o.id
		WHERE o.slug=$1 OR o.id::text=$1 OR (d.domain=$1 AND d.verified_at IS NOT NULL)
		LIMIT 1`, strings.ToLower(strings.TrimSpace(org))).Scan(&orgID, &orgName)
	if err != nil {
		return nil, fmt.Errorf("org not found")
	}
	rows, err := s.db.Pool.Query(ctx, `SELECT id::text, protocol, name FROM auth_sso_connections WHERE org_id=$1::uuid AND enabled=true ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := []map[string]any{}
	for rows.Next() {
		var id, protocol, name string
		if err := rows.Scan(&id, &protocol, &name); err != nil {
			return nil, err
		}
		connections = append(connections, map[string]any{"connectionId": id, "protocol": protocol, "connectionName": name})
	}
	return map[string]any{"region": s.cfg.Region, "orgName": orgName, "connections": connections}, nil
}

func (s *Service) DiscoveryByEmail(ctx context.Context, email string) []map[string]any {
	domain := ""
	if i := strings.LastIndex(email, "@"); i >= 0 {
		domain = email[i+1:]
	}
	if domain != "" {
		var connectionID, protocol, orgName, connectionName string
		err := s.db.Pool.QueryRow(ctx, `
			SELECT c.id::text, c.protocol, o.name, c.name
			FROM auth_org_domains d
			JOIN auth_organizations o ON o.id=d.org_id
			JOIN auth_sso_connections c ON c.org_id=o.id AND c.enabled=true
			WHERE d.domain=$1 AND d.verified_at IS NOT NULL AND o.sso_required=true
			LIMIT 1`, domain).Scan(&connectionID, &protocol, &orgName, &connectionName)
		if err == nil {
			return []map[string]any{{"type": "sso", "connectionId": connectionID, "protocol": protocol, "orgName": orgName, "connectionName": connectionName, "ssoRequired": true}}
		}
	}
	return []map[string]any{{"type": "email_code"}}
}

func (s *Service) IdentityCard(ctx context.Context, membershipID, purpose, nonce string) (string, time.Time, error) {
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	claims := map[string]any{
		"typ": "identity-card", "iss": s.cfg.PublicBaseURL, "aud": "cindy-provider-share",
		"sub": m.ID, "name": m.DisplayName, "region": s.cfg.Region,
		"purpose": purpose, "nonce": nonce, "iat": now.Unix(), "exp": exp.Unix(),
	}
	if m.AvatarURL != nil {
		claims["picture"] = *m.AvatarURL
	}
	card, err := s.tokens.SignMap(claims)
	return card, exp, err
}

func (s *Service) ConnectionToken(ctx context.Context, membershipID, audience string) (string, int, error) {
	m, err := s.loadMembership(ctx, membershipID)
	if err != nil {
		return "", 0, err
	}
	if m.Kind != "org" {
		return "", 0, fmt.Errorf("organization membership required")
	}
	now := time.Now().UTC()
	claims := map[string]any{
		"iss": s.cfg.PublicBaseURL, "aud": audience, "sub": m.ID,
		"org": m.OrgID, "iat": now.Unix(), "exp": now.Add(30 * time.Minute).Unix(),
	}
	token, err := s.tokens.SignMap(claims)
	return token, 1800, err
}

func (s *Service) Providers() map[string]any {
	phoneEnabled := s.sms != nil && s.sms.Available()
	attribution := "email"
	if s.cfg.Region == "cn" {
		attribution = "phone"
	}
	// Social login is still a contract skeleton: advertising providers here
	// would render buttons that always fail with SOCIAL_PROVIDER_UNAVAILABLE.
	// Only advertise a provider once its token exchange is implemented.
	social := []string{}
	return map[string]any{
		"region":      s.cfg.Region,
		"attribution": attribution,
		"email":       true,
		"phone":       phoneEnabled,
		"social":      social,
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
