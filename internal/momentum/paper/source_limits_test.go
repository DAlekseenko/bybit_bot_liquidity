package paper

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// canonicalTestValue независимо строит только небольшую fixture нормативного JSON.
func canonicalTestValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	var result []byte
	switch v := value.(type) {
	case time.Time:
		result, _ = json.Marshal(v.UTC().Format(time.RFC3339Nano))
	case model.Decimal:
		n, d, err := v.CanonicalRatio()
		if err != nil {
			t.Fatal(err)
		}
		result, _ = json.Marshal(struct {
			Numerator   string `json:"numerator"`
			Denominator string `json:"denominator"`
		}{n, d})
	default:
		rv := reflect.ValueOf(value)
		switch rv.Kind() {
		case reflect.Struct:
			result = []byte("{")
			for i := 0; i < rv.NumField(); i++ {
				if i > 0 {
					result = append(result, ',')
				}
				key, _ := json.Marshal(rv.Type().Field(i).Name)
				result = append(result, key...)
				result = append(result, ':')
				result = append(result, canonicalTestValue(t, rv.Field(i).Interface())...)
			}
			result = append(result, '}')
		case reflect.Slice:
			if rv.IsNil() {
				return json.RawMessage("null")
			}
			result = []byte("[")
			for i := 0; i < rv.Len(); i++ {
				if i > 0 {
					result = append(result, ',')
				}
				result = append(result, canonicalTestValue(t, rv.Index(i).Interface())...)
			}
			result = append(result, ']')
		default:
			var err error
			result, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return result
}

// TestObservationExactCounter сверяет каждый DTO и escaping с независимым JSON encoder.
func TestObservationExactCounter(t *testing.T) {
	r, _ := sourceFixture(t, "ALT123USDT")
	// Все типы и непустые страницы уже присутствуют в valid acquisition fixture.
	o, err := ReadCycleObservation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	o.FundingPages[0].Endpoint = "<&>\"\\\n\r\t\b\f\x00\x01\u2028\u2029Русский漢字🙂\xff"
	c := observationCounter{}
	c.observation([]paperwire.Observation{o})
	encoded := canonicalTestValue(t, o)
	if c.err != nil || c.bytes != len(encoded) {
		t.Fatal(c.bytes, len(encoded), c.err)
	}
	// Incremental measurement должно совпадать с реальным append без второго header.
	extra := paperwire.Observation{FundingPages: []model.FundingPage{o.FundingPages[0]}}
	c = observationCounter{}
	c.observation([]paperwire.Observation{o, extra})
	o.FundingPages = append(o.FundingPages, extra.FundingPages...)
	if encoded = canonicalTestValue(t, o); c.err != nil || c.bytes != len(encoded) {
		t.Fatal(c.bytes, len(encoded), c.err)
	}
}

// TestObservationExactByteBoundary проверяет positive exact cap и escapes без /6 policy.
func TestObservationExactByteBoundary(t *testing.T) {
	o := paperwire.Observation{FundingPages: []model.FundingPage{{}}}
	c := observationCounter{}
	c.observation([]paperwire.Observation{o})
	spare := paperwire.MaxCanonicalBytes - c.bytes
	if spare <= paperwire.MaxCanonicalBytes/6 {
		t.Fatal("fixture does not exercise old heuristic")
	}
	o.FundingPages[0].Endpoint = strings.Repeat("a", spare)
	if err := observationBudget(o); err != nil {
		t.Fatal("exact cap rejected", err)
	}
	o.FundingPages[0].Endpoint += "a"
	if err := observationBudget(o); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal("cap+1 accepted", err)
	}
	o.FundingPages[0].Endpoint = strings.Repeat("<", spare/6) + strings.Repeat("x", spare%6)
	if err := observationBudget(o); err != nil {
		t.Fatal("escaped exact cap rejected", err)
	}
	o.FundingPages[0].Endpoint += "\n"
	if err := observationBudget(o); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal("escaped excess accepted", err)
	}
}
