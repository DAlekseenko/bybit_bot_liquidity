package storage

const momentumConfigSnapshotSchema = `CREATE TABLE momentum.config_snapshots (
 config_hash BYTEA PRIMARY KEY CHECK (pg_catalog.octet_length(config_hash) = 32),
 payload BYTEA NOT NULL CHECK (pg_catalog.octet_length(payload) > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
   CHECK (pg_catalog.isfinite(created_at) AND created_at >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 CHECK (config_hash = pg_catalog.sha256(payload))
);`
