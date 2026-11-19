package storage

const momentumPlanRevisionSchema = `CREATE TABLE momentum.plan_revisions (
 run_id TEXT COLLATE "C" NOT NULL REFERENCES momentum.rebalance_runs(run_id),
 revision NUMERIC NOT NULL CHECK (revision >= 1 AND revision <= 18446744073709551615
   AND revision = pg_catalog.trunc(revision)),
 plan_hash BYTEA NOT NULL CHECK (pg_catalog.octet_length(plan_hash) = 32),
 previous_hash BYTEA,
 predecessor_revision NUMERIC GENERATED ALWAYS AS (
   CASE WHEN revision > 1 THEN revision - 1 END) STORED,
 payload BYTEA NOT NULL CHECK (pg_catalog.octet_length(payload) > 0),
 payload_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(payload_sha256) = 32
   AND payload_sha256 = pg_catalog.sha256(payload)),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(created_at)
   AND created_at >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 PRIMARY KEY (run_id, revision),
 UNIQUE (run_id, revision, plan_hash),
 CHECK ((revision = 1 AND previous_hash IS NULL)
   OR (revision > 1 AND previous_hash IS NOT NULL AND pg_catalog.octet_length(previous_hash) = 32)),
 FOREIGN KEY (run_id, predecessor_revision, previous_hash)
   REFERENCES momentum.plan_revisions(run_id, revision, plan_hash)
);
CREATE FUNCTION momentum.reject_plan_revision_mutation() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum plan revision is immutable';
 END;
 $body$;
CREATE TRIGGER plan_revisions_immutable BEFORE UPDATE OR DELETE
 ON momentum.plan_revisions FOR EACH ROW
 EXECUTE FUNCTION momentum.reject_plan_revision_mutation();`
