package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paper"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// cliMarket задаёт курсы и одно funding-наблюдение в следующую неделю.
type cliMarket struct {
	*testing.T
	now, funding time.Time
	rising       string
	calls        int
	broken       bool
}

// RoundTrip строит страницы по реальным запросам и немедленно замечает попытку подписанного вызова.
func (m *cliMarket) RoundTrip(r *http.Request) (*http.Response, error) {
	m.Helper()
	m.calls++
	if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api-testnet.bybit.com" || r.Header.Get("Authorization") != "" || r.Header.Get("X-BAPI-API-KEY") != "" || r.Header.Get("X-BAPI-SIGN") != "" {
		m.Fatal("непубличный запрос", r.URL.Path)
	}
	q := r.URL.Query()
	symbol := q.Get("symbol")
	result := map[string]any{"symbol": symbol, "category": "linear"}
	rows := []any{}
	switch r.URL.Path {
	case "/v5/market/time":
	case "/v5/market/kline", "/v5/market/mark-price-kline":
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		if r.URL.Path == "/v5/market/kline" {
			start++
		}
		step := int64(time.Hour / time.Millisecond)
		if q.Get("interval") == "D" {
			step = dayMS
		}
		missingDaily := m.broken && symbol == "HYPEUSDT" && step == dayMS
		for at := end / step * step; at >= start && !missingDaily; at -= step {
			price := "10"
			if q.Get("interval") == "D" && symbol == m.rising {
				price = strconv.Itoa(100 - len(rows))
			}
			row := []string{strconv.FormatInt(at, 10), price, price, price, price}
			if r.URL.Path == "/v5/market/kline" {
				row = append(row, "1", "10")
			}
			rows = append(rows, row)
		}
	case "/v5/market/funding/history":
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
		if at := m.funding.UnixMilli(); at >= start && at <= end {
			rows = append(rows, map[string]string{"symbol": symbol, "fundingRate": "0.001", "fundingRateTimestamp": strconv.FormatInt(at, 10)})
		}
	default:
		m.Fatal("закрытый endpoint", r.URL.Path)
	}
	result["list"] = rows
	raw, err := json.Marshal(map[string]any{"retCode": 0, "retMsg": "OK", "time": m.now.UnixMilli(), "result": result})
	if err != nil {
		m.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
}

// cliPaperArgs выдаёт реальные файлы YAML и canonical profile c явно заданными test-only параметрами.
func cliPaperArgs(t *testing.T, policy string, anchor time.Time) []string {
	t.Helper()
	unit, oneErr := model.ParseAmount("1")
	max, maxErr := model.ParseAmount("100000")
	if err := errors.Join(oneErr, maxErr); err != nil {
		t.Fatal(err)
	}
	profile := paperwire.Profile{Parameters: backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Hour, Rules: map[string]planner.MarketDeltaRules{}}, SourceMaxAge: time.Minute, DataDeadline: time.Hour, Poll: time.Second, RPCTimeout: time.Second, ExecutionReference: "closed-hourly-trade-close/v1", FundingSchedules: map[string]paperwire.FundingSchedule{}}
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		profile.Parameters.Rules[sym] = planner.MarketDeltaRules{QtyStep: unit, MinQty: unit, MaxMarketQty: max, MinIncreaseNotional: model.Zero()}
		profile.FundingSchedules[sym] = paperwire.FundingSchedule{Anchor: anchor, Interval: 7 * 24 * time.Hour}
	}
	profileRaw, hash, err := paperwire.EncodeProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paperConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("roc30"), []byte(hash), 1)
	raw = bytes.Replace(raw, []byte("next_monday"), []byte(policy), 1)
	dir := t.TempDir()
	conf, prof := filepath.Join(dir, "scope.yaml"), filepath.Join(dir, "profile.json")
	if err = errors.Join(os.WriteFile(conf, raw, 0600), os.WriteFile(prof, profileRaw, 0600)); err != nil {
		t.Fatal(err)
	}
	args := []string{"paper", "-config", conf, "-profile", prof, "-account", "paper/cli"}
	if policy == "now" {
		args = append(args, "-onboarding", "test-onboarding")
	}
	return args
}

