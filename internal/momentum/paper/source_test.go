package paper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
)

type publicFake struct {
	now   time.Time
	calls int
	edit  func(*http.Request, map[string]any)
	after func()
}

// RoundTrip возвращает только явные публичные fixtures и запрещает signed API.
func (f *publicFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	if r.URL.Host != "api-testnet.bybit.com" || r.Method != "GET" || r.Header.Get("X-BAPI-API-KEY") != "" || !strings.HasPrefix(r.URL.Path, "/v5/market/") {
		return nil, errors.New("unexpected private request")
	}
	q := r.URL.Query()
	result := map[string]any{"category": "linear", "symbol": q.Get("symbol")}
	rows := make([]any, 0)
	switch r.URL.Path {
	case "/v5/market/time":
	case "/v5/market/kline", "/v5/market/mark-price-kline":
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		if r.URL.Path == "/v5/market/kline" {
			start++
		}
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		step := int64(3600000)
		if q.Get("interval") == "D" {
			step = 86400000
		}
		for at := end / step * step; at >= start; at -= step {
			price := "10"
			if r.URL.Path == "/v5/market/mark-price-kline" {
				price = strconv.FormatInt(20+(at/3600000)%10, 10)
			}
			row := []string{strconv.FormatInt(at, 10), price, price, price, price}
			if r.URL.Path == "/v5/market/kline" {
				row = append(row, "1", "10")
			}
			rows = append(rows, row)
		}
		result["list"] = rows
	case "/v5/market/funding/history":
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
		step := int64((8 * time.Hour) / time.Millisecond)
		for at := end / step * step; at >= start && len(rows) < 200; at -= step {
			rows = append(rows, map[string]string{"symbol": q.Get("symbol"), "fundingRate": "0.001", "fundingRateTimestamp": strconv.FormatInt(at, 10)})
		}
		result["list"] = rows
	default:
		return nil, errors.New("unexpected endpoint")
	}
	if f.edit != nil {
		f.edit(r, result)
	}
	raw, err := json.Marshal(map[string]any{"retCode": 0, "retMsg": "OK", "time": f.now.UnixMilli(), "result": result})
	if f.after != nil {
		f.after()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, err
}

// sourceFixture задаёт явные часы2025, configured N и финансовые параметры.
func sourceFixture(t *testing.T, symbols string) (SourceRequest, *publicFake) {
	t.Helper()
	if symbols == "ALT123USDT" {
		symbols += ", BTCUSDT, ETHUSDT"
	}
	cfg, err := config.Parse([]byte(fmt.Sprintf(`schema_version: 1
strategy_id: momentum-v1
profile_version: v1
model_id: roc30
mode: paper
start_policy: now
symbols: [%s]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
data_source:
  kind: bybit_testnet_public
  environment: testnet
`, symbols)))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 3, 15, 16, 30, 0, 0, time.UTC)
	f := &publicFake{now: at}
	one, _ := model.ParseAmount("1")
	p := paperwire.Profile{Parameters: backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Hour, Rules: make(map[string]planner.MarketDeltaRules)}, SourceMaxAge: time.Minute, DataDeadline: time.Hour, Poll: time.Second, RPCTimeout: time.Second, ExecutionReference: "closed-hourly-trade-close/v1", FundingSchedules: make(map[string]paperwire.FundingSchedule)}
	for _, s := range cfg.Values().Symbols {
		p.Parameters.Rules[s] = planner.MarketDeltaRules{QtyStep: one, MinQty: one, MaxMarketQty: one, MinIncreaseNotional: model.Zero()}
		p.FundingSchedules[s] = paperwire.FundingSchedule{Anchor: at.Truncate(24 * time.Hour), Interval: 8 * time.Hour}
	}
	return SourceRequest{Run: paperwire.RunBinding{RunID: "run", AccountID: "paper-account", StrategyID: "momentum-v1", OnboardingID: "onboarding", ScheduledAt: at, Config: cfg}, Purpose: paperwire.Purpose{Kind: "rebalance"}, Profile: p, DailyBars: 31, FundingAfter: at.Add(-24 * time.Hour), Now: func() time.Time { return f.now }, Transport: f}, f
}

