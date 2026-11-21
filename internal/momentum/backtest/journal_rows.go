package backtest

import (
	"bybit_bot_liquidity/internal/momentum/config"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrReplayJournal обозначает неполный или некорректный журнал воспроизведения.
var ErrReplayJournal = errors.New("invalid replay journal")

type journalAmount struct{ value model.Decimal }

// MarshalJSON сохраняет точную дробь без округления.
func (v journalAmount) MarshalJSON() ([]byte, error) {
	numerator, denominator, err := v.value.CanonicalRatio()
	if err != nil {
		return nil, ErrReplayJournal
	}
	return json.Marshal(struct {
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
	}{numerator, denominator})
}

type journalPosition struct{ Quantity, CostBasisUSDT journalAmount }
type journalValuation struct {
	Symbol, ObservedAt                         string
	Position                                   journalPosition
	MarkPrice, NotionalUSDT, UnrealizedPnLUSDT journalAmount
}
type journalAccounting struct {
	WalletUSDT, FundingCashDeltaUSDT, UnrealizedPnLUSDT, EquityUSDT journalAmount
	Valuations                                                      []journalValuation
}
type journalFunding struct {
	FundingID, Symbol string
	Rate, MarkPrice   journalAmount
	Before            journalPosition
	CashDeltaUSDT     journalAmount
}
type journalAccountingRow struct {
	ID, At, Role, CycleID, PlanHash string
	Funding                         *journalFunding    `json:",omitempty"`
	Accounting                      *journalAccounting `json:",omitempty"`
}

// journalTime нормализует точный момент для канонического вывода.
func journalTime(at time.Time) string { return at.Round(0).UTC().Format(time.RFC3339Nano) }

// journalPositionValue переносит вычисленные величины позиции.
func journalPositionValue(p model.LongPosition) journalPosition {
	return journalPosition{journalAmount{p.Quantity}, journalAmount{p.CostBasisUSDT}}
}

// journalAccountingRows сохраняет funding до оценки прежних позиций.
func journalAccountingRows(run, cycle, plan, role string, value AccountingResult, symbols []string) ([]journalAccountingRow, error) {
	if value.AsOf.IsZero() || len(value.Valuations) != len(symbols) {
		return nil, ErrReplayJournal
	}
	at := journalTime(value.AsOf)
	rows := make([]journalAccountingRow, 0, len(value.FundingPostings)+1)
	for _, p := range value.FundingPostings {
		a := p.Assessment
		if a.FundingID == "" || !a.Event.FundingTime.Equal(value.AsOf) {
			return nil, ErrReplayJournal
		}
		rows = append(rows, journalAccountingRow{ID: baselineIdentity([]string{run, "funding", a.FundingID}), At: at,
			Role: "funding", CycleID: cycle, PlanHash: plan,
			Funding: &journalFunding{
				FundingID: a.FundingID, Symbol: a.Symbol,
				Rate: journalAmount{a.Event.Rate}, MarkPrice: journalAmount{a.MarkPrice},
				Before: journalPositionValue(p.Position), CashDeltaUSDT: journalAmount{p.CashDeltaUSDT},
			},
		})
	}
	valuations := make([]journalValuation, 0, len(symbols))
	for i, p := range value.Valuations {
		if p.Mark.Symbol != symbols[i] || !p.AsOf.Equal(value.AsOf) || p.Mark.ObservedAt.IsZero() || p.Mark.ObservedAt.After(value.AsOf) {
			return nil, ErrReplayJournal
		}
		valuations = append(valuations, journalValuation{
			Symbol: p.Mark.Symbol, ObservedAt: journalTime(p.Mark.ObservedAt),
			Position: journalPositionValue(p.Position), MarkPrice: journalAmount{p.Mark.Price},
			NotionalUSDT: journalAmount{p.NotionalUSDT}, UnrealizedPnLUSDT: journalAmount{p.UnrealizedPnLUSDT},
		})
	}
	rows = append(rows, journalAccountingRow{ID: baselineIdentity([]string{run, role, at}), At: at, Role: role, CycleID: cycle, PlanHash: plan,
		Accounting: &journalAccounting{
			WalletUSDT: journalAmount{value.WalletUSDT}, FundingCashDeltaUSDT: journalAmount{value.FundingCashDeltaUSDT},
			UnrealizedPnLUSDT: journalAmount{value.UnrealizedPnLUSDT}, EquityUSDT: journalAmount{value.EquityUSDT},
			Valuations: valuations,
		},
	})
	return rows, nil
}

var journalHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReplayJournalSegment хранит отдельный проверенный участок полного журнала.
type ReplayJournalSegment struct {
	rows    []journalAccountingRow
	payload []byte
}

// Bytes возвращает отдельную копию канонического массива строк участка.
func (s ReplayJournalSegment) Bytes() []byte { return slices.Clone(s.payload) }

// sealJournalSegment проверяет все вложенные дроби перед выдачей участка.
func sealJournalSegment(rows []journalAccountingRow, err error) (ReplayJournalSegment, error) {
	if err != nil {
		return ReplayJournalSegment{}, ErrReplayJournal
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if seen[row.ID] {
			return ReplayJournalSegment{}, ErrReplayJournal
		}
		seen[row.ID] = true
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return ReplayJournalSegment{}, ErrReplayJournal
	}
	return ReplayJournalSegment{rows, payload}, nil
}

// ProjectAccountingJournalSegment кодирует funding и полную оценку портфеля.
func ProjectAccountingJournalSegment(run, cycle, plan, role string, value AccountingResult, symbols []string) (ReplayJournalSegment, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	cyclic := role == "pretrade" || role == "posttrade"
	if err != nil || !slices.Equal(canonical, symbols) || !journalHashPattern.MatchString(run) || cyclic && (!journalHashPattern.MatchString(cycle) || !journalHashPattern.MatchString(plan)) || !cyclic && (cycle != "" || plan != "") {
		return ReplayJournalSegment{}, ErrReplayJournal
	}
	switch role {
	case "initial", "posttrade", "terminal":
		if len(value.FundingPostings) != 0 {
			return ReplayJournalSegment{}, ErrReplayJournal
		}
	case "hourly", "pretrade":
	default:
		return ReplayJournalSegment{}, ErrReplayJournal
	}
	lastSymbol := -1
	for _, posting := range value.FundingPostings {
		index := slices.Index(symbols, posting.Assessment.Symbol)
		if index <= lastSymbol {
			return ReplayJournalSegment{}, ErrReplayJournal
		}
		lastSymbol = index
	}
	return sealJournalSegment(journalAccountingRows(run, cycle, plan, role, value, symbols))
}
