package execution

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestLeveragePreflightCheck(t *testing.T) {
	t.Parallel()

	source := testLeverageSource()
	preflight := NewLeveragePreflight(source)

	got, err := preflight.Check(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if got != source.info {
		t.Fatalf("Check() = %+v, want %+v", got, source.info)
	}
	if source.leverageCalls != 1 || source.rulesCalls != 1 {
		t.Fatalf("calls = leverage %d, rules %d; want 1/1", source.leverageCalls, source.rulesCalls)
	}
}

func TestLeveragePreflightAcceptsRangeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		leverage float64
	}{
		{name: "minimum", leverage: 1},
		{name: "maximum", leverage: 100},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testLeverageSource()
			source.info.Leverage = test.leverage
			if _, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT"); err != nil {
				t.Fatalf("Check() error = %v", err)
			}
		})
	}
}

func TestLeveragePreflightRejectsLeverageLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		leverage float64
		wantErr  error
	}{
		{name: "below minimum", leverage: 0.5, wantErr: ErrLeverageOutOfRange},
		{name: "above maximum", leverage: 101, wantErr: ErrLeverageOutOfRange},
		{name: "step mismatch", leverage: 1.005, wantErr: ErrLeverageStepMismatch},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testLeverageSource()
			source.info.Leverage = test.leverage
			_, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Check() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLeveragePreflightRejectsInvalidLeverageInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*exchange.LeverageInfo)
	}{
		{
			name:   "symbol mismatch",
			change: func(info *exchange.LeverageInfo) { info.Symbol = "ETHUSDT" },
		},
		{
			name:   "zero leverage",
			change: func(info *exchange.LeverageInfo) { info.Leverage = 0 },
		},
		{
			name:   "nan leverage",
			change: func(info *exchange.LeverageInfo) { info.Leverage = math.NaN() },
		},
		{
			name:   "missing time",
			change: func(info *exchange.LeverageInfo) { info.Time = time.Time{} },
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testLeverageSource()
			test.change(&source.info)
			_, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, ErrInvalidLeverageInfo) {
				t.Fatalf("Check() error = %v, want %v", err, ErrInvalidLeverageInfo)
			}
			if source.rulesCalls != 0 {
				t.Fatalf("GetInstrumentRules() calls = %d, want 0", source.rulesCalls)
			}
		})
	}
}

func TestLeveragePreflightRejectsInvalidInstrumentRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(*exchange.InstrumentRules)
		wantErr error
	}{
		{
			name:    "symbol mismatch",
			change:  func(rules *exchange.InstrumentRules) { rules.Symbol = "ETHUSDT" },
			wantErr: ErrInvalidInstrumentRules,
		},
		{
			name:    "instrument not trading",
			change:  func(rules *exchange.InstrumentRules) { rules.Status = "PreLaunch" },
			wantErr: ErrInstrumentNotTrading,
		},
		{
			name:    "zero minimum",
			change:  func(rules *exchange.InstrumentRules) { rules.MinLeverage = 0 },
			wantErr: ErrInvalidInstrumentRules,
		},
		{
			name:    "zero maximum",
			change:  func(rules *exchange.InstrumentRules) { rules.MaxLeverage = 0 },
			wantErr: ErrInvalidInstrumentRules,
		},
		{
			name:    "zero step",
			change:  func(rules *exchange.InstrumentRules) { rules.LeverageStep = 0 },
			wantErr: ErrInvalidInstrumentRules,
		},
		{
			name: "inconsistent range",
			change: func(rules *exchange.InstrumentRules) {
				rules.MinLeverage = 101
				rules.MaxLeverage = 100
			},
			wantErr: ErrInvalidInstrumentRules,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testLeverageSource()
			test.change(&source.rules)
			_, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Check() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLeveragePreflightRequiresSourceAndSymbol(t *testing.T) {
	t.Parallel()

	var preflight *LeveragePreflight
	if _, err := preflight.Check(context.Background(), "BTCUSDT"); !errors.Is(err, ErrLeverageSourceRequired) {
		t.Fatalf("nil Check() error = %v, want %v", err, ErrLeverageSourceRequired)
	}
	if _, err := NewLeveragePreflight(nil).Check(context.Background(), "BTCUSDT"); !errors.Is(err, ErrLeverageSourceRequired) {
		t.Fatalf("Check() error = %v, want %v", err, ErrLeverageSourceRequired)
	}

	source := testLeverageSource()
	if _, err := NewLeveragePreflight(source).Check(context.Background(), ""); !errors.Is(err, ErrInvalidLeverageInfo) {
		t.Fatalf("empty symbol error = %v, want %v", err, ErrInvalidLeverageInfo)
	}
	if source.leverageCalls != 0 || source.rulesCalls != 0 {
		t.Fatalf("calls = leverage %d, rules %d; want 0/0", source.leverageCalls, source.rulesCalls)
	}
}

func TestLeveragePreflightPreservesSourceErrors(t *testing.T) {
	t.Parallel()

	leverageErr := errors.New("leverage unavailable")
	source := testLeverageSource()
	source.leverageErr = leverageErr
	if _, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT"); !errors.Is(err, leverageErr) {
		t.Fatalf("leverage error = %v, want wrapped %v", err, leverageErr)
	}
	if source.rulesCalls != 0 {
		t.Fatalf("GetInstrumentRules() calls = %d, want 0", source.rulesCalls)
	}

	rulesErr := errors.New("rules unavailable")
	source = testLeverageSource()
	source.rulesErr = rulesErr
	if _, err := NewLeveragePreflight(source).Check(context.Background(), "BTCUSDT"); !errors.Is(err, rulesErr) {
		t.Fatalf("rules error = %v, want wrapped %v", err, rulesErr)
	}
}

type leverageSourceStub struct {
	info          exchange.LeverageInfo
	rules         exchange.InstrumentRules
	leverageErr   error
	rulesErr      error
	leverageCalls int
	rulesCalls    int
}

func testLeverageSource() *leverageSourceStub {
	return &leverageSourceStub{
		info: exchange.LeverageInfo{
			Symbol:   "BTCUSDT",
			Leverage: 10,
			Time:     time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		},
		rules: exchange.InstrumentRules{
			Symbol:       "BTCUSDT",
			Status:       "Trading",
			MinLeverage:  1,
			MaxLeverage:  100,
			LeverageStep: 0.01,
		},
	}
}

func (s *leverageSourceStub) GetLeverage(context.Context, string) (exchange.LeverageInfo, error) {
	s.leverageCalls++
	return s.info, s.leverageErr
}

func (s *leverageSourceStub) GetInstrumentRules(context.Context, string) (exchange.InstrumentRules, error) {
	s.rulesCalls++
	return s.rules, s.rulesErr
}
