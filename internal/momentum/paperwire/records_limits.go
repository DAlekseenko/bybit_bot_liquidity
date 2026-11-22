package paperwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

type recordMeter struct {
	bytes, tokens, depth, maxDepth int
	err                            error
}

// add ограничивает счётчики до сложения, исключая переполнение.
func (m *recordMeter) add(size, tokens int) {
	if size > MaxCanonicalBytes-m.bytes || tokens > MaxJSONTokens-m.tokens {
		m.err = resource()
		return
	}
	m.bytes += size
	m.tokens += tokens
}

// text считает escaping encoding/json без выделения escaped строки.
func (m *recordMeter) text(s string) {
	m.add(2, 1)
	for i := 0; i < len(s) && m.err == nil; {
		c := s[i]
		size := 1
		if c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f' {
			size = 2
		} else if c < 32 || c == '<' || c == '>' || c == '&' {
			size = 6
		} else if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(s[i:])
			size = n
			if r == utf8.RuneError && n == 1 {
				m.err = ErrPaperWire
				return
			}
			if r == '\u2028' || r == '\u2029' {
				size = 6
			}
			m.add(size, 0)
			i += n
			continue
		}
		m.add(size, 0)
		i++
	}
}

// object считает фиксированные declaration-order поля без reflection или clone.
func (m *recordMeter) object(fields ...field) {
	m.add(2+max(0, len(fields)-1)+len(fields), 2)
	m.depth++
	m.maxDepth = max(m.maxDepth, m.depth)
	if m.depth > MaxJSONDepth {
		m.err = resource()
	}
	for _, f := range fields {
		m.text(f.name)
		m.value(f.value)
	}
	m.depth--
}

// value учитывает реальные scalar bytes и полный ratio через foundation codec.
func (m *recordMeter) value(v any) {
	if m.err != nil {
		return
	}
	switch x := v.(type) {
	case string:
		m.text(x)
	case func():
		x()
	case int:
		m.add(len(strconv.Itoa(x)), 1)
	case int64:
		m.add(len(strconv.FormatInt(x, 10)), 1)
	case bool:
		if x {
			m.add(4, 1)
		} else {
			m.add(5, 1)
		}
	case time.Time:
		raw, e := (instant{x}).MarshalJSON()
		if e != nil {
			m.err = e
			return
		}
		m.add(len(raw), 1)
	case model.Decimal:
		raw, e := (amount{x}).MarshalJSON()
		if e != nil {
			m.err = e
			return
		}
		m.fragment(raw)
	case json.RawMessage:
		m.fragment(x)
	default:
		m.err = invalid("count scalar")
	}
}

// fragment считает уже bounded canonical profile/ratio и учитывает вложенную глубину.
func (m *recordMeter) fragment(raw []byte) {
	m.add(len(raw), 0)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	depth := m.depth
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			m.err = ErrPaperWire
			break
		}
		m.add(0, 1)
		if c, ok := t.(json.Delim); ok {
			if c == '{' || c == '[' {
				depth++
				m.maxDepth = max(m.maxDepth, depth)
				if depth > MaxJSONDepth {
					m.err = resource()
				}
			} else {
				depth--
			}
		}
	}
}

// array считает границы и запятые до обхода ограниченного массива.
func (m *recordMeter) array(n int, row func(int)) {
	if n > MaxArrayItems {
		m.err = resource()
		return
	}
	m.add(2+max(0, n-1), 2)
	m.depth++
	m.maxDepth = max(m.maxDepth, m.depth)
	if m.depth > MaxJSONDepth {
		m.err = resource()
	}
	for i := 0; i < n && m.err == nil; i++ {
		row(i)
	}
	m.depth--
}

// state считает полные positions без сортировки или создания новой карты.
func (m *recordMeter) state(s PortfolioState) {
	m.object(field{"AsOf", s.AsOf}, field{"FundingThrough", s.FundingThrough}, field{"WalletUSDT", s.WalletUSDT}, field{"EquityUSDT", s.EquityUSDT}, field{"Positions", func() {
		m.add(2+max(0, len(s.Positions)-1)+len(s.Positions), 2)
		m.depth++
		m.maxDepth = max(m.maxDepth, m.depth)
		if len(s.Positions) > MaxArrayItems || m.depth > MaxJSONDepth {
			m.err = resource()
		}
		for symbol, p := range s.Positions {
			m.text(symbol)
			m.object(field{"Quantity", p.Quantity}, field{"CostBasisUSDT", p.CostBasisUSDT})
		}
		m.depth--
	}})
}