// TestPublicObservation проверяет полные страницы, generic N, часы и отдельные проекции.
func TestPublicObservation(t *testing.T) {
	t.Setenv("BYBIT_API_KEY", "poison")
	t.Setenv("BYBIT_API_SECRET", "poison")
	for _, symbols := range []string{"ALT123USDT", "BTCUSDT, ETHUSDT, HYPEUSDT"} {
		r, f := sourceFixture(t, symbols)
		o, err := ReadCycleObservation(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		r.Transport = nil
		m, err := ValidateCycleObservation(r, o)
		if err != nil {
			t.Fatal(err)
		}
		n := len(r.Run.Config.Values().Symbols)
		if len(m.DailySeries) != n || len(m.Funding) != n*3 || len(o.TradeWindows) != n*2 || len(o.MarkPages) != n*3 || len(o.FundingPages) != n*2 {
			t.Fatalf("missing pages/projections: %+v", o)
		}
		for s, rows := range m.DailySeries {
			if len(rows) != 31 || !rows[0].OpenTime.Before(rows[30].OpenTime) {
				t.Fatal("daily ordering/count")
			}
			c, _ := m.ReferencePrices[s].Cmp(m.Marks[s].Price)
			if c == 0 {
				t.Fatal("trade used as mark")
			}
			rows[0].Symbol = "mutated"
			if o.TradeWindows[0].Page.Candles[0].Symbol == "mutated" {
				t.Fatal("aliased rows")
			}
		}
		if f.calls == 0 {
			t.Fatal("fixture made no calls")
		}
	}
}

// TestPublicReadiness проверяет фактические строки всех символов и общий boundary.
func TestPublicReadiness(t *testing.T) {
	r, f := sourceFixture(t, "BTCUSDT, ETHUSDT, HYPEUSDT")
	readiness := PublicReadiness{Config: r.Run.Config, DailyBars: 31, RPCTimeout: time.Second, SourceMaxAge: time.Minute, Now: r.Now, Transport: f}
	got, err := readiness.ClosedThrough(context.Background())
	if err != nil || !got.Equal(f.now.Truncate(24*time.Hour)) || f.calls != 4 {
		t.Fatal(got, err, f.calls)
	}
	f.edit = func(q *http.Request, result map[string]any) {
		if q.URL.Query().Get("symbol") == "HYPEUSDT" {
			result["list"] = []any{}
		}
	}
	if _, err = readiness.ClosedThrough(context.Background()); !errors.Is(err, ErrPaperObservation) {
		t.Fatal("missing HYPE accepted", err)
	}
	readiness.Transport = nil
	if _, err = readiness.ClosedThrough(context.Background()); err == nil {
		t.Fatal("nil transport")
	}
}

// TestSourceDeadlines проверяет логическую и parent отмену без host-time dependency.
func TestSourceDeadlines(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	f.now = r.Run.ScheduledAt.Add(r.Profile.DataDeadline)
	if _, err := ReadCycleObservation(context.Background(), r); !errors.Is(err, context.DeadlineExceeded) || f.calls != 0 {
		t.Fatal(err, f.calls)
	}
	f.now = r.Run.ScheduledAt
	f.after = func() { f.now = r.Run.ScheduledAt.Add(r.Profile.DataDeadline) }
	if _, err := ReadCycleObservation(context.Background(), r); !errors.Is(err, context.DeadlineExceeded) || f.calls != 1 {
		t.Fatal(err, f.calls)
	}
	f.now, f.after = r.Run.ScheduledAt, nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadCycleObservation(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	f.after = cancel
	if _, err := ReadCycleObservation(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestSourcePreflight проверяет fail-closed до первого RPC и большого массива.
func TestSourcePreflight(t *testing.T) {
	for _, kind := range []string{"nil-clock", "nil-transport", "bars", "purpose", "sub-ms", "anchor", "tiny", "large-gap"} {
		t.Run(kind, func(t *testing.T) {
			r, f := sourceFixture(t, "ALT123USDT")
			s := r.Profile.FundingSchedules["ALT123USDT"]
			switch kind {
			case "nil-clock":
				r.Now = nil
			case "nil-transport":
				r.Transport = nil
			case "bars":
				r.DailyBars = 201
			case "purpose":
				r.Purpose.Kind = "live"
			case "sub-ms":
				s.Interval = time.Nanosecond
			case "anchor":
				s.Anchor = s.Anchor.Add(time.Nanosecond)
			case "tiny":
				s.Interval = time.Millisecond
			case "large-gap":
				s.Interval = time.Millisecond
				r.FundingAfter = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			r.Profile.FundingSchedules["ALT123USDT"] = s
			_, err := ReadCycleObservation(context.Background(), r)
			if err == nil || f.calls != 0 {
				t.Fatal(err, f.calls)
			}
			if (kind == "tiny" || kind == "large-gap") && !errors.Is(err, paperwire.ErrPaperResourceLimit) {
				t.Fatal(err)
			}
		})
	}
}

// TestFundingZeroAndClose проверяет отсутствие signals при close-all и empty expected.
func TestFundingZeroAndClose(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	r.FundingAfter = r.Run.ScheduledAt
	r.DailyBars = 0
	r.Purpose = paperwire.Purpose{Kind: "close-all", CommandID: "close", ControlRevision: 1}
	o, err := ReadCycleObservation(context.Background(), r)
	if err != nil || len(o.FundingPages) != 0 || len(o.MarkPages) != 3 || len(o.TradeWindows) != 3 || f.calls != 7 {
		t.Fatal(err, f.calls)
	}
	r.FundingAfter = r.Run.ScheduledAt.Add(-time.Minute)
	o, err = ReadCycleObservation(context.Background(), r)
	if err != nil || len(o.FundingPages) != 3 || len(o.FundingPages[0].Events) != 0 {
		t.Fatal(err)
	}
}

// TestHTTPFundingNormalization проверяет существующее упорядочение адаптера без потери values.
func TestHTTPFundingNormalization(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	f.edit = func(q *http.Request, result map[string]any) {
		if q.URL.Path == "/v5/market/funding/history" {
			rows := result["list"].([]any)
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	o, err := ReadCycleObservation(context.Background(), r)
	if err != nil || len(o.FundingPages[0].Events) != 3 || !o.FundingPages[0].Events[0].FundingTime.After(o.FundingPages[0].Events[1].FundingTime) {
		t.Fatal(err)
	}
}
