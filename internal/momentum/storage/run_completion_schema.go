package storage

// momentumRunCompletionSchema фиксирует итог run одной неизменяемой строкой.
// Завершённый run не принимает новых plan revisions, а итог ссылается на
// последнюю revision (NULL, если планов не было). Обе вставки блокируют строку
// run, поэтому гонка «новая revision против завершения» сериализуется.
const momentumRunCompletionSchema = `CREATE TABLE momentum.run_completions (
 run_id TEXT COLLATE "C" PRIMARY KEY REFERENCES momentum.rebalance_runs(run_id),
 outcome TEXT NOT NULL CHECK (outcome IN ('COMPLETED','SKIPPED','FAILED')),
 plan_revision NUMERIC CHECK (plan_revision >= 1 AND plan_revision = pg_catalog.trunc(plan_revision)),
 plan_hash BYTEA,
 reason TEXT COLLATE "C" NOT NULL CHECK (reason ~ '[^[:space:]]'),
 completed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(completed_at)),
 CHECK ((plan_revision IS NULL) = (plan_hash IS NULL)),
 CHECK (outcome <> 'COMPLETED' OR plan_revision IS NOT NULL),
 FOREIGN KEY (run_id, plan_revision, plan_hash) REFERENCES momentum.plan_revisions(run_id, revision, plan_hash)
);
CREATE FUNCTION momentum.guard_run_completion() RETURNS trigger
 LANGUAGE plpgsql SET search_path = pg_catalog AS $body$
 BEGIN
   IF TG_OP <> 'INSERT' THEN
     RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum run completion is immutable';
   END IF;
   IF current_setting('transaction_isolation') <> 'read committed' THEN
     RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum run writer requires read committed';
   END IF;
   PERFORM 1 FROM momentum.rebalance_runs r WHERE r.run_id = NEW.run_id FOR UPDATE;
   IF TG_TABLE_NAME = 'plan_revisions' THEN
     -- Повтор существующей revision разрешает ON CONFLICT вызывающего кода.
     IF EXISTS (SELECT 1 FROM momentum.run_completions c WHERE c.run_id = NEW.run_id)
        AND NOT EXISTS (SELECT 1 FROM momentum.plan_revisions p
          WHERE p.run_id = NEW.run_id AND p.revision = NEW.revision) THEN
       RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'momentum run is completed';
     END IF;
   ELSIF (SELECT max(p.revision) FROM momentum.plan_revisions p WHERE p.run_id = NEW.run_id)
       IS DISTINCT FROM NEW.plan_revision THEN
     RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'momentum run completion must reference the latest plan';
   END IF;
   RETURN NEW;
 END;
 $body$;
CREATE TRIGGER run_completions_guard BEFORE INSERT OR UPDATE OR DELETE ON momentum.run_completions
 FOR EACH ROW EXECUTE FUNCTION momentum.guard_run_completion();
CREATE TRIGGER plan_revisions_reject_completed BEFORE INSERT ON momentum.plan_revisions
 FOR EACH ROW EXECUTE FUNCTION momentum.guard_run_completion();`
