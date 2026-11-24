package model

import (
	"errors"
	"sort"
	"time"
)

// ErrLedgerEventConflict означает повтор ID события с другими данными.
var ErrLedgerEventConflict = errors.New("ledger event ID has different data")

// CashflowEvents содержит подтверждённые события одного счёта в порядке журнала каждого вида.
type CashflowEvents struct {
	Fills           []ConfirmedFill
	Funding         []LongFundingAssessment
	Transfers       []ExternalTransfer
	MarginMovements []MarginMovement
}

// CashflowLedger содержит проекцию fills, funding и переводов в детерминированном порядке.
// WalletDeltaUSDT = RealizedPnLUSDT - FeesUSDT + FundingCashflowUSDT + NetTransfersUSDT.
type CashflowLedger struct {
	Positions           map[string]LongPosition
	Fills               []FillPosting
	Funding             []FundingPosting
	Transfers           []TransferPosting
	MarginMovements     []MarginMovement
	RealizedPnLUSDT     Decimal
	FeesUSDT            Decimal
	FundingCashflowUSDT Decimal
	NetTransfersUSDT    Decimal
	WalletDeltaUSDT     Decimal
}

// Ранги событий одного timestamp по D-07: funding к старым позициям раньше
// исполнений rebalance; переводы и margin movements не меняют позиций.
const (
	rankFunding = iota
	rankTransfer
	rankMargin
	rankFill
)

// cashflowEvent хранит одно нормализованное событие для сортировки.
type cashflowEvent struct {
	at       time.Time
	rank     int
	fill     ConfirmedFill
	funding  LongFundingAssessment
	transfer ExternalTransfer
	margin   MarginMovement
}

// ReplayCashflowLedger применяет события ровно один раз по ID в порядке времени и D-07.
// При равном времени и виде сохраняется порядок журнала; funding берёт позицию на свой timestamp.
func ReplayCashflowLedger(events CashflowEvents) (CashflowLedger, error) {
	fills, err := uniqueEvents(events.Fills, validConfirmedFill,
		func(f *ConfirmedFill) (string, *time.Time) { return f.ExecutionID, &f.ExecutedAt }, sameConfirmedFill, ErrInvalidLedgerFill, ErrLedgerFillConflict)
	if err != nil {
		return CashflowLedger{}, err
	}
	funding, err := uniqueEvents(events.Funding, validLongFundingAssessment,
		func(f *LongFundingAssessment) (string, *time.Time) { return f.FundingID, &f.Event.FundingTime }, sameFunding, ErrInvalidFundingAssessment, ErrLedgerEventConflict)
	if err != nil {
		return CashflowLedger{}, err
	}
	transfers, err := uniqueEvents(events.Transfers, validExternalTransfer,
		func(t *ExternalTransfer) (string, *time.Time) { return t.TransferID, &t.TransferredAt }, sameTransfer, ErrInvalidTransfer, ErrLedgerEventConflict)
	if err != nil {
		return CashflowLedger{}, err
	}
	margins, err := uniqueEvents(events.MarginMovements, validMarginMovement,
		func(m *MarginMovement) (string, *time.Time) { return m.MovementID, &m.MovedAt }, sameMargin, ErrInvalidMarginMovement, ErrLedgerEventConflict)
	if err != nil {
		return CashflowLedger{}, err
	}

	ordered := make([]cashflowEvent, 0, len(fills)+len(funding)+len(transfers)+len(margins))
	for _, f := range funding {
		ordered = append(ordered, cashflowEvent{at: f.Event.FundingTime, rank: rankFunding, funding: f})
	}
	for _, t := range transfers {
		ordered = append(ordered, cashflowEvent{at: t.TransferredAt, rank: rankTransfer, transfer: t})
	}
	for _, m := range margins {
		ordered = append(ordered, cashflowEvent{at: m.MovedAt, rank: rankMargin, margin: m})
	}
	for _, f := range fills {
		ordered = append(ordered, cashflowEvent{at: f.ExecutedAt, rank: rankFill, fill: f})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].at.Before(ordered[j].at)
		}
		return ordered[i].rank < ordered[j].rank
	})

	result := CashflowLedger{Positions: make(map[string]LongPosition), RealizedPnLUSDT: Zero(), FeesUSDT: Zero(),
		FundingCashflowUSDT: Zero(), NetTransfersUSDT: Zero(), WalletDeltaUSDT: Zero()}
	for _, event := range ordered {
		if err := result.apply(event); err != nil {
			return CashflowLedger{}, err
		}
	}
	return result, nil
}

