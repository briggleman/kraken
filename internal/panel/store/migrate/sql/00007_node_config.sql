-- +goose Up
-- Per-node configuration (backup target selection + credentials, replication),
-- one JSONB row per node. Stored as JSONB so new fields can be added without a
-- migration; credential fields are encrypted at rest by the store layer. Rows
-- are removed when their node is deleted (see DeleteNode).
CREATE TABLE node_config (
    node_id    uuid PRIMARY KEY,
    data       jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS node_config;
