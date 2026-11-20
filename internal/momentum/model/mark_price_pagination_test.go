package model

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// markPaginationFixture создаёт точные закрытые строки с недочитанным нижним префиксом.
func markPaginationFixture(t *testing.T, interval string, count int) (MarkPricePageRequest, MarkPricePage) {
	t.Helper()
	step := time.Hour
	if interval == "D" {
		step = 24 * time.Hour
	}
	start := time.UnixMilli(step.Milliseconds()).UTC()
	end := start.Add(time.Duration(count+2)*step - time.Millisecond)
	request := MarkPricePageRequest{"testnet", "BTCUSDT", interval, start, end}
	page := MarkPricePage{"testnet", "bybit_v5", "linear", "https://api-testnet.bybit.com/v5/market/mark-price-kline", "BTCUSDT", interval, start, end, end.Add(time.Millisecond), make([]MarkPriceCandle, count)}
	price, err := ParsePrice("9007199254740993.00000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	for i := range page.Candles {
		page.Candles[i] = MarkPriceCandle{start.Add(time.Duration(count+1-i) * step), price, price, price, price, true}
	}
	return request, page
}

// TestNextMarkPricePage проверяет bounded исходы без предположений о полноте источника.
func TestNextMarkPricePage(t *testing.T) {
	for _, interval := range []string{"D", "60"} {
		for _, tc := range []struct {
			name   string
			count  int
			want   MarkPricePageAction
			change func(*MarkPricePageRequest, *MarkPricePage)
		}{
			{"short", 1, MarkPricePageContinue, nil}, {"full", 200, MarkPricePageContinue, nil},
			{"empty", 0, MarkPricePageEmpty, nil},
			{"lower", 2, MarkPricePageLowerReached, func(r *MarkPricePageRequest, p *MarkPricePage) {
				r.Start = p.Candles[1].OpenTime
				p.StartTime = r.Start
			}},
			{"missing-newest", 2, MarkPricePageGap, func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles = p.Candles[1:] }},
			{"internal-gap", 3, MarkPricePageGap, func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles = slices.Delete(p.Candles, 1, 2) }},
			{"unconfirmed", 2, MarkPricePageUnconfirmed, func(_ *MarkPricePageRequest, p *MarkPricePage) {
				p.ServerTime = p.ServerTime.Add(-time.Millisecond)
				p.Candles[0].Confirmed = false
			}},
			{"empty-not-closed", 0, MarkPricePageUnconfirmed, func(_ *MarkPricePageRequest, p *MarkPricePage) { p.ServerTime = p.ServerTime.Add(-time.Millisecond) }},
			{"mainnet", 2, MarkPricePageContinue, func(r *MarkPricePageRequest, p *MarkPricePage) {
				r.Environment, p.Environment = "mainnet", "mainnet"
				p.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
			}},
		} {
			t.Run(interval+"/"+tc.name, func(t *testing.T) {
				request, page := markPaginationFixture(t, interval, tc.count)
				if tc.change != nil {
					tc.change(&request, &page)
				}
				beforeRequest, beforePage := request, page
				beforePage.Candles = slices.Clone(page.Candles)
				got, err := NextMarkPricePage(request, page)
				if err != nil || got.Action != tc.want || !reflect.DeepEqual(request, beforeRequest) || !reflect.DeepEqual(page, beforePage) {
					t.Fatalf("decision=%+v want=%s error=%v", got, tc.want, err)
				}
				if tc.want == MarkPricePageContinue {
					want := page.Candles[len(page.Candles)-1].OpenTime.Add(-time.Millisecond).UTC()
					if got.NextEnd != want || !got.NextEnd.Before(request.End) || got.NextEnd.Before(request.Start) {
						t.Fatalf("cursor=%v want=%v", got.NextEnd, want)
					}
				} else if !got.NextEnd.IsZero() {
					t.Fatal("terminal cursor", got)
				}
			})
		}
	}
}

