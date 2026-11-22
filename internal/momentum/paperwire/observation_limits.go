package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ObservationMeasure содержит точные размеры momentum/paper-observation/v1.
type ObservationMeasure struct{ Bytes, Tokens, MaxDepth int }

type observationCounter struct {
	ObservationMeasure
	depth int
	err   error
}

// observationResource сохраняет оба sentinel при превышении ресурсов.
func observationResource() error { return errors.Join(ErrPaperWire, ErrPaperResourceLimit) }

// add проверяет сложение до переполнения нормативного cap.
func (c *observationCounter) add(n int) {
	if c.err == nil {
		if n < 0 || n > MaxCanonicalBytes-c.Bytes {
			c.err = observationResource()
		} else {
			c.Bytes += n
		}
	}
}

// token учитывает каждый json.Decoder.Token, включая закрывающие delimiters.
func (c *observationCounter) token() {
	if c.Tokens == MaxJSONTokens {
		c.err = observationResource()
		return
	}
	c.Tokens++
}

// open держит container в stack до полного завершения children.
func (c *observationCounter) open() {
	c.token()
	c.add(1)
	c.depth++
	if c.depth > c.MaxDepth {
		c.MaxDepth = c.depth
	}
	if c.depth > MaxJSONDepth {
		c.err = observationResource()
	}
}

// close завершает container после всех вложенных значений.
func (c *observationCounter) close() { c.token(); c.add(1); c.depth-- }