// useCLIJournal указывает команде отдельную тестовую базу и сохраняет независимый наблюдающий пул.
func useCLIJournal(t *testing.T) *pgtest.Journal {
	t.Helper()
	j := pgtest.OpenJournal(t, s.OpenMomentumPostgres)
	u, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	j.Require(err == nil, err)
	u.Path = "/" + j.Name()
	t.Setenv("MOMENTUM_POSTGRES_DSN", u.String())
	return j
}

// runPaperCLI выполняет полную команду до одного такта и проверяет фактическое освобождение владельца.
func runPaperCLI(j *pgtest.Journal, m *cliMarket, args []string, before func(context.Context, paper.RecoveredPaper) error, repeat ...int) error {
	j.Helper()
	ctx, cancel := context.WithCancel(j.Context())
	defer cancel()
	wait := func(_ context.Context, delay time.Duration) error {
		j.Require(delay > 0 && delay <= time.Second, "срок poll", delay)
		if len(repeat) > 0 && repeat[0] > 0 {
			repeat[0]--
			return nil
		}
		cancel()
		return ctx.Err()
	}
	err := dispatch(ctx, args, io.Discard, paperRuntime{now: func() time.Time { return m.now }, wait: wait, transport: m, before: before})
	j.Require(j.ReadHistoryAudit(j.Pool()).Active == "", "владелец остался активен после shutdown")
	return err
}

// cliRecovery перечитывает финансовое состояние после завершения отдельной команды.
func cliRecovery(j *pgtest.Journal) paper.RecoveredPaper {
	j.Helper()
	db := pgtest.Existing(j.T, j.Name(), s.OpenMomentumPostgres)
	scope := s.ControlScope{Environment: "paper", AccountID: "paper/cli", StrategyID: "momentum-v1"}
	v, err := paper.Recover(j.Context(), paper.RecoveryRequest{DB: db, Scope: scope, PlanBuilder: planner.BuildBaselinePaperPlan, Admission: planner.AdmitBaselineExecution})
	j.Require(err == nil, err)
	return v
}

// TestPaperCLIWeeks проверяет start now, следующий Monday, фактическую выплату и удержанный BUY.
func TestPaperCLIWeeks(t *testing.T) {
	j := useCLIJournal(t)
	t.Setenv("BYBIT_API_KEY", "synthetic")
	t.Setenv("BYBIT_API_SECRET", "synthetic")
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	m := &cliMarket{T: t, now: monday.Add(-72*time.Hour + 13*time.Minute), funding: monday, rising: "BTCUSDT"}
	args := cliPaperArgs(t, "now", monday)
	j.Require(errors.Is(runPaperCLI(j, m, args, nil), context.Canceled), "остановка genesis")
	first, ok := cliRecovery(j).Last()
	j.Require(ok && first.Binding().ScheduledAt.Equal(m.now) && len(first.Fills()) == 1 && first.Fills()[0].Symbol == "BTCUSDT" && first.Fills()[0].Side == model.FillBuy, "фактический onboarding", first.Binding())
	quantity, e := first.Fills()[0].Quantity.WireDecimal()
	equity, eqErr := first.ActualAfter().EquityUSDT.WireDecimal()
	j.Require(e == nil && quantity == "333" && eqErr == nil && equity == "10000", "реальные цель и equity", quantity, equity)
	scope := s.ControlScope(first.Binding().Scope)
	j.Done(s.ApplyPaperCommandState(t.Context(), j.Pool(), s.PaperCommandRequest{Scope: scope, ExpectedRevision: 1, ConfigHash: first.Binding().ConfigHash, Action: "PAUSE_ENTRIES", Actor: "OPERATOR", ActorID: "operator/test", CommandID: "pause", Kind: "pause", Reason: "test", Confirmed: true}))
	m.now, m.rising = monday, "ETHUSDT"
	j.Require(errors.Is(runPaperCLI(j, m, args, nil), context.Canceled), "остановка Monday")
	current := cliRecovery(j)
	p, found := current.Current()
	progress, open := current.Progress()
	j.Require(found && open && p.Binding().ScheduledAt.Equal(monday) && p.Binding().Sequence == 2 && p.Before().FundingThrough.Equal(first.Binding().ScheduledAt) && p.Cutoff().FundingThrough.Equal(monday), "UTC границы недели и funding")
	j.Require(!progress.Complete() && len(progress.Fills()) == 1 && progress.Fills()[0].Side == model.FillSell && progress.Fills()[0].Symbol == "BTCUSDT", "разрешённая редукция")
	blocked := false
	for _, left := range progress.Remaining() {
		if left.Symbol == "ETHUSDT" && left.Side == model.FillBuy {
			q, e := left.Remaining.WireDecimal()
			blocked = e == nil && q == "333"
		}
	}
	j.Require(blocked, "покупка осталась открытой")
	start, startErr := first.ActualAfter().WalletUSDT.WireDecimal()
	end, endErr := p.Cutoff().WalletUSDT.WireDecimal()
	sold, sellErr := progress.Fills()[0].Quantity.WireDecimal()
	j.Require(startErr == nil && endErr == nil && start == "10000" && end == "9996.67", "одна ставка 0.001 по mark=10", start, end)
	j.Require(sellErr == nil && sold == "333", "точное исполнение продажи BTC", sold)
	before, calls := j.ReadHistoryAudit(j.Pool()), m.calls
	m.now = m.now.Add(7 * 24 * time.Hour)
	m.broken = true
	j.Require(errors.Is(runPaperCLI(j, m, args, nil, 1), context.Canceled), "продолжение после рестарта")
	after := j.ReadHistoryAudit(j.Pool())
	j.Require(m.calls == calls && before.Financial == after.Financial && after.Sessions == before.Sessions+1 && after.Fills == 2 && after.Inputs == 2, "без повторного funding, fill или HTTP")
}