// apply проводит одно событие и обновляет точные итоги проекции.
func (l *CashflowLedger) apply(event cashflowEvent) error {
	switch event.rank {
	case rankFunding:
		posting, err := ProjectLongFunding(l.position(event.funding.Symbol), event.funding)
		if err != nil {
			return err
		}
		l.Funding = append(l.Funding, posting)
		return l.addCash(&l.FundingCashflowUSDT, posting.CashDeltaUSDT)
	case rankTransfer:
		posting, err := ProjectExternalTransfer(event.transfer)
		if err != nil {
			return err
		}
		l.Transfers = append(l.Transfers, posting)
		return l.addCash(&l.NetTransfersUSDT, posting.CashDeltaUSDT)
	case rankMargin:
		l.MarginMovements = append(l.MarginMovements, event.margin)
		return nil
	default:
		posting, err := projectLongFill(l.position(event.fill.Symbol), event.fill)
		if err != nil {
			return err
		}
		realized, err := l.RealizedPnLUSDT.Add(posting.RealizedPnLUSDT)
		if err != nil {
			return ErrLedgerArithmetic
		}
		fees, err := l.FeesUSDT.Add(posting.Fill.FeeUSDT)
		if err != nil {
			return ErrLedgerArithmetic
		}
		wallet, err := l.WalletDeltaUSDT.Add(posting.CashDeltaUSDT)
		if err != nil {
			return ErrLedgerArithmetic
		}
		l.RealizedPnLUSDT, l.FeesUSDT, l.WalletDeltaUSDT = realized, fees, wallet
		l.Positions[posting.Fill.Symbol] = posting.Position
		l.Fills = append(l.Fills, posting)
		return nil
	}
}

// addCash прибавляет денежный поток к итогу его вида и к изменению wallet.
func (l *CashflowLedger) addCash(total *Decimal, delta Decimal) error {
	sum, err := total.Add(delta)
	if err != nil {
		return ErrLedgerArithmetic
	}
	wallet, err := l.WalletDeltaUSDT.Add(delta)
	if err != nil {
		return ErrLedgerArithmetic
	}
	*total, l.WalletDeltaUSDT = sum, wallet
	return nil
}

// position возвращает текущую позицию символа или нулевую позицию.
func (l *CashflowLedger) position(symbol string) LongPosition {
	if p, ok := l.Positions[symbol]; ok {
		return p
	}
	return LongPosition{Quantity: Zero(), CostBasisUSDT: Zero()}
}

// uniqueEvents проверяет события, приводит время к UTC и отбрасывает идентичные повторы ID.
func uniqueEvents[T any](items []T, valid func(T) bool, fields func(*T) (string, *time.Time),
	same func(a, b T) bool, invalidErr, conflictErr error) ([]T, error) {
	seen := make(map[string]T, len(items))
	out := make([]T, 0, len(items))
	for _, item := range items {
		if !valid(item) {
			return nil, invalidErr
		}
		id, at := fields(&item)
		*at = at.Round(0).UTC()
		if previous, ok := seen[id]; ok {
			if !same(previous, item) {
				return nil, conflictErr
			}
			continue
		}
		seen[id] = item
		out = append(out, item)
	}
	return out, nil
}

// sameFunding сравнивает все поля нормализованных событий funding.
func sameFunding(a, b LongFundingAssessment) bool {
	return a.Symbol == b.Symbol && a.Event.FundingTime.Equal(b.Event.FundingTime) &&
		a.Event.Rate.value.Cmp(b.Event.Rate.value) == 0 && a.MarkPrice.value.Cmp(b.MarkPrice.value) == 0
}

// sameTransfer сравнивает все поля нормализованных переводов.
func sameTransfer(a, b ExternalTransfer) bool {
	return a.Direction == b.Direction && a.TransferredAt.Equal(b.TransferredAt) &&
		a.AmountUSDT.value.Cmp(b.AmountUSDT.value) == 0
}

// sameMargin сравнивает все поля нормализованных движений маржи.
func sameMargin(a, b MarginMovement) bool {
	return a.Symbol == b.Symbol && a.MovedAt.Equal(b.MovedAt) && a.AmountUSDT.value.Cmp(b.AmountUSDT.value) == 0
}
