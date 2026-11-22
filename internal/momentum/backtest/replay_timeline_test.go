package backtest

import (
	"bybit_bot_liquidity/internal/momentum/model"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// timelineCase хранит явные принадлежащие одному тесту входы чистого обхода.
type timelineCase struct {
	input     BaselineReplayInput
	schedule  []ReplaySchedulePoint
	seed      BaselineReplayResult
	wallet    model.Decimal
	positions map[string]model.LongPosition
}

// timelineRequire останавливает тест на ошибке настоящего producer или арифметики.
func timelineRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// timelineFixture строит два настоящих цикла и явно заданное исходное состояние.
func timelineFixture(t *testing.T, overflow bool) timelineCase {
	t.Helper()
	cycle := cycleFixture(t, "now")
	cycle.End = cycle.Start.Add(169 * time.Hour)
	cycle.Funding = nil
	if overflow {
		divisor, err := amount(t, "1"), error(nil)
		for range 3900 {
			divisor, err = divisor.Mul(amount(t, "2"))
			timelineRequire(t, err)
		}
		cycle.WalletUSDT, err = amount(t, "1").Quo(divisor)
		timelineRequire(t, err)
	}
	for symbol := range cycle.Positions {
		cycle.Positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
	}
	prepared, err := PrepareBaselineCycle(cycle)
	timelineRequire(t, err)
	_, configHash, err := cycle.Config.Snapshot()
	timelineRequire(t, err)
	_, modelHash, err := cycle.Model.Snapshot()
	timelineRequire(t, err)
	id := BaselineReplayIdentity{
		RunID: prepared.RunID, ConfigSHA256: configHash,
		DatasetSHA256: cycle.Config.Values().DataSource.ManifestSHA256,
		ModelSHA256:   modelHash, SourceModelAssumptions: cycle.Dataset.Manifest.ModelAssumptions,
		Start: cycle.Start, End: cycle.End,
		Symbols: cycle.Config.Values().Symbols, StartPolicy: "now",
	}
	fixture := timelineCase{
		input: BaselineReplayInput{
			Config: cycle.Config, Model: cycle.Model, Dataset: cycle.Dataset,
			Start: cycle.Start, End: cycle.End,
		},
		seed:   BaselineReplayResult{Identity: id, Initial: prepared.Cutoff},
		wallet: cycle.WalletUSDT, positions: cycle.Positions,
	}
	for i := 0; i <= 169; i++ {
		at := cycle.Start.Add(time.Duration(i) * time.Hour)
		point := ReplaySchedulePoint{Market: MarketBoundary{At: at, Terminal: i == 169}, NominalWeekly: i%168 == 0}
		for _, symbol := range id.Symbols {
			mark := cycle.Marks[symbol]
			mark.ObservedAt = at
			point.Market.Marks = append(point.Market.Marks, mark)
			if !point.Market.Terminal {
				price := mark.Price
				if overflow && i == 168 && symbol == "BTCUSDT" {
					price = amount(t, "1"+strings.Repeat("0", 127))
				}
				point.Market.TradeOpens = append(point.Market.TradeOpens, NextOpenReference{Symbol: symbol, Price: price})
			}
		}
		fixture.schedule = append(fixture.schedule, point)
	}
	return fixture
}

// timelineContext считает пройденные границы без подмены вычислений или clock.
type timelineContext struct {
	context.Context
	checks int
}

// Err сохраняет настоящий контекст и считает завершённые безопасные границы.
func (c *timelineContext) Err() error {
	c.checks++
	return c.Context.Err()
}

// TestReplayTimeline проверяет полную композицию и позднюю настоящую арифметическую ошибку.
func TestReplayTimeline(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		fixture := timelineFixture(t, overflow)
		before := cloneCycleInput(BaselineCycleInput{Model: fixture.input.Model, Dataset: fixture.input.Dataset, WalletUSDT: fixture.wallet, Positions: fixture.positions})
		ctx := &timelineContext{Context: context.Background()}
		got, err := replayBaselineTimeline(ctx, fixture.input, fixture.schedule, fixture.seed, fixture.wallet, fixture.positions)
		if overflow {
			_, cause := fixture.wallet.Quo(fixture.schedule[168].Market.TradeOpens[0].Price)
			if cause == nil || !errors.Is(err, cause) || !errors.Is(err, ErrBaselineReplay) ||
				!errors.Is(err, ErrZeroSlipBaselineApply) || !errors.Is(err, ErrZeroSlipBaselineReserve) ||
				!reflect.DeepEqual(got, BaselineReplayResult{}) || ctx.checks != 172 {
				t.Fatal("real late arithmetic/atomicity missing", err, ctx.checks)
			}
		} else {
			if err != nil || got.Status != "complete" || len(got.Points) != 170 || got.Points[0].Cycle == nil || got.Points[168].Cycle == nil || !got.Points[169].Terminal {
				t.Fatal("complete concrete traversal", err)
			}
			assertDecimal(t, got.Points[0].Cycle.Batch.FeesUSDT, "5.988")
			assertDecimal(t, got.Points[0].Cycle.PostFill.WalletUSDT, "8994.012")
		}
		if !reflect.DeepEqual(before, BaselineCycleInput{Model: fixture.input.Model, Dataset: fixture.input.Dataset, WalletUSDT: fixture.wallet, Positions: fixture.positions}) {
			t.Fatal("timeline inputs mutated")
		}
	}
}
