package storage

// momentumControlRiskSchema хранит состояние управления (active/pause/halt) как
// неизменяемую цепочку ревизий, снимки equity, события риска и метаданные
// владельца исполнения. Область — environment + account + strategy; правил
// конкретной стратегии схема не содержит. Выход из HALTED возможен только
// подтверждённой командой оператора RESUME со ссылкой на сверку.
const momentumControlRiskSchema = `CREATE TABLE momentum.control_revisions (
 environment TEXT COLLATE "C" NOT NULL CHECK (environment ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 strategy_id TEXT COLLATE "C" NOT NULL CHECK (strategy_id ~ '[^[:space:]]'),
 revision BIGINT NOT NULL CHECK (revision >= 1),
 predecessor_revision BIGINT GENERATED ALWAYS AS (CASE WHEN revision > 1 THEN revision - 1 END) STORED,
 mode TEXT NOT NULL CHECK (mode IN ('ACTIVE', 'PAUSED_ENTRIES', 'HALTED')),
 action TEXT NOT NULL CHECK (action IN ('INITIALIZE', 'PAUSE_ENTRIES', 'HALT', 'RESUME')),
 actor TEXT NOT NULL CHECK (actor IN ('OPERATOR', 'RISK_CONTROLLER', 'RECONCILIATION', 'SYSTEM')),
 reason TEXT COLLATE "C" NOT NULL CHECK (reason ~ '[^[:space:]]'),
 config_hash BYTEA NOT NULL REFERENCES momentum.config_snapshots(config_hash),
 confirmation_id TEXT COLLATE "C" CHECK (confirmation_id ~ '[^[:space:]]'),
 reconciliation_id TEXT COLLATE "C" CHECK (reconciliation_id ~ '[^[:space:]]'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(created_at)),
 PRIMARY KEY (environment, account_id, strategy_id, revision),
 FOREIGN KEY (environment, account_id, strategy_id, predecessor_revision)
   REFERENCES momentum.control_revisions(environment, account_id, strategy_id, revision),
 CHECK ((revision = 1) = (action = 'INITIALIZE')),
 CHECK (action <> 'PAUSE_ENTRIES' OR mode = 'PAUSED_ENTRIES'),
 CHECK (action <> 'HALT' OR mode = 'HALTED'),
 CHECK (action <> 'RESUME' OR (mode = 'ACTIVE' AND actor = 'OPERATOR'
   AND confirmation_id IS NOT NULL AND reconciliation_id IS NOT NULL))
);
CREATE TABLE momentum.equity_snapshots (
 environment TEXT COLLATE "C" NOT NULL CHECK (environment ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 strategy_id TEXT COLLATE "C" NOT NULL CHECK (strategy_id ~ '[^[:space:]]'),
 observed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(observed_at)),
 source TEXT NOT NULL CHECK (source IN ('WEBSOCKET', 'REST', 'SIMULATION')),
 wallet_usdt NUMERIC NOT NULL CHECK (wallet_usdt > '-Infinity'::NUMERIC AND wallet_usdt < 'Infinity'::NUMERIC),
 unrealized_pnl_usdt NUMERIC NOT NULL
   CHECK (unrealized_pnl_usdt > '-Infinity'::NUMERIC AND unrealized_pnl_usdt < 'Infinity'::NUMERIC),
 equity_usdt NUMERIC NOT NULL CHECK (equity_usdt = wallet_usdt + unrealized_pnl_usdt),
 data_fresh BOOLEAN NOT NULL,
 PRIMARY KEY (environment, account_id, strategy_id, observed_at, source)
);
CREATE TABLE momentum.risk_events (
 event_id TEXT COLLATE "C" PRIMARY KEY CHECK (event_id ~ '[^[:space:]]'),
 environment TEXT COLLATE "C" NOT NULL,
 account_id TEXT COLLATE "C" NOT NULL,
 strategy_id TEXT COLLATE "C" NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('EQUITY_STOP', 'STALE_DATA', 'UNKNOWN_POSITION',
   'UNKNOWN_ORDER', 'LOCK_LOST', 'RECONCILIATION_MISMATCH')),
 threshold_usdt NUMERIC CHECK (threshold_usdt > '-Infinity'::NUMERIC AND threshold_usdt < 'Infinity'::NUMERIC),
 equity_usdt NUMERIC CHECK (equity_usdt > '-Infinity'::NUMERIC AND equity_usdt < 'Infinity'::NUMERIC),
 config_hash BYTEA NOT NULL REFERENCES momentum.config_snapshots(config_hash),
 control_revision BIGINT,
 detail TEXT COLLATE "C" NOT NULL CHECK (detail ~ '[^[:space:]]'),
 occurred_at TIMESTAMPTZ NOT NULL CHECK (isfinite(occurred_at)),
 CHECK (kind <> 'EQUITY_STOP' OR (threshold_usdt IS NOT NULL AND equity_usdt IS NOT NULL
   AND equity_usdt <= threshold_usdt AND control_revision IS NOT NULL)),
 FOREIGN KEY (environment, account_id, strategy_id, control_revision)
   REFERENCES momentum.control_revisions(environment, account_id, strategy_id, revision)
);
CREATE TABLE momentum.execution_owner_sessions (
 session_id TEXT COLLATE "C" PRIMARY KEY CHECK (session_id ~ '[^[:space:]]'),
 environment TEXT COLLATE "C" NOT NULL CHECK (environment ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 strategy_id TEXT COLLATE "C" NOT NULL CHECK (strategy_id ~ '[^[:space:]]'),
 host TEXT COLLATE "C" NOT NULL CHECK (host ~ '[^[:space:]]'),
 process_id BIGINT NOT NULL CHECK (process_id > 0),
 started_at TIMESTAMPTZ NOT NULL CHECK (isfinite(started_at)),
 last_seen_at TIMESTAMPTZ NOT NULL CHECK (isfinite(last_seen_at) AND last_seen_at >= started_at),
 ended_at TIMESTAMPTZ CHECK (isfinite(ended_at) AND ended_at >= started_at),
 end_reason TEXT CHECK (end_reason IN ('CLEAN_SHUTDOWN', 'LOCK_LOST', 'STALE_TAKEOVER')),
 CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
CREATE UNIQUE INDEX execution_owner_sessions_single_active
 ON momentum.execution_owner_sessions(environment, account_id, strategy_id) WHERE ended_at IS NULL;
CREATE FUNCTION momentum.reject_control_record_mutation() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum control record is immutable';
 END;
 $body$;
CREATE TRIGGER control_revisions_immutable BEFORE UPDATE OR DELETE ON momentum.control_revisions
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_control_record_mutation();
CREATE TRIGGER equity_snapshots_immutable BEFORE UPDATE OR DELETE ON momentum.equity_snapshots
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_control_record_mutation();
CREATE TRIGGER risk_events_immutable BEFORE UPDATE OR DELETE ON momentum.risk_events
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_control_record_mutation();
CREATE FUNCTION momentum.require_halt_resume() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 DECLARE previous_mode TEXT;
 BEGIN
   -- Generated predecessor_revision ещё не вычислена в BEFORE-триггере.
   SELECT c.mode INTO previous_mode FROM momentum.control_revisions c
    WHERE c.environment = NEW.environment AND c.account_id = NEW.account_id
      AND c.strategy_id = NEW.strategy_id AND c.revision = NEW.revision - 1;
   IF previous_mode = 'HALTED' AND NEW.mode <> 'HALTED' AND NEW.action <> 'RESUME' THEN
     RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'momentum halt can be left only by confirmed RESUME';
   END IF;
   RETURN NEW;
 END;
 $body$;
CREATE TRIGGER control_revisions_require_resume BEFORE INSERT ON momentum.control_revisions
 FOR EACH ROW EXECUTE FUNCTION momentum.require_halt_resume();
CREATE FUNCTION momentum.guard_owner_session() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   IF TG_OP = 'DELETE' OR OLD.ended_at IS NOT NULL
      OR (NEW.session_id, NEW.environment, NEW.account_id, NEW.strategy_id, NEW.host, NEW.process_id, NEW.started_at)
         IS DISTINCT FROM (OLD.session_id, OLD.environment, OLD.account_id, OLD.strategy_id, OLD.host,
           OLD.process_id, OLD.started_at)
      OR NEW.last_seen_at < OLD.last_seen_at THEN
     RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum owner session history is immutable';
   END IF;
   RETURN NEW;
 END;
 $body$;
CREATE TRIGGER execution_owner_sessions_guard BEFORE UPDATE OR DELETE ON momentum.execution_owner_sessions
 FOR EACH ROW EXECUTE FUNCTION momentum.guard_owner_session();`
