package paper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// runnerMarket возвращает позитивный публичный рынок с явным курсом каждого дня.
func runnerMarket(t *testing.T, in CycleInput) (CycleRunner, *publicFake) {
	t.Helper()
	req, f := sourceFixture(t, "BTCUSDT, ETHUSDT, HYPEUSDT")
	f.edit = func(q *http.Request, data map[string]any) {
		if q.URL.Path != "/v5/market/kline" || q.URL.Query().Get("interval") != "D" {
			return
		}
		for i, row := range data["list"].([]any) {
			fields := row.([]string)
			for j := 1; j <= 4; j++ {
				fields[j] = fmt.Sprint(100 - i)
			}
		}
	}
	return CycleRunner{RecoveryRequest: RecoveryRequest{PlanBuilder: in.PlanBuilder, Admission: in.Admission}, DailyBars: req.DailyBars, FormulaID: in.FormulaID, Now: req.Now, Transport: f}, f
}

// TestCyclePublicPreparation проверяет фактические данные, signal и прекращение при отказе источника.
func TestCyclePublicPreparation(t *testing.T) {
	in := cycleFixture(t)
	want := preparedFixture(t, in)
	for _, kind := range []string{"positive", "model", "signal", "empty-daily", "missing-mark", "missing-funding", "stale", "cancel", "late", "out-of-order", "close-all"} {
		t.Run(kind, func(t *testing.T) {
			r, f := runnerMarket(t, in)
			run, purpose, profile := in.Run, in.Purpose, in.Profile
			ctx := t.Context()
			var previous *ResolvedCycle
			switch kind {
			case "model":
				profile.DataDeadline++
			case "signal":
				r.DailyBars--
			case "missing-funding", "out-of-order", "close-all":
				prior := replayedReceipt(t, want, want.ExpectedFills(), ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: run.ScheduledAt})
				previous = &prior
				if kind != "out-of-order" {
					run.ScheduledAt = run.ScheduledAt.Add(24 * time.Hour)
					run.RunID = "later"
					f.now = run.ScheduledAt
				}
				if kind == "close-all" {
					purpose = paperwire.Purpose{Kind: "close-all", CommandID: "operator", ControlRevision: 2}
				}
			case "stale":
				r.Now = func() time.Time { return f.now.Add(2 * profile.SourceMaxAge) }
			case "late":
				run.ScheduledAt = run.ScheduledAt.Add(24 * time.Hour)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			routes := map[string]string{"missing-mark": "/v5/market/mark-price-kline", "empty-daily": "/v5/market/kline", "missing-funding": "/v5/market/funding/history"}
			if route := routes[kind]; route != "" {
				f.edit = func(q *http.Request, data map[string]any) {
					if q.URL.Path == route && q.URL.Query().Get("symbol") == "HYPEUSDT" {
						data["list"] = []any{}
					}
				}
			}
			p, err := r.observeCycle(ctx, run, purpose, profile, previous)
			switch kind {
			case "positive":
				if err != nil || p.Hash() != want.Hash() || p.Plan().Hash() != want.Plan().Hash() || len(p.ExpectedFills()) == 0 || f.calls <= 4 {
					t.Fatal(kind, err, f.calls)
				}
			case "close-all":
				if err != nil || p.Purpose() != purpose || len(p.ExpectedFills()) == 0 {
					t.Fatal(kind, err)
				}
				for _, fill := range p.ExpectedFills() {
					if fill.Side != model.FillSell {
						t.Fatal(fill.Side)
					}
				}
			default:
				if err == nil || p.Hash() != "" {
					t.Fatal(kind, err)
				}
				if (kind == "model" || kind == "out-of-order") && f.calls != 0 {
					t.Fatal("лишний запрос", f.calls)
				}
			}
		})
	}
}

