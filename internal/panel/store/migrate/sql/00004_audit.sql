-- +goose Up
CREATE TABLE audit_log (
    id    uuid PRIMARY KEY,
    ts    timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL,
    data  jsonb NOT NULL
);
CREATE INDEX idx_audit_log_ts ON audit_log (ts DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
