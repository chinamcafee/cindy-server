-- +goose Up
CREATE TABLE dl_provider_share_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id uuid NOT NULL REFERENCES dl_provider_shares(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dl_provider_share_links_share_idx ON dl_provider_share_links (share_id, expires_at);

-- +goose Down
DROP TABLE IF EXISTS dl_provider_share_links;
