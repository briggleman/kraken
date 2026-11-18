-- +goose Up
-- Server share links (#475): a read-only capability link to one server's
-- public card, meant to be pasted into a chat where it unfurls. The bearer
-- token is never stored — token_hash is its SHA-256 digest, as sessions and
-- invites are stored, so a dump of this table holds no usable links.
--
-- A link is never consumed: a chat service fetches it several times and every
-- friend who taps it fetches it again. Reading it only moves last_seen_at. It
-- closes when revoked_at is set or, if expires_at was chosen, once that
-- passes; a NULL expires_at lives until it is revoked.
--
-- show_address and show_password widen what the public card shows beyond
-- status and players; the spec's join block decides which values they cover.
-- label is the operator's own name for the link and is never public.
-- created_by is a plain reference rather than a foreign key, so deleting the
-- user who made a link leaves the link working until someone revokes it.
--
-- Deleting the server deletes its links with it.
CREATE TABLE server_share_links (
    id            uuid PRIMARY KEY,
    server_id     uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    token_hash    text NOT NULL UNIQUE,
    label         text NOT NULL DEFAULT '',
    show_address  boolean NOT NULL DEFAULT false,
    show_password boolean NOT NULL DEFAULT false,
    created_by    uuid,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz,
    revoked_at    timestamptz,
    last_seen_at  timestamptz
);

CREATE INDEX server_share_links_server ON server_share_links (server_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS server_share_links;
