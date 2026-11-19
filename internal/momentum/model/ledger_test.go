package model

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

// ledgerDecimal разбирает точное число тестовой проводки.
func ledgerDecimal(t *testing.T, amount string) Decimal {
	t.Helper()
	value, err := ParseAmount(amount)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// ledgerFill создаёт тестовое исполнение с явной суммой комиссии.
func ledgerFill(t *testing.T, id, symbol string, side FillSide, qty, price, fee string) ConfirmedFill {
	t.Helper()
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	return ConfirmedFill{id, symbol, side, at, ledgerDecimal(t, qty), ledgerDecimal(t, price), ledgerDecimal(t, fee)}
}

// requireLedgerRatio проверяет точную дробь без десятичного округления.
func requireLedgerRatio(t *testing.T, value Decimal, want string) {
	t.Helper()
	n, d, err := value.CanonicalRatio()
	if err != nil || n+"/"+d != want {
		t.Fatalf("got %s/%s, %v; want %s", n, d, err, want)
	}
}

// TestReplayFillLedger проверяет частичное закрытие, полную реализацию и повторы.
func TestReplayFillLedger(t *testing.T) {
	empty, err := ReplayFillLedger(nil)
	if err != nil || len(empty.Positions) != 0 || len(empty.Postings) != 0 {
		t.Fatalf("unexpected empty projection: %#v, %v", empty, err)
	}
	requireLedgerRatio(t, empty.CashDeltaUSDT, "0/1")
	a := ledgerFill(t, "a", "BTCUSDT", FillBuy, "2", "100", "0.2")
	a.ExecutedAt = a.ExecutedAt.In(time.FixedZone("UTC+3", 3*3600))
	b := ledgerFill(t, "b", "BTCUSDT", FillBuy, "1", "101", "0.101")
	c := ledgerFill(t, "c", "BTCUSDT", FillSell, "1", "110", "0.11")
	d := ledgerFill(t, "d", "BTCUSDT", FillSell, "2", "99", "0.198")
	e := ledgerFill(t, "e", "ETHUSDT", FillBuy, "1", "50", "0")
	retry := d
	retry.Quantity = ledgerDecimal(t, "2.00")
	retry.ExecutedAt = d.ExecutedAt.In(time.FixedZone("UTC+3", 3*3600))
	fills := []ConfirmedFill{a, b, c, d, retry, a, e}
	result, err := ReplayFillLedger(fills)
	if err != nil || len(result.Postings) != 5 || len(result.Positions) != 2 {
		t.Fatalf("unexpected projection: %#v, %v", result, err)
	}
	requireLedgerRatio(t, result.Postings[0].CashDeltaUSDT, "-1/5")
	requireLedgerRatio(t, result.Postings[2].Position.Quantity, "2/1")
	requireLedgerRatio(t, result.Postings[2].Position.CostBasisUSDT, "602/3")
	requireLedgerRatio(t, result.Postings[2].RealizedPnLUSDT, "29/3")
	requireLedgerRatio(t, result.Postings[3].RealizedPnLUSDT, "-8/3")
	requireLedgerRatio(t, result.Positions["BTCUSDT"].Quantity, "0/1")
	requireLedgerRatio(t, result.Positions["BTCUSDT"].CostBasisUSDT, "0/1")
	requireLedgerRatio(t, result.Positions["ETHUSDT"].CostBasisUSDT, "50/1")
	requireLedgerRatio(t, result.RealizedPnLUSDT, "7/1")
	requireLedgerRatio(t, result.FeesUSDT, "609/1000")
	requireLedgerRatio(t, result.CashDeltaUSDT, "6391/1000")
	if result.Postings[4].Fill.ExecutionID != "e" || result.Postings[0].Fill.ExecutedAt.Location() != time.UTC || fills[4] != retry {
		t.Fatal("wrong first-occurrence order or input mutated")
	}
	replayed, err := ReplayFillLedger(fills)
	if err != nil {
		t.Fatal(err)
	}
	delete(result.Positions, "ETHUSDT")
	result.Postings[4] = FillPosting{}
	requireLedgerRatio(t, replayed.Positions["ETHUSDT"].Quantity, "1/1")
	requireLedgerRatio(t, replayed.Postings[4].Position.CostBasisUSDT, "50/1")
}

// TestReplayFillLedgerConflict проверяет отказ для каждого изменённого поля повтора.
func TestReplayFillLedgerConflict(t *testing.T) {
	first := ledgerFill(t, "a", "BTCUSDT", FillBuy, "2", "100", "0.2")
	changedNumber := ledgerDecimal(t, "3")
	for name, mutate := range map[string]func(*ConfirmedFill){
		"symbol": func(f *ConfirmedFill) { f.Symbol = "ETHUSDT" },
		"side":   func(f *ConfirmedFill) { f.Side = FillSell },
		"time":   func(f *ConfirmedFill) { f.ExecutedAt = f.ExecutedAt.Add(time.Nanosecond) },
		"qty":    func(f *ConfirmedFill) { f.Quantity = changedNumber },
		"price":  func(f *ConfirmedFill) { f.ExecutionPrice = changedNumber },
		"fee":    func(f *ConfirmedFill) { f.FeeUSDT = changedNumber },
	} {
		t.Run(name, func(t *testing.T) {
			retry := first
			mutate(&retry)
			result, err := ReplayFillLedger([]ConfirmedFill{first, retry})
			if !errors.Is(err, ErrLedgerFillConflict) || result.Positions != nil || result.Postings != nil {
				t.Fatalf("expected conflict without partial projection, got %#v %v", result, err)
			}
		})
	}
}

// TestProjectLongFillValidation проверяет безопасный отказ и signed комиссию.
func TestProjectLongFillValidation(t *testing.T) {
	flat := LongPosition{Quantity: Zero(), CostBasisUSDT: Zero()}
	buy := ledgerFill(t, "a", "BTCUSDT", FillBuy, "2", "100", "-0.1")
	posting, err := ProjectLongFill(flat, buy)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerRatio(t, posting.Position.CostBasisUSDT, "200/1")
	requireLedgerRatio(t, posting.CashDeltaUSDT, "1/10")
	sell := ledgerFill(t, "b", "BTCUSDT", FillSell, "3", "90", "0")
	for _, position := range []LongPosition{flat, posting.Position} {
		if _, err := ProjectLongFill(position, sell); !errors.Is(err, ErrLedgerOverclose) {
			t.Fatalf("expected overclose, got %v", err)
		}
	}
	if _, err := ProjectLongFill(LongPosition{Zero(), buy.Quantity}, buy); !errors.Is(err, ErrInvalidLedgerPosition) {
		t.Fatalf("expected invalid position, got %v", err)
	}
	for name, mutate := range map[string]func(*ConfirmedFill){
		"id":     func(f *ConfirmedFill) { f.ExecutionID = " " },
		"symbol": func(f *ConfirmedFill) { f.Symbol = "" },
		"side":   func(f *ConfirmedFill) { f.Side = "SHORT" },
		"time":   func(f *ConfirmedFill) { f.ExecutedAt = time.Time{} },
		"qty":    func(f *ConfirmedFill) { f.Quantity = Zero() },
		"price":  func(f *ConfirmedFill) { f.ExecutionPrice = f.FeeUSDT },
		"fee":    func(f *ConfirmedFill) { f.FeeUSDT = Decimal{} },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := buy
			mutate(&invalid)
			if _, err := ProjectLongFill(flat, invalid); !errors.Is(err, ErrInvalidLedgerFill) {
				t.Fatalf("expected invalid fill, got %v", err)
			}
		})
	}
}

// TestProjectLongFillBound проверяет отказ при превышении точного численного лимита.
func TestProjectLongFillBound(t *testing.T) {
	huge := Decimal{new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), maxDecimalBits-1))}
	buy := ledgerFill(t, "a", "BTCUSDT", FillBuy, "1", "1", "0")
	buy.ExecutionPrice = huge
	if _, err := ProjectLongFill(LongPosition{Quantity: buy.Quantity, CostBasisUSDT: huge}, buy); !errors.Is(err, ErrLedgerArithmetic) {
		t.Fatalf("expected arithmetic limit, got %v", err)
	}
}
