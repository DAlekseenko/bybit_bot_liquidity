package model

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Target задаёт точное количество символа в плане.
type Target struct {
	Symbol   string
	Quantity Decimal
}

// PlanMetadata содержит идентичность, расписание и ревизию плана.
type PlanMetadata struct {
	StrategyID, RunID, ProfileID, ModelID, FormulaID string
	ConfigHash                                       string
	ScheduledAt, EquityCutoffAt                      time.Time
	Revision                                         uint64
	PreviousHash                                     string
}

// PlanInput объединяет проверенный снимок, метаданные и переданные цели.
type PlanInput struct {
	Snapshot PortfolioSnapshot
	Metadata PlanMetadata
	Targets  []Target
}

// TradePlan хранит неизменяемый снимок, цели и ревизию.
type TradePlan struct {
	snapshot PortfolioSnapshot
	metadata PlanMetadata
	targets  []Target
	hash     string
}

// NewTradePlan проверяет вход и создаёт канонический план.
func NewTradePlan(in PlanInput) (TradePlan, error) {
	m := in.Metadata
	if !validPlanSnapshot(in.Snapshot) ||
		strings.TrimSpace(m.StrategyID) == "" || strings.TrimSpace(m.RunID) == "" ||
		strings.TrimSpace(m.ProfileID) == "" || strings.TrimSpace(m.ModelID) == "" || strings.TrimSpace(m.FormulaID) == "" ||
		!sha256Pattern.MatchString(m.ConfigHash) || m.ScheduledAt.IsZero() || m.EquityCutoffAt.IsZero() ||
		m.Revision == 0 || (m.Revision == 1 && m.PreviousHash != "") ||
		(m.Revision > 1 && !sha256Pattern.MatchString(m.PreviousHash)) {
		return TradePlan{}, errModel
	}
	targets := append([]Target(nil), in.Targets...)
	seen := make(map[string]struct{}, len(targets))
	prices := in.Snapshot.Prices()
	for _, target := range targets {
		if strings.TrimSpace(target.Symbol) == "" || !target.Quantity.valid() {
			return TradePlan{}, errModel
		}
		if _, ok := seen[target.Symbol]; ok {
			return TradePlan{}, errModel
		}
		cmp, _ := target.Quantity.Cmp(Zero())
		if cmp < 0 {
			return TradePlan{}, errModel
		}
		if _, ok := prices[target.Symbol]; !ok {
			return TradePlan{}, errModel
		}
		seen[target.Symbol] = struct{}{}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Symbol < targets[j].Symbol })
	m.ScheduledAt = m.ScheduledAt.Round(0).UTC()
	m.EquityCutoffAt = m.EquityCutoffAt.Round(0).UTC()
	p := TradePlan{snapshot: in.Snapshot, metadata: m, targets: targets}
	p.hash = planHash(p)
	return p, nil
}

// validPlanSnapshot проверяет поля снимка и совпадение его канонического hash.
func validPlanSnapshot(s PortfolioSnapshot) bool {
	checked, err := NewPortfolioSnapshot(SnapshotInput{
		Environment: s.Environment(), AccountID: s.AccountID(), ConfigVersion: s.ConfigVersion(),
		CapturedAt: s.CapturedAt(), Equity: s.Equity(), Prices: s.Prices(), Positions: s.Positions(),
	})
	return err == nil && sha256Pattern.MatchString(s.Hash()) && checked.Hash() == s.Hash()
}

// Snapshot возвращает значение исходного снимка.
func (p TradePlan) Snapshot() PortfolioSnapshot { return p.snapshot }

// Metadata возвращает значение метаданных плана.
func (p TradePlan) Metadata() PlanMetadata { return p.metadata }

// Targets возвращает копию отсортированных целей.
func (p TradePlan) Targets() []Target { return append([]Target(nil), p.targets...) }

// Hash возвращает SHA-256 плана.
func (p TradePlan) Hash() string { return p.hash }

// planHash хеширует snapshot и все поля плана в каноническом порядке.
func planHash(p TradePlan) string {
	var b strings.Builder
	writeField(&b, "momentum/trade-plan/v1")
	writeField(&b, p.snapshot.Hash())
	m := p.metadata
	for _, value := range []string{
		m.StrategyID, m.RunID, m.ProfileID, m.ModelID, m.FormulaID, m.ConfigHash,
		m.ScheduledAt.Format(time.RFC3339Nano), m.EquityCutoffAt.Format(time.RFC3339Nano),
		strconv.FormatUint(m.Revision, 10), m.PreviousHash,
	} {
		writeField(&b, value)
	}
	writeField(&b, strconv.Itoa(len(p.targets)))
	for _, target := range p.targets {
		writeField(&b, target.Symbol)
		writeDecimal(&b, target.Quantity)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}