// countInput считает точный wire до bulk adapters и сверяется с окончательной длиной.
func countInput(d CycleDocument, w inputWire, o ObservationMeasure) (recordMeter, error) {
	m := recordMeter{}
	fields := inputFields(&w)
	m.add(2+len(fields)-1+len(fields), 2)
	m.depth = 1
	for _, f := range fields {
		m.text(f.name)
		switch f.name {
		case "scope":
			m.object(field{"environment", w.Scope.Environment}, field{"account_id", w.Scope.AccountID}, field{"strategy_id", w.Scope.StrategyID})
		case "purpose":
			m.object(field{"kind", w.Purpose.Kind}, field{"command_id", w.Purpose.CommandID}, field{"control_revision", w.Purpose.ControlRevision})
		case "run":
			m.object(field{"run_id", w.Run.RunID}, field{"scheduled_at", w.Run.ScheduledAt.Time}, field{"onboarding_id", w.Run.OnboardingID}, field{"config_payload", w.Run.ConfigPayload}, field{"config_sha256", w.Run.ConfigHash})
		case "observation":
			m.add(o.Bytes, o.Tokens)
			m.maxDepth = max(m.maxDepth, m.depth+o.MaxDepth)
			if m.depth+o.MaxDepth > MaxJSONDepth {
				m.err = resource()
			}
		case "before":
			m.state(d.Before)
		case "cutoff":
			m.state(d.Cutoff)
		case "expected_after":
			m.state(d.ExpectedAfter)
		case "fills":
			if d.Fills == nil {
				m.add(4, 1)
				break
			}
			m.array(len(d.Fills), func(i int) {
				x := d.Fills[i]
				m.object(field{"ExecutionID", x.ExecutionID}, field{"Symbol", x.Symbol}, field{"Side", string(x.Side)}, field{"ExecutedAt", x.ExecutedAt}, field{"Quantity", x.Quantity}, field{"ExecutionPrice", x.ExecutionPrice}, field{"FeeUSDT", x.FeeUSDT})
			})
		default:
			switch x := f.value.(type) {
			case *string:
				m.text(*x)
			case *json.RawMessage:
				m.fragment(*x)
			}
		}
	}
	return m, m.err
}

// verifyMeasurement сверяет точные bytes/tokens/depth после bounded serialization.
func verifyMeasurement(raw []byte, want recordMeter) error {
	if err := preflight(raw); err != nil {
		return err
	}
	got := recordMeter{}
	got.fragment(raw)
	if got.bytes != want.bytes || got.tokens != want.tokens || got.maxDepth != want.maxDepth {
		return invalid("measurement mismatch")
	}
	return got.err
}

// encodedGuard проверяет encoded/decoded границы без base64 allocation.
func encodedGuard(s string, limit int) error {
	if len(s) > base64.StdEncoding.EncodedLen(limit) {
		return resource()
	}
	n := len(s) / 4 * 3
	if strings.HasSuffix(s, "=") {
		n--
	}
	if strings.HasSuffix(s, "==") {
		n--
	}
	if n > limit {
		return resource()
	}
	return nil
}

// inputPreflight считает fills и embedded bounds до typed allocation.
func inputPreflight(raw []byte) error {
	if err := preflight(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return scanInput(d, "", 0)
}

// scanInput обходит ограниченный JSON, не создавая DTO arrays.
func scanInput(d *json.Decoder, path string, depth int) error {
	t, err := d.Token()
	if err != nil {
		return ErrPaperWire
	}
	if s, ok := t.(string); ok {
		if path == "/run/config_payload" {
			return encodedGuard(s, MaxConfigBytes)
		}
		if path == "/plan_payload" {
			return encodedGuard(s, MaxPlanBytes)
		}
	}
	c, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	count := 0
	for d.More() {
		child := path
		if c == '{' {
			k, e := d.Token()
			if e != nil {
				return ErrPaperWire
			}
			child = path + "/" + k.(string)
		} else {
			count++
			if path == "/fills" && count > MaxActualFillRecords {
				return resource()
			}
		}
		if err := scanInput(d, child, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
