package pgtest

import (
	"context"
	"database/sql"
)

// HoldConfig задерживает контрольный writer только в тестовой транзакции.
func (j *Journal) HoldConfig(ctx context.Context, tx *sql.Tx) {
	j.Done(tx.ExecContext(ctx, "LOCK TABLE momentum.config_snapshots IN ACCESS EXCLUSIVE MODE"))
}

// Wait возвращает PID контролируемого ожидания в этой БД.
func (j *Journal) Wait(ctx context.Context, query func(context.Context, string, ...any) *sql.Row, table, mode string, granted bool, blocker int) int {
	var pid int
	for pid == 0 {
		err := query(ctx, `SELECT COALESCE((SELECT pid FROM pg_locks WHERE database=(SELECT oid FROM pg_database WHERE datname=current_database())
 AND relation=$4::regclass AND mode=$1 AND granted=$2
 AND cardinality(pg_blocking_pids(pid))>0 AND ($3=0 OR COALESCE(NULLIF($3,-1),pg_backend_pid())=ANY(pg_blocking_pids(pid))) LIMIT 1),0)`, mode, granted, blocker, table).Scan(&pid)
		j.Require(err == nil, err)
	}
	return pid
}

// KillOwner завершает только подтверждённого владельца advisory lock этой тестовой БД.
func (j *Journal) KillOwner(key int64) {
	var killed bool
	err := j.db.QueryRowContext(j.Context(), `SELECT pg_terminate_backend(pid,5000) FROM pg_locks
 WHERE database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND locktype='advisory'
 AND mode='ExclusiveLock' AND granted AND objsubid=1 AND classid=(($1::bigint >> 32) & 4294967295)::oid
 AND objid=($1::bigint & 4294967295)::oid`, key).Scan(&killed)
	j.Require(err == nil && killed, "потеря backend не подтверждена", err)
}

// ReplaceOwner подставляет в журнале чужую активную сессию при сохранённом advisory lock.
func (j *Journal) ReplaceOwner(oldID, newID string) {
	var swapped string
	err := j.db.QueryRowContext(j.Context(), `WITH old AS (UPDATE momentum.execution_owner_sessions SET ended_at=last_seen_at,
 end_reason='STALE_TAKEOVER' WHERE session_id=$1 AND ended_at IS NULL RETURNING *)
 INSERT INTO momentum.execution_owner_sessions(session_id,environment,account_id,strategy_id,host,process_id,started_at,last_seen_at)
 SELECT $2,environment,account_id,strategy_id,host,process_id,last_seen_at,last_seen_at FROM old RETURNING session_id`, oldID, newID).Scan(&swapped)
	j.Require(err == nil && swapped == newID, "подмена журнала не подтверждена", err)
}