// TestPaperCLIInputBeforePlan воспроизводит реальную запись input и более поздний restart до Tick.
func TestPaperCLIInputBeforePlan(t *testing.T) {
	source := useCLIJournal(t)
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	m := &cliMarket{T: t, now: monday.Add(20 * time.Minute), funding: monday, rising: "BTCUSDT", broken: true}
	args := cliPaperArgs(t, "next_monday", monday)
	missing := runPaperCLI(source, m, args, nil)
	audit := source.ReadHistoryAudit(source.Pool())
	source.Require(missing != nil && !errors.Is(missing, context.Canceled) && audit.Inputs == 0 && audit.Fills == 0, "неготовые D закрывают вход", missing)
	m.broken = false
	source.Require(errors.Is(runPaperCLI(source, m, args, nil), context.Canceled), "недельный запуск")
	result, ok := cliRecovery(source).Last()
	source.Require(ok && result.Binding().ScheduledAt.Equal(monday) && result.Binding().RunID == "weekly/momentum-v1/paper/cli/2026-11-16T00:00:00Z", "привязка понедельника", result.Binding())
	row, found, err := s.ReadPaperInput(t.Context(), source.Pool(), result.Binding().Scope, 1)
	source.Require(err == nil && found, err)
	target := useCLIJournal(t)
	interrupt := func(ctx context.Context, _ paper.RecoveredPaper) error {
		target.Done(s.CreateOrReadRebalanceRun(ctx, target.Pool(), s.RebalanceRunInput(row.Input.Values().Run)))
		target.Done(s.StorePaperInput(ctx, target.Pool(), row.Input))
		return errors.New("stop after input")
	}
	target.Require(runPaperCLI(target, m, args, interrupt) != nil, "прерывание после input")
	facts, err := s.ReadPaperRunFacts(t.Context(), target.Pool(), s.ControlScope(result.Binding().Scope), result.Binding().RunID)
	target.Require(err == nil && facts.InputFound && !facts.PlanFound && !facts.Completed && len(facts.Intents()) == 0, "исходная фаза", err)
	m.now = monday.Add(3 * 24 * time.Hour)
	m.broken = true
	calls := m.calls
	target.Require(errors.Is(runPaperCLI(target, m, args, nil), context.Canceled), "продолжение после дедлайна")
	recovered, done := cliRecovery(target).Last()
	target.Require(done && result.Hash() == recovered.Hash() && m.calls == calls && target.ReadHistoryAudit(target.Pool()).Runs == 1, "исторический receipt без лишнего run или сети")
	loss := func(ctx context.Context, _ paper.RecoveredPaper) error {
		target.KillOwner(s.ExecutionOwnerLockKey(s.OwnerIdentity{Environment: "paper", AccountID: "paper/cli", StrategyID: "momentum-v1"}))
		return nil
	}
	before := target.ReadHistoryAudit(target.Pool())
	err = runPaperCLI(target, m, args, loss)
	target.Require(err != nil && !errors.Is(err, context.Canceled) && before.Financial == target.ReadHistoryAudit(target.Pool()).Financial, "потеря lock без финансовых изменений")
	m.now = monday.Add(8 * 24 * time.Hour)
	target.Require(errors.Is(runPaperCLI(target, m, args, nil), context.Canceled), "подтверждение SKIPPED")
	skipped, present, err := s.ReadRunCompletion(t.Context(), target.Pool(), "weekly/momentum-v1/paper/cli/2026-11-23T00:00:00Z")
	target.Require(err == nil && present && skipped.Outcome == s.RunSkipped && skipped.Reason == paper.SkipDeadlineMissed && m.calls == calls, "без догоняющего run", err)
}

