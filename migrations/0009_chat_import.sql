-- +goose Up
CREATE TABLE chat_import_progress (
    room_id uuid PRIMARY KEY REFERENCES chat_rooms(id) ON DELETE CASCADE,
    last_source_id text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS chat_import_progress;