// TestCycleControlAdmission проверяет выбор допустимых направлений из действующего режима.
func TestCycleControlAdmission(t *testing.T) {
	normal := paperwire.Purpose{Kind: "rebalance"}
	close := paperwire.Purpose{Kind: "close-all", CommandID: "operator", ControlRevision: 3}
	for _, v := range []struct {
		mode, action, actor, kind, id string
		revision                      uint64
		purpose                       paperwire.Purpose
		buy, sell                     bool
	}{
		{"ACTIVE", "INITIALIZE", "SYSTEM", "initialize", "init", 1, normal, true, true},
		{"PAUSED_ENTRIES", "PAUSE_ENTRIES", "OPERATOR", "pause", "pause", 2, normal, false, true},
		{"HALTED", "HALT", "RISK", "risk", "risk", 3, normal, false, false},
		{"HALTED", "HALT", "OPERATOR", "close-all", "operator", 3, close, false, true},
		{"HALTED", "HALT", "OPERATOR", "close-all", "other", 3, close, false, false},
		{"HALTED", "HALT", "OPERATOR", "close-all", "operator", 4, close, false, false},
		{"HALTED", "HALT", "OPERATOR", "halt", "operator", 3, close, false, false},
	} {
		c := s.PaperCommandState{State: s.ControlState{Mode: v.mode, Revision: v.revision}, Action: v.action, Actor: v.actor, Kind: v.kind, CommandID: v.id}
		if s.MayAttemptPaperFill(v.purpose, c, model.FillBuy) != v.buy || s.MayAttemptPaperFill(v.purpose, c, model.FillSell) != v.sell {
			t.Fatal(v)
		}
	}
}

// runnerOn связывает проверенную область и живую сессию с реальным публичным transport.
func runnerOn(x *fillFixture) (CycleRunner, *publicFake, s.StoredRebalanceRun) {
	x.Helper()
	x.p = preparedFixture(x.T, x.in)
	r, f := runnerMarket(x.T, x.in)
	r.DB, r.Scope, r.Owner = x.Pool(), x.r.Key.Scope, x.owner
	run, err := s.CreateOrReadRebalanceRun(x.Context(), x.Pool(), s.RebalanceRunInput(x.in.Run))
	requireCycleSuccess(x.T, err)
	return r, f, run
}

// TestCycleAdvanceDurable проверяет фактические фазы, финансовые проводки и повтор после фиксации.
func TestCycleAdvanceDurable(t *testing.T) {
	t.Setenv("BYBIT_API_KEY", "synthetic")
	t.Setenv("BYBIT_API_SECRET", "synthetic")
	x := emptyRecordedFixture(t)
	r, f, run := runnerOn(x)
	r.Transport = sourceRoundTrip(func(q *http.Request) (*http.Response, error) {
		x.Require(q.URL.Host == "api-testnet.bybit.com" && q.Method == "GET" && strings.HasPrefix(q.URL.Path, "/v5/market/") && q.Header.Get("X-BAPI-API-KEY") == "" && q.Header.Get("X-BAPI-SIGN") == "" && q.Header.Get("Authorization") == "", "недопустимый HTTP-запрос")
		return f.RoundTrip(q)
	})
	x.Require(errors.Is(r.Owner.PermitPaperScope(t.Context(), new(sql.DB), r.Scope), s.ErrOwnerArgument), "чужой пул")
	gate := beginReconcile(x)
	r.Owner = x.owner
	before := x.ReadHistoryAudit(x.Pool())
	blocked, err := r.AdvanceCycle(t.Context(), run, x.in.Purpose, x.in.Profile)
	x.Require(errors.Is(err, s.ErrOwnerReconciliationRequired) && reflect.DeepEqual(blocked, RecordedPaperCycle{}) && f.calls == 0 && before == x.ReadHistoryAudit(x.Pool()), "до проверенной сверки нет эффектов", err)
	requireCycleSuccess(t, ReconcilePaperOwner(t.Context(), x.Pool(), r.Owner, gate.proof))
	view, err := r.AdvanceCycle(t.Context(), run, x.in.Purpose, x.in.Profile)
	requireCycleSuccess(t, err)
	receipt, ok := view.Resolution()
	want := replayedReceipt(t, x.p, x.p.ExpectedFills(), ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: run.ScheduledAt})
	x.Require(ok && receipt.Hash() == want.Hash() && reflect.DeepEqual(receipt.ActualAfter(), want.ActualAfter()), "канонический receipt и состояние")
	durable := x.named(run.RunID, nil)
	row, present := durable.Resolution()
	x.Require(present && row.Hash() == want.Hash() && durable.PlanFound() && durable.facts.Plan.Hash() == x.p.Plan().Hash() && durable.DataHash() == view.DataHash(), "независимое чтение фаз")
	count, a := f.calls, x.ReadHistoryAudit(x.Pool())
	x.Require(a.Runs == 1 && a.Inputs == 1 && a.Intents == len(want.Fills()) && a.Fills == a.Intents && a.Postings == a.Fills && a.Fees == a.Fills && f.calls > 0, "эффекты исходного цикла", a)
	for range 2 {
		again, err := r.AdvanceCycle(t.Context(), run, x.in.Purpose, paperwire.Profile{})
		requireCycleSuccess(t, err)
		after, ok := again.Resolution()
		x.Require(ok && after.Hash() == receipt.Hash() && again.DataHash() == durable.DataHash() && f.calls == count && a == x.ReadHistoryAudit(x.Pool()), "повтор исходного состояния")
	}
}