// text считает точный encoding/json escape без выделения escaped строки.
func (c *observationCounter) text(s string) {
	c.token()
	c.add(2)
	if len(s) > MaxCanonicalBytes {
		c.err = observationResource()
		return
	}
	for len(s) > 0 && c.err == nil {
		r, n := utf8.DecodeRuneInString(s)
		size := n
		switch {
		case r == utf8.RuneError && n == 1:
			c.err = ErrPaperWire
			return
		case r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
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

// value обходит только fixed DTO allowlist с явными decimal/time handlers.
func (c *observationCounter) value(value any) {
	if c.err != nil {
		return
	}
	switch v := value.(type) {
	case time.Time:
		if v.UTC().Year() < 1970 || v.UTC().Year() > 9999 {
			c.err = ErrPaperWire
			return
		}
		c.text(v.UTC().Format(time.RFC3339Nano))
	case model.Decimal:
		n, d, err := v.CanonicalRatio()
		if err != nil {
			c.err = ErrPaperWire
			return
		}
		c.open()
		c.text("numerator")
		c.add(1)
		c.text(n)
		c.add(1)
		c.text("denominator")
		c.add(1)
		c.text(d)
		c.close()
	case string:
		c.text(v)
	case int:
		c.token()
		c.add(len(strconv.Itoa(v)))
	case int64:
		c.token()
		c.add(len(strconv.FormatInt(v, 10)))
	case bool:
		c.token()
		if v {
			c.add(4)
		} else {
			c.add(5)
		}
	case Observation, TradeWindow, model.CandlePage, model.Candle, model.MarkPricePage, model.MarkPriceCandle, model.FundingPage, model.FundingEvent:
		object := reflect.ValueOf(v)
		c.open()
		for i := 0; i < object.NumField() && c.err == nil; i++ {
			if i > 0 {
				c.add(1)
			}
			c.text(object.Type().Field(i).Name)
			c.add(1)
			c.value(object.Field(i).Interface())
		}
		c.close()
	case []TradeWindow, []model.MarkPricePage, []model.FundingPage, []model.Candle, []model.MarkPriceCandle, []model.FundingEvent:
		rows := reflect.ValueOf(v)
		if rows.Len() > MaxArrayItems {
			c.err = observationResource()
			return
		}
		if rows.IsNil() {
			c.token()
			c.add(4)
			return
		}
		c.open()
		for i := 0; i < rows.Len() && c.err == nil; i++ {
			if i > 0 {
				c.add(1)
			}
			c.value(rows.Index(i).Interface())
		}
		c.close()
	default:
		c.err = ErrPaperWire
	}
}

// observationCounts проверяет суммарные lengths до clone, sort и обхода значений.
func observationCounts(o Observation) error {
	pages, rows := 0, 0
	for _, n := range []int{len(o.TradeWindows), len(o.MarkPages), len(o.FundingPages)} {
		if n > MaxObservationPages-pages {
			return observationResource()
		}
		pages += n
	}
	for _, w := range o.TradeWindows {
		if len(w.Page.Candles) > MaxObservationRows-rows {
			return observationResource()
		}
		rows += len(w.Page.Candles)
	}
	for _, p := range o.MarkPages {
		if len(p.Candles) > MaxObservationRows-rows {
			return observationResource()
		}
		rows += len(p.Candles)
	}
	for _, p := range o.FundingPages {
		if len(p.Events) > MaxObservationRows-rows {
			return observationResource()
		}
		rows += len(p.Events)
	}
	return nil
}

// MeasureObservation считает exact canonical output без wire arrays и bulk cloning.
func MeasureObservation(o Observation) (ObservationMeasure, error) {
	if err := observationCounts(o); err != nil {
		return ObservationMeasure{}, err
	}
	c := observationCounter{}
	c.value(o)
	if c.err != nil {
		return ObservationMeasure{}, c.err
	}
	if err := observationRequests(o); err != nil {
		return ObservationMeasure{}, err
	}
	return c.ObservationMeasure, nil
}

type observationRequest struct {
	kind, symbol, interval string
	start, end             time.Time
}

// observationRequests проверяет bounds и identity без сортировки и копирования rows.
func observationRequests(o Observation) error {
	seen := make(map[observationRequest]bool)
	requests := func(kind, symbol, interval string, start, end time.Time) error {
		if !requestTimes(start, end) {
			return invalid("request bounds")
		}
		key := observationRequest{kind, symbol, interval, start.UTC().Round(0), end.UTC().Round(0)}
		if seen[key] {
			return invalid("duplicate request")
		}
		seen[key] = true
		return nil
	}
	for _, w := range o.TradeWindows {
		if w.StartMS < 0 || w.EndMS < w.StartMS {
			return invalid("trade bounds")
		}
		if err := requests("trade", w.Symbol, w.Interval, time.UnixMilli(w.StartMS), time.UnixMilli(w.EndMS)); err != nil {
			return err
		}
	}
	for _, p := range o.MarkPages {
		if err := requests("mark", p.Symbol, p.IntervalCode, p.StartTime, p.EndTime); err != nil {
			return err
		}
	}
	for _, p := range o.FundingPages {
		if err := requests("funding", p.Symbol, "", p.StartTime, p.EndTime); err != nil {
			return err
		}
	}
	return nil
}

// observationStreamCounts считает только schema paths до typed unmarshal.
func observationStreamCounts(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	pages, rows := 0, 0
	var walk func(string) error
	// walk читает bounded token stream, не создавая массивов страниц и строк.
	walk = func(path string) error {
		token, err := decoder.Token()
		if err != nil {
			return ErrPaperWire
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			fields := observationKeys(path)
			index := 0
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return ErrPaperWire
				}
				name, ok := key.(string)
				if !ok || index >= len(fields) || fields[index] != name {
					return ErrPaperWire
				}
				index++
				if err := walk(path + "/" + name); err != nil {
					return err
				}
			}
			if index != len(fields) || fields == nil {
				return ErrPaperWire
			}
		case '[':
			switch path {
			case "/TradeWindows", "/MarkPages", "/FundingPages", "/TradeWindows/*/Page/Candles", "/MarkPages/*/Candles", "/FundingPages/*/Events":
			default:
				return ErrPaperWire
			}
			for decoder.More() {
				switch path {
				case "/TradeWindows", "/MarkPages", "/FundingPages":
					if pages == MaxObservationPages {
						return observationResource()
					}
					pages++
				case "/TradeWindows/*/Page/Candles", "/MarkPages/*/Candles", "/FundingPages/*/Events":
					if rows == MaxObservationRows {
						return observationResource()
					}
					rows++
				}
				if err := walk(path + "/*"); err != nil {
					return err
				}
			}
		default:
			return ErrPaperWire
		}
		_, err = decoder.Token()
		if err != nil {
			return ErrPaperWire
		}
		return nil
	}
	return walk("")
}

// observationKeys задаёт полный fixed schema до выделения typed arrays.
func observationKeys(path string) []string {
	var names string
	switch path {
	case "":
		names = "CapturedAt SourceClock DailyBars TradeWindows MarkPages FundingPages"
	case "/TradeWindows/*":
		names = "Symbol Interval StartMS EndMS Page"
	case "/TradeWindows/*/Page":
		names = "Environment Source Category Endpoint ServerTime Candles"
	case "/TradeWindows/*/Page/Candles/*":
		names = "Symbol IntervalCode OpenTime Open High Low Close Volume Turnover Confirmed"
	case "/MarkPages/*":
		names = "Environment Source Category Endpoint Symbol IntervalCode StartTime EndTime ServerTime Candles"
	case "/MarkPages/*/Candles/*":
		names = "OpenTime Open High Low Close Confirmed"
	case "/FundingPages/*":
		names = "Environment Source Category Endpoint Symbol StartTime EndTime ServerTime Events"
	case "/FundingPages/*/Events/*":
		names = "FundingTime Rate"
	default:
		index := strings.LastIndexByte(path, '/')
		if index < 0 {
			return nil
		}
		parent, name := path[:index], path[index+1:]
		if parent == "/TradeWindows/*/Page/Candles/*" && (name == "Open" || name == "High" || name == "Low" || name == "Close" || name == "Volume" || name == "Turnover") || parent == "/MarkPages/*/Candles/*" && (name == "Open" || name == "High" || name == "Low" || name == "Close") || parent == "/FundingPages/*/Events/*" && name == "Rate" {
			names = "numerator denominator"
		} else {
			return nil
		}
	}
	return strings.Fields(names)
}
