package paper

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/storage"
)

var (
	// ErrRecordedCycleArgument означает ошибку аргументов чтения.
	ErrRecordedCycleArgument = errors.New("invalid recorded cycle argument")
	// ErrRecordedCycleAbsent означает отсутствие области или указанного run.
	ErrRecordedCycleAbsent = errors.New("recorded cycle absent")
	// ErrRecordedCycleCorrupt означает несовместимость исходных строк и модели.
	ErrRecordedCycleCorrupt = errors.New("corrupt recorded cycle")
	// ErrRecordedCycleUnavailable означает недоступность снимка.
	ErrRecordedCycleUnavailable = errors.New("recorded cycle unavailable")
)

// RecordedPaperCycle объединяет один SQL-снимок с проверенными финансовыми проекциями.
type RecordedPaperCycle struct {
	facts      storage.PaperRunFacts
	prepared   PreparedCycle
	progress   Progress
	resolution *ResolvedCycle
	intents    []storage.OrderIntent
}

// Prepared возвращает финансовую модель записанного входа.
func (v RecordedPaperCycle) Prepared() (PreparedCycle, bool) { return v.prepared, v.facts.InputFound }

// Progress возвращает фактический префикс исполнений.
func (v RecordedPaperCycle) Progress() (Progress, bool) { return v.progress, v.facts.InputFound }

// Resolution возвращает исходный подтверждённый итог.
func (v RecordedPaperCycle) Resolution() (ResolvedCycle, bool) {
	if v.resolution == nil {
		return ResolvedCycle{}, false
	}
	return *v.resolution, true
}

// OrderIntents возвращает независимую копию полных исходных намерений.
func (v RecordedPaperCycle) OrderIntents() []storage.OrderIntent { return slices.Clone(v.intents) }

// PlanFound сообщает о наличии плана в прочитанном снимке.
func (v RecordedPaperCycle) PlanFound() bool { return v.facts.PlanFound }

// DataHash возвращает отпечаток фактов выбранного run.
func (v RecordedPaperCycle) DataHash() string { return v.facts.DataHash }

// ReadRecordedPaperCycle читает один снимок явно заданного run и проверяет его модель.
func ReadRecordedPaperCycle(ctx context.Context, db *sql.DB, scope storage.ControlScope, runID string, previous *ResolvedCycle, build PlanBuilder, admit FillAdmission) (RecordedPaperCycle, error) {
	if runID == "" || build == nil || admit == nil {
		return RecordedPaperCycle{}, ErrRecordedCycleArgument
	}
	f, err := storage.ReadPaperRunFacts(ctx, db, scope, runID)
	if err != nil {
		kind := ErrRecordedCycleUnavailable
		switch {
		case errors.Is(err, storage.ErrPaperFactsArgument):
			kind = ErrRecordedCycleArgument
		case errors.Is(err, storage.ErrPaperFactsAbsent):
			kind = ErrRecordedCycleAbsent
		case errors.Is(err, storage.ErrPaperFactsCorrupt):
			kind = ErrRecordedCycleCorrupt
		}
		return RecordedPaperCycle{}, errors.Join(kind, err)
	}
	return replayRecordedPaperCycle(f, previous, build, admit)
}

// replayRecordedPaperCycle восстанавливает финансовую модель из переданных фактов без I/O.
func replayRecordedPaperCycle(f storage.PaperRunFacts, previous *ResolvedCycle, build PlanBuilder, admit FillAdmission) (RecordedPaperCycle, error) {
	result := RecordedPaperCycle{facts: f}
	if !f.InputFound {
		return result, nil
	}
	p, err := DecodePreparedCycle(f.Input.Payload(), previous, build, admit)
	if err != nil {
		return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
	}
	if err = CheckPaperFill(p)(f); err != nil {
		return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
	}
	fills, err := f.ConfirmedFills()
	if err != nil {
		return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
	}
	result.prepared = p
	result.progress, err = ProjectProgress(p, fills)
	if err != nil {
		return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
	}
	if f.Completed {
		r, err := DecodeResolution([]byte(f.Completion.Reason), p)
		if err != nil {
			return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
		}
		if !slices.EqualFunc(r.Fills(), result.progress.fills, equalFill) {
			return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, ErrPaperProgress)
		}
		result.resolution = &r
	}
	result.intents, err = result.projectIntents()
	if err != nil {
		return RecordedPaperCycle{}, errors.Join(ErrRecordedCycleCorrupt, err)
	}
	return result, nil
}

// projectIntents строит ожидаемые намерения и сверяет каждую уже опубликованную строку.
func (v RecordedPaperCycle) projectIntents() ([]storage.OrderIntent, error) {
	h := v.prepared.Binding()
	revision := v.prepared.Plan().Metadata().Revision
	actual := v.facts.Intents()
	fills := v.prepared.ExpectedFills()
	intents := make([]storage.OrderIntent, 0, len(fills))
	for i, fill := range fills {
		link, err := storage.PaperIntentIdentity(h.InputHash, i)
		if err != nil {
			return nil, err
		}
		intent := storage.OrderIntent{IntentID: link, OrderLinkID: link, Environment: h.Scope.Environment, AccountID: h.Scope.AccountID,
			RunID: h.RunID, PlanHash: h.PlanHash, PlanRevision: revision, Symbol: fill.Symbol, Side: fill.Side, ReduceOnly: fill.Side == model.FillSell, Quantity: fill.Quantity}
		intents = append(intents, intent)
		row, ok := actual[link]
		if !ok {
			continue
		}
		amount, err := paperwire.ParseRecordedAmount(row.Quantity)
		if err != nil || !decimalEqual(amount, intent.Quantity) || row.Symbol != intent.Symbol || row.Side != string(intent.Side) || row.ReduceOnly != intent.ReduceOnly {
			return nil, ErrPaperProgress
		}
		r := v.progress.remaining[i]
		state := "PARTIALLY_FILLED"
		switch {
		case isZero(r.Remaining):
			state = "FILLED"
		case row.State == "CANCELLED" && v.resolution != nil && v.resolution.Kind() == "ABORTED":
			state = "CANCELLED"
		case isZero(r.Confirmed):
			state = "PENDING"
		}
		if row.State != state {
			return nil, ErrPaperProgress
		}
	}
	return intents, nil
}
