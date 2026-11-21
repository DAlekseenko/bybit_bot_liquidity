package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrExecution обозначает неподтверждённую операцию execution repository.
	ErrExecution = errors.New("momentum execution repository failed")
	// ErrExecutionArgument обозначает некорректный вход.
	ErrExecutionArgument = errors.New("invalid momentum execution argument")
	// ErrExecutionAbsent обозначает отсутствие намерения заявки.
	ErrExecutionAbsent = errors.New("momentum order intent absent")
	// ErrExecutionConflict обозначает расхождение с уже сохранёнными данными.
	ErrExecutionConflict = errors.New("momentum execution record conflict")
)

// Состояния намерения заявки, которые выставляет вызывающий код. FILLED и
// PARTIALLY_FILLED выставляет только RecordFill.
var settableIntentStates = map[string]bool{"SENT": true, "ACKNOWLEDGED": true, "CANCELLED": true, "REJECTED": true, "UNKNOWN": true}

// OrderIntent — намерение заявки, сохраняемое до отправки на биржу.
type OrderIntent struct {
	IntentID, Environment, AccountID, OrderLinkID string
	RunID                                         string
	PlanRevision                                  uint64
	PlanHash                                      string
	Symbol                                        string
	Side                                          model.FillSide
	ReduceOnly                                    bool
	Quantity                                      model.Decimal
}

// FillResult сообщает, записан ли fill, накопленное количество и новое состояние намерения.
type FillResult struct {
	Inserted       bool
	FilledQuantity model.Decimal
	State          string
}

// RecordOrderIntent сохраняет намерение в состоянии PENDING. Повтор с теми же
// данными — no-op, с другими — ErrExecutionConflict.
func RecordOrderIntent(ctx context.Context, db *sql.DB, in OrderIntent) error {
	planKey, decErr := hex.DecodeString(in.PlanHash)
	qty, qtyErr := in.Quantity.WireDecimal()
	if ctx == nil || db == nil || decErr != nil || len(planKey) != 32 || qtyErr != nil || in.PlanRevision == 0 ||
		(in.Side != model.FillBuy && in.Side != model.FillSell) ||
		!nonBlank(in.IntentID, in.Environment, in.AccountID, in.OrderLinkID, in.RunID, in.Symbol) {
		return executionFailure(ctx, ErrExecutionArgument)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,
 run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8,$9,$10,$11::numeric)
 ON CONFLICT DO NOTHING`, in.IntentID, in.Environment, in.AccountID, in.OrderLinkID, in.RunID,
		strconv.FormatUint(in.PlanRevision, 10), planKey, in.Symbol, string(in.Side), in.ReduceOnly, qty)
	if err != nil {
		return executionFailure(ctx, nil)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var same bool
	err = db.QueryRowContext(ctx, `SELECT intent_id=$1 AND run_id=$5 AND plan_revision=$6::numeric AND plan_hash=$7
 AND symbol=$8 AND side=$9 AND reduce_only=$10 AND quantity=$11::numeric FROM momentum.order_intents
 WHERE environment=$2 AND account_id=$3 AND order_link_id=$4`, in.IntentID, in.Environment, in.AccountID, in.OrderLinkID,
		in.RunID, strconv.FormatUint(in.PlanRevision, 10), planKey, in.Symbol, string(in.Side), in.ReduceOnly, qty).Scan(&same)
	if err != nil || !same {
		return executionFailure(ctx, ErrExecutionConflict)
	}
	return nil
}

// SetOrderIntentState выставляет состояние отправки/ответа биржи, включая UNKNOWN.
// Заполненное намерение (FILLED) не меняется.
func SetOrderIntentState(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID, state string) error {
	if ctx == nil || db == nil || !settableIntentStates[state] || !nonBlank(environment, accountID, orderLinkID) {
		return executionFailure(ctx, ErrExecutionArgument)
	}
	res, err := db.ExecContext(ctx, `UPDATE momentum.order_intents SET state=$4, updated_at=CURRENT_TIMESTAMP
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3 AND state<>'FILLED'`, environment, accountID, orderLinkID, state)
	if err != nil {
		return executionFailure(ctx, nil)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return executionFailure(ctx, ErrExecutionAbsent)
	}
	return nil
}

// RecordFill одной транзакцией сохраняет fill, его проводки FILL/FEE и новое
// состояние намерения. Повтор того же execution ID — no-op; конкурентные записи
// одного намерения сериализуются блокировкой строки намерения.
func RecordFill(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID string, fill model.ConfirmedFill) (FillResult, error) {
	return recordFill(ctx, db, environment, accountID, orderLinkID, fill, nil)
}

// recordFill реализует RecordFill; beforeCommit позволяет тестам внедрить сбой перед COMMIT.
func recordFill(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID string, fill model.ConfirmedFill,
	beforeCommit func(*sql.Tx) error) (FillResult, error) {
	values, valErr := wireValues(fill.Quantity, fill.ExecutionPrice, fill.FeeUSDT)
	if ctx == nil || db == nil || valErr != nil || fill.ExecutedAt.IsZero() ||
		(fill.Side != model.FillBuy && fill.Side != model.FillSell) ||
		!nonBlank(environment, accountID, orderLinkID, fill.ExecutionID, fill.Symbol) {
		return FillResult{}, executionFailure(ctx, ErrExecutionArgument)
	}
	if c, err := fill.Quantity.Cmp(model.Zero()); err != nil || c <= 0 {
		return FillResult{}, executionFailure(ctx, ErrExecutionArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	defer tx.Rollback()
	var symbol, side, intentQty string
	err = tx.QueryRowContext(ctx, `SELECT symbol, side, quantity::text FROM momentum.order_intents
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3 FOR UPDATE`, environment, accountID, orderLinkID).Scan(&symbol, &side, &intentQty)
	if errors.Is(err, sql.ErrNoRows) {
		return FillResult{}, executionFailure(ctx, ErrExecutionAbsent)
	} else if err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	if symbol != fill.Symbol || side != string(fill.Side) {
		return FillResult{}, executionFailure(ctx, ErrExecutionConflict)
	}
	at := fill.ExecutedAt.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `INSERT INTO momentum.fills(environment,account_id,execution_id,order_link_id,symbol,side,
 quantity,execution_price,fee_usdt,executed_at) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9::numeric,$10::timestamptz)
 ON CONFLICT DO NOTHING`, environment, accountID, fill.ExecutionID, orderLinkID, fill.Symbol, side, values[0], values[1], values[2], at)
	if err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	inserted := false
	if n, _ := res.RowsAffected(); n == 1 {
		inserted = true
		if err := insertFillLedger(ctx, tx, environment, accountID, fill, at); err != nil {
			return FillResult{}, err
		}
	} else if err := sameStoredFill(ctx, tx, environment, accountID, orderLinkID, fill, values, at); err != nil {
		return FillResult{}, err
	}
	var filled, state string
	err = tx.QueryRowContext(ctx, `WITH s AS (SELECT sum(quantity) AS q FROM momentum.fills
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3)
 SELECT s.q::text, CASE WHEN s.q = $4::numeric THEN 'FILLED' WHEN s.q < $4::numeric THEN 'PARTIALLY_FILLED' ELSE 'OVERFILLED' END
 FROM s`, environment, accountID, orderLinkID, intentQty).Scan(&filled, &state)
	if err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	if state == "OVERFILLED" {
		return FillResult{}, executionFailure(ctx, ErrExecutionConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE momentum.order_intents SET state=$4, updated_at=CURRENT_TIMESTAMP
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3`, environment, accountID, orderLinkID, state); err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	if beforeCommit != nil {
		if err := beforeCommit(tx); err != nil {
			return FillResult{}, executionFailure(ctx, nil)
		}
	}
	if err := tx.Commit(); err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	qty, err := model.ParseQty(filled)
	if err != nil {
		return FillResult{}, executionFailure(ctx, nil)
	}
	return FillResult{Inserted: inserted, FilledQuantity: qty, State: state}, nil
}

