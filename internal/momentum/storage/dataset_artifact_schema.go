package storage

const momentumDatasetArtifactSchema = `CREATE TABLE momentum.dataset_artifacts (
 artifact_hash BYTEA PRIMARY KEY CHECK (pg_catalog.octet_length(artifact_hash) = 32),
 payload BYTEA NOT NULL CHECK (pg_catalog.octet_length(payload) > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
   CHECK (pg_catalog.isfinite(created_at) AND created_at >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 CHECK (artifact_hash = pg_catalog.sha256(payload))
);
CREATE FUNCTION momentum.reject_dataset_artifact_mutation() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum dataset artifact is immutable';
 END;
 $body$;
CREATE TRIGGER dataset_artifacts_immutable
 BEFORE UPDATE OR DELETE OR TRUNCATE ON momentum.dataset_artifacts
 FOR EACH STATEMENT EXECUTE FUNCTION momentum.reject_dataset_artifact_mutation();`