// TestNextMarkPricePageBoundaries проверяет две страницы, UTC и отсутствие ложного metadata cap.
func TestNextMarkPricePageBoundaries(t *testing.T) {
	request, page := markPaginationFixture(t, "60", 2)
	first, err := NextMarkPricePage(request, page)
	if err != nil {
		t.Fatal(err)
	}
	request.End, page.EndTime = first.NextEnd, first.NextEnd
	page.Candles[0].OpenTime, page.Candles[1].OpenTime = request.Start.Add(time.Hour), request.Start
	zone := time.FixedZone("east", 3*3600)
	request.Start, request.End = request.Start.In(zone), request.End.In(zone)
	page.StartTime, page.EndTime, page.ServerTime = page.StartTime.In(zone), page.EndTime.In(zone), page.ServerTime.In(zone)
	got, err := NextMarkPricePage(request, page)
	if err != nil || got.Action != MarkPricePageLowerReached || !got.NextEnd.IsZero() {
		t.Fatalf("second page=%+v %v", got, err)
	}
	page.Candles[0].OpenTime = request.End.Add(time.Millisecond)
	if got, err := NextMarkPricePage(request, page); !errors.Is(err, ErrMarkPricePagination) || got != (MarkPricePageDecision{}) {
		t.Fatalf("repeated prior boundary=%+v %v", got, err)
	}
	for _, year := range []int{2300, 3000, 294276} {
		request, page = markPaginationFixture(t, "D", 1)
		request.Start = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		request.End, page.StartTime, page.EndTime = request.Start, request.Start, request.Start
		page.ServerTime, page.Candles[0].OpenTime = request.Start.Add(24*time.Hour), request.Start
		got, err = NextMarkPricePage(request, page)
		if err != nil || got.Action != MarkPricePageLowerReached {
			t.Fatalf("year=%d decision=%+v %v", year, got, err)
		}
	}
	request, page = markPaginationFixture(t, "D", 0)
	request.Symbol, page.Symbol = "123USDT", "123USDT"
	request.End, page.EndTime = time.UnixMilli(1<<63-1), time.UnixMilli(1<<63-1)
	page.ServerTime = request.End
	if got, err = NextMarkPricePage(request, page); err != nil || got.Action != MarkPricePageUnconfirmed {
		t.Fatalf("max representable inclusive end=%+v %v", got, err)
	}
	request, page = markPaginationFixture(t, "D", 1)
	page.Candles[0].Low, _ = ParsePrice("0." + strings.Repeat("0", 63) + "1")
	if got, err := NextMarkPricePage(request, page); err != nil || got.Action != MarkPricePageContinue {
		t.Fatalf("64 fraction digits=%+v %v", got, err)
	}
	request, page = markPaginationFixture(t, "D", 0)
	page.ServerTime = time.UnixMilli(1<<63 - 1)
	if got, err := NextMarkPricePage(request, page); err != nil || got.Action != MarkPricePageEmpty {
		t.Fatalf("representable metadata=%+v %v", got, err)
	}
}

