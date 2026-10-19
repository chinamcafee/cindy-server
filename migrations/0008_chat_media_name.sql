-- +goose Up
ALTER TABLE chat_media ADD COLUMN IF NOT EXISTS original_name text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_media DROP COLUMN IF EXISTS original_name;
