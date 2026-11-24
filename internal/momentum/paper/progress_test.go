package paper

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// nextCycle получает только fake-страницы следующего cutoff с прежним actual состоянием.
func nextCycle(t *testing.T, in CycleInput, previous ResolvedCycle, closeAll bool) CycleInput {
	t.Helper()
	r, f := sourceFixture(t, "BTCUSDT, ETHUSDT, HYPEUSDT")
	in.Run.ScheduledAt = in.Run.ScheduledAt.Add(24 * time.Hour)
	in.Run.RunID += "-next"
	r.Run = in.Run
	r.Profile = in.Profile
	r.FundingAfter = previous.ActualAfter().FundingThrough
	f.now = in.Run.ScheduledAt
	in.Sequence++
	in.Previous = &previous
	if closeAll {
		in.Purpose = paperwire.Purpose{Kind: "close-all", CommandID: "close", ControlRevision: 2}
		r.DailyBars = 0
	}
	r.Purpose = in.Purpose
	r.Now = func() time.Time { return f.now }
	o, err := ReadCycleObservation(t.Context(), r)
	requireCycleSuccess(t, err)
	in.Observation = o
	return in
}

// TestPaperActualPrefix проверяет частичное исполнение, abort и закрытие ActualAfter.
func TestPaperActualPrefix(t *testing.T) {
	in := cycleFixture(t)
	p := preparedFixture(t, in)
	half, _ := p.ExpectedFills()[0].Quantity.Quo(cycleAmount(t, "2"))
	f, err := MakeFillSlice(p, 0, model.Zero(), half)
	requireCycleSuccess(t, err)
	progress, err := ProjectProgress(p, []model.ConfirmedFill{f, f})
	if err != nil || progress.Complete() || len(progress.Fills()) != 1 {
		t.Fatal(err)
	}
	if !decimalEqual(progress.Remaining()[0].Remaining, half) {
		t.Fatal("residual")
	}
	if _, err = SealResolution(p, []model.ConfirmedFill{f}, ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt}); err == nil {
		t.Fatal("partial completion")
	}
	aborted := replayedReceipt(t, p, []model.ConfirmedFill{f}, ResolutionDisposition{Kind: "ABORTED", CommandID: "stop", ControlRevision: 1, ResolvedAt: in.Run.ScheduledAt})
	next := nextCycle(t, in, aborted, true)
	closed := preparedFixture(t, next)
	for _, hash := range []string{aborted.Hash(), aborted.InputHash()} {
		raw := bytes.ReplaceAll(closed.Payload(), []byte(hash), bytes.Repeat([]byte("a"), 64))
		if _, err := paperwire.DecodeInputRecord(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePreparedCycle(raw, &aborted, next.PlanBuilder, next.Admission); !errors.Is(err, ErrPaperCycle) {
			t.Fatal("forged previous hash", err)
		}
	}
	badClose := next
	badClose.PlanBuilder = func(_ string, m model.PlanMetadata, s model.PortfolioSnapshot, _ map[string][]data.DailyClose, _ map[string]planner.MarketDeltaRules, _ model.Decimal) (model.TradePlan, error) {
		targets := closed.Plan().Targets()
		targets[0].Quantity = half
		return model.NewTradePlan(model.PlanInput{Metadata: m, Snapshot: s, Targets: targets})
	}
	if _, err := PrepareCycle(badClose); !errors.Is(err, ErrPaperCycle) {
		t.Fatal("accepted nonzero close target", err)
	}
	if !decimalEqual(closed.Before().Positions[f.Symbol].Quantity, half) || len(closed.ExpectedFills()) != 1 {
		t.Fatal("expected instead of actual")
	}
	for _, target := range closed.Plan().Targets() {
		if !isZero(target.Quantity) {
			t.Fatal("close target")
		}
	}
	wallet := aborted.ActualAfter().WalletUSDT
	market, err := ValidateCycleObservation(SourceRequest{Run: next.Run, Purpose: next.Purpose, Profile: next.Profile, FundingAfter: aborted.ActualAfter().FundingThrough, Now: func() time.Time { return next.Observation.CapturedAt }}, next.Observation)
	requireCycleSuccess(t, err)
	if len(market.Funding) != 9 || decimalEqual(market.Funding[0].MarkPrice, market.Marks[market.Funding[0].Symbol].Price) {
		t.Fatal("missing distinct funding instants or mark fixture")
	}
	for _, assessment := range market.Funding {
		posting, err := model.ProjectLongFunding(aborted.ActualAfter().Positions[assessment.Symbol], assessment)
		requireCycleSuccess(t, err)
		wallet, err = wallet.Add(posting.CashDeltaUSDT)
		requireCycleSuccess(t, err)
	}
	if !decimalEqual(wallet, closed.Cutoff().WalletUSDT) {
		t.Fatal("funding repeated/missing")
	}
	full, err := ProjectProgress(closed, closed.ExpectedFills())
	if err != nil || !full.Complete() {
		t.Fatal(err)
	}
	for _, position := range full.ActualAfter().Positions {
		if !isZero(position.Quantity) || !isZero(position.CostBasisUSDT) {
			t.Fatal("pending increase executed")
		}
	}
	altered := next
	altered.Sequence++
	if _, err = PrepareCycle(altered); err == nil {
		t.Fatal("sequence")
	}
	altered = next
	altered.Run.AccountID = "paper/other"
	if _, err = PrepareCycle(altered); err == nil {
		t.Fatal("scope")
	}
	altered = next
	altered.Run.ScheduledAt = in.Run.ScheduledAt
	if _, err = PrepareCycle(altered); err == nil {
		t.Fatal("time")
	}
}

