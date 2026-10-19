-- +goose Up
CREATE TABLE plugin_plugins (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ghost_id text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('public','organization','personal')),
    org_id uuid,
    namespace text,
    name text NOT NULL,
    description text,
    author text,
    status text NOT NULL DEFAULT 'active',
    default_install boolean NOT NULL DEFAULT false,
    icon_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (ghost_id, scope, org_id)
);
CREATE INDEX plugin_plugins_scope_idx ON plugin_plugins (scope, org_id, status);
CREATE INDEX plugin_plugins_name_trgm_idx ON plugin_plugins USING gin (name gin_trgm_ops);

CREATE TABLE plugin_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plugin_id uuid NOT NULL REFERENCES plugin_plugins(id) ON DELETE CASCADE,
    version text NOT NULL,
    manifest jsonb NOT NULL,
    sha256 text NOT NULL,
    size_bytes bigint NOT NULL,
    icon_key text,
    status text NOT NULL DEFAULT 'active',
    review_status text NOT NULL DEFAULT 'pending' CHECK (review_status IN ('pending','approved','rejected')),
    review_reason text,
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (plugin_id, version)
);
CREATE INDEX plugin_releases_plugin_idx ON plugin_releases (plugin_id, created_at DESC);

CREATE TABLE plugin_download_tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plugin_id uuid NOT NULL,
    release_id uuid NOT NULL,
    account_id uuid NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE TABLE plugin_usage_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id text NOT NULL UNIQUE,
    plugin_id uuid NOT NULL,
    release_id uuid NOT NULL,
    account_id uuid NOT NULL,
    occurred_at timestamptz NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    rejected_code text
);
CREATE INDEX plugin_usage_plugin_idx ON plugin_usage_events (plugin_id, occurred_at DESC);

CREATE TABLE plugin_usage_daily (
    plugin_id uuid NOT NULL,
    release_id uuid NOT NULL,
    day date NOT NULL,
    event_count bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (plugin_id, release_id, day)
);

CREATE TABLE plugin_org_prefixes (
    org_id uuid PRIMARY KEY,
    prefix text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE plugin_upload_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    org_id uuid,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    object_key text NOT NULL,
    status text NOT NULL DEFAULT 'awaiting_upload',
    put_url_expires_at timestamptz NOT NULL,
    failure_code text,
    plugin_id uuid,
    release_id uuid,
    ghost_id text,
    version text,
    review_status text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX plugin_uploads_account_idx ON plugin_upload_sessions (account_id, created_at DESC);

CREATE TABLE plugin_removals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plugin_id uuid NOT NULL,
    ghost_id text NOT NULL,
    scope text NOT NULL,
    org_id uuid,
    namespace text,
    action text NOT NULL DEFAULT 'purge',
    removed_at timestamptz NOT NULL DEFAULT now()
);

-- Skill Hub
CREATE TABLE sk_skills (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    owner_account_id uuid NOT NULL,
    owner_type text NOT NULL DEFAULT 'personal',
    display_name text NOT NULL,
    summary text,
    description text,
    visibility text NOT NULL DEFAULT 'public' CHECK (visibility IN ('private','shared','public')),
    status text NOT NULL DEFAULT 'active',
    latest_version text,
    icon_key text,
    github_url text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sk_skills_visibility_idx ON sk_skills (visibility, status);
CREATE INDEX sk_skills_display_trgm_idx ON sk_skills USING gin (display_name gin_trgm_ops);

CREATE TABLE sk_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id uuid NOT NULL REFERENCES sk_skills(id) ON DELETE CASCADE,
    version text NOT NULL,
    folder_hash text,
    zip_sha256 text NOT NULL,
    file_size bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    scan_status text NOT NULL DEFAULT 'pending',
    rejection_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (skill_id, version)
);

CREATE TABLE sk_files (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version_id uuid NOT NULL REFERENCES sk_versions(id) ON DELETE CASCADE,
    path text NOT NULL,
    size_bytes bigint NOT NULL,
    language text,
    sha256 text,
    truncated boolean NOT NULL DEFAULT false
);
CREATE INDEX sk_files_version_idx ON sk_files (version_id);

CREATE TABLE sk_categories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL,
    name text NOT NULL,
    parent_id uuid REFERENCES sk_categories(id),
    source text NOT NULL DEFAULT 'system',
    UNIQUE (slug, parent_id)
);

CREATE TABLE sk_skill_categories (
    skill_id uuid NOT NULL REFERENCES sk_skills(id) ON DELETE CASCADE,
    category_id uuid NOT NULL REFERENCES sk_categories(id) ON DELETE CASCADE,
    PRIMARY KEY (skill_id, category_id)
);

CREATE TABLE sk_tags (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    source text NOT NULL DEFAULT 'system'
);

CREATE TABLE sk_skill_tags (
    skill_id uuid NOT NULL REFERENCES sk_skills(id) ON DELETE CASCADE,
    tag_id uuid NOT NULL REFERENCES sk_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (skill_id, tag_id)
);

CREATE TABLE sk_teams (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    type text NOT NULL DEFAULT 'team',
    source text NOT NULL DEFAULT 'system',
    is_personal boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sk_team_members (
    team_id uuid NOT NULL REFERENCES sk_teams(id) ON DELETE CASCADE,
    account_id uuid NOT NULL,
    role text NOT NULL DEFAULT 'member',
    PRIMARY KEY (team_id, account_id)
);

CREATE TABLE sk_departments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid,
    slug text NOT NULL,
    name text NOT NULL,
    parent_id uuid REFERENCES sk_departments(id),
    UNIQUE (org_id, slug)
);

CREATE TABLE sk_visibility_shares (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id uuid NOT NULL REFERENCES sk_skills(id) ON DELETE CASCADE,
    team_id uuid REFERENCES sk_teams(id) ON DELETE CASCADE,
    dept_id uuid REFERENCES sk_departments(id) ON DELETE CASCADE
);

CREATE TABLE sk_auto_install (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL,
    skill_id uuid NOT NULL REFERENCES sk_skills(id) ON DELETE CASCADE,
    version text NOT NULL,
    audience jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, skill_id)
);

CREATE TABLE sk_download_tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id uuid NOT NULL,
    version_id uuid NOT NULL,
    account_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sk_publish_uploads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    slug text NOT NULL,
    version text NOT NULL,
    oss_key text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS sk_publish_uploads;
DROP TABLE IF EXISTS sk_download_tickets;
DROP TABLE IF EXISTS sk_auto_install;
DROP TABLE IF EXISTS sk_visibility_shares;
DROP TABLE IF EXISTS sk_departments;
DROP TABLE IF EXISTS sk_team_members;
DROP TABLE IF EXISTS sk_teams;
DROP TABLE IF EXISTS sk_skill_tags;
DROP TABLE IF EXISTS sk_tags;
DROP TABLE IF EXISTS sk_skill_categories;
DROP TABLE IF EXISTS sk_categories;
DROP TABLE IF EXISTS sk_files;
DROP TABLE IF EXISTS sk_versions;
DROP TABLE IF EXISTS sk_skills;
DROP TABLE IF EXISTS plugin_removals;
DROP TABLE IF EXISTS plugin_upload_sessions;
DROP TABLE IF EXISTS plugin_org_prefixes;
DROP TABLE IF EXISTS plugin_usage_daily;
DROP TABLE IF EXISTS plugin_usage_events;
DROP TABLE IF EXISTS plugin_download_tickets;
DROP TABLE IF EXISTS plugin_releases;
DROP TABLE IF EXISTS plugin_plugins;
