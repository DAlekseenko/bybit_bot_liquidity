package execution

import (
	"errors"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrAccounting — общая категория ошибок сверки учёта.
	ErrAccounting = errors.New("momentum accounting reconciliation failed")
	// ErrEquityMismatch означает расхождение equity ledger и биржи сверх допуска.
	ErrEquityMismatch = errors.New("momentum ledger equity differs from broker equity")
)

// BrokerTransaction — строка transaction log биржи в USDT (bybit.TransactionRecord).
type BrokerTransaction struct {
	ID, Type, Symbol               string
	CashFlow, Funding, Fee, Change model.Decimal
	At                             time.Time
}

// LedgerKind — вид проводки, импортируемой из transaction log.
type LedgerKind string

// Виды проводок: funding по позиции и внешний перевод (без символа).
const (
	LedgerFunding  LedgerKind = "FUNDING"
	LedgerTransfer LedgerKind = "TRANSFER"
)

// LedgerEntry — проводка с уникальным source ID для momentum.ledger_entries.
type LedgerEntry struct {
	Kind       LedgerKind
	SourceID   string
	Symbol     string // пусто для TRANSFER
	AmountUSDT model.Decimal
	OccurredAt time.Time
}

// TransactionImport — проводки, уже учтённые через fills записи и нераспознанные строки.
type TransactionImport struct {
	Entries    []LedgerEntry
	Covered    []string // TRADE: комиссия и cash flow учтены через fills (CM-031)
	Unexpected []string // тип без правила импорта — блокирует увеличение бюджета
}

// ImportTransactions переводит transaction log в проводки ledger. SETTLEMENT —
// funding по символу, TRANSFER_IN/TRANSFER_OUT — внешние переводы (net_transfers),
// TRADE уже учтён через fills. Перемещение маржи между кошельком и isolated
// позицией в transaction log не попадает и переводом не считается. Повтор одного
// ID с теми же данными — no-op, с другими — ErrAccounting.
func ImportTransactions(rows []BrokerTransaction) (TransactionImport, error) {
	var out TransactionImport
	seen := map[string]BrokerTransaction{}
	for _, row := range rows {
		if prev, ok := seen[row.ID]; ok {
			if !sameTransaction(prev, row) {
				return TransactionImport{}, ErrAccounting
			}
			continue
		}
		if row.ID == "" || row.At.IsZero() {
			return TransactionImport{}, ErrAccounting
		}
		seen[row.ID] = row
		switch row.Type {
		case "SETTLEMENT":
			if row.Symbol == "" {
				return TransactionImport{}, ErrAccounting
			}
			out.Entries = append(out.Entries, LedgerEntry{Kind: LedgerFunding, SourceID: row.ID, Symbol: row.Symbol,
				AmountUSDT: row.Funding, OccurredAt: row.At.UTC()})
		case "TRANSFER_IN", "TRANSFER_OUT":
			out.Entries = append(out.Entries, LedgerEntry{Kind: LedgerTransfer, SourceID: row.ID, AmountUSDT: row.Change,
				OccurredAt: row.At.UTC()})
		case "TRADE":
			out.Covered = append(out.Covered, row.ID)
		default:
			out.Unexpected = append(out.Unexpected, row.Type+"/"+row.ID)
		}
	}
	sort.Strings(out.Unexpected)
	return out, nil
}

// EquityCheck — equity ledger и биржи на общем cutoff.
type EquityCheck struct {
	LedgerEquity, BrokerEquity model.Decimal
	LedgerAt, BrokerAt         time.Time
	Tolerance                  model.Decimal // допуск в USDT, задаёт владелец (D-04/D-10)
}

// CheckEquity разрешает увеличение бюджета, только если оба значения сняты на
// один cutoff и отличаются не больше допуска; иначе — ErrEquityMismatch.
func CheckEquity(c EquityCheck) error {
	tol, err := c.Tolerance.Cmp(model.Zero())
	if err != nil || tol < 0 || c.LedgerAt.IsZero() || !c.LedgerAt.Equal(c.BrokerAt) {
		return errors.Join(ErrAccounting, ErrEquityMismatch)
	}
	diff, err := c.LedgerEquity.Sub(c.BrokerEquity)
	if err != nil {
		return errors.Join(ErrAccounting, ErrEquityMismatch)
	}
	if neg, _ := diff.Cmp(model.Zero()); neg < 0 {
		diff, _ = model.Zero().Sub(diff)
	}
	if over, err := diff.Cmp(c.Tolerance); err != nil || over > 0 {
		return errors.Join(ErrAccounting, ErrEquityMismatch)
	}
	return nil
}

// sameTransaction сравнивает две строки transaction log.
func sameTransaction(a, b BrokerTransaction) bool {
	eq := func(x, y model.Decimal) bool { c, err := x.Cmp(y); return err == nil && c == 0 }
	return a.Type == b.Type && a.Symbol == b.Symbol && a.At.Equal(b.At) && eq(a.CashFlow, b.CashFlow) &&
		eq(a.Funding, b.Funding) && eq(a.Fee, b.Fee) && eq(a.Change, b.Change)
}
