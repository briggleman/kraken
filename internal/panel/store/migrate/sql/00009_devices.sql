-- +goose Up
-- Phones registered for push alerts (#348, docs/design/push-alerts.md). A
-- device belongs to a user, not to a session: sessions expire daily, and a
-- device tied to one would go silent with it. The id is the app's own install
-- id, chosen on the phone, so it is text rather than a Panel-minted uuid; the
-- API accepts only a UUID and stores it in its canonical lowercase form.
-- Deleting the user deletes their devices with them.
--
-- apns_token is sealed with the Panel's secrets key when one is configured:
-- with a relay that does not authenticate its callers, a token and the
-- device's public key are all anybody needs to put an alert on the phone.
-- public_key is the device's raw 32-byte X25519 key, which is public.
CREATE TABLE device_registrations (
    id               text PRIMARY KEY,
    user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    platform         text NOT NULL,
    apns_token       text NOT NULL,
    apns_environment text NOT NULL,
    public_key       bytea NOT NULL,
    name             text NOT NULL DEFAULT '',
    rules            jsonb NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    last_sent_at     timestamptz,
    token_invalid_at timestamptz
);
CREATE INDEX idx_device_registrations_user_id ON device_registrations (user_id);

-- The Panel's random install id, sent to the push relay as X-Kraken-Panel-Id.
-- A single row like cluster_ca, and not a field in panel_settings: settings
-- are rewritten whole by every save, and an id that a stale save could drop
-- would change under the relay. The row is written by the first read
-- (store.PanelID), never by this migration, so the id comes from the Panel.
CREATE TABLE panel_identity (
    id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    panel_id   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS panel_identity;
DROP TABLE IF EXISTS device_registrations;