// TestCycleResumePhases продолжает вход до плана и остаток реального частичного fill без нового источника.
func TestCycleResumePhases(t *testing.T) {
	for _, phase := range []string{"input", "one-intent", "partial", "pause", "risk", "owner", "foreign", "source-error"} {
		t.Run(phase, func(t *testing.T) {
			x := emptyRecordedFixture(t)
			r, f, run := runnerOn(x)
			x.r.Key.RunID, x.r.Key.InputHash = run.RunID, x.p.Hash()
			if phase != "source-error" && phase != "foreign" {
				x.Done(s.StorePaperInput(t.Context(), x.Pool(), x.p.input))
			}
			if phase == "one-intent" || phase == "partial" {
				x.Done(s.StorePlanRevision(t.Context(), x.Pool(), x.p.Plan()))
				intent := x.named(run.RunID, nil).OrderIntents()[0]
				requireCycleSuccess(t, s.RecordOrderIntent(t.Context(), x.Pool(), intent))
				if phase == "partial" {
					got, err := s.RecordPaperFill(t.Context(), x.Pool(), x.request(x.slice(0, model.Zero(), cycleAmount(t, "0.25"))), CheckPaperFill(x.p))
					x.Require(err == nil && got.Confirmed, "исходное частичное исполнение", err)
				}
			}
			if phase == "risk" || phase == "pause" {
				requireCycleSuccess(t, x.transition(t.Context(), 1, phase, "block"))
			}
			if phase == "owner" {
				requireCycleSuccess(t, x.owner.Release(t.Context(), time.Now()))
			}
			if phase == "foreign" {
				r.Scope.StrategyID += "/foreign"
				run.StrategyID = r.Scope.StrategyID
			}
			before := x.ReadHistoryAudit(x.Pool())
			profile := paperwire.Profile{}
			if phase == "source-error" {
				profile = x.in.Profile
				f.edit = func(_ *http.Request, m map[string]any) { m["list"] = []any{} }
			}
			view, err := r.AdvanceCycle(t.Context(), run, x.in.Purpose, profile)
			if phase == "owner" || phase == "foreign" || phase == "source-error" {
				x.Require(err != nil && (f.calls > 0) == (phase == "source-error") && reflect.DeepEqual(view, RecordedPaperCycle{}) && before == x.ReadHistoryAudit(x.Pool()), "отказ и сохранность", err)
				if phase == "owner" {
					x.Require(errors.Is(err, s.ErrOwnerLost), "потеря владельца", err)
				}
				return
			}
			requireCycleSuccess(t, err)
			x.Require(f.calls == 0, "сеть при возобновлении")
			proof, done := view.Resolution()
			if phase == "pause" || phase == "risk" {
				x.Require(!done && len(view.facts.Fills()) == 0 && !view.facts.Completed, "режим блокирует покупки")
				return
			}
			fills := x.p.ExpectedFills()
			if phase == "partial" {
				q := cycleAmount(t, "0.25")
				rem, err := fills[0].Quantity.Sub(q)
				requireCycleSuccess(t, err)
				fills = append([]model.ConfirmedFill{x.slice(0, model.Zero(), q).Fill, x.slice(0, q, rem).Fill}, fills[1:]...)
			}
			want := replayedReceipt(t, x.p, fills, ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: run.ScheduledAt})
			x.Require(done && proof.Hash() == want.Hash(), "учтён фактический префикс и точный остаток")
		})
	}
}

