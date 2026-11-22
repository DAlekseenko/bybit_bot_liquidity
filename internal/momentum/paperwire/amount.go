package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"bybit_bot_liquidity/internal/momentum/model"
)

type amount struct{ value model.Decimal }
type ratio struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}

// MarshalJSON сохраняет полный сокращённый ratio без финансового округления.
func (a amount) MarshalJSON() ([]byte, error) {
	n, d, err := a.value.CanonicalRatio()
	if err != nil {
		return nil, ErrPaperWire
	}
	return json.Marshal(ratio{n, d})
}

// UnmarshalJSON проверяет канонические целые и восстанавливает точное значение.
func (a *amount) UnmarshalJSON(raw []byte) error {
	if len(raw) > 2*1234+64 {
		return errors.Join(invalid("ratio size"), ErrPaperResourceLimit)
	}
	var r ratio
	if err := json.Unmarshal(raw, &r); err != nil {
		return ErrPaperWire
	}
	if !integerText(r.Numerator, true) || !integerText(r.Denominator, false) {
		return invalid("ratio grammar")
	}
	n, nok := new(big.Int).SetString(r.Numerator, 10)
	d, dok := new(big.Int).SetString(r.Denominator, 10)
	if !nok || !dok || n.String() != r.Numerator || d.String() != r.Denominator || d.Sign() <= 0 || n.BitLen() > 4096 || d.BitLen() > 4096 || new(big.Int).GCD(nil, nil, n, d).Cmp(big.NewInt(1)) != 0 {
		return ErrPaperWire
	}
	value, err := reducedAmount(n, d)
	if err != nil {
		return err
	}
	canonical, err := (amount{value}).MarshalJSON()
	if err != nil || !bytes.Equal(canonical, raw) {
		return invalid("ratio canonical form")
	}
	a.value = value
	return nil
}

// integerAmount собирает ограниченное целое из допустимых коротких decimal chunks.
func integerAmount(n *big.Int) (model.Decimal, error) {
	text := new(big.Int).Abs(n).String()
	value := model.Zero()
	for len(text) > 0 {
		size := min(64, len(text))
		factor, _ := model.ParseAmount("1" + strings.Repeat("0", size))
		part, err := model.ParseAmount(strings.TrimLeft(text[:size], "0"))
		if strings.TrimLeft(text[:size], "0") == "" {
			part = model.Zero()
			err = nil
		}
		if err != nil {
			return model.Decimal{}, invalid("recorded amount")
		}
		value, err = value.Mul(factor)
		if err == nil {
			value, err = value.Add(part)
		}
		if err != nil {
			return model.Decimal{}, invalid("recorded amount")
		}
		text = text[size:]
	}
	if n.Sign() < 0 {
		return model.Zero().Sub(value)
	}
	return value, nil
}

// reducedAmount проверяет модельный предел после сокращения дроби.
func reducedAmount(n, d *big.Int) (model.Decimal, error) {
	if n.BitLen() > 4096 || d.Sign() <= 0 || d.BitLen() > 4096 {
		return model.Decimal{}, invalid("recorded amount")
	}
	numerator, err := integerAmount(n)
	if err != nil {
		return model.Decimal{}, err
	}
	denominator, err := integerAmount(d)
	if err != nil {
		return model.Decimal{}, err
	}
	value, err := numerator.Quo(denominator)
	if err != nil {
		return model.Decimal{}, invalid("recorded amount")
	}
	return value, nil
}

// ParseRecordedAmount читает SQL NUMERIC, сокращая большой коэффициент до Decimal.
func ParseRecordedAmount(text string) (model.Decimal, error) {
	if len(text) == 0 {
		return model.Decimal{}, invalid("empty recorded amount")
	}
	if len(text) > MaxCanonicalBytes {
		return model.Decimal{}, errors.Join(invalid("recorded amount size"), ErrPaperResourceLimit)
	}
	negative := strings.HasPrefix(text, "-")
	body := text
	if negative {
		body = body[1:]
	}
	parts := strings.Split(body, ".")
	if len(parts) > 2 || parts[0] == "" {
		return model.Decimal{}, invalid("recorded amount")
	}
	for _, part := range parts {
		if part == "" {
			return model.Decimal{}, invalid("recorded amount")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return model.Decimal{}, invalid("recorded amount")
			}
		}
	}
	whole := strings.TrimLeft(parts[0], "0")
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if len(whole) > 1234 || len(fraction) > 4095 {
		return model.Decimal{}, invalid("recorded amount")
	}
	coefficient, ok := new(big.Int).SetString("0"+whole+fraction, 10)
	if !ok {
		return model.Decimal{}, invalid("recorded amount")
	}
	if negative {
		coefficient.Neg(coefficient)
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
	value := new(big.Rat).SetFrac(coefficient, denominator)
	return reducedAmount(value.Num(), value.Denom())
}

// integerText ограничивает грамматику и длину целого до его выделения big.Int.
func integerText(text string, signed bool) bool {
	if signed && strings.HasPrefix(text, "-") {
		text = text[1:]
		if text == "0" {
			return false
		}
	}
	if len(text) == 0 || len(text) > 1234 || len(text) > 1 && text[0] == '0' {
		return false
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// invalid добавляет контекст ожидаемого отказа без потери sentinel.
func invalid(where string) error { return fmt.Errorf("paperwire %s: %w", where, ErrPaperWire) }
