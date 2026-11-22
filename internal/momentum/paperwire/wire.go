package paperwire

import (
	"encoding/json"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// Scope определяет отдельное пространство бумажного учёта.
type Scope struct{ Environment, AccountID, StrategyID string }

// FundingSchedule задаёт явно переданный календарь funding.
type FundingSchedule struct {
	Anchor   time.Time
	Interval time.Duration
}

// Profile хранит явные параметры симуляции и источника без численных defaults.
type Profile struct {
	Parameters                                   backtest.SimulationParameters
	SourceMaxAge, DataDeadline, Poll, RPCTimeout time.Duration
	FundingSchedules                             map[string]FundingSchedule
	ExecutionReference                           string
}

// RunBinding связывает запись с неизменяемой конфигурацией запуска.
type RunBinding struct {
	RunID, AccountID, StrategyID, OnboardingID string
	ScheduledAt                                time.Time
	Config                                     config.Config
}

// Purpose отличает обычный цикл от явно запрошенного виртуального закрытия.
type Purpose struct {
	Kind, CommandID string
	ControlRevision uint64
}

// PortfolioState сохраняет точное состояние без повторной финансовой проекции.
type PortfolioState struct {
	AsOf, FundingThrough   time.Time
	WalletUSDT, EquityUSDT model.Decimal
	Positions              map[string]model.LongPosition
}

// TradeWindow сохраняет границы запроса вместе с исходной страницей.
type TradeWindow struct {
	Symbol, Interval string
	StartMS, EndMS   int64
	Page             model.CandlePage
}

// Observation содержит полные неизменённые страницы публичного источника.
type Observation struct {
	CapturedAt, SourceClock time.Time
	DailyBars               int
	TradeWindows            []TradeWindow
	MarkPages               []model.MarkPricePage
	FundingPages            []model.FundingPage
}

// CycleBinding содержит проверяемую идентичность записи и её предшественника.
type CycleBinding struct {
	Scope                                             Scope
	RunID, ConfigHash, ModelHash, InputHash, PlanHash string
	PreviousInputHash, PreviousResolutionHash         string
	Sequence                                          uint64
	ScheduledAt                                       time.Time
}

// IntentRemainder описывает форму остатка, не подтверждая его финансовую истинность.
type IntentRemainder struct {
	IntentIndex                     int
	Symbol                          string
	Side                            model.FillSide
	Requested, Confirmed, Remaining model.Decimal
}

// CycleDocument является изменяемым входным DTO; record сохраняет отдельные байты.
type CycleDocument struct {
	Run                                       RunBinding
	Sequence                                  uint64
	PreviousInputHash, PreviousResolutionHash string
	Purpose                                   Purpose
	Profile                                   Profile
	Observation                               Observation
	Before, Cutoff, ExpectedAfter             PortfolioState
	Plan                                      model.TradePlan
	Fills                                     []model.ConfirmedFill
}

// ResolutionDocument содержит заявленное разрешение цикла без доказательства PG fills.
type ResolutionDocument struct {
	InputHash, RunID, PlanHash, Kind string
	ResolvedAt                       time.Time
	ControlRevision                  uint64
	CommandID                        string
	Fills                            []model.ConfirmedFill
	Remaining                        []IntentRemainder
	ActualAfter                      PortfolioState
}

type instant struct{ time.Time }

// MarshalJSON нормализует допустимый момент к точному UTC без округления.
func (t instant) MarshalJSON() ([]byte, error) {
	if t.UTC().Year() < 1970 || t.UTC().Year() > 9999 {
		return nil, ErrPaperWire
	}
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
}

// UnmarshalJSON принимает только каноническое UTC представление момента.
func (t *instant) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ErrPaperWire
	}
	parsed, err := time.ParseInLocation(time.RFC3339Nano, value, time.UTC)
	if err != nil || parsed.Year() < 1970 || parsed.Year() > 9999 || value != parsed.UTC().Format(time.RFC3339Nano) {
		return ErrPaperWire
	}
	t.Time = parsed.UTC()
	return nil
}
