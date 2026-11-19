package storage

const momentumRebalanceRunSchema = `CREATE TABLE momentum.rebalance_runs (
 run_id TEXT COLLATE "C" PRIMARY KEY CHECK (run_id ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 strategy_id TEXT COLLATE "C" NOT NULL CHECK (strategy_id ~ '[^[:space:]]'),
 scheduled_at TIMESTAMPTZ NOT NULL CHECK (isfinite(scheduled_at)
   AND scheduled_at >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 onboarding_id TEXT COLLATE "C" CHECK (onboarding_id ~ '[^[:space:]]'),
 config_hash BYTEA NOT NULL REFERENCES momentum.config_snapshots(config_hash),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(created_at)
   AND created_at >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 UNIQUE (account_id, strategy_id, onboarding_id)
);
CREATE UNIQUE INDEX rebalance_runs_weekly_key
 ON momentum.rebalance_runs(account_id, strategy_id, scheduled_at)
 WHERE onboarding_id IS NULL;
CREATE FUNCTION momentum.reject_rebalance_run_mutation() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum run identity is immutable';
 END;
 $body$;
CREATE TRIGGER rebalance_runs_identity_immutable
 BEFORE UPDATE OF run_id, account_id, strategy_id, scheduled_at, onboarding_id, config_hash, created_at
 OR DELETE ON momentum.rebalance_runs
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_rebalance_run_mutation();`
