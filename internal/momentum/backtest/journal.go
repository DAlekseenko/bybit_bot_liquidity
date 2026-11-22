package backtest

import (
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

type journalFill struct {
	ExecutionID, Symbol                                               string
	Side                                                              model.FillSide
	Quantity, ExecutionPrice, FeeUSDT, RealizedPnLUSDT, CashDeltaUSDT journalAmount
	After                                                             journalPosition
}
type journalDelta struct {
	Symbol                                   string
	ReduceOnly                               bool
	RequestedQty, CandidateQty, UnplannedQty journalAmount
	Residual                                 planner.DeltaResidual
}
type journalPlan struct {
	WalletUSDT, RealizedPnLUSDT, FeesUSDT, CashDeltaUSDT       journalAmount
	CycleID, Kind, PlanHash, PayloadSHA256, ProjectionPlanHash string
	Payload                                                    []byte
	RequiresReductionConfirmation                              bool
	Deltas                                                     []journalDelta
	Status                                                     string
}
type journalRow struct {
	journalAccountingRow
	Fill *journalFill `json:",omitempty"`
	Plan *journalPlan `json:",omitempty"`
}

// journalRowsFromSegment сохраняет точный порядок проверенного участка учёта.
func journalRowsFromSegment(segment ReplayJournalSegment) []journalRow {
	rows := make([]journalRow, 0, len(segment.rows))
	for _, row := range segment.rows {
		rows = append(rows, journalRow{journalAccountingRow: row})
	}
	return rows
}

// EncodedReplayJournal хранит проверенные канонические байты полного журнала.
type EncodedReplayJournal struct {
	payload []byte
	hash    string
	counts  ReplayJournalCounts
}

// ReplayJournalCounts содержит только сырые количества фактических записей.
type ReplayJournalCounts struct {
	FundingAssessmentCount int `json:"funding_assessment_count"`
	SimulatedFillCount     int `json:"simulated_fill_count"`
	CycleCount             int `json:"cycle_count"`
	EquitySampleCount      int `json:"equity_sample_count"`
}

// Bytes возвращает отдельную копию полного канонического журнала.
func (j EncodedReplayJournal) Bytes() []byte { return slices.Clone(j.payload) }

// SHA256 возвращает хеш точных байтов журнала.
func (j EncodedReplayJournal) SHA256() string { return j.hash }

// Counts возвращает количества принятых строк.
func (j EncodedReplayJournal) Counts() ReplayJournalCounts { return j.counts }

// journalDigest вычисляет SHA-256 точных байтов.
func journalDigest(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

// EncodeReplayJournal кодирует полный вычисленный результат в причинном порядке.
func EncodeReplayJournal(result BaselineReplayResult) (EncodedReplayJournal, error) {
	id := result.Identity
	symbols := id.Symbols
	if result.Status != "complete" || id.RunID == "" || id.Start.IsZero() || !id.Start.Before(id.End) || len(result.Points) == 0 || !result.Initial.AsOf.Equal(id.Start) || len(result.Initial.FundingPostings) != 0 {
		return EncodedReplayJournal{}, ErrReplayJournal
	}
	if !journalHashPattern.MatchString(id.ConfigSHA256) || !journalHashPattern.MatchString(id.DatasetSHA256) || !journalHashPattern.MatchString(id.ModelSHA256) || id.StartPolicy != "now" && id.StartPolicy != "next_monday" {
		return EncodedReplayJournal{}, ErrReplayJournal
	}
	if !id.Start.Equal(id.Start.Truncate(time.Hour)) || !id.End.Equal(id.End.Truncate(time.Hour)) {
		return EncodedReplayJournal{}, ErrReplayJournal
	}
	expectedRun := baselineIdentity(baselineReplayIdentity{Format: "momentum/baseline-replay/v1", ConfigSHA256: id.ConfigSHA256, DatasetSHA256: id.DatasetSHA256, ModelSHA256: id.ModelSHA256, Start: journalTime(id.Start), End: journalTime(id.End)})
	if id.RunID != expectedRun {
		return EncodedReplayJournal{}, ErrReplayJournal
	}
	initial, err := ProjectAccountingJournalSegment(id.RunID, "", "", "initial", result.Initial, symbols)
	if err != nil {
		return EncodedReplayJournal{}, err
	}
	rows := journalRowsFromSegment(initial)
	for index, point := range result.Points {
		if index == 0 && !point.At.Equal(id.Start) || index > 0 && !result.Points[index-1].At.Add(time.Hour).Equal(point.At) || point.Terminal != (index == len(result.Points)-1) || point.Terminal && !point.At.Equal(id.End) || !point.Terminal && !point.At.Before(id.End) || (point.Accounting == nil) == (point.Cycle == nil) {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		if !point.Terminal {
			weekly, exists, clockErr := FirstWeeklyBoundary(point.At, id.End)
			expectsCycle := index == 0 && id.StartPolicy == "now" || exists && weekly.Equal(point.At)
			if clockErr != nil || expectsCycle != (point.Cycle != nil) {
				return EncodedReplayJournal{}, ErrReplayJournal
			}
		}
		if point.Accounting != nil {
			role := "hourly"
			if point.Terminal {
				role = "terminal"
			}
			if !point.Accounting.AsOf.Equal(point.At) || point.Terminal && len(point.Accounting.FundingPostings) != 0 {
				return EncodedReplayJournal{}, ErrReplayJournal
			}
			next, err := ProjectAccountingJournalSegment(id.RunID, "", "", role, *point.Accounting, symbols)
			if err != nil {
				return EncodedReplayJournal{}, err
			}
			rows = append(rows, journalRowsFromSegment(next)...)
			continue
		}
		cycle := point.Cycle
		if point.Terminal || !cycle.Prepared.Cutoff.AsOf.Equal(point.At) || !cycle.PostFill.AsOf.Equal(point.At) || len(cycle.PostFill.FundingPostings) != 0 {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		if cycle.Prepared.Plan.Metadata().ConfigHash != id.ConfigSHA256 {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		kind := "weekly"
		if index == 0 && id.StartPolicy == "now" {
			kind = "onboarding"
		}
		if cycle.Prepared.CycleID != baselineIdentity([]string{"momentum/baseline-cycle/v1", id.RunID, kind, journalTime(point.At)}) {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		if len(cycle.Batch.Positions) != len(symbols) || !sameApplyDecimal(cycle.Batch.WalletUSDT, cycle.PostFill.WalletUSDT) {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		for _, valuation := range cycle.PostFill.Valuations {
			position, exists := cycle.Batch.Positions[valuation.Mark.Symbol]
			if !exists || !sameApplyDecimal(position.Quantity, valuation.Position.Quantity) || !sameApplyDecimal(position.CostBasisUSDT, valuation.Position.CostBasisUSDT) {
				return EncodedReplayJournal{}, ErrReplayJournal
			}
		}
		pre, err := ProjectAccountingJournalSegment(id.RunID, cycle.Prepared.CycleID, cycle.Prepared.Plan.Hash(), "pretrade", cycle.Prepared.Cutoff, symbols)
		if err != nil {
			return EncodedReplayJournal{}, err
		}
		operations, err := journalCycleRows(id.RunID, kind, point.At, cycle.Prepared, cycle.Batch)
		if err != nil {
			return EncodedReplayJournal{}, err
		}
		post, err := ProjectAccountingJournalSegment(id.RunID, cycle.Prepared.CycleID, cycle.Prepared.Plan.Hash(), "posttrade", cycle.PostFill, symbols)
		if err != nil {
			return EncodedReplayJournal{}, err
		}
		rows = append(rows, journalRowsFromSegment(pre)...)
		rows = append(rows, operations...)
		rows = append(rows, journalRowsFromSegment(post)...)
	}
	seen := make(map[string]bool, len(rows))
	counts := ReplayJournalCounts{}
	for _, row := range rows {
		if seen[row.ID] {
			return EncodedReplayJournal{}, ErrReplayJournal
		}
		seen[row.ID] = true
		switch {
		case row.Funding != nil:
			counts.FundingAssessmentCount++
		case row.Fill != nil:
			counts.SimulatedFillCount++
		case row.Plan != nil:
			counts.CycleCount++
		case row.Accounting != nil:
			counts.EquitySampleCount++
		}
	}
	payload, err := json.Marshal(struct {
		Format, RunID, Start, End string
		Rows                      []journalRow
	}{"momentum/replay-journal/v1", id.RunID, journalTime(id.Start), journalTime(id.End), rows})
	if err != nil {
		return EncodedReplayJournal{}, ErrReplayJournal
	}
	return EncodedReplayJournal{payload, journalDigest(payload), counts}, nil
}

// journalCycleRows переносит план и проводки в принятом порядке исполнения.
func journalCycleRows(run, kind string, at time.Time, prepared PreparedBaselineCycle, batch BaselineFillBatchResult) ([]journalRow, error) {
	meta := prepared.Plan.Metadata()
	payload, err := model.EncodeTradePlan(prepared.Plan)
	if err != nil || prepared.RunID != run || prepared.CycleID == "" || meta.RunID != prepared.CycleID || !meta.ScheduledAt.Equal(at) || !meta.EquityCutoffAt.Equal(at) || prepared.Projection.PlanHash() != prepared.Plan.Hash() {
		return nil, ErrReplayJournal
	}
	if prepared.CycleID != baselineIdentity([]string{"momentum/baseline-cycle/v1", run, kind, journalTime(at)}) {
		return nil, ErrReplayJournal
	}
	deltas := prepared.Projection.Deltas()
	encoded := make([]journalDelta, 0, len(deltas))
	for _, d := range deltas {
		if d.Residual != "" || !sameApplyDecimal(d.CandidateQty, d.RequestedQty) || !sameApplyDecimal(d.UnplannedQty, model.Zero()) {
			return nil, ErrReplayJournal
		}
		encoded = append(encoded, journalDelta{
			Symbol: d.Symbol, ReduceOnly: d.ReduceOnly, RequestedQty: journalAmount{d.RequestedQty},
			CandidateQty: journalAmount{d.CandidateQty}, UnplannedQty: journalAmount{d.UnplannedQty}, Residual: d.Residual,
		})
	}
	if len(batch.Postings) != len(deltas) {
		return nil, ErrReplayJournal
	}
	plan := &journalPlan{
		WalletUSDT:      journalAmount{batch.WalletUSDT},
		RealizedPnLUSDT: journalAmount{batch.RealizedPnLUSDT},
		FeesUSDT:        journalAmount{batch.FeesUSDT},
		CashDeltaUSDT:   journalAmount{batch.CashDeltaUSDT},
		CycleID:         prepared.CycleID, Kind: kind, PlanHash: prepared.Plan.Hash(),
		PayloadSHA256: journalDigest(payload), ProjectionPlanHash: prepared.Projection.PlanHash(),
		Payload: payload, RequiresReductionConfirmation: prepared.Projection.RequiresReductionConfirmation(),
		Deltas: encoded, Status: "complete",
	}
	rows := []journalRow{{journalAccountingRow: journalAccountingRow{ID: baselineIdentity([]string{run, "plan", prepared.CycleID}), At: journalTime(at), Role: "plan", CycleID: prepared.CycleID, PlanHash: prepared.Plan.Hash()}, Plan: plan}}
	for i, p := range batch.Postings {
		d, f := deltas[i], p.Fill
		side, role := model.FillBuy, "increase"
		if d.ReduceOnly {
			side, role = model.FillSell, "reduce"
		}
		expectedID := baselineIdentity([]string{"momentum/baseline-zero-slip-fill/v1", run, prepared.CycleID, prepared.Plan.Hash(), role, f.Symbol})
		if f.ExecutionID != expectedID || !f.ExecutedAt.Equal(at) || f.Symbol != d.Symbol || f.Side != side || !sameApplyDecimal(f.Quantity, d.CandidateQty) {
			return nil, ErrReplayJournal
		}
		rows = append(rows, journalRow{journalAccountingRow: journalAccountingRow{ID: baselineIdentity([]string{run, "fill", f.ExecutionID}), At: journalTime(at), Role: role, CycleID: prepared.CycleID, PlanHash: prepared.Plan.Hash()}, Fill: &journalFill{
			ExecutionID: f.ExecutionID, Symbol: f.Symbol, Side: f.Side,
			Quantity: journalAmount{f.Quantity}, ExecutionPrice: journalAmount{f.ExecutionPrice}, FeeUSDT: journalAmount{f.FeeUSDT},
			RealizedPnLUSDT: journalAmount{p.RealizedPnLUSDT}, CashDeltaUSDT: journalAmount{p.CashDeltaUSDT},
			After: journalPositionValue(p.Position),
		}})
	}
	return rows, nil
}