// TestPaperProgressRejections проверяет identity, order, limits и неоднозначные offsets.
func TestPaperProgressRejections(t *testing.T) {
	in := cycleFixture(t)
	p := preparedFixture(t, in)
	full := p.ExpectedFills()
	half, _ := full[0].Quantity.Quo(cycleAmount(t, "2"))
	first, _ := MakeFillSlice(p, 0, model.Zero(), half)
	last, _ := MakeFillSlice(p, 0, half, half)
	ordered := append([]model.ConfirmedFill{first, last}, full[1:]...)
	slices.Reverse(ordered)
	progress, err := ProjectProgress(p, ordered)
	if err != nil || !progress.Complete() {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.ConfirmedFill){
		func(f *model.ConfirmedFill) { f.ExecutionID = "unknown" },
		func(f *model.ConfirmedFill) { f.Symbol = "BAD" },
		func(f *model.ConfirmedFill) { f.Side = model.FillSell },
		func(f *model.ConfirmedFill) { f.ExecutedAt = f.ExecutedAt.Add(time.Microsecond) },
		func(f *model.ConfirmedFill) { f.FeeUSDT = model.Zero() },
		func(f *model.ConfirmedFill) { f.ExecutionPrice = cycleAmount(t, "11") },
		func(f *model.ConfirmedFill) { f.Quantity = cycleAmount(t, "1000000") },
	} {
		f := full[0]
		mutate(&f)
		if _, err = ProjectProgress(p, []model.ConfirmedFill{f}); !errors.Is(err, ErrPaperProgress) {
			t.Fatal("accepted", err)
		}
	}
	for _, fills := range [][]model.ConfirmedFill{{full[1]}, {last}, {first, full[0]}} {
		if _, err = ProjectProgress(p, fills); err == nil {
			t.Fatal("nonprefix/ambiguous")
		}
	}
	modified := first
	modified.FeeUSDT = model.Zero()
	if _, err = ProjectProgress(p, []model.ConfirmedFill{first, modified}); !errors.Is(err, model.ErrLedgerFillConflict) {
		t.Fatal(err)
	}
	if _, err = ProjectProgress(p, make([]model.ConfirmedFill, 257)); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	if _, err = ProjectProgress(PreparedCycle{}, nil); !errors.Is(err, ErrPaperProgress) {
		t.Fatal(err)
	}
	if _, err = MakeFillSlice(p, 0, model.Zero(), model.Zero()); err == nil {
		t.Fatal("zero quantity")
	}
	if _, err = MakeFillSlice(p, 0, cycleAmount(t, "-1"), half); err == nil {
		t.Fatal("negative offset")
	}
	if _, err = MakeFillSlice(p, 3, model.Zero(), half); err == nil {
		t.Fatal("index")
	}
	receipt := replayedReceipt(t, p, full, ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt})
	for _, change := range []func(*paperwire.ResolutionDocument){
		func(d *paperwire.ResolutionDocument) { d.ActualAfter.WalletUSDT = model.Zero() },
		func(d *paperwire.ResolutionDocument) { d.Remaining = nil },
		func(d *paperwire.ResolutionDocument) {
			d.ActualAfter.Positions[full[0].Symbol] = model.LongPosition{Quantity: full[0].Quantity, CostBasisUSDT: half}
		},
	} {
		doc := receipt.record.Values()
		change(&doc)
		forged, err := paperwire.EncodeResolutionRecord(doc, p.input)
		requireCycleSuccess(t, err)
		if _, err = DecodeResolution(forged.Payload(), p); !errors.Is(err, ErrPaperProgress) {
			t.Fatal("forged receipt", err)
		}
	}
	saved := receipt.Payload()
	receipt.Payload()[0] = '!'
	delete(receipt.ActualAfter().Positions, full[0].Symbol)
	receipt.Fills()[0].Symbol = "BAD"
	receipt.Remaining()[0].Symbol = "BAD"
	if !bytes.Equal(saved, receipt.Payload()) || len(receipt.ActualAfter().Positions) != 3 {
		t.Fatal("mutable receipt")
	}
	for _, disposition := range []ResolutionDisposition{
		{Kind: "ABORTED", ResolvedAt: in.Run.ScheduledAt},
		{Kind: "COMPLETED", CommandID: "x", ResolvedAt: in.Run.ScheduledAt},
		{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt.Add(-time.Microsecond)},
		{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt.Add(time.Nanosecond)},
	} {
		if _, err = SealResolution(p, full, disposition); err == nil {
			t.Fatal("invalid disposition")
		}
	}
}

