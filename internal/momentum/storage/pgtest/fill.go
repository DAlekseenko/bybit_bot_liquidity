package pgtest

import (
	"database/sql"
	"testing"
)

// Journal работает только с созданной для теста базой.
type Journal struct {
	*testing.T
	db *sql.DB
}

// Pool возвращает тестовое подключение к журналу.
func (j *Journal) Pool() *sql.DB { return j.db }

// Require останавливает тест, если проверка не прошла.
func (j *Journal) Require(ok bool, detail ...any) {
	j.Helper()
	if !ok {
		j.Fatal(detail...)
	}
}

// Done проверяет операцию, результат которой затем читается через repository.
func (j *Journal) Done(_ any, err error) {
	j.Helper()
	j.Require(err == nil, err)
}

// OpenJournal создаёт журнал только в отдельной временной базе теста.
func OpenJournal(t *testing.T, open Opener) *Journal {
	t.Helper()
	return &Journal{T: t, db: Open(t, open)}
}

// Snapshot сохраняет полное содержимое таблиц исполнения для сравнения после чтения.
func (j *Journal) Snapshot() string {
	var rows string
	err := j.db.QueryRowContext(j.Context(), `SELECT
 (SELECT jsonb_agg(i ORDER BY intent_id) FROM momentum.order_intents i)::text ||
 (SELECT jsonb_agg(f ORDER BY execution_id) FROM momentum.fills f)::text ||
 (SELECT jsonb_agg(l ORDER BY source_id,kind) FROM momentum.ledger_entries l)::text`).Scan(&rows)
	j.Require(err == nil, err)
	return rows
}

// RemoveFees удаляет комиссии только в созданной для теста базе.
func (j *Journal) RemoveFees() {
	j.Done(j.db.ExecContext(j.Context(), "ALTER TABLE momentum.ledger_entries DISABLE TRIGGER ledger_entries_immutable; DELETE FROM momentum.ledger_entries WHERE kind='FEE'"))
}

// Pending создаёт вторую равную часть с обеими проводками и оставляет TX открытой.
func (j *Journal) Pending(first, second string) *sql.Tx {
	tx, err := j.db.BeginTx(j.Context(), nil)
	j.Require(err == nil, err)
	j.Cleanup(func() { tx.Rollback() })
	j.Done(tx.ExecContext(j.Context(), `WITH copy AS (INSERT INTO momentum.fills
 SELECT environment,account_id,$1,order_link_id,symbol,side,quantity,execution_price,fee_usdt,executed_at
 FROM momentum.fills WHERE execution_id=$2 RETURNING execution_id)
 INSERT INTO momentum.ledger_entries SELECT l.environment,l.account_id,l.kind,c.execution_id,
 c.execution_id,l.symbol,l.amount_usdt,l.occurred_at FROM momentum.ledger_entries l CROSS JOIN copy c
 WHERE l.execution_id=$2`, second, first))
	return tx
}
