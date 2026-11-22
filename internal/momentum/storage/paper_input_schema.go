package storage

import "strconv"

import "bybit_bot_liquidity/internal/momentum/paperwire"

// momentumPaperInputSchema сохраняет immutable exact bytes и непосредственную связь.
func momentumPaperInputSchema() string {
	return `CREATE TABLE momentum.paper_run_inputs (
 run_id TEXT COLLATE "C" PRIMARY KEY REFERENCES momentum.rebalance_runs(run_id),
 environment TEXT COLLATE "C" NOT NULL CHECK (environment='paper'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 strategy_id TEXT COLLATE "C" NOT NULL CHECK (strategy_id ~ '[^[:space:]]'),
 sequence NUMERIC NOT NULL CHECK (sequence BETWEEN 1 AND 18446744073709551615 AND sequence=pg_catalog.trunc(sequence)),
 predecessor_sequence NUMERIC GENERATED ALWAYS AS (CASE WHEN sequence>1 THEN sequence-1 END) STORED,
 previous_input_hash BYTEA,
 previous_resolution_hash BYTEA,
 input_hash BYTEA NOT NULL CHECK (octet_length(input_hash)=32),
 payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND ` + strconv.Itoa(paperwire.MaxCanonicalBytes) + `),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(created_at)),
 CHECK (input_hash=pg_catalog.sha256(payload)),
 CHECK ((sequence=1 AND previous_input_hash IS NULL AND previous_resolution_hash IS NULL)
 OR (sequence>1 AND octet_length(previous_input_hash)=32 AND previous_input_hash IS NOT NULL
 AND octet_length(previous_resolution_hash)=32 AND previous_resolution_hash IS NOT NULL)),
 UNIQUE(environment,account_id,strategy_id,sequence),
 UNIQUE(environment,account_id,strategy_id,sequence,input_hash),
 FOREIGN KEY(environment,account_id,strategy_id,predecessor_sequence,previous_input_hash)
 REFERENCES momentum.paper_run_inputs(environment,account_id,strategy_id,sequence,input_hash)
);
CREATE TRIGGER paper_run_inputs_immutable BEFORE UPDATE OR DELETE ON momentum.paper_run_inputs
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_rebalance_run_mutation();
CREATE TRIGGER paper_run_inputs_no_truncate BEFORE TRUNCATE ON momentum.paper_run_inputs
 FOR EACH STATEMENT EXECUTE FUNCTION momentum.reject_rebalance_run_mutation();`
}
