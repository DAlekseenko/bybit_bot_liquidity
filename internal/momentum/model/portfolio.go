package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errModel = errors.New("invalid model value")

// SnapshotInput задаёт проверяемые данные снимка портфеля.
type SnapshotInput struct {
	Environment   string
	AccountID     string
	ConfigVersion string
	CapturedAt    time.Time
	Equity        Decimal
	Prices        map[string]Decimal
	Positions     map[string]Decimal
}

// PortfolioSnapshot хранит проверенный неизменяемый снимок.
type PortfolioSnapshot struct {
	environment, accountID, configVersion string
	capturedAt                            time.Time
	equity                                Decimal
	prices, positions                     map[string]Decimal
	hash                                  string
}

// NewPortfolioSnapshot проверяет вход и создаёт канонический снимок.
func NewPortfolioSnapshot(in SnapshotInput) (PortfolioSnapshot, error) {
	if (in.Environment != "backtest" && in.Environment != "paper" && in.Environment != "testnet") || strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.ConfigVersion) == "" || in.CapturedAt.IsZero() || !in.Equity.valid() {
		return PortfolioSnapshot{}, errModel
	}
	prices, err := copyDecimals(in.Prices, true)
	if err != nil {
		return PortfolioSnapshot{}, err
	}
	positions, err := copyDecimals(in.Positions, false)
	if err != nil {
		return PortfolioSnapshot{}, err
	}
	s := PortfolioSnapshot{environment: in.Environment, accountID: in.AccountID, configVersion: in.ConfigVersion, capturedAt: in.CapturedAt.Round(0).UTC(), equity: in.Equity, prices: prices, positions: positions}
	s.hash = snapshotHash(s)
	return s, nil
}

// Environment возвращает среду снимка.
func (s PortfolioSnapshot) Environment() string { return s.environment }

// AccountID возвращает идентификатор счёта.
func (s PortfolioSnapshot) AccountID() string { return s.accountID }

// ConfigVersion возвращает версию конфигурации.
func (s PortfolioSnapshot) ConfigVersion() string { return s.configVersion }

// CapturedAt возвращает время снимка в UTC.
func (s PortfolioSnapshot) CapturedAt() time.Time { return s.capturedAt }

// Equity возвращает точную оценку капитала.
func (s PortfolioSnapshot) Equity() Decimal { return s.equity }

// Prices возвращает копию цен.
func (s PortfolioSnapshot) Prices() map[string]Decimal { return maps.Clone(s.prices) }

// Positions возвращает копию позиций.
func (s PortfolioSnapshot) Positions() map[string]Decimal { return maps.Clone(s.positions) }

// Hash возвращает SHA-256 снимка.
func (s PortfolioSnapshot) Hash() string { return s.hash }

// copyDecimals проверяет ключи и значения, создавая независимую карту.
func copyDecimals(src map[string]Decimal, positive bool) (map[string]Decimal, error) {
	out := make(map[string]Decimal, len(src))
	for key, value := range src {
		if strings.TrimSpace(key) == "" || !value.valid() {
			return nil, errModel
		}
		cmp, _ := value.Cmp(Zero())
		if positive && cmp <= 0 {
			return nil, errModel
		}
		out[key] = value
	}
	return out, nil
}

// snapshotHash считает версионированный hash полей в устойчивом порядке.
func snapshotHash(s PortfolioSnapshot) string {
	var b strings.Builder
	for _, v := range []string{"momentum/portfolio-snapshot/v1", s.environment, s.accountID, s.configVersion, s.capturedAt.Format(time.RFC3339Nano)} {
		writeField(&b, v)
	}
	writeDecimal(&b, s.equity)
	writeMap(&b, s.prices)
	writeMap(&b, s.positions)
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

// writeField кодирует строку длиной и содержимым без неоднозначных границ.
func writeField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)) + ":" + s)
}

// writeDecimal записывает сокращённое рациональное значение.
func writeDecimal(b *strings.Builder, d Decimal) {
	n, den, _ := d.CanonicalRatio()
	writeField(b, n+"/"+den)
}

// writeMap записывает карту по отсортированным символам.
func writeMap(b *strings.Builder, m map[string]Decimal) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeField(b, strconv.Itoa(len(keys)))
	for _, k := range keys {
		writeField(b, k)
		writeDecimal(b, m[k])
	}
}