// TestNextMarkPricePageRejects проверяет целый ответ, identity, сроки, OHLC и отсутствие мутаций.
func TestNextMarkPricePageRejects(t *testing.T) {
	for name, change := range map[string]func(*MarkPricePageRequest, *MarkPricePage){
		"environment": func(r *MarkPricePageRequest, _ *MarkPricePage) { r.Environment = "paper" },
		"symbol":      func(r *MarkPricePageRequest, _ *MarkPricePage) { r.Symbol = "btcusdt" },
		"interval":    func(r *MarkPricePageRequest, _ *MarkPricePage) { r.IntervalCode = "1" },
		"epoch": func(r *MarkPricePageRequest, p *MarkPricePage) {
			r.Start, p.StartTime = time.UnixMilli(0), time.UnixMilli(0)
		},
		"start-grid": func(r *MarkPricePageRequest, p *MarkPricePage) {
			r.Start = r.Start.Add(time.Millisecond)
			p.StartTime = r.Start
		},
		"start-ns": func(r *MarkPricePageRequest, _ *MarkPricePage) { r.Start = r.Start.Add(time.Nanosecond) },
		"end-ns":   func(r *MarkPricePageRequest, _ *MarkPricePage) { r.End = r.End.Add(time.Nanosecond) },
		"reverse": func(r *MarkPricePageRequest, p *MarkPricePage) {
			r.End, p.EndTime = r.Start.Add(-time.Millisecond), r.Start.Add(-time.Millisecond)
		},
		"page-env":    func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Environment = "mainnet" },
		"page-symbol": func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Symbol = "ETHUSDT" },
		"page-step":   func(_ *MarkPricePageRequest, p *MarkPricePage) { p.IntervalCode = "60" },
		"source":      func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Source = "secret-value" },
		"category":    func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Category = "spot" },
		"endpoint":    func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Endpoint += "?bad=1" },
		"trade": func(_ *MarkPricePageRequest, p *MarkPricePage) {
			p.Endpoint = "https://api-testnet.bybit.com/v5/market/kline"
		},
		"bounds":      func(_ *MarkPricePageRequest, p *MarkPricePage) { p.EndTime = p.EndTime.Add(time.Millisecond) },
		"server-zero": func(_ *MarkPricePageRequest, p *MarkPricePage) { p.ServerTime = time.Time{} },
		"server-ns":   func(_ *MarkPricePageRequest, p *MarkPricePage) { p.ServerTime = p.ServerTime.Add(time.Nanosecond) },
		"overflow": func(_ *MarkPricePageRequest, p *MarkPricePage) {
			p.ServerTime = time.UnixMilli(1<<63 - 1).Add(time.Millisecond)
		},
		"nil": func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles = nil },
		"201": func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles = make([]MarkPriceCandle, 201) },
		"row-grid": func(_ *MarkPricePageRequest, p *MarkPricePage) {
			p.Candles[1].OpenTime = p.Candles[1].OpenTime.Add(time.Hour)
		},
		"before": func(r *MarkPricePageRequest, p *MarkPricePage) { p.Candles[1].OpenTime = r.Start.Add(-24 * time.Hour) },
		"after":  func(r *MarkPricePageRequest, p *MarkPricePage) { p.Candles[0].OpenTime = r.End.Add(time.Millisecond) },
		"future": func(_ *MarkPricePageRequest, p *MarkPricePage) {
			p.ServerTime = p.Candles[0].OpenTime.Add(-time.Millisecond)
		},
		"duplicate": func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles[1] = p.Candles[0] },
		"ascending": func(_ *MarkPricePageRequest, p *MarkPricePage) { slices.Reverse(p.Candles) },
		"closure":   func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles[0].Confirmed = false },
		"bad-high":  func(_ *MarkPricePageRequest, p *MarkPricePage) { p.Candles[1].High, _ = ParsePrice("1") },
		"bad-low": func(_ *MarkPricePageRequest, p *MarkPricePage) {
			p.Candles[1].Low, _ = ParsePrice("99999999999999999999")
		},
		"pg-row": func(r *MarkPricePageRequest, p *MarkPricePage) {
			r.Start = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC)
			r.End, p.StartTime, p.EndTime, p.Candles[0].OpenTime = r.Start, r.Start, r.Start, r.Start
			p.Candles, p.ServerTime = p.Candles[:1], r.Start.Add(24*time.Hour)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request, page := markPaginationFixture(t, "D", 2)
			change(&request, &page)
			before := page
			before.Candles = slices.Clone(page.Candles)
			got, err := NextMarkPricePage(request, page)
			if !errors.Is(err, ErrMarkPricePagination) || got != (MarkPricePageDecision{}) || strings.Contains(err.Error(), "secret-value") || !reflect.DeepEqual(page, before) {
				t.Fatalf("invalid result=%+v error=%v", got, err)
			}
		})
	}
	one, _ := ParsePrice("1")
	three, _ := ParsePrice("3")
	fraction, _ := one.Quo(three)
	negative, _ := ParseAmount("-1")
	denominator, _ := ParseAmount("1" + strings.Repeat("0", 65))
	overscale, _ := one.Quo(denominator)
	for field := range 4 {
		for _, bad := range []Decimal{{}, Zero(), negative, fraction, overscale} {
			request, page := markPaginationFixture(t, "D", 2)
			row := &page.Candles[1]
			*([]*Decimal{&row.Open, &row.High, &row.Low, &row.Close}[field]) = bad
			if got, err := NextMarkPricePage(request, page); !errors.Is(err, ErrMarkPricePagination) || got != (MarkPricePageDecision{}) {
				t.Fatalf("field=%d invalid result=%+v %v", field, got, err)
			}
		}
	}
}
