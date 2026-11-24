package storage

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperFillConfirmationArgument означает неверный запрос.
	ErrPaperFillConfirmationArgument = errors.New("invalid paper fill confirmation argument")
	// ErrPaperFillConfirmationConflict означает противоречие сохранённым фактам.
	ErrPaperFillConfirmationConflict = errors.New("paper fill confirmation conflict")
	// ErrPaperFillConfirmationUnavailable означает недоступность снимка.
	ErrPaperFillConfirmationUnavailable = errors.New("paper fill confirmation unavailable")
	// ErrPaperFillConfirmationRejected означает отказ финансовой проверки.
	ErrPaperFillConfirmationRejected = errors.New("paper fill confirmation rejected")
)

// PaperWriteKey указывает конкретный вход цикла.
type PaperWriteKey struct {
	Scope            ControlScope
	RunID, InputHash string
}

// PaperFillExpectation задаёт ожидаемые неизменяемые поля исполнения.
type PaperFillExpectation struct {
	Key         PaperWriteKey
	OrderLinkID string
	Fill        model.ConfirmedFill
}

// PaperFillConfirmation содержит состояние и исполненное количество указанного намерения.
type PaperFillConfirmation struct {
	FilledQuantity model.Decimal
	State          string
}

// PaperFillCheck проверяет финансовую модель по снимку транзакции.
type PaperFillCheck func(PaperRunFacts) error

// valid проверяет адрес и представимость исполнения до обращения к хранилищу.
func (e PaperFillExpectation) valid(ctx context.Context) bool {
	key, fill := e.Key, e.Fill
	_, hashErr := datasetArtifactKey(key.InputHash)
	_, wireErr := wireValues(fill.Quantity, fill.ExecutionPrice, fill.FeeUSDT)
	q, _ := fill.Quantity.Cmp(model.Zero())
	p, _ := fill.ExecutionPrice.Cmp(model.Zero())
	base, ok := strings.CutSuffix(fill.Symbol, "USDT")
	if !ok || base == "" || len(fill.Symbol) > paperwire.MaxCanonicalBytes {
		return false
	}
	for _, c := range base {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	at := fill.ExecutedAt
	return validLifecycleRequest(ctx, key.Scope, key.RunID) && hashErr == nil && wireErr == nil && q > 0 && p > 0 &&
		paperControlText(e.OrderLinkID, fill.ExecutionID, fill.Symbol) && len(e.OrderLinkID) <= paperwire.MaxCanonicalBytes &&
		len(fill.ExecutionID) <= paperwire.MaxCanonicalBytes && len(key.RunID) <= paperwire.MaxCanonicalBytes &&
		len(key.Scope.AccountID) <= paperwire.MaxCanonicalBytes && len(key.Scope.StrategyID) <= paperwire.MaxCanonicalBytes &&
		(fill.Side == model.FillBuy || fill.Side == model.FillSell) && at.Location() == time.UTC &&
		at.Year() >= 1970 && at.Year() <= 9999 && at.Nanosecond()%1000 == 0
}

// ReadPaperFillConfirmation проверяет конкретное исполнение в одном согласованном снимке.
func ReadPaperFillConfirmation(ctx context.Context, db *sql.DB, expected PaperFillExpectation, check PaperFillCheck) (PaperFillConfirmation, bool, error) {
	if db == nil || check == nil || !expected.valid(ctx) {
		return PaperFillConfirmation{}, false, ErrPaperFillConfirmationArgument
	}
	f, err := ReadPaperRunFacts(ctx, db, expected.Key.Scope, expected.Key.RunID)
	if errors.Is(err, ErrPaperFactsAbsent) || errors.Is(err, ErrPaperFactsCorrupt) {
		return PaperFillConfirmation{}, false, ErrPaperFillConfirmationConflict
	}
	if err != nil {
		return PaperFillConfirmation{}, false, errors.Join(ErrPaperFillConfirmationUnavailable, ctx.Err())
	}
	return expected.confirmation(f, check)
}

// confirmation связывает адрес с исходным индексом и проверяет все фактические исполнения.
func (e PaperFillExpectation) confirmation(f PaperRunFacts, check PaperFillCheck) (PaperFillConfirmation, bool, error) {
	key, fill := e.Key, e.Fill
	h := f.Input.Header()
	if !f.InputFound || !f.PlanFound || f.Input.Hash() != key.InputHash || f.Plan.Hash() != h.PlanHash ||
		f.RunID != key.RunID || f.Scope != key.Scope || h.RunID != key.RunID || h.Scope != paperwire.Scope(key.Scope) {
		return PaperFillConfirmation{}, false, ErrPaperFillConfirmationConflict
	}
	intent, present := f.intents[e.OrderLinkID]
	for index, expected := range f.Input.Values().Fills {
		link, err := PaperIntentIdentity(key.InputHash, index)
		if err != nil || link != e.OrderLinkID {
			continue
		}
		if fill.Symbol != expected.Symbol || fill.Side != expected.Side {
			break
		}
		if present && (intent.Symbol != expected.Symbol || intent.Side != string(expected.Side) ||
			intent.ReduceOnly != (expected.Side == model.FillSell) || !paperAmountEqual(intent.Quantity, expected.Quantity)) {
			break
		}
		row, found := f.fills[fill.ExecutionID]
		if !found {
			return PaperFillConfirmation{}, false, nil
		}
		if !present || row.OrderLinkID != link || row.Symbol != fill.Symbol || row.Side != string(fill.Side) ||
			!row.ExecutedAt.Equal(fill.ExecutedAt) || !paperAmountEqual(row.Quantity, fill.Quantity) ||
			!paperAmountEqual(row.ExecutionPrice, fill.ExecutionPrice) || !paperAmountEqual(row.FeeUSDT, fill.FeeUSDT) {
			break
		}
		if err := check(f); err != nil {
			return PaperFillConfirmation{}, false, errors.Join(ErrPaperFillConfirmationRejected, err)
		}
		total := model.Zero()
		for _, id := range slices.Sorted(maps.Keys(f.fills)) {
			other := f.fills[id]
			if other.OrderLinkID == link {
				q, err := paperwire.ParseRecordedAmount(other.Quantity)
				if err != nil {
					return PaperFillConfirmation{}, false, ErrPaperFillConfirmationConflict
				}
				total, err = total.Add(q)
				if err != nil {
					return PaperFillConfirmation{}, false, ErrPaperFillConfirmationConflict
				}
			}
		}
		return PaperFillConfirmation{total, intent.State}, true, nil
	}
	return PaperFillConfirmation{}, false, ErrPaperFillConfirmationConflict
}

// paperAmountEqual сравнивает SQL NUMERIC с точной величиной модели.
func paperAmountEqual(raw string, want model.Decimal) bool {
	got, err := paperwire.ParseRecordedAmount(raw)
	c, cmpErr := got.Cmp(want)
	return err == nil && cmpErr == nil && c == 0
}
