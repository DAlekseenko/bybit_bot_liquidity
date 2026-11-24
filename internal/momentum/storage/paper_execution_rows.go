package storage

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperExecutionRowsArgument означает ошибку аргументов чтения.
	ErrPaperExecutionRowsArgument = errors.New("invalid paper execution rows argument")
	// ErrPaperExecutionRowsAbsent означает отсутствие run в указанной области.
	ErrPaperExecutionRowsAbsent = errors.New("paper execution run absent")
	// ErrPaperExecutionRowsCorrupt означает нарушение связей или значений строк.
	ErrPaperExecutionRowsCorrupt = errors.New("corrupt paper execution rows")
	// ErrPaperExecutionRowsUnavailable означает ошибку получения снимка PostgreSQL.
	ErrPaperExecutionRowsUnavailable = errors.New("paper execution rows unavailable")
)

// PaperIntentFact хранит точные сохранённые поля намерения.
type PaperIntentFact struct {
	IntentID, OrderLinkID, Symbol, Side     string
	Quantity, PlanRevision, PlanHash, State string
	ReduceOnly                              bool
}

// PaperFillFact хранит исполнение с исходным масштабом SQL NUMERIC.
type PaperFillFact struct {
	OrderLinkID, ExecutionID, Symbol, Side string
	Quantity, ExecutionPrice, FeeUSDT      string
	ExecutedAt                             time.Time
}

// PaperExecutionRows содержит проверенные строки одного снимка и признак намерений области.
type PaperExecutionRows struct {
	Scope           ControlScope
	RunID           string
	ScopeHasIntents bool
	intents         map[string]PaperIntentFact
	fills           map[string]PaperFillFact
}

// Intents возвращает отдельную карту намерений по OrderLinkID.
func (r PaperExecutionRows) Intents() map[string]PaperIntentFact { return maps.Clone(r.intents) }

// Fills возвращает отдельную карту исполнений по ExecutionID.
func (r PaperExecutionRows) Fills() map[string]PaperFillFact { return maps.Clone(r.fills) }

// ConfirmedFills переводит точные величины в модель по возрастанию ExecutionID.
func (r PaperExecutionRows) ConfirmedFills() ([]model.ConfirmedFill, error) {
	out := make([]model.ConfirmedFill, 0, len(r.fills))
	for _, id := range slices.Sorted(maps.Keys(r.fills)) {
		f := r.fills[id]
		q, qe := paperwire.ParseRecordedAmount(f.Quantity)
		p, pe := paperwire.ParseRecordedAmount(f.ExecutionPrice)
		fee, fe := paperwire.ParseRecordedAmount(f.FeeUSDT)
		if err := errors.Join(qe, pe, fe); err != nil {
			return nil, errors.Join(ErrPaperExecutionRowsCorrupt, err)
		}
		out = append(out, model.ConfirmedFill{ExecutionID: id, Symbol: f.Symbol, Side: model.FillSide(f.Side),
			Quantity: q, ExecutionPrice: p, FeeUSDT: fee, ExecutedAt: f.ExecutedAt})
	}
	return out, nil
}

// validExecutionRead проверяет аргументы обоих вариантов чтения.
func validExecutionRead(ctx context.Context, scope ControlScope, run string) bool {
	return ctx != nil && paperControlScope(scope) && (run == "" || validRebalanceRunID(run))
}

