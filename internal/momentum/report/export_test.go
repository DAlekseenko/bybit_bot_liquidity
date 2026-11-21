package report

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestFillsCSVStable проверяет сортировку, точный формат и повторяемость.
func TestFillsCSVStable(t *testing.T) {
	fills := []model.ConfirmedFill{
		{ExecutionID: "b", Symbol: "ETHUSDT", Side: model.FillSell, ExecutedAt: day0.Add(time.Hour),
			Quantity: dec(t, "0.5"), ExecutionPrice: dec(t, "3000"), FeeUSDT: dec(t, "0.9")},
		{ExecutionID: "a", Symbol: "BTCUSDT", Side: model.FillBuy, ExecutedAt: day0,
			Quantity: dec(t, "0.010"), ExecutionPrice: dec(t, "90000.50"), FeeUSDT: dec(t, "0.54")},
	}
	got, err := FillsCSV(fills)
	if err != nil {
		t.Fatal(err)
	}
	want := "execution_id,symbol,side,executed_at,quantity,execution_price,fee_usdt\n" +
		"a,BTCUSDT,BUY,2026-01-05T00:00:00Z,0.01,90000.5,0.54\n" +
		"b,ETHUSDT,SELL,2026-01-05T01:00:00Z,0.5,3000,0.9\n"
	if string(got) != want {
		t.Fatalf("csv:\n%s", got)
	}
	again, _ := FillsCSV([]model.ConfirmedFill{fills[1], fills[0]})
	if !bytes.Equal(got, again) {
		t.Fatal("csv depends on input order")
	}
}

// TestEquityCSV проверяет колонку, скорректированную на переводы.
func TestEquityCSV(t *testing.T) {
	got, err := EquityCSV(series(t, 24*time.Hour, [2]string{"100", "0"}, [2]string{"150", "60"}))
	if err != nil {
		t.Fatal(err)
	}
	want := "at,equity_usdt,cumulative_transfers_usdt,transfer_adjusted_equity_usdt\n" +
		"2026-01-05T00:00:00Z,100,0,100\n2026-01-06T00:00:00Z,150,60,90\n"
	if string(got) != want {
		t.Fatalf("csv:\n%s", got)
	}
}

// TestSummaryJSONClosedFields проверяет закрытый список полей, повторяемость и unavailable.
func TestSummaryJSONClosedFields(t *testing.T) {
	s, err := BuildSummary(summaryInput(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := SummaryJSON(s)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := SummaryJSON(s)
	if !bytes.Equal(b, again) {
		t.Fatal("json is not deterministic")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := "config_hash,coverage,daily_drawdown,dataset_hash,end,entry_definition,entry_fill_count," +
		"entry_intent_count,equity_identity,fill_count,final_equity_usdt,hourly_drawdown,initial_equity_usdt," +
		"net_return,profile_id,run_id,scenario,start,strategy_id,totals,transfer_adjusted_daily_drawdown,transfer_adjusted_return"
	if strings.Join(keys, ",") != want {
		t.Fatalf("keys: %s", strings.Join(keys, ","))
	}
	text := string(b)
	for _, banned := range []string{"account", "api_key", "secret", "signature"} {
		if strings.Contains(strings.ToLower(text), banned) {
			t.Fatalf("json contains %q", banned)
		}
	}
	if !strings.Contains(text, `"hourly_drawdown": {`+"\n"+`    "available": false,`+"\n"+`    "reason": "no observations"`) {
		t.Fatalf("hourly unavailable not visible:\n%s", text)
	}
}

// TestRatioJSONTruncation проверяет точную пару и усечение к нулю для обоих знаков.
func TestRatioJSONTruncation(t *testing.T) {
	third, _ := dec(t, "1").Quo(dec(t, "3"))
	neg, _ := model.Zero().Sub(third)
	for d, want := range map[*model.Decimal]jsonRatio{
		&third: {Num: "1", Den: "3", Truncated8: "0.33333333"},
		&neg:   {Num: "-1", Den: "3", Truncated8: "-0.33333333"},
	} {
		got, err := ratioJSON(*d)
		if err != nil || got != want {
			t.Fatalf("ratio: %+v %v", got, err)
		}
	}
}
