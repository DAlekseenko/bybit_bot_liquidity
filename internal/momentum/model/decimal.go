package model

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

const maxDecimalBits = 4096

var errDecimal = errors.New("invalid decimal")

// Decimal хранит точное безразмерное рациональное значение.
type Decimal struct{ value *big.Rat }

// Zero возвращает валидный точный ноль.
func Zero() Decimal { return Decimal{new(big.Rat)} }

// ParseAmount разбирает signed сумму без преобразования через float.
func ParseAmount(s string) (Decimal, error) { return parseDecimal(s) }

// ParsePrice разбирает только строго положительную цену.
func ParsePrice(s string) (Decimal, error) {
	d, err := parseDecimal(s)
	if err != nil || d.value.Sign() <= 0 {
		return Decimal{}, errDecimal
	}
	return d, nil
}

// ParseQty разбирает неотрицательное количество.
func ParseQty(s string) (Decimal, error) {
	d, err := parseDecimal(s)
	if err != nil || d.value.Sign() < 0 {
		return Decimal{}, errDecimal
	}
	return d, nil
}

// ParseRate разбирает signed долю, ограничения которой задаёт потребитель.
func ParseRate(s string) (Decimal, error) { return parseDecimal(s) }

// parseDecimal проверяет грамматику и пределы десятичного ввода.
func parseDecimal(s string) (Decimal, error) {
	if len(s) == 0 || len(s) > 128 {
		return Decimal{}, errDecimal
	}
	neg, body := strings.HasPrefix(s, "-"), s
	if neg {
		body = s[1:]
	}
	parts := strings.Split(body, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts[0]) > 1 && parts[0][0] == '0') {
		return Decimal{}, errDecimal
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return Decimal{}, errDecimal
		}
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
		if len(frac) == 0 || len(frac) > 64 {
			return Decimal{}, errDecimal
		}
		for _, c := range frac {
			if c < '0' || c > '9' {
				return Decimal{}, errDecimal
			}
		}
	}
	n := new(big.Int)
	if _, ok := n.SetString(parts[0]+frac, 10); !ok {
		return Decimal{}, errDecimal
	}
	if neg {
		n.Neg(n)
	}
	d := Decimal{new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(frac))), nil))}
	if d.value.Sign() == 0 {
		d.value.SetInt64(0)
	}
	if !d.valid() {
		return Decimal{}, errDecimal
	}
	return d, nil
}

// valid проверяет наличие значения и ограничение размера дроби.
func (d Decimal) valid() bool {
	return d.value != nil && d.value.Num().BitLen() <= maxDecimalBits && d.value.Denom().BitLen() <= maxDecimalBits
}

// Mul умножает значения, сохраняя точность и ограничение размера.
func (d Decimal) Mul(other Decimal) (Decimal, error) {
	if !d.valid() || !other.valid() {
		return Decimal{}, errDecimal
	}
	r := Decimal{new(big.Rat).Mul(d.value, other.value)}
	if !r.valid() {
		return Decimal{}, errDecimal
	}
	return r, nil
}

// Quo делит значения и отклоняет нулевой делитель.
func (d Decimal) Quo(other Decimal) (Decimal, error) {
	if !d.valid() || !other.valid() || other.value.Sign() == 0 {
		return Decimal{}, errDecimal
	}
	r := Decimal{new(big.Rat).Quo(d.value, other.value)}
	if !r.valid() {
		return Decimal{}, errDecimal
	}
	return r, nil
}

// Cmp сравнивает два валидных значения.
func (d Decimal) Cmp(other Decimal) (int, error) {
	if !d.valid() || !other.valid() {
		return 0, errDecimal
	}
	return d.value.Cmp(other.value), nil
}

// CanonicalRatio возвращает сокращённые числитель и положительный знаменатель.
func (d Decimal) CanonicalRatio() (string, string, error) {
	if !d.valid() {
		return "", "", errDecimal
	}
	return d.value.Num().String(), d.value.Denom().String(), nil
}

// WireDecimal возвращает точную конечную десятичную строку без лишних нулей.
func (d Decimal) WireDecimal() (string, error) {
	if !d.valid() {
		return "", errDecimal
	}
	n, den := new(big.Int).Set(d.value.Num()), new(big.Int).Set(d.value.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	a, b := 0, 0
	for new(big.Int).Mod(den, two).Sign() == 0 {
		den.Div(den, two)
		a++
	}
	for new(big.Int).Mod(den, five).Sign() == 0 {
		den.Div(den, five)
		b++
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", errDecimal
	}
	scale := a
	if b > scale {
		scale = b
	}
	if a < scale {
		n.Mul(n, new(big.Int).Exp(two, big.NewInt(int64(scale-a)), nil))
	}
	if b < scale {
		n.Mul(n, new(big.Int).Exp(five, big.NewInt(int64(scale-b)), nil))
	}
	neg := n.Sign() < 0
	digits := new(big.Int).Abs(n).String()
	if scale == 0 {
		if neg {
			return "-" + digits, nil
		}
		return digits, nil
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	cut := len(digits) - scale
	s := strings.TrimRight(digits[:cut]+"."+digits[cut:], "0")
	s = strings.TrimSuffix(s, ".")
	if neg {
		s = "-" + s
	}
	return s, nil
}

// MarshalText сериализует только точное конечное decimal значение.
func (d Decimal) MarshalText() ([]byte, error) {
	s, err := d.WireDecimal()
	return []byte(s), err
}

// MarshalJSON сериализует decimal как JSON-строку.
func (d Decimal) MarshalJSON() ([]byte, error) {
	s, err := d.WireDecimal()
	if err != nil {
		return nil, err
	}
	return strconv.AppendQuote(nil, s), nil
}

// FloorQty округляет неотрицательное количество вниз до шага.
func FloorQty(raw, step Decimal) (Decimal, error) {
	if !raw.valid() || !step.valid() || raw.value.Sign() < 0 || step.value.Sign() <= 0 {
		return Decimal{}, errDecimal
	}
	ratio := new(big.Rat).Quo(raw.value, step.value)
	units := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	r := Decimal{new(big.Rat).Mul(new(big.Rat).SetInt(units), step.value)}
	if !r.valid() {
		return Decimal{}, errDecimal
	}
	return r, nil
}

// MeetsMinNotional сравнивает точный notional уже округлённого Qty с минимумом.
func MeetsMinNotional(qty, price, min Decimal) (bool, error) {
	if !qty.valid() || !price.valid() || !min.valid() || qty.value.Sign() < 0 || price.value.Sign() <= 0 || min.value.Sign() < 0 {
		return false, errDecimal
	}
	n, err := qty.Mul(price)
	if err != nil {
		return false, err
	}
	return n.value.Cmp(min.value) >= 0, nil
}
