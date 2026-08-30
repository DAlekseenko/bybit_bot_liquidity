package strategy

import (
	"fmt"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

// SignalType описывает решение стратегии для текущего рыночного состояния.
type SignalType string

const (
	SignalLong  SignalType = "LONG"
	SignalShort SignalType = "SHORT"
	SignalNone  SignalType = "NONE"
)

// CandleDirection описывает направление подтверждённой сигнальной свечи.
type CandleDirection string

const (
	CandleBullish CandleDirection = "BULLISH"
	CandleBearish CandleDirection = "BEARISH"
	CandleFlat    CandleDirection = "FLAT"
)

// Config содержит пороги стратегии из MVP.
type Config struct {
	// OIGrowthPercent - минимальный рост open interest в процентах.
	OIGrowthPercent float64
	// VolumeSMAPeriod - число закрытых свечей в среднем объёме.
	VolumeSMAPeriod int
	// VolumeMultiplier - множитель к среднему объёму текущей свечи.
	VolumeMultiplier float64
	// LiquidationWindow - окно суммирования ликвидаций.
	LiquidationWindow time.Duration
	// LiquidationThreshold - минимальная сумма ликвидаций за окно в USD.
	LiquidationThreshold float64
}

// Input объединяет рыночные данные, необходимые для расчета сигнала.
type Input struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Candles содержат свечи, из которых стратегия использует только закрытые.
	Candles []exchange.Candle
	// PreviousOpenInterest и CurrentOpenInterest нужны для расчета роста OI.
	PreviousOpenInterest exchange.OpenInterest
	CurrentOpenInterest  exchange.OpenInterest
	// Liquidations - события ликвидаций, из которых берется настроенное окно.
	Liquidations []exchange.Liquidation
	// Now задает текущий момент и упрощает тестирование временного окна.
	Now time.Time
}

// Signal описывает результат работы стратегии: LONG, SHORT или NONE.
type Signal struct {
	// Symbol - торговый инструмент, для которого рассчитан сигнал.
	Symbol string
	// Type - направление сигнала.
	Type SignalType
	// Reason - человекочитаемое объяснение принятого решения.
	Reason string
}

// Indicators содержит полный диагностический снимок расчёта стратегии.
// Complete=false означает, что свечей недостаточно для volume/candle-полей.
type Indicators struct {
	Complete                     bool
	ConfirmedCandles             int
	RequiredCandles              int
	PreviousOpenInterest         float64
	CurrentOpenInterest          float64
	PreviousOpenInterestAt       time.Time
	CurrentOpenInterestAt        time.Time
	OIGrowthPercent              float64
	OIGrowthThresholdPercent     float64
	OIConditionMet               bool
	CurrentVolume                float64
	VolumeSMA                    float64
	VolumeRatio                  float64
	VolumeRatioAvailable         bool
	VolumeThreshold              float64
	VolumeMultiplier             float64
	VolumeConditionMet           bool
	CandleOpen                   float64
	CandleClose                  float64
	CandleDirection              CandleDirection
	SignalCandleAt               time.Time
	ShortLiquidationsUSD         float64
	LongLiquidationsUSD          float64
	LiquidationThresholdUSD      float64
	ShortLiquidationConditionMet bool
	LongLiquidationConditionMet  bool
	LongSetupMet                 bool
	ShortSetupMet                bool
	EvaluatedAt                  time.Time
	LiquidationWindowStart       time.Time
}

// Evaluation объединяет торговое решение и значения, на которых оно основано.
type Evaluation struct {
	Signal     Signal
	Indicators Indicators
}

// Strategy реализует правила входа из docs/DOMAIN.md без отправки ордеров.
type Strategy struct {
	cfg Config
}

func New(cfg Config) *Strategy {
	return &Strategy{cfg: cfg}
}

func (s *Strategy) Evaluate(input Input) Signal {
	return s.EvaluateDetailed(input).Signal
}

