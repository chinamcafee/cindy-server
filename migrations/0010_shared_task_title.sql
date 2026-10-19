-- +goose Up
ALTER TABLE dl_shared_tasks ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT 'Shared task';

-- +goose Down
ALTER TABLE dl_shared_tasks DROP COLUMN IF EXISTS title;
