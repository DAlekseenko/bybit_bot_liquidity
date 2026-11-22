package paperwire

import (
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestObservationBytesBoundary проверяет exact bytes cap и большой ASCII без /6 heuristic.
func TestObservationBytesBoundary(t *testing.T) {
	o := observationFixture(t)
	m, err := MeasureObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	o.FundingPages[0].Source = strings.Repeat("a", MaxCanonicalBytes-m.Bytes+len(o.FundingPages[0].Source))
	exact, err := MeasureObservation(o)
	if err != nil || exact.Bytes != MaxCanonicalBytes {
		t.Fatal(exact, err)
	}
	raw, err := EncodeObservation(o)
	if err != nil || len(raw) != MaxCanonicalBytes {
		t.Fatal(len(raw), err)
	}
	if _, err := DecodeObservation(raw); err != nil {
		t.Fatal(err)
	}
	o.FundingPages[0].Source += "a"
	if _, err := EncodeObservation(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	if _, err := DecodeObservation(append(raw, ' ')); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	o.FundingPages[0].Source = strings.Repeat("<", MaxCanonicalBytes/6)
	if _, err := EncodeObservation(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("escape", err)
	}
}

// TestObservationCollectionBoundaries проверяет counts независимо от более строгого token cap.
func TestObservationCollectionBoundaries(t *testing.T) {
	for _, n := range []int{MaxObservationPages, MaxObservationPages + 1} {
		o := Observation{TradeWindows: make([]TradeWindow, n)}
		err := observationCounts(o)
		if (err == nil) != (n == MaxObservationPages) {
			t.Fatal("pages", n, err)
		}
		raw := []byte(`{"CapturedAt":null,"SourceClock":null,"DailyBars":0,"TradeWindows":[` + strings.TrimSuffix(strings.Repeat("null,", n), ",") + `],"MarkPages":null,"FundingPages":null}`)
		err = observationStreamCounts(raw)
		if (err == nil) != (n == MaxObservationPages) {
			t.Fatal("stream pages", n, err)
		}
	}
	for _, kind := range []string{"trade", "mark", "funding"} {
		for _, n := range []int{MaxObservationRows, MaxObservationRows + 1} {
			var o Observation
			var path string
			switch kind {
			case "trade":
				o.TradeWindows = []TradeWindow{{Page: model.CandlePage{Candles: make([]model.Candle, n)}}}
				path = `{"CapturedAt":null,"SourceClock":null,"DailyBars":0,"TradeWindows":[{"Symbol":"","Interval":"","StartMS":0,"EndMS":0,"Page":{"Environment":"","Source":"","Category":"","Endpoint":"","ServerTime":null,"Candles":[`
			case "mark":
				o.MarkPages = []model.MarkPricePage{{Candles: make([]model.MarkPriceCandle, n)}}
				path = `{"CapturedAt":null,"SourceClock":null,"DailyBars":0,"TradeWindows":null,"MarkPages":[{"Environment":"","Source":"","Category":"","Endpoint":"","Symbol":"","IntervalCode":"","StartTime":null,"EndTime":null,"ServerTime":null,"Candles":[`
			case "funding":
				o.FundingPages = []model.FundingPage{{Events: make([]model.FundingEvent, n)}}
				path = `{"CapturedAt":null,"SourceClock":null,"DailyBars":0,"TradeWindows":null,"MarkPages":null,"FundingPages":[{"Environment":"","Source":"","Category":"","Endpoint":"","Symbol":"","StartTime":null,"EndTime":null,"ServerTime":null,"Events":[`
			}
			err := observationCounts(o)
			if (err == nil) != (n == MaxObservationRows) {
				t.Fatal("rows", kind, n, err)
			}
			ending := `]}]}`
			if kind == "mark" {
				ending = `]}],"FundingPages":null}`
			}
			if kind == "trade" {
				ending = `]}}],"MarkPages":null,"FundingPages":null}`
			}
			raw := []byte(path + strings.TrimSuffix(strings.Repeat("null,", n), ",") + ending)
			err = observationStreamCounts(raw)
			if (err == nil) != (n == MaxObservationRows) {
				t.Fatal("stream rows", kind, n, err)
			}
		}
	}
	// Сумма всех streams учитывается совместно, а не отдельно.
	o := Observation{TradeWindows: make([]TradeWindow, MaxObservationPages), MarkPages: []model.MarkPricePage{{}}}
	if err := observationCounts(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	o = Observation{MarkPages: []model.MarkPricePage{{Candles: make([]model.MarkPriceCandle, MaxObservationRows)}}, FundingPages: []model.FundingPage{{Events: []model.FundingEvent{{}}}}}
	if err := observationCounts(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
}

// TestObservationGlobalBoundaries проверяет exact caps decoder guard и visitor primitives.
func TestObservationGlobalBoundaries(t *testing.T) {
	for _, n := range []int{MaxArrayItems, MaxArrayItems + 1} {
		raw := []byte("[" + strings.TrimSuffix(strings.Repeat("null,", n), ",") + "]")
		err := preflight(raw)
		if (err == nil) != (n == MaxArrayItems) {
			t.Fatal("array", n, err)
		}
		c := observationCounter{}
		c.value(make([]model.FundingEvent, n))
		if n > MaxArrayItems && !errors.Is(c.err, ErrPaperResourceLimit) {
			t.Fatal("visitor array", c.err)
		}
	}
	for _, depth := range []int{MaxJSONDepth, MaxJSONDepth + 1} {
		raw := []byte(strings.Repeat("[", depth) + strings.Repeat("]", depth))
		err := preflight(raw)
		if (err == nil) != (depth == MaxJSONDepth) {
			t.Fatal("depth", depth, err)
		}
		c := observationCounter{}
		for i := 0; i < depth; i++ {
			c.open()
		}
		for i := 0; i < depth; i++ {
			c.close()
		}
		if (c.err == nil) != (depth == MaxJSONDepth) {
			t.Fatal("visitor depth", c.err)
		}
	}
	// 4 arrays с 65533 scalars дают ровно 262144 tokens и соблюдают array cap.
	chunk := "[" + strings.TrimSuffix(strings.Repeat("null,", 65533), ",") + "]"
	raw := "[" + strings.Join([]string{chunk, chunk, chunk, chunk}, ",") + ",null,null]"
	if m := observationActualMeasure(t, []byte(raw)); m.Tokens != MaxJSONTokens {
		t.Fatal(m)
	}
	if err := preflight([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	raw = strings.TrimSuffix(raw, "]") + ",null]"
	if err := preflight([]byte(raw)); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("tokens+1", err)
	}
	c := observationCounter{}
	for i := 0; i < MaxJSONTokens; i++ {
		c.token()
	}
	if c.err != nil {
		t.Fatal(c.err)
	}
	c.token()
	if !errors.Is(c.err, ErrPaperResourceLimit) {
		t.Fatal(c.err)
	}
	c = observationCounter{}
	c.add(MaxCanonicalBytes)
	if c.err != nil {
		t.Fatal(c.err)
	}
	c.add(1)
	if !errors.Is(c.err, ErrPaperResourceLimit) {
		t.Fatal(c.err)
	}
}

// TestObservationBudgetBeforeValues не обходит invalid values после length overflow.
func TestObservationBudgetBeforeValues(t *testing.T) {
	o := Observation{CapturedAt: time.Time{}, TradeWindows: make([]TradeWindow, MaxObservationPages+1)}
	if _, err := MeasureObservation(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	o = observationFixture(t)
	o.MarkPages[0].Candles = make([]model.MarkPriceCandle, MaxObservationRows+1)
	if _, err := MeasureObservation(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	// Скромный typed DTO с многими rows отвергается по tokens до wire allocation.
	o = observationFixture(t)
	event := o.FundingPages[0].Events[0]
	o.FundingPages[0].Events = make([]model.FundingEvent, MaxObservationRows)
	for i := range o.FundingPages[0].Events {
		o.FundingPages[0].Events[i] = event
	}
	if _, err := EncodeObservation(o); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal(err)
	}
}
