package paper

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// cycleAmount читает точное fixture-число через foundation.
func cycleAmount(t *testing.T, s string) model.Decimal {
	t.Helper()
	v, err := paperwire.ParseRecordedAmount(s)
	requireCycleSuccess(t, err)
	return v
}

// cycleConfig связывает fixture с явным hash профиля без изменения codecs.
func cycleConfig(t *testing.T, c config.Config, p paperwire.Profile, modelID string) config.Config {
	t.Helper()
	_, hash, err := paperwire.EncodeProfile(p)
	requireCycleSuccess(t, err)
	if modelID == "" {
		modelID = hash
	}
	raw, _, err := c.Snapshot()
	requireCycleSuccess(t, err)
	raw = bytes.Replace(raw, []byte(c.Values().ModelID), []byte(modelID), 1)
	cfg, err := config.Parse(raw)
	requireCycleSuccess(t, err)
	return cfg
}

// cycleFixture строит saved observations fake transport с положительным ROC.
func cycleFixture(t *testing.T) CycleInput {
	t.Helper()
	r, f := sourceFixture(t, "BTCUSDT, ETHUSDT, HYPEUSDT")
	r.Run.AccountID = "paper/test"
	r.FundingAfter = r.Run.ScheduledAt
	for s, rule := range r.Profile.Parameters.Rules {
		rule.MaxMarketQty = cycleAmount(t, "100000")
		r.Profile.Parameters.Rules[s] = rule
	}
	r.Profile.Parameters.FeeRate = cycleAmount(t, "0.001")
	r.Run.Config = cycleConfig(t, r.Run.Config, r.Profile, "")
	f.edit = func(request *http.Request, result map[string]any) {
		if request.URL.Path == "/v5/market/kline" && request.URL.Query().Get("interval") == "D" {
			rows := result["list"].([]any)
			for i, row := range rows {
				values := row.([]string)
				price := fmt.Sprint(100 - i)
				for j := 1; j < 5; j++ {
					values[j] = price
				}
			}
		}
	}
	o, err := ReadCycleObservation(t.Context(), r)
	requireCycleSuccess(t, err)
	return CycleInput{Run: r.Run, Sequence: 1, Purpose: r.Purpose, Profile: r.Profile, Observation: o, FormulaID: planner.BaselinePaperFormulaID, PlanBuilder: planner.BuildBaselinePaperPlan, Admission: planner.AdmitBaselineExecution}
}

// preparedFixture требует успешной полной baseline-подготовки.
func preparedFixture(t *testing.T, in CycleInput) PreparedCycle {
	t.Helper()
	p, err := PrepareCycle(in)
	requireCycleSuccess(t, err)
	return p
}

// TestPaperCycle проверяет реальный planner, metadata, wallet и replay.
func TestPaperCycle(t *testing.T) {
	in := cycleFixture(t)
	p := preparedFixture(t, in)
	if len(p.ExpectedFills()) != 3 {
		t.Fatal("positive ROC targets absent")
	}
	if p.Binding().ModelHash != in.Run.Config.Values().ModelID || p.Plan().Metadata().ModelID != p.Binding().ModelHash {
		t.Fatal("model binding")
	}
	genesis := p.Before()
	if !genesis.AsOf.Equal(in.Run.ScheduledAt) || !genesis.FundingThrough.Equal(genesis.AsOf) || !decimalEqual(genesis.WalletUSDT, cycleAmount(t, in.Run.Config.Values().InitialEquity)) {
		t.Fatal("incorrect genesis state")
	}
	for _, position := range genesis.Positions {
		if !isZero(position.Quantity) || !isZero(position.CostBasisUSDT) {
			t.Fatal("nonzero genesis position")
		}
	}
	fee := model.Zero()
	for _, f := range p.ExpectedFills() {
		fee, _ = fee.Add(f.FeeUSDT)
	}
	want, _ := p.Before().WalletUSDT.Sub(fee)
	if !decimalEqual(p.ExpectedAfter().WalletUSDT, want) {
		t.Fatal("spot notional debited")
	}
	repeated := preparedFixture(t, in)
	if !bytes.Equal(p.Payload(), repeated.Payload()) {
		t.Fatal("nondeterministic")
	}
	decoded, err := DecodePreparedCycle(p.Payload(), nil, in.PlanBuilder, in.Admission)
	if err != nil || decoded.Hash() != p.Hash() {
		t.Fatal(err)
	}
	if p.Plan().Snapshot().ConfigVersion() != in.Run.Config.Values().ProfileVersion || p.Plan().Metadata().FormulaID != in.FormulaID {
		t.Fatal("metadata")
	}
	before := p.Payload()
	p.Payload()[0] = '!'
	state := p.Cutoff()
	delete(state.Positions, "BTCUSDT")
	profile := p.Profile()
	delete(profile.Parameters.Rules, "BTCUSDT")
	fills := p.ExpectedFills()
	fills[0].Symbol = "BAD"
	if !bytes.Equal(before, p.Payload()) || len(p.Cutoff().Positions) != 3 || len(p.Profile().Parameters.Rules) != 3 {
		t.Fatal("mutable accessors")
	}
}

