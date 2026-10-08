-- +goose Up
-- The Steam build check (#392): the build a server's install tree holds, the
-- build Steam has on its branch, and when and how the last check went. Real
-- columns rather than fields in `data`, because the check is written by a
-- background job while every other writer rewrites the whole `data` document;
-- kept apart, neither can undo the other (see store.ServerBuild). All
-- nullable: a server that has never been checked has none of them.
ALTER TABLE servers
    ADD COLUMN installed_build    text,
    ADD COLUMN available_build    text,
    ADD COLUMN available_build_at timestamptz,
    ADD COLUMN update_checked_at  timestamptz,
    ADD COLUMN update_check_error text;

-- +goose Down
ALTER TABLE servers
    DROP COLUMN IF EXISTS installed_build,
    DROP COLUMN IF EXISTS available_build,
    DROP COLUMN IF EXISTS available_build_at,
    DROP COLUMN IF EXISTS update_checked_at,
    DROP COLUMN IF EXISTS update_check_error;
