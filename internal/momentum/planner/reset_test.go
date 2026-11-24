package planner

import (
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// resetSnapshot строит снимок 3 символов по цене 100 с заданными equity и позицией BTCUSDT.
func resetSnapshot(t *testing.T, equity, btc string) model.PortfolioSnapshot {
	t.Helper()
	d := func(s string) model.Decimal {
		v, err := model.ParseAmount(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	price := d("100")
	s, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "backtest", AccountID: "acc", ConfigVersion: "v1",
		CapturedAt: time.Unix(1700000000, 0), Equity: d(equity),
		Prices:    map[string]model.Decimal{"BTCUSDT": price, "ETHUSDT": price, "HYPEUSDT": price},
		Positions: map[string]model.Decimal{"BTCUSDT": d(btc), "ETHUSDT": model.Zero(), "HYPEUSDT": model.Zero()}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var resetSymbols = []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}

// resetSteps возвращает шаг 0.001 для всех символов.
func resetSteps(t *testing.T) map[string]model.Decimal {
	step, _ := model.ParseQty("0.001")
	return map[string]model.Decimal{"BTCUSDT": step, "ETHUSDT": step, "HYPEUSDT": step}
}

// TestResetOpeningTargets проверяет формулу 1/2/3/5x, резерв комиссии, floor Qty и неактивную долю.
func TestResetOpeningTargets(t *testing.T) {
	fee, _ := model.ParseRate("0.001")
	active := map[string]bool{"BTCUSDT": true, "ETHUSDT": true, "HYPEUSDT": false}
	for leverage, want := range map[int]string{1: "29.94", 2: "59.76", 3: "89.463", 5: "148.514"} {
		targets, err := ResetOpeningTargets(resetSymbols, resetSnapshot(t, "9000", "0"), active, resetSteps(t), fee, leverage)
		if err != nil {
			t.Fatalf("%dx: %v", leverage, err)
		}
		got := map[string]string{}
		for _, target := range targets {
			got[target.Symbol], _ = target.Quantity.WireDecimal()
		}
		if got["BTCUSDT"] != want || got["ETHUSDT"] != want || got["HYPEUSDT"] != "0" {
			t.Fatalf("%dx targets=%v want %s", leverage, got, want)
		}
	}
}

// TestResetGuards проверяет, что opening невозможен до flat-снимка и при неверных входах.
func TestResetGuards(t *testing.T) {
	fee, _ := model.ParseRate("0.001")
	active := map[string]bool{"BTCUSDT": true, "ETHUSDT": true, "HYPEUSDT": true}
	for name, tc := range map[string]struct {
		snapshot model.PortfolioSnapshot
		leverage int
		want     error
	}{
		"not-flat":       {resetSnapshot(t, "9000", "0.5"), 2, ReasonResetNotFlat},
		"zero-equity":    {resetSnapshot(t, "0", "0"), 2, ReasonResetNonPositiveEquity},
		"negative":       {resetSnapshot(t, "-1", "0"), 2, ReasonResetNonPositiveEquity},
		"bad-leverage":   {resetSnapshot(t, "9000", "0"), 4, ReasonInvalidResetInput},
		"empty-snapshot": {model.PortfolioSnapshot{}, 2, ReasonInvalidResetInput},
	} {
		if _, err := ResetOpeningTargets(resetSymbols, tc.snapshot, active, resetSteps(t), fee, tc.leverage); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := ResetOpeningTargets(resetSymbols, resetSnapshot(t, "9000", "0"), map[string]bool{"BTCUSDT": true}, resetSteps(t), fee, 2); !errors.Is(err, ReasonInvalidResetInput) {
		t.Fatalf("partial active mask: %v", err)
	}
}

// TestResetCloseTargets проверяет, что close-фаза закрывает все позиции даже при неизменном сигнале.
func TestResetCloseTargets(t *testing.T) {
	targets, err := ResetCloseTargets(resetSymbols, resetSnapshot(t, "9000", "0.5"))
	if err != nil || len(targets) != 3 {
		t.Fatalf("close=%v %v", targets, err)
	}
	for _, target := range targets {
		if q, _ := target.Quantity.WireDecimal(); q != "0" {
			t.Fatalf("close target %s=%s", target.Symbol, q)
		}
	}
	if _, err := ResetCloseTargets([]string{"BTCUSDT"}, resetSnapshot(t, "9000", "0")); !errors.Is(err, ReasonInvalidResetInput) {
		t.Fatalf("universe mismatch: %v", err)
	}
}
