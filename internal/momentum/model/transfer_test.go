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

// TestValidMarginMovement проверяет оба направления и отказ некорректных движений маржи.
func TestValidMarginMovement(t *testing.T) {
	valid := MarginMovement{"m-1", "BTCUSDT", cashflowAt(2), ledgerDecimal(t, "300")}
	back := MarginMovement{"m-2", "BTCUSDT", cashflowAt(3), ledgerDecimal(t, "-50")}
	if !validMarginMovement(valid) || !validMarginMovement(back) {
		t.Fatal("valid margin movement rejected")
	}
	for name, mutate := range map[string]func(*MarginMovement){
		"empty-id":      func(x *MarginMovement) { x.MovementID = "" },
		"spaced-id":     func(x *MarginMovement) { x.MovementID = "m-1 " },
		"empty-symbol":  func(x *MarginMovement) { x.Symbol = "" },
		"spaced-symbol": func(x *MarginMovement) { x.Symbol = " BTCUSDT" },
		"zero-time":     func(x *MarginMovement) { x.MovedAt = time.Time{} },
		"zero-sum":      func(x *MarginMovement) { x.AmountUSDT = Zero() },
		"no-decimal":    func(x *MarginMovement) { x.AmountUSDT = Decimal{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if validMarginMovement(bad) {
				t.Fatal("invalid margin movement accepted")
			}
		})
	}
}
