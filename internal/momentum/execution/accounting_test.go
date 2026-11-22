package execution

import (
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// tx строит строку transaction log.
func tx(t *testing.T, id, kind, symbol, funding, change string) BrokerTransaction {
	return BrokerTransaction{ID: id, Type: kind, Symbol: symbol, Funding: dec(t, funding), Change: dec(t, change),
		CashFlow: model.Zero(), Fee: model.Zero(), At: time.Unix(1700000000, 0)}
}

// TestImportTransactions проверяет funding, внешние переводы, покрытые fills сделки, повторы и неизвестные типы.
func TestImportTransactions(t *testing.T) {
	rows := []BrokerTransaction{
		tx(t, "f1", "SETTLEMENT", "BTCUSDT", "-0.12", "-0.12"),
		tx(t, "t1", "TRANSFER_IN", "", "0", "500"),
		tx(t, "t1", "TRANSFER_IN", "", "0", "500"),
		tx(t, "t2", "TRANSFER_OUT", "", "0", "-100"),
		tx(t, "x1", "TRADE", "BTCUSDT", "0", "-0.1"),
		tx(t, "b1", "BONUS", "", "0", "5"),
	}
	got, err := ImportTransactions(rows)
	if err != nil || len(got.Entries) != 3 || strings.Join(got.Covered, ",") != "x1" || strings.Join(got.Unexpected, ",") != "BONUS/b1" {
		t.Fatalf("import=%+v %v", got, err)
	}
	var kinds []string
	for _, e := range got.Entries {
		amount, _ := e.AmountUSDT.WireDecimal()
		kinds = append(kinds, string(e.Kind)+":"+e.SourceID+":"+e.Symbol+":"+amount)
	}
	if strings.Join(kinds, ",") != "FUNDING:f1:BTCUSDT:-0.12,TRANSFER:t1::500,TRANSFER:t2::-100" {
		t.Fatalf("entries=%v", kinds)
	}
	conflict := tx(t, "t1", "TRANSFER_IN", "", "0", "501")
	if _, err := ImportTransactions(append(rows, conflict)); !errors.Is(err, ErrAccounting) {
		t.Fatalf("conflicting duplicate=%v", err)
	}
	if _, err := ImportTransactions([]BrokerTransaction{tx(t, "f2", "SETTLEMENT", "", "1", "1")}); !errors.Is(err, ErrAccounting) {
		t.Fatalf("funding without symbol=%v", err)
	}
}

// TestCheckEquity проверяет допуск, общий cutoff и внедрённое расхождение.
func TestCheckEquity(t *testing.T) {
	at := time.Unix(1700000000, 0)
	ok := EquityCheck{LedgerEquity: dec(t, "1000.40"), BrokerEquity: dec(t, "1000"), LedgerAt: at, BrokerAt: at, Tolerance: dec(t, "0.5")}
	if err := CheckEquity(ok); err != nil {
		t.Fatalf("within tolerance=%v", err)
	}
	for name, mutate := range map[string]func(*EquityCheck){
		"discrepancy":      func(c *EquityCheck) { c.BrokerEquity = dec(t, "990") },
		"negative-diff":    func(c *EquityCheck) { c.LedgerEquity = dec(t, "999.4") },
		"different-cutoff": func(c *EquityCheck) { c.BrokerAt = at.Add(time.Second) },
		"bad-tolerance":    func(c *EquityCheck) { c.Tolerance = dec(t, "-1") },
	} {
		c := ok
		mutate(&c)
		if err := CheckEquity(c); !errors.Is(err, ErrEquityMismatch) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
