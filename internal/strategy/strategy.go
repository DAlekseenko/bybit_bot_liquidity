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

// Config содержит пороги стратегии из MVP.
type Config struct {
	// OIGrowthPercent - минимальный рост open interest в процентах.
	OIGrowthPercent float64
	// VolumeMultiplier - множитель к SMA20 объема текущей свечи.
	VolumeMultiplier float64
	// LiquidationThreshold - минимальная сумма ликвидаций за 5 минут в USD.
	LiquidationThreshold float64
}

// Input объединяет рыночные данные, необходимые для расчета сигнала.
type Input struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Candles должны содержать минимум 21 свечу: 20 для SMA и текущую свечу.
	Candles []exchange.Candle
	// PreviousOpenInterest и CurrentOpenInterest нужны для расчета роста OI.
	PreviousOpenInterest exchange.OpenInterest
	CurrentOpenInterest  exchange.OpenInterest
	// Liquidations - события ликвидаций, из которых берется окно последних 5 минут.
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

// Strategy реализует правила входа из AGENT.md без отправки ордеров.
type Strategy struct {
	cfg Config
}

func New(cfg Config) *Strategy {
	return &Strategy{cfg: cfg}
}

func (s *Strategy) Evaluate(input Input) Signal {
	if len(input.Candles) < 21 {
		return Signal{Symbol: input.Symbol, Type: SignalNone, Reason: "not enough candles"}
	}

	current := input.Candles[len(input.Candles)-1]
	oiGrowth := percentGrowth(input.PreviousOpenInterest.Value, input.CurrentOpenInterest.Value)
	if oiGrowth <= s.cfg.OIGrowthPercent {
		return Signal{Symbol: input.Symbol, Type: SignalNone, Reason: "open interest growth below threshold"}
	}

	volumeSMA := smaVolume(input.Candles[len(input.Candles)-21 : len(input.Candles)-1])
	if current.Volume <= volumeSMA*s.cfg.VolumeMultiplier {
		return Signal{Symbol: input.Symbol, Type: SignalNone, Reason: "volume below spike threshold"}
	}

	windowStart := input.Now.Add(-5 * time.Minute)
	shortLiqUSD, longLiqUSD := liquidationSums(input.Liquidations, windowStart)

	switch {
	case current.Close > current.Open && shortLiqUSD > s.cfg.LiquidationThreshold:
		return Signal{
			Symbol: input.Symbol,
			Type:   SignalLong,
			Reason: fmt.Sprintf("oi %.2f%%, volume %.2fx sma20, short liquidations %.2f USD", oiGrowth, current.Volume/volumeSMA, shortLiqUSD),
		}
	case current.Close < current.Open && longLiqUSD > s.cfg.LiquidationThreshold:
		return Signal{
			Symbol: input.Symbol,
			Type:   SignalShort,
			Reason: fmt.Sprintf("oi %.2f%%, volume %.2fx sma20, long liquidations %.2f USD", oiGrowth, current.Volume/volumeSMA, longLiqUSD),
		}
	default:
		return Signal{Symbol: input.Symbol, Type: SignalNone, Reason: "candle direction or liquidation threshold not matched"}
	}
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
