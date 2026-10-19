-- +goose Up
CREATE TABLE upd_desktop_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    platform text NOT NULL,
    arch text NOT NULL,
    channel text NOT NULL DEFAULT 'release' CHECK (channel IN ('release','beta','canary')),
    version text NOT NULL,
    manifest jsonb NOT NULL,
    files jsonb NOT NULL DEFAULT '{}'::jsonb,
    rollout int NOT NULL DEFAULT 100,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (platform, arch, channel, version)
);

CREATE TABLE upd_mobile_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    platform text NOT NULL,
    channel text NOT NULL DEFAULT 'release',
    version text NOT NULL,
    build_number text NOT NULL,
    runtime_version text NOT NULL,
    install_url text,
    itms_url text,
    min_version text,
    release_notes text,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (platform, channel, version)
);

CREATE TABLE upd_endpoint_manifests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    region text NOT NULL,
    version int NOT NULL DEFAULT 1,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (region, version)
);

CREATE TABLE upd_wallpapers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('video','webp')),
    sha256 text NOT NULL UNIQUE,
    url text NOT NULL,
    size_bytes bigint NOT NULL,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hb_daily (
    account_id uuid NOT NULL,
    day date NOT NULL,
    platform text,
    version text,
    first_seen timestamptz NOT NULL DEFAULT now(),
    last_seen timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, day)
);
CREATE INDEX hb_daily_day_idx ON hb_daily (day);

CREATE TABLE gh_issues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    type text NOT NULL CHECK (type IN ('bug','feature')),
    title text NOT NULL,
    description text,
    app_version text,
    github_number int,
    url text,
    status text NOT NULL DEFAULT 'submitted',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gh_issues_account_idx ON gh_issues (account_id, created_at DESC);

CREATE TABLE hook_channels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL CHECK (provider IN ('slack','telegram','x')),
    external_id text NOT NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, external_id)
);

CREATE TABLE hook_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    channel_id uuid NOT NULL REFERENCES hook_channels(id) ON DELETE CASCADE,
    external_user_id text NOT NULL,
    device_id text,
    status text NOT NULL DEFAULT 'active',
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (channel_id, external_user_id)
);
CREATE INDEX hook_bindings_account_idx ON hook_bindings (account_id);

CREATE TABLE hook_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id uuid NOT NULL REFERENCES hook_bindings(id) ON DELETE CASCADE,
    task_id text,
    session_id text,
    external_key text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    last_message_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id, external_key)
);

CREATE TABLE hook_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid REFERENCES hook_sessions(id) ON DELETE CASCADE,
    direction text NOT NULL CHECK (direction IN ('in','out')),
    type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hook_messages_session_idx ON hook_messages (session_id, created_at DESC);

CREATE TABLE hook_confirmations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid REFERENCES hook_sessions(id) ON DELETE CASCADE,
    request_id text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','expired')),
    decision jsonb,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, request_id)
);

CREATE TABLE hook_group_relays (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id uuid NOT NULL REFERENCES hook_channels(id) ON DELETE CASCADE,
    group_id text NOT NULL,
    binding_id uuid,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hook_delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id uuid,
    attempt int NOT NULL DEFAULT 1,
    status text NOT NULL,
    error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS hook_delivery_attempts;
DROP TABLE IF EXISTS hook_group_relays;
DROP TABLE IF EXISTS hook_confirmations;
DROP TABLE IF EXISTS hook_messages;
DROP TABLE IF EXISTS hook_sessions;
DROP TABLE IF EXISTS hook_bindings;
DROP TABLE IF EXISTS hook_channels;
DROP TABLE IF EXISTS gh_issues;
DROP TABLE IF EXISTS hb_daily;
DROP TABLE IF EXISTS upd_wallpapers;
DROP TABLE IF EXISTS upd_endpoint_manifests;
DROP TABLE IF EXISTS upd_mobile_releases;
DROP TABLE IF EXISTS upd_desktop_releases;