// insertFillLedger записывает денежный поток исполнения (BUY — отток) и комиссию (отток).
func insertFillLedger(ctx context.Context, tx *sql.Tx, environment, accountID string, fill model.ConfirmedFill, at string) error {
	notional, err := fill.Quantity.Mul(fill.ExecutionPrice)
	if err != nil {
		return executionFailure(ctx, ErrExecutionArgument)
	}
	cash := notional
	if fill.Side == model.FillBuy {
		if cash, err = model.Zero().Sub(notional); err != nil {
			return executionFailure(ctx, ErrExecutionArgument)
		}
	}
	fee, err := model.Zero().Sub(fill.FeeUSDT)
	if err != nil {
		return executionFailure(ctx, ErrExecutionArgument)
	}
	amounts, err := wireValues(cash, fee)
	if err != nil {
		return executionFailure(ctx, ErrExecutionArgument)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO momentum.ledger_entries(environment,account_id,kind,source_id,execution_id,
 symbol,amount_usdt,occurred_at) VALUES ($1,$2,'FILL',$3,$3,$4,$5::numeric,$7::timestamptz),
 ($1,$2,'FEE',$3,$3,$4,$6::numeric,$7::timestamptz)`, environment, accountID, fill.ExecutionID, fill.Symbol, amounts[0], amounts[1], at); err != nil {
		return executionFailure(ctx, nil)
	}
	return nil
}

// sameStoredFill подтверждает, что повтор execution ID совпадает с сохранённым fill.
func sameStoredFill(ctx context.Context, tx *sql.Tx, environment, accountID, orderLinkID string, fill model.ConfirmedFill, values []string, at string) error {
	var same bool
	err := tx.QueryRowContext(ctx, `SELECT order_link_id=$4 AND symbol=$5 AND side=$6 AND quantity=$7::numeric
 AND execution_price=$8::numeric AND fee_usdt=$9::numeric AND executed_at=$10::timestamptz FROM momentum.fills
 WHERE environment=$1 AND account_id=$2 AND execution_id=$3`, environment, accountID, fill.ExecutionID, orderLinkID,
		fill.Symbol, string(fill.Side), values[0], values[1], values[2], at).Scan(&same)
	if err != nil || !same {
		return executionFailure(ctx, ErrExecutionConflict)
	}
	return nil
}

// wireValues переводит decimal в точные конечные строки для NUMERIC.
func wireValues(values ...model.Decimal) ([]string, error) {
	out := make([]string, len(values))
	for i, v := range values {
		s, err := v.WireDecimal()
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// nonBlank проверяет, что все идентификаторы непусты и без NUL.
func nonBlank(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" || strings.ContainsRune(v, 0) {
			return false
		}
	}
	return true
}

// executionFailure сохраняет категорию и отмену контекста без сырых SQL-диагностик.
func executionFailure(ctx context.Context, kind error) error {
	if ctx == nil {
		return errors.Join(ErrExecution, kind)
	}
	return errors.Join(ErrExecution, kind, ctx.Err())
}