// TestPaperModelBinding проверяет ранний отказ для всех изменений explicit profile.
func TestPaperModelBinding(t *testing.T) {
	base := cycleFixture(t)
	changes := []func(*CycleInput){
		func(in *CycleInput) { in.Run.Config = cycleConfig(t, in.Run.Config, in.Profile, "roc30") },
		func(in *CycleInput) {
			in.Run.Config = cycleConfig(t, in.Run.Config, in.Profile, strings.ToUpper(in.Run.Config.Values().ModelID))
		},
		func(in *CycleInput) {
			in.Run.Config = cycleConfig(t, in.Run.Config, in.Profile, strings.Repeat("a", 64))
		},
		func(in *CycleInput) { in.Profile.Parameters.FeeRate = cycleAmount(t, "0.002") },
		func(in *CycleInput) { in.Profile.Parameters.SlippageRate = cycleAmount(t, "0.001") },
		func(in *CycleInput) {
			r := in.Profile.Parameters.Rules["BTCUSDT"]
			r.MaxMarketQty = cycleAmount(t, "90000")
			in.Profile.Parameters.Rules["BTCUSDT"] = r
		},
		func(in *CycleInput) {
			s := in.Profile.FundingSchedules["BTCUSDT"]
			s.Interval = 4 * time.Hour
			in.Profile.FundingSchedules["BTCUSDT"] = s
		},
		func(in *CycleInput) { in.Profile.SourceMaxAge += time.Second },
		func(in *CycleInput) { in.Profile.DataDeadline += time.Second },
		func(in *CycleInput) { in.Profile.Poll += time.Second },
		func(in *CycleInput) { in.Profile.RPCTimeout += time.Second },
	}
	for i, change := range changes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			in := base
			in.Profile.Parameters.Rules = maps.Clone(base.Profile.Parameters.Rules)
			in.Profile.FundingSchedules = maps.Clone(base.Profile.FundingSchedules)
			change(&in)
			called := false
			in.PlanBuilder = func(string, model.PlanMetadata, model.PortfolioSnapshot, map[string][]data.DailyClose, map[string]planner.MarketDeltaRules, model.Decimal) (model.TradePlan, error) {
				called = true
				return model.TradePlan{}, nil
			}
			if _, err := PrepareCycle(in); !errors.Is(err, ErrPaperModelBinding) || called {
				t.Fatalf("late/permissive model rejection %v", err)
			}
		})
	}
	if err := ValidatePaperModel(config.Config{}, base.Profile); !errors.Is(err, ErrPaperModelBinding) {
		t.Fatal(err)
	}
	permuted := base.Profile
	permuted.Parameters.Rules = map[string]planner.MarketDeltaRules{}
	for _, s := range []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"} {
		permuted.Parameters.Rules[s] = base.Profile.Parameters.Rules[s]
	}
	if err := ValidatePaperModel(base.Run.Config, permuted); err != nil {
		t.Fatal(err)
	}
	bad := base.Profile
	bad.Poll = 0
	if err := ValidatePaperModel(base.Run.Config, bad); !errors.Is(err, paperwire.ErrPaperWire) {
		t.Fatal(err)
	}
}

