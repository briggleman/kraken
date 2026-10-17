-- +goose Up
-- Account invites (#451): a one-time, expiring link that lets a newly created
-- user choose their own first password, sent by email. The link's bearer
-- token is never stored — token_hash is its SHA-256 digest, as sessions are
-- stored, so a dump of this table holds nothing anyone could redeem.
--
-- sent_at is when the relay accepted the message. A row whose send failed
-- keeps it NULL and can never be redeemed: a link nobody received must not
-- be a link somebody could guess. consumed_at and revoked_at close a row;
-- an open row is the user's current invite, and at most one may be open per
-- user (the partial unique index), so sending again revokes before it
-- inserts. created_by is the administrator who sent it, kept as a plain
-- reference rather than a foreign key so deleting that administrator does
-- not disturb the record of who invited whom.
--
-- Deleting the user deletes their invites with them.
CREATE TABLE user_invites (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    sent_to     text NOT NULL,
    created_by  uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    sent_at     timestamptz,
    consumed_at timestamptz,
    revoked_at  timestamptz
);

CREATE UNIQUE INDEX user_invites_one_open ON user_invites (user_id)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
CREATE INDEX user_invites_created_at ON user_invites (created_at);

-- +goose Down
DROP TABLE IF EXISTS user_invites;
