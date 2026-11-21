package report

import (
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var day0 = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// dec разбирает decimal для тестов и падает при ошибке.
func dec(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

// series строит ряд с шагом step из пар equity/накопленные переводы.
func series(t *testing.T, step time.Duration, pairs ...[2]string) []EquityPoint {
	t.Helper()
	out := make([]EquityPoint, len(pairs))
	for i, p := range pairs {
		out[i] = EquityPoint{At: day0.Add(time.Duration(i) * step), EquityUSDT: dec(t, p[0]), CumulativeTransfersUSDT: dec(t, p[1])}
	}
	return out
}

// equal сравнивает decimal со строкой.
func equal(t *testing.T, name string, got model.Decimal, want string) {
	t.Helper()
	if c, err := got.Cmp(dec(t, want)); err != nil || c != 0 {
		n, d, _ := got.CanonicalRatio()
		t.Fatalf("%s = %s/%s, want %s", name, n, d, want)
	}
}

// TestMaxDrawdownHandCalculated проверяет пики и выбор максимума по доле.
func TestMaxDrawdownHandCalculated(t *testing.T) {
	// 100 → 50: 50%; затем 1000 → 900: 10%, хотя абсолютно больше.
	pts := series(t, 24*time.Hour, [2]string{"100", "0"}, [2]string{"50", "0"}, [2]string{"1000", "0"}, [2]string{"900", "0"})
	d, err := MaxDrawdown(pts, 24*time.Hour, false)
	if err != nil || !d.Available {
		t.Fatalf("drawdown: %+v %v", d, err)
	}
	equal(t, "peak", d.PeakUSDT, "100")
	equal(t, "trough", d.TroughUSDT, "50")
	equal(t, "amount", d.AmountUSDT, "50")
	equal(t, "ratio", d.Ratio, "0.5")
	if !d.PeakAt.Equal(day0) || !d.TroughAt.Equal(day0.Add(24*time.Hour)) {
		t.Fatalf("times: %v %v", d.PeakAt, d.TroughAt)
	}
}

// TestMaxDrawdownUnavailable проверяет, что пустой ряд и пропуск дают unavailable, а не 0.
func TestMaxDrawdownUnavailable(t *testing.T) {
	cases := map[string][]EquityPoint{
		"empty": nil,
		"gap": {{At: day0, EquityUSDT: dec(t, "100"), CumulativeTransfersUSDT: model.Zero()},
			{At: day0.Add(2 * time.Hour), EquityUSDT: dec(t, "90"), CumulativeTransfersUSDT: model.Zero()}},
		"non-positive": series(t, time.Hour, [2]string{"0", "0"}, [2]string{"1", "0"}),
	}
	for name, pts := range cases {
		d, err := MaxDrawdown(pts, time.Hour, false)
		if err != nil || d.Available || d.Reason == "" {
			t.Fatalf("%s: %+v %v", name, d, err)
		}
	}
	unordered := series(t, time.Hour, [2]string{"1", "0"}, [2]string{"1", "0"})
	unordered[1].At = day0
	if _, err := MaxDrawdown(unordered, time.Hour, false); !errors.Is(err, ErrInvalidReportInput) {
		t.Fatalf("unordered: %v", err)
	}
}

// TestTransferAdjustedDrawdown проверяет, что пополнение не скрывает просадку.
func TestTransferAdjustedDrawdown(t *testing.T) {
	pts := series(t, 24*time.Hour, [2]string{"100", "0"}, [2]string{"150", "60"})
	raw, _ := MaxDrawdown(pts, 24*time.Hour, false)
	adj, _ := MaxDrawdown(pts, 24*time.Hour, true)
	equal(t, "raw", raw.Ratio, "0")
	equal(t, "adjusted", adj.Ratio, "0.1")
}

// summaryInput возвращает согласованный вход: delta 20 = 30 + (5-0) - 10 - 5 + 0.
func summaryInput(t *testing.T) SummaryInput {
	return SummaryInput{RunID: "run-1", StrategyID: "momentum-v1", ProfileID: "baseline_delta_1x",
		ConfigHash: "cfg", DatasetHash: "ds",
		Daily: series(t, 24*time.Hour, [2]string{"1000", "0"}, [2]string{"980", "0"}, [2]string{"1020", "0"}),
		Totals: Totals{RealizedPnLUSDT: dec(t, "30"), UnrealizedPnLStartUSDT: dec(t, "0"), UnrealizedPnLEndUSDT: dec(t, "5"),
			FeesUSDT: dec(t, "10"), FundingUSDT: dec(t, "-5"), SlippageUSDT: dec(t, "2"), NetTransfersUSDT: dec(t, "0")},
		Fills: []model.ConfirmedFill{{Side: model.FillBuy}, {Side: model.FillSell}, {Side: model.FillBuy}}, EntryIntents: 2,
		Coverage: Coverage{FundingComplete: true, MarkComplete: true}}
}

// TestBuildSummary проверяет доходность, счётчики, hourly unavailable и сверку тождества.
func TestBuildSummary(t *testing.T) {
	s, err := BuildSummary(summaryInput(t))
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "net return", s.NetReturn, "0.02")
	equal(t, "daily dd", s.DailyDrawdown.Ratio, "0.02")
	if s.HourlyDrawdown.Available || s.Identity.Status != IdentityVerified || s.Scenario != "actual_funding" ||
		s.EntryFillCount != 2 || s.FillCount != 3 || s.EntryIntentCount != 2 {
		t.Fatalf("summary: %+v", s)
	}
}

// TestIdentityStatuses проверяет MISMATCH и UNVERIFIED без подгонки остатка.
func TestIdentityStatuses(t *testing.T) {
	in := summaryInput(t)
	in.Totals.FeesUSDT = dec(t, "11")
	s, err := BuildSummary(in)
	if err != nil || s.Identity.Status != IdentityMismatch {
		t.Fatalf("mismatch: %+v %v", s.Identity, err)
	}
	equal(t, "difference", s.Identity.DifferenceUSDT, "1")
	in.Coverage.FundingComplete = false
	if s, _ = BuildSummary(in); s.Identity.Status != IdentityUnverified {
		t.Fatalf("unverified: %+v", s.Identity)
	}
}

// TestFundingZeroScenario проверяет явную пометку сценария и запрет ненулевого funding.
func TestFundingZeroScenario(t *testing.T) {
	in := summaryInput(t)
	in.Coverage.FundingZeroScenario = true
	if _, err := BuildSummary(in); !errors.Is(err, ErrInvalidReportInput) {
		t.Fatalf("non-zero funding accepted: %v", err)
	}
	in.Totals.FundingUSDT = model.Zero()
	in.Totals.FeesUSDT = dec(t, "15")
	s, err := BuildSummary(in)
	if err != nil || s.Scenario != "funding_zero" || s.Identity.Status != IdentityVerified {
		t.Fatalf("funding zero: %+v %v", s, err)
	}
}

// TestBuildSummaryRejectsInvalid проверяет обязательные поля и невалидные суммы.
func TestBuildSummaryRejectsInvalid(t *testing.T) {
	for name, mutate := range map[string]func(*SummaryInput){
		"no run":     func(in *SummaryInput) { in.RunID = "" },
		"no daily":   func(in *SummaryInput) { in.Daily = nil },
		"zero total": func(in *SummaryInput) { in.Totals.FeesUSDT = model.Decimal{} },
		"intents":    func(in *SummaryInput) { in.EntryIntents = -1 },
	} {
		in := summaryInput(t)
		mutate(&in)
		if _, err := BuildSummary(in); !errors.Is(err, ErrInvalidReportInput) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