// EvaluateDetailed рассчитывает сигнал и все индикаторы независимо от того,
// какое из последовательных условий первым запретило вход.
func (s *Strategy) EvaluateDetailed(input Input) Evaluation {
	requiredCandles := s.cfg.VolumeSMAPeriod + 1
	indicators := Indicators{
		RequiredCandles:          requiredCandles,
		PreviousOpenInterest:     input.PreviousOpenInterest.Value,
		CurrentOpenInterest:      input.CurrentOpenInterest.Value,
		PreviousOpenInterestAt:   input.PreviousOpenInterest.Time.UTC(),
		CurrentOpenInterestAt:    input.CurrentOpenInterest.Time.UTC(),
		OIGrowthPercent:          percentGrowth(input.PreviousOpenInterest.Value, input.CurrentOpenInterest.Value),
		OIGrowthThresholdPercent: s.cfg.OIGrowthPercent,
		VolumeMultiplier:         s.cfg.VolumeMultiplier,
		LiquidationThresholdUSD:  s.cfg.LiquidationThreshold,
		EvaluatedAt:              input.Now.UTC(),
	}
	indicators.OIConditionMet = indicators.OIGrowthPercent > indicators.OIGrowthThresholdPercent

	if s.cfg.LiquidationWindow > 0 {
		indicators.LiquidationWindowStart = input.Now.Add(-s.cfg.LiquidationWindow).UTC()
		indicators.ShortLiquidationsUSD, indicators.LongLiquidationsUSD = liquidationSums(
			input.Liquidations,
			indicators.LiquidationWindowStart,
		)
		indicators.ShortLiquidationConditionMet = indicators.ShortLiquidationsUSD > indicators.LiquidationThresholdUSD
		indicators.LongLiquidationConditionMet = indicators.LongLiquidationsUSD > indicators.LiquidationThresholdUSD
	}

	if s.cfg.VolumeSMAPeriod <= 0 || s.cfg.LiquidationWindow <= 0 {
		return evaluation(input.Symbol, SignalNone, "invalid strategy config", indicators)
	}

	closedCandles := confirmedCandles(input.Candles)
	indicators.ConfirmedCandles = len(closedCandles)
	if len(closedCandles) < requiredCandles {
		return evaluation(input.Symbol, SignalNone, "not enough candles", indicators)
	}

	current := closedCandles[len(closedCandles)-1]
	indicators.Complete = true
	indicators.CurrentVolume = current.Volume
	indicators.CandleOpen = current.Open
	indicators.CandleClose = current.Close
	indicators.SignalCandleAt = current.OpenTime.UTC()
	switch {
	case current.Close > current.Open:
		indicators.CandleDirection = CandleBullish
	case current.Close < current.Open:
		indicators.CandleDirection = CandleBearish
	default:
		indicators.CandleDirection = CandleFlat
	}

	indicators.VolumeSMA = smaVolume(closedCandles[len(closedCandles)-requiredCandles : len(closedCandles)-1])
	indicators.VolumeThreshold = indicators.VolumeSMA * indicators.VolumeMultiplier
	indicators.VolumeConditionMet = indicators.CurrentVolume > indicators.VolumeThreshold
	if indicators.VolumeSMA > 0 {
		indicators.VolumeRatio = indicators.CurrentVolume / indicators.VolumeSMA
		indicators.VolumeRatioAvailable = true
	}

	indicators.LongSetupMet = indicators.OIConditionMet &&
		indicators.VolumeConditionMet &&
		indicators.CandleDirection == CandleBullish &&
		indicators.ShortLiquidationConditionMet
	indicators.ShortSetupMet = indicators.OIConditionMet &&
		indicators.VolumeConditionMet &&
		indicators.CandleDirection == CandleBearish &&
		indicators.LongLiquidationConditionMet

	switch {
	case !indicators.OIConditionMet:
		return evaluation(input.Symbol, SignalNone, "open interest growth below threshold", indicators)
	case !indicators.VolumeConditionMet:
		return evaluation(input.Symbol, SignalNone, "volume below spike threshold", indicators)
	case indicators.LongSetupMet:
		return evaluation(
			input.Symbol,
			SignalLong,
			fmt.Sprintf(
				"oi %.2f%%, volume %.2fx sma%d, short liquidations %.2f USD",
				indicators.OIGrowthPercent,
				indicators.VolumeRatio,
				s.cfg.VolumeSMAPeriod,
				indicators.ShortLiquidationsUSD,
			),
			indicators,
		)
	case indicators.ShortSetupMet:
		return evaluation(
			input.Symbol,
			SignalShort,
			fmt.Sprintf(
				"oi %.2f%%, volume %.2fx sma%d, long liquidations %.2f USD",
				indicators.OIGrowthPercent,
				indicators.VolumeRatio,
				s.cfg.VolumeSMAPeriod,
				indicators.LongLiquidationsUSD,
			),
			indicators,
		)
	default:
		return evaluation(input.Symbol, SignalNone, "candle direction or liquidation threshold not matched", indicators)
	}
}

func evaluation(symbol string, signalType SignalType, reason string, indicators Indicators) Evaluation {
	return Evaluation{
		Signal: Signal{
			Symbol: symbol,
			Type:   signalType,
			Reason: reason,
		},
		Indicators: indicators,
	}
}

func confirmedCandles(candles []exchange.Candle) []exchange.Candle {
	confirmed := make([]exchange.Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.Confirmed {
			confirmed = append(confirmed, candle)
		}
	}

	return confirmed
}

func percentGrowth(previous, current float64) float64 {
	if previous <= 0 {
		return 0
	}

	return (current - previous) / previous * 100
}

func smaVolume(candles []exchange.Candle) float64 {
	if len(candles) == 0 {
		return 0
	}

	var sum float64
	for _, candle := range candles {
		sum += candle.Volume
	}

	return sum / float64(len(candles))
}

func liquidationSums(liquidations []exchange.Liquidation, since time.Time) (shortUSD float64, longUSD float64) {
	for _, liquidation := range liquidations {
		if liquidation.Time.Before(since) {
			continue
		}

		switch liquidation.Side {
		case exchange.SideShort:
			shortUSD += liquidation.USDValue()
		case exchange.SideLong:
			longUSD += liquidation.USDValue()
		}
	}

	return shortUSD, longUSD
}
