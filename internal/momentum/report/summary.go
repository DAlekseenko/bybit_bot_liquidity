// Package report строит итоговый отчёт прогона: просадки, доходность, сверку
// тождества equity и признаки покрытия данных. Пакет не выполняет I/O и не знает
// о конкретной стратегии.
package report

import (
	"errors"
	"fmt"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrInvalidReportInput обозначает некорректный или неполный вход отчёта.
var ErrInvalidReportInput = errors.New("invalid report input")

// Статусы сверки тождества equity.
const (
	IdentityVerified   = "VERIFIED"
	IdentityMismatch   = "MISMATCH"
	IdentityUnverified = "UNVERIFIED"
)

// EntryDefinition фиксирует, что считается входом для счётчика entry fills.
const EntryDefinition = "BUY fill increasing a long position"

// EquityPoint — наблюдение equity и накопленных внешних переводов на момент At.
type EquityPoint struct {
	At                      time.Time
	EquityUSDT              model.Decimal
	CumulativeTransfersUSDT model.Decimal
}

// Drawdown — максимальная просадка ряда или причина, по которой она недоступна.
type Drawdown struct {
	Available  bool
	Reason     string
	PeakAt     time.Time
	TroughAt   time.Time
	PeakUSDT   model.Decimal
	TroughUSDT model.Decimal
	AmountUSDT model.Decimal
	Ratio      model.Decimal
}

// MaxDrawdown считает максимальную по доле от пика просадку ряда с шагом step. Пустой ряд,
// пропуск шага или неположительный пик дают недоступный результат с причиной,
// а не ноль. adjusted вычитает накопленные переводы из equity.
func MaxDrawdown(points []EquityPoint, step time.Duration, adjusted bool) (Drawdown, error) {
	if step <= 0 {
		return Drawdown{}, ErrInvalidReportInput
	}
	if len(points) == 0 {
		return Drawdown{Reason: "no observations"}, nil
	}
	values := make([]model.Decimal, len(points))
	for i, p := range points {
		if p.At.IsZero() || (i > 0 && !p.At.After(points[i-1].At)) {
			return Drawdown{}, ErrInvalidReportInput
		}
		v, err := pointValue(p, adjusted)
		if err != nil {
			return Drawdown{}, err
		}
		values[i] = v
	}
	for i := 1; i < len(points); i++ {
		if points[i].At.Sub(points[i-1].At) != step {
			return Drawdown{Reason: "gap after " + points[i-1].At.UTC().Format(time.RFC3339)}, nil
		}
	}
	best := Drawdown{Available: true, PeakAt: points[0].At.UTC(), TroughAt: points[0].At.UTC(),
		PeakUSDT: values[0], TroughUSDT: values[0], AmountUSDT: model.Zero(), Ratio: model.Zero()}
	peak, peakAt := values[0], points[0].At.UTC()
	for i, v := range values {
		if c, err := v.Cmp(peak); err != nil {
			return Drawdown{}, ErrInvalidReportInput
		} else if c > 0 {
			peak, peakAt = v, points[i].At.UTC()
		}
		if c, _ := peak.Cmp(model.Zero()); c <= 0 {
			return Drawdown{Reason: "non-positive equity peak"}, nil
		}
		amount, err := peak.Sub(v)
		if err != nil {
			return Drawdown{}, ErrInvalidReportInput
		}
		ratio, err := amount.Quo(peak)
		if err != nil {
			return Drawdown{}, ErrInvalidReportInput
		}
		// Максимум выбирается по доле от пика, а не по абсолютной сумме.
		if c, _ := ratio.Cmp(best.Ratio); c > 0 {
			best = Drawdown{Available: true, PeakAt: peakAt, TroughAt: points[i].At.UTC(),
				PeakUSDT: peak, TroughUSDT: v, AmountUSDT: amount, Ratio: ratio}
		}
	}
	return best, nil
}

// pointValue возвращает equity точки, при adjusted — за вычетом переводов.
func pointValue(p EquityPoint, adjusted bool) (model.Decimal, error) {
	if _, err := p.EquityUSDT.Cmp(model.Zero()); err != nil {
		return model.Decimal{}, ErrInvalidReportInput
	}
	if _, err := p.CumulativeTransfersUSDT.Cmp(model.Zero()); err != nil {
		return model.Decimal{}, ErrInvalidReportInput
	}
	if !adjusted {
		return p.EquityUSDT, nil
	}
	v, err := p.EquityUSDT.Sub(p.CumulativeTransfersUSDT)
	if err != nil {
		return model.Decimal{}, ErrInvalidReportInput
	}
	return v, nil
}

// Totals — итоговые денежные величины прогона в USDT. Funding: поступление
// положительно, списание отрицательно. Slippage уже входит в цены исполнения и
// показывается только для анализа.
type Totals struct {
	RealizedPnLUSDT        model.Decimal
	UnrealizedPnLStartUSDT model.Decimal
	UnrealizedPnLEndUSDT   model.Decimal
	FeesUSDT               model.Decimal
	FundingUSDT            model.Decimal
	SlippageUSDT           model.Decimal
	NetTransfersUSDT       model.Decimal
}

// Coverage описывает полноту исходных данных прогона.
type Coverage struct {
	FundingComplete     bool
	FundingZeroScenario bool
	MarkComplete        bool
	MarginHistory       bool
	Notes               []string
}

// SummaryInput — всё, из чего строится итоговый отчёт. Daily обязателен,
// Hourly может отсутствовать.
type SummaryInput struct {
	RunID, StrategyID, ProfileID, ConfigHash, DatasetHash string
	Daily, Hourly                                         []EquityPoint
	Totals                                                Totals
	Fills                                                 []model.ConfirmedFill
	EntryIntents                                          int
	Coverage                                              Coverage
}

// Identity — результат сверки delta_equity = realized + delta_unrealized - fees + funding + transfers.
type Identity struct {
	Status          string
	DeltaEquityUSDT model.Decimal
	ExplainedUSDT   model.Decimal
	DifferenceUSDT  model.Decimal
}

// Summary — итоговый отчёт прогона без идентификаторов счёта и секретов.
type Summary struct {
	RunID, StrategyID, ProfileID, ConfigHash, DatasetHash string
	Scenario                                              string
	Start, End                                            time.Time
	InitialEquityUSDT, FinalEquityUSDT                    model.Decimal
	NetReturn, TransferAdjustedReturn                     model.Decimal
	Totals                                                Totals
	DailyDrawdown, HourlyDrawdown                         Drawdown
	TransferAdjustedDailyDrawdown                         Drawdown
	Identity                                              Identity
	FillCount, EntryFillCount, EntryIntentCount           int
	EntryDefinition                                       string
	Coverage                                              Coverage
}

// BuildSummary проверяет вход и строит итоговый отчёт.
func BuildSummary(in SummaryInput) (Summary, error) {
	if in.RunID == "" || in.StrategyID == "" || in.ConfigHash == "" || in.DatasetHash == "" ||
		len(in.Daily) == 0 || in.EntryIntents < 0 {
		return Summary{}, ErrInvalidReportInput
	}
	t := in.Totals
	for _, d := range []model.Decimal{t.RealizedPnLUSDT, t.UnrealizedPnLStartUSDT, t.UnrealizedPnLEndUSDT,
		t.FeesUSDT, t.FundingUSDT, t.SlippageUSDT, t.NetTransfersUSDT} {
		if _, err := d.Cmp(model.Zero()); err != nil {
			return Summary{}, ErrInvalidReportInput
		}
	}
	scenario := "actual_funding"
	if in.Coverage.FundingZeroScenario {
		if c, _ := t.FundingUSDT.Cmp(model.Zero()); c != 0 {
			return Summary{}, fmt.Errorf("%w: funding_zero scenario with non-zero funding", ErrInvalidReportInput)
		}
		scenario = "funding_zero"
	}
	first, last := in.Daily[0], in.Daily[len(in.Daily)-1]
	s := Summary{RunID: in.RunID, StrategyID: in.StrategyID, ProfileID: in.ProfileID,
		ConfigHash: in.ConfigHash, DatasetHash: in.DatasetHash, Scenario: scenario,
		Start: first.At.UTC(), End: last.At.UTC(),
		InitialEquityUSDT: first.EquityUSDT, FinalEquityUSDT: last.EquityUSDT,
		Totals: t, EntryIntentCount: in.EntryIntents, EntryDefinition: EntryDefinition,
		FillCount: len(in.Fills), Coverage: in.Coverage}
	var err error
	if s.DailyDrawdown, err = MaxDrawdown(in.Daily, 24*time.Hour, false); err != nil {
		return Summary{}, err
	}
	if s.TransferAdjustedDailyDrawdown, err = MaxDrawdown(in.Daily, 24*time.Hour, true); err != nil {
		return Summary{}, err
	}
	if s.HourlyDrawdown, err = MaxDrawdown(in.Hourly, time.Hour, false); err != nil {
		return Summary{}, err
	}
	for _, f := range in.Fills {
		if f.Side == model.FillBuy {
			s.EntryFillCount++
		}
	}
	delta, err := s.FinalEquityUSDT.Sub(s.InitialEquityUSDT)
	if err != nil {
		return Summary{}, ErrInvalidReportInput
	}
	if s.NetReturn, err = delta.Quo(s.InitialEquityUSDT); err != nil {
		return Summary{}, ErrInvalidReportInput
	}
	pnl, err := delta.Sub(t.NetTransfersUSDT)
	if err != nil {
		return Summary{}, ErrInvalidReportInput
	}
	if s.TransferAdjustedReturn, err = pnl.Quo(s.InitialEquityUSDT); err != nil {
		return Summary{}, ErrInvalidReportInput
	}
	if s.Identity, err = checkIdentity(delta, t, in.Coverage); err != nil {
		return Summary{}, err
	}
	return s, nil
}

// checkIdentity сверяет изменение equity с суммой его составляющих. При неполных
// funding или mark данных результат UNVERIFIED, а остаток не подгоняется.
func checkIdentity(delta model.Decimal, t Totals, c Coverage) (Identity, error) {
	steps := []struct {
		v   model.Decimal
		add bool
	}{{t.UnrealizedPnLEndUSDT, true}, {t.UnrealizedPnLStartUSDT, false}, {t.FeesUSDT, false},
		{t.FundingUSDT, true}, {t.NetTransfersUSDT, true}}
	explained := t.RealizedPnLUSDT
	for _, s := range steps {
		var err error
		if s.add {
			explained, err = explained.Add(s.v)
		} else {
			explained, err = explained.Sub(s.v)
		}
		if err != nil {
			return Identity{}, ErrInvalidReportInput
		}
	}
	diff, err := delta.Sub(explained)
	if err != nil {
		return Identity{}, ErrInvalidReportInput
	}
	status := IdentityVerified
	if !c.FundingComplete || !c.MarkComplete {
		status = IdentityUnverified
	} else if cmp, _ := diff.Cmp(model.Zero()); cmp != 0 {
		status = IdentityMismatch
	}
	return Identity{Status: status, DeltaEquityUSDT: delta, ExplainedUSDT: explained, DifferenceUSDT: diff}, nil
}
