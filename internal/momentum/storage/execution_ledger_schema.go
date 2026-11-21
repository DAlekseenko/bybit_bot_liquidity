package storage

// momentumExecutionLedgerSchema хранит намерения заявок, ответы биржи, подтверждённые
// исполнения и денежные проводки. Схема общая для движка и не содержит правил
// конкретной стратегии. Fill и его проводки FILL/FEE обязаны появиться в одной
// транзакции: отложенная проверка отклоняет COMMIT без них.
const momentumExecutionLedgerSchema = `CREATE TABLE momentum.order_intents (
 intent_id TEXT COLLATE "C" PRIMARY KEY CHECK (intent_id ~ '[^[:space:]]'),
 environment TEXT COLLATE "C" NOT NULL CHECK (environment ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 order_link_id TEXT COLLATE "C" NOT NULL CHECK (order_link_id ~ '[^[:space:]]'),
 run_id TEXT COLLATE "C" NOT NULL,
 plan_revision NUMERIC NOT NULL,
 plan_hash BYTEA NOT NULL,
 symbol TEXT COLLATE "C" NOT NULL CHECK (symbol ~ '^[A-Z0-9]+$'),
 side TEXT NOT NULL CHECK (side IN ('BUY', 'SELL')),
 reduce_only BOOLEAN NOT NULL,
 quantity NUMERIC NOT NULL CHECK (quantity > 0 AND quantity < 'Infinity'::NUMERIC),
 state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'SENT', 'ACKNOWLEDGED',
   'PARTIALLY_FILLED', 'FILLED', 'CANCELLED', 'REJECTED', 'UNKNOWN')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(created_at)),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(updated_at)),
 UNIQUE (environment, account_id, order_link_id),
 FOREIGN KEY (run_id, plan_revision, plan_hash)
   REFERENCES momentum.plan_revisions(run_id, revision, plan_hash)
);
CREATE TABLE momentum.broker_orders (
 environment TEXT COLLATE "C" NOT NULL,
 account_id TEXT COLLATE "C" NOT NULL,
 order_link_id TEXT COLLATE "C" NOT NULL,
 exchange_order_id TEXT COLLATE "C" NOT NULL CHECK (exchange_order_id ~ '[^[:space:]]'),
 exchange_status TEXT COLLATE "C" NOT NULL CHECK (exchange_status ~ '[^[:space:]]'),
 observed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(observed_at)),
 PRIMARY KEY (environment, account_id, order_link_id),
 UNIQUE (environment, account_id, exchange_order_id),
 FOREIGN KEY (environment, account_id, order_link_id)
   REFERENCES momentum.order_intents(environment, account_id, order_link_id)
);
CREATE TABLE momentum.fills (
 environment TEXT COLLATE "C" NOT NULL,
 account_id TEXT COLLATE "C" NOT NULL,
 execution_id TEXT COLLATE "C" NOT NULL CHECK (execution_id ~ '[^[:space:]]'),
 order_link_id TEXT COLLATE "C" NOT NULL,
 symbol TEXT COLLATE "C" NOT NULL CHECK (symbol ~ '^[A-Z0-9]+$'),
 side TEXT NOT NULL CHECK (side IN ('BUY', 'SELL')),
 quantity NUMERIC NOT NULL CHECK (quantity > 0 AND quantity < 'Infinity'::NUMERIC),
 execution_price NUMERIC NOT NULL CHECK (execution_price > 0 AND execution_price < 'Infinity'::NUMERIC),
 fee_usdt NUMERIC NOT NULL CHECK (fee_usdt > '-Infinity'::NUMERIC AND fee_usdt < 'Infinity'::NUMERIC),
 executed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(executed_at)),
 PRIMARY KEY (environment, account_id, execution_id),
 FOREIGN KEY (environment, account_id, order_link_id)
   REFERENCES momentum.order_intents(environment, account_id, order_link_id)
);
CREATE TABLE momentum.ledger_entries (
 environment TEXT COLLATE "C" NOT NULL CHECK (environment ~ '[^[:space:]]'),
 account_id TEXT COLLATE "C" NOT NULL CHECK (account_id ~ '[^[:space:]]'),
 kind TEXT NOT NULL CHECK (kind IN ('FILL', 'FEE', 'FUNDING', 'TRANSFER')),
 source_id TEXT COLLATE "C" NOT NULL CHECK (source_id ~ '[^[:space:]]'),
 execution_id TEXT COLLATE "C",
 symbol TEXT COLLATE "C" CHECK (symbol ~ '^[A-Z0-9]+$'),
 amount_usdt NUMERIC NOT NULL CHECK (amount_usdt > '-Infinity'::NUMERIC AND amount_usdt < 'Infinity'::NUMERIC),
 occurred_at TIMESTAMPTZ NOT NULL CHECK (isfinite(occurred_at)),
 PRIMARY KEY (environment, account_id, kind, source_id),
 CHECK ((kind IN ('FILL', 'FEE')) = (execution_id IS NOT NULL)),
 CHECK (execution_id IS NULL OR execution_id = source_id),
 CHECK ((kind = 'TRANSFER') = (symbol IS NULL)),
 FOREIGN KEY (environment, account_id, execution_id)
   REFERENCES momentum.fills(environment, account_id, execution_id)
);
CREATE FUNCTION momentum.reject_execution_record_mutation() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum execution record is immutable';
 END;
 $body$;
CREATE TRIGGER order_intents_identity_immutable
 BEFORE UPDATE OF intent_id, environment, account_id, order_link_id, run_id, plan_revision,
   plan_hash, symbol, side, reduce_only, quantity, created_at OR DELETE
 ON momentum.order_intents FOR EACH ROW
 EXECUTE FUNCTION momentum.reject_execution_record_mutation();
CREATE TRIGGER broker_orders_identity_immutable
 BEFORE UPDATE OF environment, account_id, order_link_id, exchange_order_id OR DELETE
 ON momentum.broker_orders FOR EACH ROW
 EXECUTE FUNCTION momentum.reject_execution_record_mutation();
CREATE TRIGGER fills_immutable BEFORE UPDATE OR DELETE ON momentum.fills
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_execution_record_mutation();
CREATE TRIGGER ledger_entries_immutable BEFORE UPDATE OR DELETE ON momentum.ledger_entries
 FOR EACH ROW EXECUTE FUNCTION momentum.reject_execution_record_mutation();
CREATE FUNCTION momentum.require_fill_ledger() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   IF (SELECT count(*) FROM momentum.ledger_entries e
       WHERE e.environment = NEW.environment AND e.account_id = NEW.account_id
         AND e.execution_id = NEW.execution_id AND e.kind IN ('FILL', 'FEE')) <> 2 THEN
     RAISE EXCEPTION USING ERRCODE = '23514',
       MESSAGE = 'momentum fill requires FILL and FEE ledger entries in the same transaction';
   END IF;
   RETURN NULL;
 END;
 $body$;
CREATE CONSTRAINT TRIGGER fills_require_ledger AFTER INSERT ON momentum.fills
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
 EXECUTE FUNCTION momentum.require_fill_ledger();`
