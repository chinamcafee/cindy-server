-- +goose Up
CREATE TABLE dl_devices (
    id text PRIMARY KEY,
    account_id uuid NOT NULL,
    name text NOT NULL,
    platform text,
    app_version text,
    device_info jsonb NOT NULL DEFAULT '{}'::jsonb,
    remote_control_enabled boolean NOT NULL DEFAULT false,
    busy boolean NOT NULL DEFAULT false,
    last_seen_at timestamptz,
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_devices_account_idx ON dl_devices (account_id, removed_at);

CREATE TABLE dl_push_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    device_id text NOT NULL,
    token text NOT NULL UNIQUE,
    platform text NOT NULL,
    provider text NOT NULL DEFAULT 'apns',
    app_variant text NOT NULL DEFAULT 'global',
    apns_env text NOT NULL DEFAULT 'prod',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_push_account_idx ON dl_push_tokens (account_id, device_id);

CREATE TABLE dl_shared_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id text NOT NULL,
    owner_account_id uuid NOT NULL,
    host_device_id text NOT NULL,
    revision int NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','closed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);
CREATE INDEX dl_shared_tasks_owner_idx ON dl_shared_tasks (owner_account_id, status);

CREATE TABLE dl_shared_task_guests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    shared_task_id uuid NOT NULL REFERENCES dl_shared_tasks(id) ON DELETE CASCADE,
    member_id uuid NOT NULL,
    account_id uuid NOT NULL,
    version int NOT NULL DEFAULT 1,
    device_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    joined_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz
);
CREATE INDEX dl_shared_guests_task_idx ON dl_shared_task_guests (shared_task_id, removed_at);
CREATE INDEX dl_shared_guests_account_idx ON dl_shared_task_guests (account_id);

CREATE TABLE dl_shared_task_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    shared_task_id uuid NOT NULL REFERENCES dl_shared_tasks(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dl_provider_shares (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_account_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_label text NOT NULL,
    host_device_id text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','revoked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_shares_owner_idx ON dl_provider_shares (owner_account_id, status);

CREATE TABLE dl_provider_share_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id uuid NOT NULL REFERENCES dl_provider_shares(id) ON DELETE CASCADE,
    member_id uuid NOT NULL,
    account_id uuid NOT NULL,
    display_name text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','revoked')),
    version int NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_share_members_share_idx ON dl_provider_share_members (share_id);
CREATE INDEX dl_share_members_account_idx ON dl_provider_share_members (account_id);

CREATE TABLE dl_provider_share_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id uuid NOT NULL REFERENCES dl_provider_shares(id) ON DELETE CASCADE,
    account_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','withdrawn','expired')),
    pairing_code text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);
CREATE INDEX dl_share_requests_share_idx ON dl_provider_share_requests (share_id, status);

CREATE TABLE dl_provider_share_guest_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id uuid NOT NULL REFERENCES dl_provider_shares(id) ON DELETE CASCADE,
    member_id uuid NOT NULL,
    credential_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dl_device_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    device_id text NOT NULL,
    public_key text NOT NULL,
    algorithm text NOT NULL DEFAULT 'ES256',
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    UNIQUE (device_id)
);

CREATE TABLE dl_identity_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id text NOT NULL,
    path text NOT NULL,
    token_hash text,
    body_hash text,
    nonce text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_identity_challenges_device_idx ON dl_identity_challenges (device_id, expires_at);

-- +goose Down
DROP TABLE IF EXISTS dl_identity_challenges;
DROP TABLE IF EXISTS dl_device_identities;
DROP TABLE IF EXISTS dl_provider_share_guest_credentials;
DROP TABLE IF EXISTS dl_provider_share_requests;
DROP TABLE IF EXISTS dl_provider_share_members;
DROP TABLE IF EXISTS dl_provider_shares;
DROP TABLE IF EXISTS dl_shared_task_invitations;
DROP TABLE IF EXISTS dl_shared_task_guests;
DROP TABLE IF EXISTS dl_shared_tasks;
DROP TABLE IF EXISTS dl_push_tokens;
DROP TABLE IF EXISTS dl_devices;
