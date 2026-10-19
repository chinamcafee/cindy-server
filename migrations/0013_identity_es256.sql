-- +goose Up
ALTER TABLE dl_identity_challenges
    ADD COLUMN IF NOT EXISTS account_id uuid,
    ADD COLUMN IF NOT EXISTS membership_id uuid,
    ADD COLUMN IF NOT EXISTS identity_id uuid,
    ADD COLUMN IF NOT EXISTS public_key text,
    ADD COLUMN IF NOT EXISTS thumbprint text,
    ADD COLUMN IF NOT EXISTS message text,
    ADD COLUMN IF NOT EXISTS purpose text;

ALTER TABLE dl_device_identities
    ADD COLUMN IF NOT EXISTS membership_id uuid,
    ADD COLUMN IF NOT EXISTS thumbprint text;

-- +goose Down
ALTER TABLE dl_device_identities DROP COLUMN IF EXISTS thumbprint;
ALTER TABLE dl_device_identities DROP COLUMN IF EXISTS membership_id;
ALTER TABLE dl_identity_challenges
    DROP COLUMN IF EXISTS purpose,
    DROP COLUMN IF EXISTS message,
    DROP COLUMN IF EXISTS thumbprint,
    DROP COLUMN IF EXISTS public_key,
    DROP COLUMN IF EXISTS identity_id,
    DROP COLUMN IF EXISTS membership_id,
    DROP COLUMN IF EXISTS account_id;
