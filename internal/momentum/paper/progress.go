package paper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// ErrPaperProgress обозначает неверное фактическое исполнение или receipt.
var ErrPaperProgress = errors.New("invalid paper progress")

// Progress хранит проверенную фактическую проекцию.
type Progress struct {
	indices   []int
	state     paperwire.PortfolioState
	fills     []model.ConfirmedFill
	remaining []paperwire.IntentRemainder
	complete  bool
}

// ResolutionDisposition задаёт явное решение без утверждения биржевой отмены.
type ResolutionDisposition struct {
	Kind, CommandID string
	ControlRevision uint64
	ResolvedAt      time.Time
}

// ResolvedCycle хранит только текущий receipt и текущий вход.
type ResolvedCycle struct {
	record paperwire.ResolutionRecord
	input  paperwire.InputRecord
}

// finiteFill проверяет SQL-представимость до создания persistable входа.
func finiteFill(f model.ConfirmedFill) error {
	for _, value := range []model.Decimal{f.Quantity, f.ExecutionPrice, f.FeeUSDT} {
		if _, err := value.WireDecimal(); err != nil {
			return errors.Join(ErrPaperProgress, err)
		}
	}
	return nil
}

// MakeFillSlice фиксирует цену, время, fee и детерминированный offset ID.
func MakeFillSlice(p PreparedCycle, index int, already, quantity model.Decimal) (model.ConfirmedFill, error) {
	if p.Hash() == "" {
		return model.ConfirmedFill{}, ErrPaperProgress
	}
	d := p.input.Values()
	if index < 0 || index >= len(d.Fills) {
		return model.ConfirmedFill{}, ErrPaperProgress
	}
	return fillSlice(p.Hash(), index, d.Fills[index], d.Profile.Parameters.FeeRate, already, quantity)
}

// fillSlice вычисляет часть одного намерения по уже проверенным данным цикла.
func fillSlice(hash string, index int, fill model.ConfirmedFill, feeRate, already, quantity model.Decimal) (model.ConfirmedFill, error) {
	if nonnegative(already) != nil || positive(quantity) != nil {
		return model.ConfirmedFill{}, ErrPaperProgress
	}
	total, err := already.Add(quantity)
	if err != nil {
		return model.ConfirmedFill{}, errors.Join(ErrPaperProgress, err)
	}
	if cmp, err := total.Cmp(fill.Quantity); err != nil || cmp > 0 {
		return model.ConfirmedFill{}, ErrPaperProgress
	}
	full := isZero(already) && decimalEqual(quantity, fill.Quantity)
	fill.Quantity = quantity
	notional, err := quantity.Mul(fill.ExecutionPrice)
	if err == nil {
		fill.FeeUSDT, err = notional.Mul(feeRate)
	}
	if err != nil {
		return model.ConfirmedFill{}, errors.Join(ErrPaperProgress, err)
	}
	if err = finiteFill(fill); err != nil {
		return model.ConfirmedFill{}, err
	}
	if !full {
		fields := []string{"momentum/paper-fill-slice/v1", hash, strconv.Itoa(index)}
		for _, value := range []model.Decimal{already, quantity, fill.ExecutionPrice, fill.FeeUSDT} {
			n, d, err := value.CanonicalRatio()
			if err != nil {
				return model.ConfirmedFill{}, errors.Join(ErrPaperProgress, err)
			}
			raw, _ := json.Marshal(struct {
				Numerator   string `json:"numerator"`
				Denominator string `json:"denominator"`
			}{n, d})
			fields = append(fields, string(raw))
		}
		raw, _ := json.Marshal(fields)
		hash := sha256.Sum256(raw)
		fill.ExecutionID = hex.EncodeToString(hash[:])
	}
	return fill, nil
}

