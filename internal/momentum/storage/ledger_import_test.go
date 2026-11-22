package storage

import (
	"context"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestCashLedgerEntries проверяет идемпотентный импорт funding и переводов и сумму wallet к cutoff.
func TestCashLedgerEntries(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	amount := func(s string) model.Decimal {
		d, err := model.ParseAmount(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	funding := CashLedgerEntry{Environment: "testnet", AccountID: "acc", Kind: "FUNDING", SourceID: "f1", Symbol: "BTCUSDT",
		AmountUSDT: amount("-0.12"), OccurredAt: at}
	transfer := CashLedgerEntry{Environment: "testnet", AccountID: "acc", Kind: "TRANSFER", SourceID: "t1",
		AmountUSDT: amount("500"), OccurredAt: at.Add(time.Hour)}
	for i, want := range []bool{true, false} {
		if inserted, err := RecordCashLedgerEntry(ctx, db, funding); err != nil || inserted != want {
			t.Fatalf("funding %d: %v %v", i, inserted, err)
		}
	}
	if _, err := RecordCashLedgerEntry(ctx, db, transfer); err != nil {
		t.Fatal(err)
	}
	changed := funding
	changed.AmountUSDT = amount("-0.13")
	_, err := RecordCashLedgerEntry(ctx, db, changed)
	requireExecutionErr(t, err, ErrExecutionConflict)
	for _, bad := range []CashLedgerEntry{
		{Environment: "testnet", AccountID: "acc", Kind: "FUNDING", SourceID: "f2", AmountUSDT: amount("1"), OccurredAt: at},
		{Environment: "testnet", AccountID: "acc", Kind: "TRANSFER", SourceID: "t2", Symbol: "BTCUSDT", AmountUSDT: amount("1"), OccurredAt: at},
		{Environment: "testnet", AccountID: "acc", Kind: "FEE", SourceID: "x", Symbol: "BTCUSDT", AmountUSDT: amount("1"), OccurredAt: at},
	} {
		_, err := RecordCashLedgerEntry(ctx, db, bad)
		requireExecutionErr(t, err, ErrExecutionArgument)
	}
	for cutoff, want := range map[time.Time]string{at: "-0.12", at.Add(time.Hour): "499.88", at.Add(-time.Hour): "0"} {
		got, err := LedgerWalletDelta(ctx, db, "testnet", "acc", cutoff)
		if s, _ := got.WireDecimal(); err != nil || s != want {
			t.Fatalf("delta at %s = %s %v, want %s", cutoff, s, err, want)
		}
	}
}
