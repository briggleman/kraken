-- +goose Up
-- Shared servers (#146). Two changes, one reason: every other writer of a
-- server rewrites its whole `data` document, so anything changed apart from
-- those writers needs a home outside it, or a stale copy written back would
-- undo it (the build check, 00008, is the same story).
--
-- owner_id moves out of `data` into a real column, written at create and then
-- only by an ownership transfer. Backfilled from the document; an owner id
-- that names no user (deleted before this column existed) is left NULL, which
-- is what it already meant: nobody owns it. Deleting a user now clears the
-- column the same way.
ALTER TABLE servers
    ADD COLUMN owner_id uuid REFERENCES users (id) ON DELETE SET NULL;

UPDATE servers SET owner_id = u.id
FROM users u
WHERE u.id::text = servers.data->>'owner_id';

CREATE INDEX idx_servers_owner_id ON servers (owner_id);

-- server_grants lets a user reach a server beside its owner. A grant makes
-- the server visible and nothing more; the user's role decides what they may
-- do on it. It goes when either its server or its user does. granted_by is a
-- plain reference, not a foreign key, so deleting whoever made a grant leaves
-- the grant in place.
CREATE TABLE server_grants (
    server_id  uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    granted_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, user_id)
);

CREATE INDEX idx_server_grants_user_id ON server_grants (user_id);

-- +goose Down
DROP TABLE IF EXISTS server_grants;
ALTER TABLE servers DROP COLUMN IF EXISTS owner_id;
