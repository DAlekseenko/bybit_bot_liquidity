package pgtest

import "database/sql"

// HistoryAudit фиксирует физические количества, финансовую историю и регистрацию владельцев.
type HistoryAudit struct {
	Runs, Inputs, Intents, Fills, Postings, Fees, Sessions int
	Financial, Owners, Active, TakenOver                   string
}

// ReadHistoryAudit читает строки disposable БД канонически и отдельно хэширует owner audit.
func (j *Journal) ReadHistoryAudit(db *sql.DB) HistoryAudit {
	j.Helper()
	var a HistoryAudit
	err := db.QueryRowContext(j.Context(), `WITH financial(kind,doc) AS (
 SELECT 'run',to_jsonb(r) FROM momentum.rebalance_runs r UNION ALL SELECT 'input',to_jsonb(i) FROM momentum.paper_run_inputs i
 UNION ALL SELECT 'plan',to_jsonb(p) FROM momentum.plan_revisions p UNION ALL SELECT 'intent',to_jsonb(i) FROM momentum.order_intents i
 UNION ALL SELECT 'fill',to_jsonb(f) FROM momentum.fills f UNION ALL SELECT kind::text,to_jsonb(l) FROM momentum.ledger_entries l
 UNION ALL SELECT 'completion',to_jsonb(c) FROM momentum.run_completions c)
 SELECT count(*) FILTER(WHERE kind='run'),count(*) FILTER(WHERE kind='input'),count(*) FILTER(WHERE kind='intent'),
 count(*) FILTER(WHERE kind='fill'),count(*) FILTER(WHERE kind='FILL'),count(*) FILTER(WHERE kind='FEE'),
 encode(sha256(convert_to(coalesce(jsonb_agg(jsonb_build_array(kind,doc) ORDER BY kind,doc)::text,'[]'),'UTF8')),'hex'),
 (SELECT count(*) FROM momentum.execution_owner_sessions),
 (SELECT encode(sha256(convert_to(coalesce(jsonb_agg(o ORDER BY session_id)::text,'[]'),'UTF8')),'hex') FROM momentum.execution_owner_sessions o),
 (SELECT coalesce(max(session_id),'') FROM momentum.execution_owner_sessions WHERE ended_at IS NULL),
 (SELECT coalesce(max(session_id),'') FROM momentum.execution_owner_sessions WHERE end_reason='STALE_TAKEOVER') FROM financial`).
		Scan(&a.Runs, &a.Inputs, &a.Intents, &a.Fills, &a.Postings, &a.Fees, &a.Financial, &a.Sessions, &a.Owners, &a.Active, &a.TakenOver)
	j.Require(err == nil, err)
	return a
}

// DamageAncestor меняет ровно одну запись до head на одноразовой БД.
func (j *Journal) DamageAncestor(fee bool) {
	table, trigger, change := "paper_run_inputs", "paper_run_inputs_immutable", "UPDATE momentum.paper_run_inputs SET previous_resolution_hash=decode(repeat('ab',32),'hex') WHERE sequence=2"
	if fee {
		table, trigger, change = "ledger_entries", "ledger_entries_immutable", `DELETE FROM momentum.ledger_entries WHERE kind='FEE' AND execution_id IN
 (SELECT f.execution_id FROM momentum.fills f JOIN momentum.order_intents i USING(order_link_id) JOIN momentum.paper_run_inputs p USING(run_id) WHERE p.sequence=1)`
	}
	tx, err := j.Pool().BeginTx(j.Context(), nil)
	j.Require(err == nil, err)
	defer tx.Rollback()
	j.Done(tx.ExecContext(j.Context(), "ALTER TABLE momentum."+table+" DISABLE TRIGGER "+trigger))
	res, err := tx.ExecContext(j.Context(), change)
	j.Require(err == nil, err)
	n, err := res.RowsAffected()
	j.Require(err == nil && n == 1, "повреждение одной строки предка", err)
	j.Done(tx.ExecContext(j.Context(), "ALTER TABLE momentum."+table+" ENABLE TRIGGER "+trigger))
	j.Require(tx.Commit() == nil, "фиксация повреждения")
}