// TestPaperInjectedZeroStrategy проверяет явные альтернативные цели и admission без Eq3.
func TestPaperInjectedZeroStrategy(t *testing.T) {
	in := cycleFixture(t)
	called := false
	in.PlanBuilder = singleTarget(model.Zero())
	in.Admission = func(_ model.TradePlan, r map[string]planner.MarketDeltaRules, _ model.Decimal, buy, sell map[string]model.Decimal) error {
		called = true
		clear(r)
		clear(buy)
		clear(sell)
		return nil
	}
	p := preparedFixture(t, in)
	if !called || len(p.ExpectedFills()) != 0 || len(in.Profile.Parameters.Rules) != 3 {
		t.Fatal("injection/copies")
	}
	progress, err := ProjectProgress(p, nil)
	if err != nil || !progress.Complete() {
		t.Fatal(err)
	}
	receipt := replayedReceipt(t, p, nil, ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: in.Run.ScheduledAt})
	if receipt.Kind() != "COMPLETED" {
		t.Fatal(err)
	}
}

// replayedReceipt требует канонический receipt и совпадение хэша после повтора.
func replayedReceipt(t *testing.T, p PreparedCycle, fills []model.ConfirmedFill, disposition ResolutionDisposition) ResolvedCycle {
	t.Helper()
	receipt, err := SealResolution(p, fills, disposition)
	requireCycleSuccess(t, err)
	replayed, err := DecodeResolution(receipt.Payload(), p)
	if err != nil || replayed.Hash() != receipt.Hash() {
		t.Fatal("receipt replay", err)
	}
	return replayed
}