// TestPaperCycleRefusals проверяет callbacks, genesis, saved forgery и neutral codec.
func TestPaperCycleRefusals(t *testing.T) {
	base := cycleFixture(t)
	rejected := errors.New("strategy rejects")
	for _, change := range []func(*CycleInput){
		func(in *CycleInput) { in.PlanBuilder = nil },
		func(in *CycleInput) { in.Admission = nil },
		func(in *CycleInput) { in.Sequence = 0 },
		func(in *CycleInput) { in.Sequence = 2 },
		func(in *CycleInput) { in.FormulaID = "" },
		func(in *CycleInput) { in.Run.AccountID = "wrong" },
		func(in *CycleInput) {
			in.PlanBuilder = func(string, model.PlanMetadata, model.PortfolioSnapshot, map[string][]data.DailyClose, map[string]planner.MarketDeltaRules, model.Decimal) (model.TradePlan, error) {
				return model.TradePlan{}, rejected
			}
		},
		func(in *CycleInput) {
			in.Admission = func(model.TradePlan, map[string]planner.MarketDeltaRules, model.Decimal, map[string]model.Decimal, map[string]model.Decimal) error {
				return rejected
			}
		},
		func(in *CycleInput) {
			in.PlanBuilder = func(p string, m model.PlanMetadata, s model.PortfolioSnapshot, d map[string][]data.DailyClose, r map[string]planner.MarketDeltaRules, f model.Decimal) (model.TradePlan, error) {
				m.FormulaID = "wrong"
				return planner.BuildBaselinePaperPlan(p, m, s, d, r, f)
			}
		},
		func(in *CycleInput) {
			in.PlanBuilder = func(p string, m model.PlanMetadata, s model.PortfolioSnapshot, d map[string][]data.DailyClose, r map[string]planner.MarketDeltaRules, f model.Decimal) (model.TradePlan, error) {
				wrong, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: s.Environment(), AccountID: "paper/other", CapturedAt: s.CapturedAt(), Equity: s.Equity(), ConfigVersion: s.ConfigVersion(), Prices: s.Prices(), Positions: s.Positions()})
				requireCycleSuccess(t, err)
				return planner.BuildBaselinePaperPlan(p, m, wrong, d, r, f)
			}
		},
	} {
		in := base
		change(&in)
		if _, err := PrepareCycle(in); !errors.Is(err, ErrPaperCycle) {
			t.Fatal("accepted", err)
		}
	}
	p := preparedFixture(t, base)
	for _, change := range []func(*paperwire.CycleDocument){
		func(d *paperwire.CycleDocument) { d.ExpectedAfter.WalletUSDT = cycleAmount(t, "42") },
		func(d *paperwire.CycleDocument) { d.Profile.Poll += time.Second },
	} {
		d := p.input.Values()
		change(&d)
		forged, err := paperwire.EncodeInputRecord(d)
		requireCycleSuccess(t, err)
		if _, err := DecodePreparedCycle(forged.Payload(), nil, base.PlanBuilder, base.Admission); !errors.Is(err, ErrPaperCycle) {
			t.Fatal("forged input", err)
		}
	}
	d := p.input.Values()
	d.Run.Config = cycleConfig(t, d.Run.Config, d.Profile, "roc30")
	_, hash, _ := d.Run.Config.Snapshot()
	m := d.Plan.Metadata()
	m.ModelID = "roc30"
	m.ConfigHash = hash
	plan, err := model.NewTradePlan(model.PlanInput{Snapshot: d.Plan.Snapshot(), Metadata: m, Targets: d.Plan.Targets()})
	requireCycleSuccess(t, err)
	d.Plan = plan
	neutral, err := paperwire.EncodeInputRecord(d)
	requireCycleSuccess(t, err)
	if _, err = paperwire.DecodeInputRecord(neutral.Payload()); err != nil {
		t.Fatal(err)
	}
	if _, err = DecodePreparedCycle(neutral.Payload(), nil, base.PlanBuilder, base.Admission); !errors.Is(err, ErrPaperModelBinding) {
		t.Fatal(err)
	}
}

