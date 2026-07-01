-- +goose Up
CREATE TABLE schedules (
    id         uuid PRIMARY KEY,
    server_id  uuid NOT NULL,
    action     text NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    data       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_schedules_server_id ON schedules (server_id);

-- +goose Down
DROP TABLE IF EXISTS schedules;