// TestCycleFinancialChoices проверяет предшественника и точный worklist с частично исполненным intent.
func TestCycleFinancialChoices(t *testing.T) {
	in := cycleFixture(t)
	p := preparedFixture(t, in)
	run := s.StoredRebalanceRun{RebalanceRunInput: s.RebalanceRunInput(in.Run), ConfigHash: p.Binding().ConfigHash}
	facts := s.PaperRunFacts{InputFound: true, Input: p.input, Control: s.PaperCommandState{ConfigHash: run.ConfigHash, State: s.ControlState{Mode: "ACTIVE"}}}
	state := RecoveredPaper{proof: RecoveryProof{facts: facts}}
	prior := replayedReceipt(t, p, p.ExpectedFills(), ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: run.ScheduledAt})
	head := state
	head.proof.facts.HeadFound, head.proof.facts.Head, head.last = true, p.Binding(), &prior
	open := head
	open.current = p
	other := run
	other.RunID = "following"
	next := other
	next.ScheduledAt = next.ScheduledAt.Add(time.Hour)
	foreign := run
	foreign.ConfigHash = "foreign"
	for _, tc := range []struct {
		state  RecoveredPaper
		run    s.StoredRebalanceRun
		prior  *ResolvedCycle
		reason error
	}{
		{state, run, nil, nil}, {head, run, nil, nil}, {head, next, &prior, nil},
		{head, other, nil, ErrCycleAdvance}, {open, next, nil, ErrCycleAdvance}, {state, foreign, nil, ErrCycleAdvance},
	} {
		got, err := cyclePredecessor(tc.state, tc.run)
		if !errors.Is(err, tc.reason) || got != tc.prior {
			t.Fatal(tc.run.RunID, got, err)
		}
	}
	partial, err := MakeFillSlice(p, 0, model.Zero(), cycleAmount(t, "0.25"))
	requireCycleSuccess(t, err)
	progress, err := ProjectProgress(p, []model.ConfirmedFill{partial})
	requireCycleSuccess(t, err)
	view := RecordedPaperCycle{facts: facts, prepared: p, progress: progress}
	view.intents, err = view.projectIntents()
	requireCycleSuccess(t, err)
	for _, kind := range []string{"active", "paused", "unknown", "failure"} {
		calls := 0
		view.facts.Control.State.Mode = map[bool]string{true: "PAUSED_ENTRIES", false: "ACTIVE"}[kind == "paused"]
		write := func(_ context.Context, _ *sql.DB, req s.PaperFillRequest, _ s.PaperFillCheck) (s.PaperFillResult, error) {
			left := progress.Remaining()[calls]
			want, e := MakeFillSlice(p, calls, left.Confirmed, left.Remaining)
			if e != nil || !equalFill(req.Fill, want) || req.OrderLinkID != view.intents[calls].OrderLinkID {
				t.Fatal(kind, calls, e)
			}
			calls++
			if kind == "failure" {
				return s.PaperFillResult{}, context.Canceled
			}
			return s.PaperFillResult{Confirmed: kind != "unknown"}, nil
		}
		err := (CycleRunner{}).applyRemaining(t.Context(), s.PaperWriteKey{}, view, write)
		if kind == "active" && (err != nil || calls != len(view.intents)) || kind == "paused" && (err != nil || calls != 0) || kind == "unknown" && !errors.Is(err, s.ErrPaperWriteUnknown) || kind == "failure" && !errors.Is(err, context.Canceled) {
			t.Fatal(kind, calls, err)
		}
	}
}
