package paper

import (
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

type observationCounter struct {
	bytes int
	err   error
}

// add насыщает счётчик до overflow или превышения нормативного cap.
func (c *observationCounter) add(n int) {
	if c.err == nil {
		if n < 0 || n > paperwire.MaxCanonicalBytes-c.bytes {
			c.err = paperwire.ErrPaperResourceLimit
		} else {
			c.bytes += n
		}
	}
}

// text считает ровно encoding/json escaping без выделения encoded строки.
func (c *observationCounter) text(s string) {
	c.add(2)
	if len(s) > paperwire.MaxCanonicalBytes {
		c.err = paperwire.ErrPaperResourceLimit
		return
	}
	for len(s) > 0 && c.err == nil {
		r, n := utf8.DecodeRuneInString(s)
		size := n
		switch {
		case r == utf8.RuneError && n == 1, r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
			size = 6
		case r == '"' || r == '\\' || r == '\b' || r == '\f' || r == '\n' || r == '\r' || r == '\t':
			size = 2
		case r < 0x20:
			size = 6
		}
		c.add(size)
		s = s[n:]
	}
}

// value обходит только фиксированные DTO источника с явными time/Decimal handlers.
func (c *observationCounter) value(value any) {
	if c.err != nil {
		return
	}
	switch v := value.(type) {
	case time.Time:
		c.text(v.UTC().Format(time.RFC3339Nano))
	case model.Decimal:
		n, d, err := v.CanonicalRatio()
		if err != nil {
			c.err = ErrPaperObservation
			return
		}
		c.add(len(`{"numerator":,"denominator":}`))
		c.text(n)
		c.text(d)
	case string:
		c.text(v)
	case int:
		c.add(len(strconv.Itoa(v)))
	case int64:
		c.add(len(strconv.FormatInt(v, 10)))
	case bool:
		if v {
			c.add(4)
		} else {
			c.add(5)
		}
	case paperwire.TradeWindow, model.CandlePage, model.Candle, model.MarkPricePage, model.MarkPriceCandle, model.FundingPage, model.FundingEvent:
		object := reflect.ValueOf(v)
		c.add(2)
		for i := 0; i < object.NumField(); i++ {
			if i > 0 {
				c.add(1)
			}
			c.text(object.Type().Field(i).Name)
			c.add(1)
			c.value(object.Field(i).Interface())
		}
	case []model.Candle, []model.MarkPriceCandle, []model.FundingEvent:
		rows := reflect.ValueOf(v)
		if rows.IsNil() {
			c.add(4)
			return
		}
		c.add(2)
		for i := 0; i < rows.Len(); i++ {
			if i > 0 {
				c.add(1)
			}
			c.value(rows.Index(i).Interface())
		}
	default:
		c.err = ErrPaperObservation
	}
}

// observation считает виртуальное объединение страниц до append без копирования rows.
func (c *observationCounter) observation(observations []paperwire.Observation) {
	first := reflect.ValueOf(observations[0])
	c.add(2)
	for field := 0; field < first.NumField(); field++ {
		if field > 0 {
			c.add(1)
		}
		c.text(first.Type().Field(field).Name)
		c.add(1)
		if field < 3 {
			c.value(first.Field(field).Interface())
			continue
		}
		nilSlice, length := true, 0
		for _, o := range observations {
			rows := reflect.ValueOf(o).Field(field)
			nilSlice = nilSlice && rows.IsNil()
			length += rows.Len()
		}
		if nilSlice {
			c.add(4)
			continue
		}
		c.add(2)
		if length > 0 {
			c.add(length - 1)
		}
		for _, o := range observations {
			rows := reflect.ValueOf(o).Field(field)
			for i := 0; i < rows.Len() && c.err == nil; i++ {
				c.value(rows.Index(i).Interface())
			}
		}
	}
}

// observationBudget проверяет counts до точного byte measurement и любых проекций.
func observationBudget(observations ...paperwire.Observation) error {
	pages, rows := 0, 0
	for _, o := range observations {
		p := len(o.TradeWindows) + len(o.MarkPages) + len(o.FundingPages)
		if p > paperwire.MaxObservationPages-pages {
			return paperwire.ErrPaperResourceLimit
		}
		pages += p
		for _, w := range o.TradeWindows {
			if len(w.Page.Candles) > paperwire.MaxObservationRows-rows {
				return paperwire.ErrPaperResourceLimit
			}
			rows += len(w.Page.Candles)
		}
		for _, p := range o.MarkPages {
			if len(p.Candles) > paperwire.MaxObservationRows-rows {
				return paperwire.ErrPaperResourceLimit
			}
			rows += len(p.Candles)
		}
		for _, p := range o.FundingPages {
			if len(p.Events) > paperwire.MaxObservationRows-rows {
				return paperwire.ErrPaperResourceLimit
			}
			rows += len(p.Events)
		}
	}
	c := observationCounter{}
	c.observation(observations)
	return c.err
}
