package pgtest

import (
	"context"
	"database/sql"
)

// HoldPaperRecoveryRow задерживает выбранную штатную операцию независимой транзакцией.
func (j *Journal) HoldPaperRecoveryRow(ctx context.Context, kind, key string) *sql.Tx {
	tx, err := j.db.BeginTx(ctx, nil)
	j.Require(err == nil, err)
	j.Cleanup(func() { _ = tx.Rollback() })
	query := map[string]string{
		"head":   "SELECT 1 FROM momentum.rebalance_runs WHERE run_id=$1 FOR UPDATE",
		"intent": "SELECT 1 FROM momentum.order_intents WHERE order_link_id=$1 FOR UPDATE",
	}[kind]
	j.Require(query != "", "неизвестный барьер", kind)
	var held int
	j.Require(tx.QueryRowContext(ctx, query, key).Scan(&held) == nil && held == 1, "не захвачена указанная строка")
	return tx
}

// DamagePaperRecoveryFact меняет строго одну запись подготовленной одноразовой области.
func (j *Journal) DamagePaperRecoveryFact(ctx context.Context, kind, run string) {
	tx, err := j.db.BeginTx(ctx, nil)
	j.Require(err == nil, err)
	defer tx.Rollback()
	j.Done(tx.ExecContext(ctx, "SET LOCAL session_replication_role=replica"))
	query := map[string]string{
		"audit":   "UPDATE momentum.control_revisions SET reason='broken' WHERE revision=(SELECT max(revision) FROM momentum.control_revisions) AND $1<>''",
		"config":  "UPDATE momentum.control_revisions SET config_hash=sha256('missing'::bytea) WHERE revision=(SELECT max(revision) FROM momentum.control_revisions) AND $1<>''",
		"receipt": "UPDATE momentum.run_completions SET reason='broken' WHERE run_id=$1",
	}[kind]
	j.Require(query != "", "неизвестное повреждение", kind)
	var n int
	err = tx.QueryRowContext(ctx, "WITH changed AS ("+query+" RETURNING 1) SELECT count(*) FROM changed", run).Scan(&n)
	j.Require(err == nil && n == 1, "область повреждения", n, err)
	j.Require(tx.Commit() == nil, "повреждение не подтверждено")
}

// EndRecoverySession завершает строку владельца из независимого SQL-соединения.
func (j *Journal) EndRecoverySession(ctx context.Context, id string) error {
	return j.db.QueryRowContext(ctx, `UPDATE momentum.execution_owner_sessions SET ended_at=last_seen_at,end_reason='STALE_TAKEOVER' WHERE session_id=$1 AND ended_at IS NULL RETURNING session_id`, id).Scan(&id)
}
