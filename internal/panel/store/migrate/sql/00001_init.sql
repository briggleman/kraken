-- +goose Up
CREATE TABLE roles (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    builtin     boolean NOT NULL DEFAULT false,
    permissions jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    email         text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    role_id       text NOT NULL,
    disabled      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE specs (
    id         uuid PRIMARY KEY,
    slug       text NOT NULL UNIQUE,
    version    integer NOT NULL DEFAULT 1,
    data       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
    id      uuid PRIMARY KEY,
    name    text NOT NULL,
    os      text NOT NULL,
    status  text NOT NULL,
    address text NOT NULL,
    data    jsonb NOT NULL
);

CREATE TABLE sessions (
    token      text PRIMARY KEY,
    user_id    uuid NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- +goose Down
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS specs;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS roles;