// TestPaperRatBoundaries проверяет полный receipt с 4096-битным кошельком и конечной SQL-дробью.
func TestPaperRatBoundaries(t *testing.T) {
	in := cycleFixture(t)
	p := preparedFixture(t, in)
	den := new(big.Int).Lsh(big.NewInt(1), 4076)
	tiny, err := cycleAmount(t, "1").Quo(cycleAmount(t, den.String()))
	requireCycleSuccess(t, err)
	raw, err := tiny.WireDecimal()
	if err != nil || len(raw) < 129 {
		t.Fatal("short amount", err)
	}
	fill, err := MakeFillSlice(p, 0, model.Zero(), cycleAmount(t, raw))
	requireCycleSuccess(t, err)
	replayed := replayedReceipt(t, p, []model.ConfirmedFill{fill}, ResolutionDisposition{"ABORTED", "stop", 1, in.Run.ScheduledAt})
	n, _, _ := replayed.ActualAfter().WalletUSDT.CanonicalRatio()
	num, ok := new(big.Int).SetString(n, 10)
	if !ok || num.BitLen() != 4096 {
		t.Fatal("wallet bit length", n)
	}
	third, _ := cycleAmount(t, "1").Quo(cycleAmount(t, "3"))
	if _, err := MakeFillSlice(p, 0, model.Zero(), third); !errors.Is(err, ErrPaperProgress) {
		t.Fatal("nonfinite quantity", err)
	}
	for _, fields := range []model.ConfirmedFill{
		{Quantity: fill.Quantity, ExecutionPrice: third, FeeUSDT: fill.FeeUSDT},
		{Quantity: fill.Quantity, ExecutionPrice: fill.ExecutionPrice, FeeUSDT: third},
	} {
		if err := finiteFill(fields); !errors.Is(err, ErrPaperProgress) {
			t.Fatal("nonfinite price or fee", err)
		}
	}
}

// TestPaperThirdCostBasis воспроизводит три цикла с точным остатком себестоимости 1/3.
func TestPaperThirdCostBasis(t *testing.T) {
	in := cycleFixture(t)
	var receipt ResolvedCycle
	for index, qty := range []string{"1", "3", "1"} {
		if index > 0 {
			in = nextCycle(t, in, receipt, false)
		}
		price := cycleAmount(t, []string{"0.2", "0.4", "0.4"}[index])
		for _, window := range in.Observation.TradeWindows {
			if window.Interval == "60" {
				for i := range window.Page.Candles {
					c := &window.Page.Candles[i]
					c.Open, c.High, c.Low, c.Close = price, price, price, price
				}
			}
		}
		in.PlanBuilder = singleTarget(cycleAmount(t, qty))
		p := preparedFixture(t, in)
		p, err := DecodePreparedCycle(p.Payload(), in.Previous, in.PlanBuilder, in.Admission)
		requireCycleSuccess(t, err)
		receipt = replayedReceipt(t, p, p.ExpectedFills(), ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt})
	}
	n, d, _ := receipt.ActualAfter().Positions["BTCUSDT"].CostBasisUSDT.CanonicalRatio()
	if n != "1" || d != "3" {
		t.Fatal(n, d)
	}
}

// singleTarget задаёт цель BTC и намеренно изменяет переданные стратегии копии.
func singleTarget(btc model.Decimal) PlanBuilder {
	return func(_ string, m model.PlanMetadata, s model.PortfolioSnapshot, series map[string][]data.DailyClose, rules map[string]planner.MarketDeltaRules, _ model.Decimal) (model.TradePlan, error) {
		clear(series)
		clear(s.Positions())
		targets := make([]model.Target, 0, len(rules))
		for symbol := range rules {
			delete(rules, symbol)
			q := model.Zero()
			if symbol == "BTCUSDT" {
				q = btc
			}
			targets = append(targets, model.Target{Symbol: symbol, Quantity: q})
		}
		return model.NewTradePlan(model.PlanInput{Metadata: m, Snapshot: s, Targets: targets})
	}
}

// requireCycleSuccess останавливает тест на строке неудачной операции.
func requireCycleSuccess(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