// ReadPaperExecutionRows читает подтверждённый согласованный снимок PostgreSQL.
func ReadPaperExecutionRows(ctx context.Context, db *sql.DB, scope ControlScope, run string) (PaperExecutionRows, error) {
	if db == nil || !validExecutionRead(ctx, scope, run) {
		return PaperExecutionRows{}, ErrPaperExecutionRowsArgument
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PaperExecutionRows{}, errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
	}
	defer func() { _ = tx.Rollback() }()
	r, err := readPaperExecutionRows(ctx, tx, scope, run)
	if err != nil {
		return PaperExecutionRows{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaperExecutionRows{}, errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
	}
	return r, nil
}

const paperExecutionScopeSQL = `SELECT
 EXISTS (SELECT FROM momentum.rebalance_runs r JOIN momentum.order_intents i USING(run_id)
         WHERE r.account_id=$1 AND r.strategy_id=$2),
 EXISTS (SELECT FROM momentum.rebalance_runs r JOIN momentum.order_intents i USING(run_id)
         WHERE r.account_id=$1 AND r.strategy_id=$2 AND ($3='' OR r.run_id=$3)
           AND (i.environment<>'paper' OR i.account_id<>$1 OR NOT EXISTS (
             SELECT FROM momentum.plan_revisions p WHERE p.run_id=i.run_id AND p.revision=i.plan_revision AND p.plan_hash=i.plan_hash)))
 OR EXISTS (SELECT FROM momentum.order_intents i WHERE i.environment='paper' AND i.account_id=$1
            AND NOT EXISTS (SELECT FROM momentum.rebalance_runs r WHERE r.run_id=i.run_id AND r.account_id=$1))
 OR EXISTS (SELECT FROM momentum.fills f WHERE f.environment='paper' AND f.account_id=$1
            AND NOT EXISTS (SELECT FROM momentum.order_intents i WHERE (i.environment,i.account_id,i.order_link_id)=
            (f.environment,f.account_id,f.order_link_id)))
 OR EXISTS (SELECT FROM momentum.ledger_entries l WHERE l.environment='paper' AND l.account_id=$1
            AND (l.kind NOT IN ('FILL','FEE') OR NOT EXISTS (SELECT FROM momentum.fills f
            WHERE (f.environment,f.account_id,f.execution_id)=(l.environment,l.account_id,l.execution_id))))`

const paperIntentRowsSQL = `SELECT intent_id,order_link_id,symbol,side,quantity::text,trunc(plan_revision)::text,
 encode(plan_hash,'hex'),state,reduce_only,
 quantity>0 AND quantity<'Infinity' AND plan_revision BETWEEN 1 AND 18446744073709551615
 AND plan_revision=trunc(plan_revision) AND octet_length(plan_hash)=32 AND symbol ~ '^[A-Z0-9]+$'
 AND side IN ('BUY','SELL') AND intent_id ~ '[^[:space:]]' AND order_link_id ~ '[^[:space:]]'
 AND state IN ('PENDING','SENT','ACKNOWLEDGED','PARTIALLY_FILLED','FILLED','CANCELLED','REJECTED','UNKNOWN')
 FROM momentum.order_intents WHERE run_id=$1 LIMIT $2`

const paperFillRowsSQL = `WITH selected AS MATERIALIZED (
 SELECT f.*,i.side AS intent_side,i.symbol AS intent_symbol FROM momentum.order_intents i
 JOIN momentum.fills f USING(environment,account_id,order_link_id) WHERE i.run_id=$1 LIMIT $2)
 SELECT f.order_link_id,f.execution_id,f.symbol,f.side,f.quantity::text,f.execution_price::text,f.fee_usdt::text,
 CASE WHEN isfinite(f.executed_at) THEN f.executed_at END,
 f.symbol=f.intent_symbol AND f.side=f.intent_side AND f.execution_id ~ '[^[:space:]]'
 AND f.fee_usdt>'-Infinity' AND f.fee_usdt<'Infinity' AND
 CASE WHEN f.quantity>0 AND f.quantity<'Infinity' AND f.execution_price>0 AND f.execution_price<'Infinity'
      AND length(trunc(f.quantity)::text)<=1234 AND min_scale(f.quantity)<=4095
      AND length(trunc(f.execution_price)::text)<=1234 AND min_scale(f.execution_price)<=4095 THEN
 (SELECT count(*)=2 AND count(*) FILTER (WHERE l.kind='FILL')=1 AND count(*) FILTER (WHERE l.kind='FEE')=1
     AND bool_and(l.source_id=f.execution_id AND l.symbol IS NOT DISTINCT FROM f.symbol
        AND l.occurred_at=f.executed_at AND l.amount_usdt=CASE l.kind WHEN 'FEE' THEN -f.fee_usdt
            ELSE trim_scale(f.quantity)*trim_scale(f.execution_price)*(CASE f.side WHEN 'BUY' THEN -1 ELSE 1 END) END)
  FROM momentum.ledger_entries l WHERE (l.environment,l.account_id,l.execution_id)=(f.environment,f.account_id,f.execution_id))
 ELSE false END FROM selected f`

// readPaperExecutionRows использует только транзакцию вызывающего, включая её собственные записи.
func readPaperExecutionRows(ctx context.Context, tx *sql.Tx, scope ControlScope, run string) (PaperExecutionRows, error) {
	if tx == nil || !validExecutionRead(ctx, scope, run) {
		return PaperExecutionRows{}, ErrPaperExecutionRowsArgument
	}
	if run != "" {
		var found bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT FROM momentum.rebalance_runs
 WHERE account_id=$1 AND strategy_id=$2 AND run_id=$3)`, scope.AccountID, scope.StrategyID, run).Scan(&found)
		if err != nil {
			return PaperExecutionRows{}, errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
		}
		if !found {
			return PaperExecutionRows{}, ErrPaperExecutionRowsAbsent
		}
	}
	r := PaperExecutionRows{Scope: scope, RunID: run, intents: make(map[string]PaperIntentFact), fills: make(map[string]PaperFillFact)}
	var corrupt bool
	if err := tx.QueryRowContext(ctx, paperExecutionScopeSQL, scope.AccountID, scope.StrategyID, run).Scan(&r.ScopeHasIntents, &corrupt); err != nil {
		return PaperExecutionRows{}, errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
	}
	if corrupt {
		return PaperExecutionRows{}, ErrPaperExecutionRowsCorrupt
	}
	if run != "" {
		if err := readExecutionFacts(ctx, tx, run, paperIntentRowsSQL, r.scanIntent); err != nil {
			return PaperExecutionRows{}, err
		}
		if err := readExecutionFacts(ctx, tx, run, paperFillRowsSQL, r.scanFill); err != nil {
			return PaperExecutionRows{}, err
		}
	}
	return r, nil
}

// readExecutionFacts ограничивает каждое множество до встраивания строк в DTO.
func readExecutionFacts(ctx context.Context, tx *sql.Tx, run, query string, scan func(*sql.Rows) (bool, error)) error {
	rows, err := tx.QueryContext(ctx, query, run, paperwire.MaxActualFillRecords+1)
	if err != nil {
		return errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
	}
	defer rows.Close()
	for n := 0; rows.Next(); n++ {
		if n == paperwire.MaxActualFillRecords {
			return paperwire.ErrPaperResourceLimit
		}
		valid, err := scan(rows)
		if err != nil {
			return errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
		}
		if !valid {
			return ErrPaperExecutionRowsCorrupt
		}
	}
	if rows.Err() != nil {
		return errors.Join(ErrPaperExecutionRowsUnavailable, ctx.Err())
	}
	return nil
}

// scanIntent сохраняет поля намерения и результат проверки домена.
func (r PaperExecutionRows) scanIntent(rows *sql.Rows) (bool, error) {
	var f PaperIntentFact
	var valid bool
	err := rows.Scan(&f.IntentID, &f.OrderLinkID, &f.Symbol, &f.Side, &f.Quantity, &f.PlanRevision, &f.PlanHash, &f.State, &f.ReduceOnly, &valid)
	r.intents[f.OrderLinkID] = f
	return valid, err
}

// scanFill сохраняет исходное исполнение и проверяет результат сверки с проводками.
func (r PaperExecutionRows) scanFill(rows *sql.Rows) (bool, error) {
	var f PaperFillFact
	var at sql.NullTime
	var valid bool
	err := rows.Scan(&f.OrderLinkID, &f.ExecutionID, &f.Symbol, &f.Side, &f.Quantity, &f.ExecutionPrice, &f.FeeUSDT, &at, &valid)
	f.ExecutedAt = at.Time.UTC()
	r.fills[f.ExecutionID] = f
	return valid && at.Valid, err
}
