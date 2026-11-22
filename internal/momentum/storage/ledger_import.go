package storage

import (
	"context"
	"database/sql"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// CashLedgerEntry — проводка FUNDING или TRANSFER из transaction log биржи.
// FILL и FEE пишет только RecordFill вместе с fill.
type CashLedgerEntry struct {
	Environment, AccountID, Kind, SourceID string
	Symbol                                 string // обязателен для FUNDING, пуст для TRANSFER
	AmountUSDT                             model.Decimal
	OccurredAt                             time.Time
}

// RecordCashLedgerEntry сохраняет проводку по уникальному source ID: повтор с теми
// же данными — no-op (inserted=false), с другими — ErrExecutionConflict.
func RecordCashLedgerEntry(ctx context.Context, db *sql.DB, e CashLedgerEntry) (inserted bool, err error) {
	amount, amountErr := e.AmountUSDT.WireDecimal()
	if ctx == nil || db == nil || amountErr != nil || e.OccurredAt.IsZero() || !nonBlank(e.Environment, e.AccountID, e.SourceID) ||
		(e.Kind == "FUNDING") == (e.Symbol == "") || (e.Kind != "FUNDING" && e.Kind != "TRANSFER") {
		return false, executionFailure(ctx, ErrExecutionArgument)
	}
	var symbol any
	if e.Symbol != "" {
		symbol = e.Symbol
	}
	at := e.OccurredAt.UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `INSERT INTO momentum.ledger_entries(environment,account_id,kind,source_id,symbol,
 amount_usdt,occurred_at) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::timestamptz) ON CONFLICT DO NOTHING`,
		e.Environment, e.AccountID, e.Kind, e.SourceID, symbol, amount, at)
	if err != nil {
		return false, executionFailure(ctx, ErrExecutionArgument)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var same bool
	err = db.QueryRowContext(ctx, `SELECT symbol IS NOT DISTINCT FROM $5 AND amount_usdt=$6::numeric
 AND occurred_at=$7::timestamptz FROM momentum.ledger_entries
 WHERE environment=$1 AND account_id=$2 AND kind=$3 AND source_id=$4`,
		e.Environment, e.AccountID, e.Kind, e.SourceID, symbol, amount, at).Scan(&same)
	if err != nil || !same {
		return false, executionFailure(ctx, ErrExecutionConflict)
	}
	return false, nil
}

// LedgerCashDelta возвращает сумму проводок FEE, FUNDING и TRANSFER счёта не позже
// cutoff. FILL-проводки хранят полный notional сделки, а не изменение wallet,
// поэтому не входят: realized P&L вычисляется replay fills (model.ReplayCashflowLedger),
// и wallet = начальный капитал + realized + LedgerCashDelta.
func LedgerCashDelta(ctx context.Context, db *sql.DB, environment, accountID string, cutoff time.Time) (model.Decimal, error) {
	if ctx == nil || db == nil || cutoff.IsZero() || !nonBlank(environment, accountID) {
		return model.Decimal{}, executionFailure(ctx, ErrExecutionArgument)
	}
	var sum string
	err := db.QueryRowContext(ctx, `SELECT coalesce(sum(amount_usdt),0)::text FROM momentum.ledger_entries
 WHERE environment=$1 AND account_id=$2 AND kind<>'FILL' AND occurred_at<=$3::timestamptz`,
		environment, accountID, cutoff.UTC().Format(time.RFC3339Nano)).Scan(&sum)
	if err != nil {
		return model.Decimal{}, executionFailure(ctx, nil)
	}
	d, err := model.ParseAmount(sum)
	if err != nil {
		return model.Decimal{}, executionFailure(ctx, ErrExecutionConflict)
	}
	return d, nil
}
