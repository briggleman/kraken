-- +goose Up
-- Single-row table holding the self-generated CA used to sign Agent enrollment
-- requests when no external CA (KRAKEN_CA_CERT/KRAKEN_CA_KEY) is configured.
-- Persisting it is critical: a regenerated CA would invalidate every enrolled Agent.
CREATE TABLE cluster_ca (
    id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    cert_pem   bytea NOT NULL,
    key_pem    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- First-run security: the bootstrap admin must rotate its password before the
-- session can do anything else.
ALTER TABLE users ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS must_change_password;
DROP TABLE IF EXISTS cluster_ca;
