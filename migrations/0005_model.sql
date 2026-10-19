-- +goose Up
CREATE TABLE ma_providers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    region text NOT NULL DEFAULT 'global',
    status text NOT NULL DEFAULT 'active',
    wire_protocol text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ma_models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id uuid NOT NULL REFERENCES ma_providers(id) ON DELETE CASCADE,
    model_key text NOT NULL,
    display_name text NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    context_window int,
    max_output_tokens int,
    pricing jsonb NOT NULL DEFAULT '{}'::jsonb,
    currency text NOT NULL DEFAULT 'USD',
    media jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','preview','deprecated','retired')),
    sort int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, model_key)
);
CREATE INDEX ma_models_provider_idx ON ma_models (provider_id, status);

CREATE TABLE ma_catalog_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version bigint NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    registry jsonb NOT NULL DEFAULT '{}'::jsonb,
    etag text,
    notes text,
    created_by text,
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ma_model_availability (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES ma_models(id) ON DELETE CASCADE,
    scope text NOT NULL DEFAULT 'all',
    target text,
    plan text,
    regions text[] NOT NULL DEFAULT '{}'::text[],
    availability text NOT NULL DEFAULT 'available' CHECK (availability IN ('available','requires_payment')),
    UNIQUE (model_id, scope, target, plan)
);

CREATE TABLE ma_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    provider_id uuid NOT NULL REFERENCES ma_providers(id) ON DELETE CASCADE,
    label text,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    key_version int NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','error','revoked')),
    last_validated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ma_credentials_account_idx ON ma_credentials (account_id, provider_id, status);

CREATE TABLE ma_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    price_minor bigint NOT NULL DEFAULT 0,
    currency text NOT NULL DEFAULT 'USD',
    interval text NOT NULL DEFAULT 'month',
    features jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ma_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    org_id uuid,
    plan_id uuid NOT NULL REFERENCES ma_plans(id),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('trialing','active','past_due','canceled')),
    current_period_start timestamptz,
    current_period_end timestamptz,
    provider_ref text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ma_subscriptions_account_idx ON ma_subscriptions (account_id, status);

CREATE TABLE ma_credit_ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    delta bigint NOT NULL,
    reason text NOT NULL,
    balance_after bigint NOT NULL,
    currency text NOT NULL DEFAULT 'USD',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ma_credit_account_idx ON ma_credit_ledger (account_id, created_at DESC);

CREATE TABLE ma_usage_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    model_id uuid,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cost_minor bigint NOT NULL DEFAULT 0,
    currency text NOT NULL DEFAULT 'USD',
    request_id text,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ma_usage_account_idx ON ma_usage_events (account_id, occurred_at DESC);

CREATE TABLE ma_quota_limits (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope text NOT NULL DEFAULT 'account',
    target uuid,
    period text NOT NULL DEFAULT 'month',
    limit_value numeric(20,2) NOT NULL DEFAULT 0,
    used_value numeric(20,2) NOT NULL DEFAULT 0,
    currency text NOT NULL DEFAULT 'USD',
    reset_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ma_quota_target_uniq ON ma_quota_limits (scope, target, period);

CREATE TABLE ma_orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    plan_id uuid,
    amount_minor bigint NOT NULL DEFAULT 0,
    currency text NOT NULL DEFAULT 'USD',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','failed','refunded')),
    provider_ref text,
    idempotency_key text UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ma_invoices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid REFERENCES ma_orders(id),
    status text NOT NULL DEFAULT 'issued',
    url text,
    issued_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ma_byok_policies (
    provider_id uuid PRIMARY KEY REFERENCES ma_providers(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT true,
    share_enabled boolean NOT NULL DEFAULT false,
    max_credentials int NOT NULL DEFAULT 5
);

CREATE TABLE ma_usage_daily (
    account_id uuid NOT NULL,
    day date NOT NULL,
    model_id uuid NOT NULL,
    tokens bigint NOT NULL DEFAULT 0,
    cost_minor bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, day, model_id)
);

CREATE TABLE ma_guides (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES ma_models(id) ON DELETE CASCADE,
    guide jsonb NOT NULL,
    revision int NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (model_id)
);

-- +goose Down
DROP TABLE IF EXISTS ma_guides;
DROP TABLE IF EXISTS ma_usage_daily;
DROP TABLE IF EXISTS ma_byok_policies;
DROP TABLE IF EXISTS ma_invoices;
DROP TABLE IF EXISTS ma_orders;
DROP TABLE IF EXISTS ma_quota_limits;
DROP TABLE IF EXISTS ma_usage_events;
DROP TABLE IF EXISTS ma_credit_ledger;
DROP TABLE IF EXISTS ma_subscriptions;
DROP TABLE IF EXISTS ma_plans;
DROP TABLE IF EXISTS ma_credentials;
DROP TABLE IF EXISTS ma_model_availability;
DROP TABLE IF EXISTS ma_catalog_revisions;
DROP TABLE IF EXISTS ma_models;
DROP TABLE IF EXISTS ma_providers;