// TestPaperCLIRoutes проверяет автономию штатных команд и принадлежность signal batch.
func TestPaperCLIRoutes(t *testing.T) {
	j := useCLIJournal(t)
	t.Setenv("BYBIT_API_KEY", "synthetic")
	t.Setenv("BYBIT_API_SECRET", "synthetic")
	var output bytes.Buffer
	f := &fixtureTransport{t: t}
	if err := run(argsFor("2s"), &output, f); err != nil {
		t.Fatal(err)
	}
	var batch success
	if err := json.Unmarshal(output.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	j.Done(s.ReadSignalBatch(t.Context(), j.Pool(), batch.BatchHash))
	before := j.ReadHistoryAudit(j.Pool())
	j.Require(before.Runs == 0 && before.Inputs == 0 && before.Fills == 0 && before.Sessions == 0, "collect записал paper")
	backtestArgs, _, _, _ := cliFixture(t)
	count := len(f.calls)
	j.Require(run(append([]string{"backtest"}, backtestArgs...), io.Discard, f) == nil && count == len(f.calls) && before == j.ReadHistoryAudit(j.Pool()), "backtest затронул сеть или journal")
	other := useCLIJournal(t)
	m := &cliMarket{T: t, now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), rising: "BTCUSDT"}
	m.funding = m.now
	other.Require(errors.Is(runPaperCLI(other, m, cliPaperArgs(t, "now", m.now), nil), context.Canceled), "изолированный paper")
	_, err := s.ReadSignalBatch(t.Context(), other.Pool(), batch.BatchHash)
	other.Require(errors.Is(err, s.ErrSignalBatchAbsent) && before == j.ReadHistoryAudit(j.Pool()), "перекрёстная запись", err)
}

// TestPaperCLIValidation запрещает неявные настройки и ручные подмены времени до сети.
func TestPaperCLIValidation(t *testing.T) {
	t.Setenv("MOMENTUM_POSTGRES_DSN", "probe-secret")
	m := &cliMarket{T: t, now: time.Now().UTC()}
	args := cliPaperArgs(t, "next_monday", m.now.Truncate(24*time.Hour))
	for _, suffix := range [][]string{{"-now", "2026-11-16"}, {"-onboarding", "forbidden"}, {"-account", "live/account"}, {"-profile", "/absent"}, {"-config", "/absent"}} {
		err := run(append(append([]string{}, args...), suffix...), io.Discard, m)
		if err == nil || m.calls != 0 || strings.Contains(err.Error(), "probe-secret") {
			t.Fatal("принят недопустимый CLI-параметр", suffix)
		}
	}
	for _, path := range []string{"/v5/order/realtime", "/v5/market/time?api_key=synthetic"} {
		request, _ := http.NewRequestWithContext(t.Context(), "GET", "https://api-testnet.bybit.com"+path, nil)
		if _, err := (publicPaperTransport{m}).RoundTrip(request); err == nil || m.calls != 0 {
			t.Fatal("вызов закрытого маршрута", path)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(waitPaper(ctx, time.Hour), context.Canceled) {
		t.Fatal("ожидание проигнорировало остановку")
	}
}
