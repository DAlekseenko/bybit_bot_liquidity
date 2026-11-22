package model

import (
	"errors"
	"testing"
	"time"
)

// cashflowAt возвращает тестовое время со сдвигом в часах от базовой точки.
func cashflowAt(hours int) time.Time {
	return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).Add(time.Duration(hours) * time.Hour)
}

// cashflowFunding создаёт событие funding с явной ставкой и ценой mark.
func cashflowFunding(t *testing.T, id, symbol string, at time.Time, rate, mark string) LongFundingAssessment {
	t.Helper()
	return LongFundingAssessment{id, symbol, FundingEvent{at, ledgerDecimal(t, rate)}, ledgerDecimal(t, mark)}
}

// cashflowTransfer создаёт внешний перевод с точной суммой.
func cashflowTransfer(t *testing.T, id string, direction TransferDirection, at time.Time, amount string) ExternalTransfer {
	t.Helper()
	return ExternalTransfer{id, direction, at, ledgerDecimal(t, amount)}
}

// TestProjectExternalTransfer проверяет знак перевода, UTC и отказ некорректных данных.
func TestProjectExternalTransfer(t *testing.T) {
	east := time.FixedZone("east", 3*3600)
	in, err := ProjectExternalTransfer(cashflowTransfer(t, "t-1", TransferIn, cashflowAt(1).In(east), "100.25"))
	if err != nil || in.Transfer.TransferredAt.Location() != time.UTC || !in.Transfer.TransferredAt.Equal(cashflowAt(1)) {
		t.Fatalf("unexpected transfer in: %+v, %v", in, err)
	}
	requireLedgerRatio(t, in.CashDeltaUSDT, "401/4")
	out, err := ProjectExternalTransfer(cashflowTransfer(t, "t-2", TransferOut, cashflowAt(1), "0.5"))
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerRatio(t, out.CashDeltaUSDT, "-1/2")
	valid := cashflowTransfer(t, "t-3", TransferIn, cashflowAt(1), "1")
	for name, mutate := range map[string]func(*ExternalTransfer){
		"empty-id":   func(x *ExternalTransfer) { x.TransferID = "" },
		"spaced-id":  func(x *ExternalTransfer) { x.TransferID = " t-3" },
		"direction":  func(x *ExternalTransfer) { x.Direction = "MARGIN" },
		"zero-time":  func(x *ExternalTransfer) { x.TransferredAt = time.Time{} },
		"zero-sum":   func(x *ExternalTransfer) { x.AmountUSDT = Zero() },
		"negative":   func(x *ExternalTransfer) { x.AmountUSDT = ledgerDecimal(t, "-1") },
		"no-decimal": func(x *ExternalTransfer) { x.AmountUSDT = Decimal{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if _, err := ProjectExternalTransfer(bad); !errors.Is(err, ErrInvalidTransfer) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// TestReplayCashflowLedgerSameTimestamp проверяет D-07: funding к позиции до rebalance того же времени.
func TestReplayCashflowLedgerSameTimestamp(t *testing.T) {
	buy := ledgerFill(t, "f-1", "BTCUSDT", FillBuy, "10", "7000", "7")
	buy.ExecutedAt = cashflowAt(0)
	sell := ledgerFill(t, "f-2", "BTCUSDT", FillSell, "10", "8000", "8")
	sell.ExecutedAt = cashflowAt(8)
	eth := ledgerFill(t, "f-3", "ETHUSDT", FillBuy, "1", "3000", "0")
	eth.ExecutedAt = cashflowAt(8)
	events := CashflowEvents{
		// Исполнения перечислены раньше funding: порядок задаёт время и ранг, а не входной срез.
		Fills: []ConfirmedFill{sell, eth, buy},
		Funding: []LongFundingAssessment{
			cashflowFunding(t, "fu-btc", "BTCUSDT", cashflowAt(8), "0.0001", "8000"),
			cashflowFunding(t, "fu-eth", "ETHUSDT", cashflowAt(8), "0.0001", "3000"),
			cashflowFunding(t, "fu-btc-late", "BTCUSDT", cashflowAt(16), "-0.0001", "8000"),
		},
	}
	got, err := ReplayCashflowLedger(events)
	if err != nil || len(got.Fills) != 3 || len(got.Funding) != 3 {
		t.Fatalf("unexpected ledger: %+v, %v", got, err)
	}
	if got.Fills[0].Fill.ExecutionID != "f-1" || got.Fills[1].Fill.ExecutionID != "f-2" || got.Fills[2].Fill.ExecutionID != "f-3" {
		t.Fatal("fills not ordered by time with stable journal order")
	}
	requireLedgerRatio(t, got.Funding[0].Position.Quantity, "10/1")
	requireLedgerRatio(t, got.Funding[0].CashDeltaUSDT, "-8/1")
	requireLedgerRatio(t, got.Funding[1].CashDeltaUSDT, "0/1")
	requireLedgerRatio(t, got.Funding[2].Position.Quantity, "0/1")
	requireLedgerRatio(t, got.Funding[2].CashDeltaUSDT, "0/1")
	requireLedgerRatio(t, got.FundingCashflowUSDT, "-8/1")
	requireLedgerRatio(t, got.RealizedPnLUSDT, "10000/1")
	requireLedgerRatio(t, got.FeesUSDT, "15/1")
	requireLedgerRatio(t, got.WalletDeltaUSDT, "9977/1")
	requireLedgerRatio(t, got.Positions["ETHUSDT"].Quantity, "1/1")
	requireLedgerRatio(t, got.NetTransfersUSDT, "0/1")
}

// TestReplayCashflowLedgerTransfers проверяет переводы, движение маржи и неизменность позиций.
func TestReplayCashflowLedgerTransfers(t *testing.T) {
	buy := ledgerFill(t, "f-1", "BTCUSDT", FillBuy, "1", "100", "0.1")
	margin := MarginMovement{"m-1", "BTCUSDT", cashflowAt(2), ledgerDecimal(t, "300")}
	back := MarginMovement{"m-2", "BTCUSDT", cashflowAt(3), ledgerDecimal(t, "-50")}
	in := cashflowTransfer(t, "t-1", TransferIn, cashflowAt(1), "1000")
	out := cashflowTransfer(t, "t-2", TransferOut, cashflowAt(4), "250.5")
	got, err := ReplayCashflowLedger(CashflowEvents{
		Fills:           []ConfirmedFill{buy},
		Transfers:       []ExternalTransfer{out, in},
		MarginMovements: []MarginMovement{back, margin},
	})
	if err != nil || len(got.Transfers) != 2 || len(got.MarginMovements) != 2 {
		t.Fatalf("unexpected ledger: %+v, %v", got, err)
	}
	if got.Transfers[0].Transfer.TransferID != "t-1" || got.MarginMovements[0].MovementID != "m-1" {
		t.Fatal("events not ordered by time")
	}
	requireLedgerRatio(t, got.NetTransfersUSDT, "1499/2")
	requireLedgerRatio(t, got.WalletDeltaUSDT, "3747/5")
	requireLedgerRatio(t, got.Positions["BTCUSDT"].Quantity, "1/1")
	requireLedgerRatio(t, got.Positions["BTCUSDT"].CostBasisUSDT, "100/1")

	onlyMargin, err := ReplayCashflowLedger(CashflowEvents{MarginMovements: []MarginMovement{margin}})
	if err != nil || len(onlyMargin.Transfers) != 0 || len(onlyMargin.Positions) != 0 {
		t.Fatalf("margin movement changed ledger: %+v, %v", onlyMargin, err)
	}
	requireLedgerRatio(t, onlyMargin.NetTransfersUSDT, "0/1")
	requireLedgerRatio(t, onlyMargin.WalletDeltaUSDT, "0/1")
}

// TestReplayCashflowLedgerDuplicates проверяет идемпотентные повторы и конфликты ID каждого вида.
func TestReplayCashflowLedgerDuplicates(t *testing.T) {
	east := time.FixedZone("east", 3*3600)
	fill := ledgerFill(t, "f-1", "BTCUSDT", FillBuy, "1", "100", "0")
	funding := cashflowFunding(t, "fu-1", "BTCUSDT", cashflowAt(1), "0.001", "100")
	transfer := cashflowTransfer(t, "t-1", TransferIn, cashflowAt(2), "10")
	margin := MarginMovement{"m-1", "BTCUSDT", cashflowAt(3), ledgerDecimal(t, "5")}
	fillRetry, fundingRetry, transferRetry, marginRetry := fill, funding, transfer, margin
	fillRetry.ExecutedAt = fill.ExecutedAt.In(east)
	fundingRetry.Event.Rate = ledgerDecimal(t, "0.0010")
	transferRetry.TransferredAt = transfer.TransferredAt.In(east)
	marginRetry.AmountUSDT = ledgerDecimal(t, "5.00")
	base := CashflowEvents{
		Fills:           []ConfirmedFill{fill, fillRetry},
		Funding:         []LongFundingAssessment{funding, fundingRetry},
		Transfers:       []ExternalTransfer{transfer, transferRetry},
		MarginMovements: []MarginMovement{margin, marginRetry},
	}
	got, err := ReplayCashflowLedger(base)
	if err != nil || len(got.Fills) != 1 || len(got.Funding) != 1 || len(got.Transfers) != 1 || len(got.MarginMovements) != 1 {
		t.Fatalf("duplicates not collapsed: %+v, %v", got, err)
	}
	requireLedgerRatio(t, got.FundingCashflowUSDT, "-1/10")
	requireLedgerRatio(t, got.WalletDeltaUSDT, "99/10")
	if base.Fills[1].ExecutedAt.Location() != east || base.Transfers[1].TransferredAt.Location() != east {
		t.Fatal("input mutated")
	}

	for name, tc := range map[string]struct {
		mutate func(*CashflowEvents)
		want   error
	}{
		"fill-conflict":     {func(e *CashflowEvents) { e.Fills[1].FeeUSDT = ledgerDecimal(t, "1") }, ErrLedgerFillConflict},
		"funding-conflict":  {func(e *CashflowEvents) { e.Funding[1].MarkPrice = ledgerDecimal(t, "101") }, ErrLedgerEventConflict},
		"transfer-conflict": {func(e *CashflowEvents) { e.Transfers[1].Direction = TransferOut }, ErrLedgerEventConflict},
		"margin-conflict":   {func(e *CashflowEvents) { e.MarginMovements[1].Symbol = "ETHUSDT" }, ErrLedgerEventConflict},
		"fill-invalid":      {func(e *CashflowEvents) { e.Fills[0].Quantity = Zero() }, ErrInvalidLedgerFill},
		"funding-invalid":   {func(e *CashflowEvents) { e.Funding[0].MarkPrice = Zero() }, ErrInvalidFundingAssessment},
		"transfer-invalid":  {func(e *CashflowEvents) { e.Transfers[0].AmountUSDT = Zero() }, ErrInvalidTransfer},
		"margin-invalid":    {func(e *CashflowEvents) { e.MarginMovements[0].AmountUSDT = Zero() }, ErrInvalidMarginMovement},
		"overclose": {func(e *CashflowEvents) {
			e.Fills = []ConfirmedFill{ledgerFill(t, "s", "BTCUSDT", FillSell, "1", "100", "0")}
		}, ErrLedgerOverclose},
	} {
		t.Run(name, func(t *testing.T) {
			events := CashflowEvents{
				Fills:           append([]ConfirmedFill(nil), base.Fills...),
				Funding:         append([]LongFundingAssessment(nil), base.Funding...),
				Transfers:       append([]ExternalTransfer(nil), base.Transfers...),
				MarginMovements: append([]MarginMovement(nil), base.MarginMovements...),
			}
			tc.mutate(&events)
			if _, err := ReplayCashflowLedger(events); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
