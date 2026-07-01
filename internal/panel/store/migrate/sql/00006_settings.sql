-- +goose Up
-- Single-row table holding Panel-global settings (e.g. the Cloudflare API token).
-- Stored as JSONB so new settings can be added as struct fields without a migration.
CREATE TABLE panel_settings (
    id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    data       jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS panel_settings;
