-- +goose Up
CREATE TABLE servers (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    spec_id    uuid NOT NULL,
    node_id    uuid NOT NULL,
    state      text NOT NULL,
    data       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_servers_node_id ON servers (node_id);
CREATE INDEX idx_servers_spec_id ON servers (spec_id);

-- +goose Down
DROP TABLE IF EXISTS servers;
