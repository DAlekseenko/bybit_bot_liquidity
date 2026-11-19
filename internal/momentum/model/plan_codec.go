package model

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const tradePlanEncodingV1 = "momentum/trade-plan-payload/v1"

// EncodeTradePlan сохраняет полное проверенное значение TradePlan в каноническом формате v1.
func EncodeTradePlan(plan TradePlan) ([]byte, error) {
	if !sha256Pattern.MatchString(plan.hash) {
		return nil, errModel
	}
	var out strings.Builder
	snap := plan.snapshot
	meta := plan.metadata
	captured, err := snap.capturedAt.UTC().MarshalBinary()
	if err != nil || !planInstantV1(string(captured)) {
		return nil, errModel
	}
	scheduled, err := meta.ScheduledAt.UTC().MarshalBinary()
	if err != nil || !planInstantV1(string(scheduled)) {
		return nil, errModel
	}
	cutoff, err := meta.EquityCutoffAt.UTC().MarshalBinary()
	if err != nil || !planInstantV1(string(cutoff)) {
		return nil, errModel
	}
	for _, value := range []string{
		tradePlanEncodingV1, plan.hash, snap.environment, snap.accountID,
		snap.configVersion, string(captured),
	} {
		writeField(&out, value)
	}
	writeDecimal(&out, snap.equity)
	writeMap(&out, snap.prices)
	writeMap(&out, snap.positions)
	for _, value := range []string{
		meta.StrategyID, meta.RunID, meta.ProfileID, meta.ModelID, meta.FormulaID, meta.ConfigHash,
		string(scheduled), string(cutoff), strconv.FormatUint(meta.Revision, 10), meta.PreviousHash,
		strconv.Itoa(len(plan.targets)),
	} {
		writeField(&out, value)
	}
	for _, target := range plan.targets {
		writeField(&out, target.Symbol)
		writeDecimal(&out, target.Quantity)
	}
	return []byte(out.String()), nil
}

// DecodeTradePlan восстанавливает неизменяемый план и сверяет канонические байты и hash.
func DecodeTradePlan(raw []byte) (TradePlan, error) {
	cursor := planDecodeCursor{remaining: string(raw)}
	if cursor.field() != tradePlanEncodingV1 {
		return TradePlan{}, errModel
	}
	expectedHash := cursor.field()
	snap, err := NewPortfolioSnapshot(SnapshotInput{
		Environment: cursor.field(), AccountID: cursor.field(), ConfigVersion: cursor.field(),
		CapturedAt: cursor.instant(), Equity: cursor.decimal(),
		Prices: cursor.quantities(), Positions: cursor.quantities(),
	})
	if err != nil || cursor.failed {
		return TradePlan{}, errModel
	}
	meta := PlanMetadata{
		StrategyID: cursor.field(), RunID: cursor.field(), ProfileID: cursor.field(),
		ModelID: cursor.field(), FormulaID: cursor.field(), ConfigHash: cursor.field(),
		ScheduledAt: cursor.instant(), EquityCutoffAt: cursor.instant(),
		Revision: cursor.unsigned(), PreviousHash: cursor.field(),
	}
	count := cursor.pairs()
	targets := make([]Target, 0, count)
	for range count {
		symbol, quantity := cursor.field(), cursor.decimal()
		if cursor.failed {
			return TradePlan{}, errModel
		}
		targets = append(targets, Target{Symbol: symbol, Quantity: quantity})
	}
	if cursor.failed || cursor.remaining != "" {
		return TradePlan{}, errModel
	}
	p, err := NewTradePlan(PlanInput{Snapshot: snap, Metadata: meta, Targets: targets})
	if err != nil || p.hash != expectedHash {
		return TradePlan{}, errModel
	}
	canonical, err := EncodeTradePlan(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return TradePlan{}, errModel
	}
	return p, nil
}

type planDecodeCursor struct {
	remaining string
	failed    bool
}

// field извлекает одно поле с десятичной длиной в байтах.
func (c *planDecodeCursor) field() string {
	if c.failed {
		return ""
	}
	length, data, ok := strings.Cut(c.remaining, ":")
	n, err := strconv.Atoi(length)
	if !ok || err != nil || n < 0 || n > len(data) || strconv.Itoa(n) != length {
		c.failed = true
		return ""
	}
	c.remaining = data[n:]
	return data[:n]
}

// unsigned извлекает целое без потери полной разрядности uint64.
func (c *planDecodeCursor) unsigned() uint64 {
	n, err := strconv.ParseUint(c.field(), 10, 64)
	if err != nil {
		c.failed = true
	}
	return n
}

// pairs ограничивает размер коллекции данными, которые ещё можно прочитать.
func (c *planDecodeCursor) pairs() int {
	n := c.unsigned()
	if n > uint64(len(c.remaining)/4) {
		c.failed = true
		return 0
	}
	return int(n)
}

// instant извлекает каноническое двоичное представление времени UTC.
func (c *planDecodeCursor) instant() time.Time {
	raw := c.field()
	var t time.Time
	if !planInstantV1(raw) || t.UnmarshalBinary([]byte(raw)) != nil {
		c.failed = true
	}
	return t
}

// planInstantV1 проверяет зафиксированный формат UTC времени, включая диапазон наносекунд.
func planInstantV1(raw string) bool {
	return len(raw) == 15 && raw[0] == 1 && raw[13] == 255 && raw[14] == 255 && binary.BigEndian.Uint32([]byte(raw[9:13])) < 1_000_000_000
}

// decimal читает точную рациональную дробь в пределах числового контракта модели.
func (c *planDecodeCursor) decimal() Decimal {
	numer, denom, ok := strings.Cut(c.field(), "/")
	const maxDigits = 1234
	if !ok || len(strings.TrimPrefix(numer, "-")) > maxDigits || len(denom) > maxDigits {
		c.failed = true
		return Decimal{}
	}
	n, nok := new(big.Int).SetString(numer, 10)
	d, dok := new(big.Int).SetString(denom, 10)
	if !nok || !dok || d.Sign() <= 0 || n.BitLen() > maxDecimalBits || d.BitLen() > maxDecimalBits {
		c.failed = true
		return Decimal{}
	}
	return Decimal{new(big.Rat).SetFrac(n, d)}
}

// quantities извлекает карту цен или позиций.
func (c *planDecodeCursor) quantities() map[string]Decimal {
	n := c.pairs()
	values := make(map[string]Decimal, n)
	for range n {
		symbol, quantity := c.field(), c.decimal()
		if c.failed {
			return nil
		}
		values[symbol] = quantity
	}
	return values
}