// equalFill сравнивает полную экономику и идентичность события.
func equalFill(a, b model.ConfirmedFill) bool {
	return a.ExecutionID == b.ExecutionID && a.Symbol == b.Symbol && a.Side == b.Side && exactUTC(a.ExecutedAt) && exactUTC(b.ExecutedAt) && a.ExecutedAt.Equal(b.ExecutedAt) && decimalEqual(a.Quantity, b.Quantity) && decimalEqual(a.ExecutionPrice, b.ExecutionPrice) && decimalEqual(a.FeeUSDT, b.FeeUSDT)
}

// ProjectProgress восстанавливает однозначный prefix по offset ID независимо от SQL order.
func ProjectProgress(p PreparedCycle, fills []model.ConfirmedFill) (Progress, error) {
	result, err := projectProgress(p, fills)
	if err != nil {
		return Progress{}, errors.Join(ErrPaperProgress, err)
	}
	return result, nil
}

// projectProgress дедуплицирует факты и применяет только доказанные fills.
func projectProgress(p PreparedCycle, fills []model.ConfirmedFill) (Progress, error) {
	if p.Hash() == "" {
		return Progress{}, ErrPaperProgress
	}
	if len(fills) > paperwire.MaxActualFillRecords {
		return Progress{}, paperwire.ErrPaperResourceLimit
	}
	pending := make(map[string]model.ConfirmedFill, len(fills))
	for _, fill := range fills {
		if old, ok := pending[fill.ExecutionID]; ok && !equalFill(old, fill) {
			return Progress{}, model.ErrLedgerFillConflict
		}
		pending[fill.ExecutionID] = fill
	}
	hash := p.Hash()
	d := p.input.Values()
	result := Progress{state: d.Cutoff, complete: true}
	stopped := false
	for index, intent := range d.Fills {
		confirmed := model.Zero()
		for !stopped && !decimalEqual(confirmed, intent.Quantity) {
			var matched model.ConfirmedFill
			matches := 0
			for _, candidate := range pending {
				expected, err := fillSlice(hash, index, intent, d.Profile.Parameters.FeeRate, confirmed, candidate.Quantity)
				if err == nil && equalFill(candidate, expected) {
					matches++
					matched = candidate
				}
			}
			if matches > 1 {
				return Progress{}, ErrPaperProgress
			}
			if matches == 0 {
				stopped = true
				break
			}
			posting, err := model.ProjectLongFill(result.state.Positions[matched.Symbol], matched)
			if err != nil {
				return Progress{}, err
			}
			result.state.WalletUSDT, err = result.state.WalletUSDT.Add(posting.CashDeltaUSDT)
			if err != nil {
				return Progress{}, err
			}
			result.state.Positions[matched.Symbol] = posting.Position
			result.indices = append(result.indices, index)
			result.fills = append(result.fills, matched)
			confirmed, err = confirmed.Add(matched.Quantity)
			if err != nil {
				return Progress{}, err
			}
			delete(pending, matched.ExecutionID)
		}
		remaining, err := intent.Quantity.Sub(confirmed)
		if err != nil {
			return Progress{}, err
		}
		result.remaining = append(result.remaining, paperwire.IntentRemainder{IntentIndex: index, Symbol: intent.Symbol, Side: intent.Side, Requested: intent.Quantity, Confirmed: confirmed, Remaining: remaining})
		result.complete = result.complete && isZero(remaining)
	}
	if len(pending) != 0 {
		return Progress{}, ErrPaperProgress
	}
	market, err := ValidateCycleObservation(SourceRequest{Run: d.Run, Purpose: d.Purpose, Profile: d.Profile, DailyBars: d.Observation.DailyBars, FundingAfter: d.Before.FundingThrough, Now: func() time.Time { return d.Observation.CapturedAt }}, d.Observation)
	if err != nil {
		return Progress{}, err
	}
	result.state, err = accountState(result.state, d, market.Marks)
	if err != nil {
		return Progress{}, err
	}
	if result.complete {
		for _, target := range d.Plan.Targets() {
			if !decimalEqual(result.state.Positions[target.Symbol].Quantity, target.Quantity) {
				return Progress{}, ErrPaperProgress
			}
		}
	}
	return result, nil
}

