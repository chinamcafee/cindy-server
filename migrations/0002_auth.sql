-- +goose Up
CREATE TABLE auth_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    region text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','deleted')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE TABLE auth_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    type text NOT NULL CHECK (type IN ('email','phone','apple','google','wechat')),
    identifier citext NOT NULL,
    verified_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (type, identifier)
);
CREATE INDEX auth_identities_account_idx ON auth_identities (account_id);

CREATE TABLE auth_organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug citext NOT NULL UNIQUE,
    name text NOT NULL,
    logo_url text,
    region text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    plan text NOT NULL DEFAULT 'free',
    sso_required boolean NOT NULL DEFAULT false,
    plugin_prefix text,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_org_domains (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    domain citext NOT NULL UNIQUE,
    verified_at timestamptz,
    verification_token text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_sso_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    protocol text NOT NULL CHECK (protocol IN ('oidc','saml','cas')),
    name text NOT NULL,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_sso_org_idx ON auth_sso_connections (org_id, enabled);

CREATE TABLE auth_memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('personal','org')),
    role text NOT NULL CHECK (role IN ('owner','admin','member')),
    display_name text NOT NULL,
    avatar_url text,
    email citext,
    org_id uuid REFERENCES auth_organizations(id),
    org_name text,
    org_logo_url text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_memberships_account_idx ON auth_memberships (account_id);
CREATE UNIQUE INDEX auth_memberships_personal_uniq ON auth_memberships (account_id) WHERE kind = 'personal';

CREATE TABLE auth_verification_codes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel text NOT NULL CHECK (channel IN ('email','phone')),
    identifier citext NOT NULL,
    purpose text NOT NULL,
    code_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    attempts int NOT NULL DEFAULT 0,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_codes_lookup_idx ON auth_verification_codes (identifier, purpose, consumed_at);

CREATE TABLE auth_login_tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    purpose text NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_refresh_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES auth_memberships(id) ON DELETE CASCADE,
    device_id text,
    token_hash text NOT NULL UNIQUE,
    family_id uuid NOT NULL DEFAULT gen_random_uuid(),
    rotated_from uuid,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    ip text,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_refresh_account_idx ON auth_refresh_sessions (account_id, revoked_at);
CREATE INDEX auth_refresh_family_idx ON auth_refresh_sessions (family_id);

CREATE TABLE auth_account_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    family_id uuid NOT NULL DEFAULT gen_random_uuid(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_desktop_authorizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_secret_hash text NOT NULL,
    client_state text UNIQUE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ok','error','expired')),
    code text UNIQUE,
    error_code text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_oauth_states (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    state text NOT NULL UNIQUE,
    nonce text,
    redirect_uri text,
    code_verifier text,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_account_deletions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','cancelled')),
    challenge_hash text,
    receipt_hash text,
    channel text,
    masked_target text,
    scheduled_at timestamptz,
    executed_at timestamptz,
    restored_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_deletions_account_idx ON auth_account_deletions (account_id, status);
CREATE INDEX auth_deletions_receipt_idx ON auth_account_deletions (receipt_hash);

-- +goose Down
DROP TABLE IF EXISTS auth_account_deletions;
DROP TABLE IF EXISTS auth_oauth_states;
DROP TABLE IF EXISTS auth_desktop_authorizations;
DROP TABLE IF EXISTS auth_account_sessions;
DROP TABLE IF EXISTS auth_refresh_sessions;
DROP TABLE IF EXISTS auth_login_tickets;
DROP TABLE IF EXISTS auth_verification_codes;
DROP TABLE IF EXISTS auth_memberships;
DROP TABLE IF EXISTS auth_sso_connections;
DROP TABLE IF EXISTS auth_org_domains;
DROP TABLE IF EXISTS auth_organizations;
DROP TABLE IF EXISTS auth_identities;
DROP TABLE IF EXISTS auth_accounts;