// SealResolution повторяет финансовую проекцию перед sealing receipt.
func SealResolution(p PreparedCycle, fills []model.ConfirmedFill, disposition ResolutionDisposition) (ResolvedCycle, error) {
	progress, err := ProjectProgress(p, fills)
	if err != nil {
		return ResolvedCycle{}, err
	}
	if disposition.Kind == "COMPLETED" && !progress.Complete() || !exactUTC(disposition.ResolvedAt) {
		return ResolvedCycle{}, ErrPaperProgress
	}
	binding := p.Binding()
	record, err := paperwire.EncodeResolutionRecord(paperwire.ResolutionDocument{InputHash: p.Hash(), RunID: binding.RunID, PlanHash: binding.PlanHash, Kind: disposition.Kind, CommandID: disposition.CommandID, ControlRevision: disposition.ControlRevision, ResolvedAt: disposition.ResolvedAt, Fills: progress.Fills(), Remaining: progress.Remaining(), ActualAfter: progress.ActualAfter()}, p.input)
	if err != nil {
		return ResolvedCycle{}, errors.Join(ErrPaperProgress, err)
	}
	return ResolvedCycle{record: record, input: p.input}, nil
}

// DecodeResolution проверяет сохранённый receipt повторной финансовой проекцией.
func DecodeResolution(raw []byte, p PreparedCycle) (ResolvedCycle, error) {
	record, err := paperwire.DecodeResolutionRecord(raw, p.input)
	if err != nil {
		return ResolvedCycle{}, errors.Join(ErrPaperProgress, err)
	}
	d := record.Values()
	result, err := SealResolution(p, d.Fills, ResolutionDisposition{Kind: d.Kind, CommandID: d.CommandID, ControlRevision: d.ControlRevision, ResolvedAt: d.ResolvedAt})
	if err != nil {
		return ResolvedCycle{}, err
	}
	if !bytes.Equal(raw, result.Payload()) {
		return ResolvedCycle{}, ErrPaperProgress
	}
	return result, nil
}

// ActualAfter возвращает отдельную карту фактического состояния.
func (p Progress) ActualAfter() paperwire.PortfolioState { return copyState(p.state) }

// Fills возвращает копию канонического prefix.
func (p Progress) Fills() []model.ConfirmedFill { return slices.Clone(p.fills) }

// Remaining возвращает копию полного residual.
func (p Progress) Remaining() []paperwire.IntentRemainder { return slices.Clone(p.remaining) }

// Complete подтверждает достижение всех целей.
func (p Progress) Complete() bool { return p.complete }

// Payload возвращает копию receipt bytes.
func (r ResolvedCycle) Payload() []byte { return r.record.Payload() }

// Hash возвращает SHA256 receipt.
func (r ResolvedCycle) Hash() string { return r.record.Hash() }

// InputHash возвращает идентичность текущего входа.
func (r ResolvedCycle) InputHash() string { return r.input.Hash() }

// Kind возвращает явный disposition.
func (r ResolvedCycle) Kind() string { return r.record.Values().Kind }

// CommandID возвращает идентичность команды abort.
func (r ResolvedCycle) CommandID() string { return r.record.Values().CommandID }

// ControlRevision возвращает ревизию команды abort.
func (r ResolvedCycle) ControlRevision() uint64 { return r.record.Values().ControlRevision }

// ActualAfter возвращает отдельное фактическое состояние.
func (r ResolvedCycle) ActualAfter() paperwire.PortfolioState { return r.record.Values().ActualAfter }

// Fills возвращает копию фактического prefix.
func (r ResolvedCycle) Fills() []model.ConfirmedFill { return r.record.Values().Fills }

// Remaining возвращает копию остатка.
func (r ResolvedCycle) Remaining() []paperwire.IntentRemainder { return r.record.Values().Remaining }

// Binding возвращает связи текущего цикла.
func (r ResolvedCycle) Binding() paperwire.CycleBinding { return r.record.Header() }
