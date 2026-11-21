package backtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// journalReplayFixture получает полный результат настоящей синтетической композиции.
func journalReplayFixture(t *testing.T) BaselineReplayResult {
	t.Helper()
	result, err := ReplayBaseline(context.Background(), runnerFixture(t, "now", ""))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestCompleteReplayJournal проверяет все типы строк и однократное отражение событий.
func TestCompleteReplayJournal(t *testing.T) {
	result := journalReplayFixture(t)
	encoded, err := EncodeReplayJournal(result)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(encoded.Bytes())
	if encoded.SHA256() != hex.EncodeToString(hash[:]) {
		t.Fatal("hash")
	}
	counts := encoded.Counts()
	if counts.CycleCount != 2 || counts.SimulatedFillCount != 6 || counts.FundingAssessmentCount != 9 || counts.EquitySampleCount != 174 {
		t.Fatalf("counts %+v", counts)
	}
	var wire struct {
		Format, RunID string
		Rows          []journalRow
	}
	if err := json.Unmarshal(encoded.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Format != "momentum/replay-journal/v1" || wire.RunID != result.Identity.RunID || wire.Rows[0].Role != "initial" || wire.Rows[len(wire.Rows)-1].Role != "terminal" {
		t.Fatal("envelope")
	}
	// Decode into raw rows because amounts deliberately expose a serialization-only adapter.
	var raw struct {
		Rows []struct {
			Role, PlanHash string
			Plan           *struct {
				Payload       []byte
				PayloadSHA256 string
			}
			Fill json.RawMessage
		}
	}
	if err := json.Unmarshal(encoded.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, row := range raw.Rows {
		if row.Plan != nil {
			plan, err := model.DecodeTradePlan(row.Plan.Payload)
			if err != nil || plan.Hash() != row.PlanHash {
				t.Fatal("plan payload")
			}
			sum := sha256.Sum256(row.Plan.Payload)
			if row.Plan.PayloadSHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("payload hash")
			}
		}
	}
	again, err := EncodeReplayJournal(result)
	if err != nil || !bytes.Equal(encoded.Bytes(), again.Bytes()) {
		t.Fatal("repeat")
	}
	bytesCopy := encoded.Bytes()
	bytesCopy[0] = 'x'
	if !bytes.Equal(encoded.Bytes(), again.Bytes()) {
		t.Fatal("alias")
	}
}

// TestCompleteJournalRefusals проверяет повреждённую позднюю точку и привязку.
func TestCompleteJournalRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BaselineReplayResult)
	}{
		{"status", func(r *BaselineReplayResult) { r.Status = "incomplete" }},
		{"run", func(r *BaselineReplayResult) { r.Identity.RunID = "other" }},
		{"config pin", func(r *BaselineReplayResult) { r.Identity.ConfigSHA256 = "other" }},
		{"dataset pin", func(r *BaselineReplayResult) { r.Identity.DatasetSHA256 = "other" }},
		{"model pin", func(r *BaselineReplayResult) { r.Identity.ModelSHA256 = "other" }},
		{"policy pin", func(r *BaselineReplayResult) { r.Identity.StartPolicy = "unknown" }},
		{"empty", func(r *BaselineReplayResult) { r.Points = nil }},
		{"missing middle", func(r *BaselineReplayResult) { r.Points = append(r.Points[:10], r.Points[11:]...) }},
		{"terminal missing", func(r *BaselineReplayResult) { r.Points = r.Points[:len(r.Points)-1] }},
		{"terminal flag", func(r *BaselineReplayResult) { r.Points[len(r.Points)-1].Terminal = false }},
		{"time", func(r *BaselineReplayResult) { r.Points[10].At = r.Points[9].At }},
		{"late amount", func(r *BaselineReplayResult) { r.Points[len(r.Points)-1].Accounting.EquityUSDT = model.Decimal{} }},
		{"empty union", func(r *BaselineReplayResult) { r.Points[1].Accounting = nil }},
		{"double union", func(r *BaselineReplayResult) { r.Points[0].Accounting = &r.Initial }},
		{"cycle reference", func(r *BaselineReplayResult) { r.Points[0].Cycle.Prepared.CycleID = "other" }},
		{"plan codec", func(r *BaselineReplayResult) { r.Points[0].Cycle.Prepared.Plan = model.TradePlan{} }},
		{"batch wallet", func(r *BaselineReplayResult) { r.Points[0].Cycle.Batch.WalletUSDT = amount(t, "123") }},
		{"batch positions", func(r *BaselineReplayResult) { delete(r.Points[0].Cycle.Batch.Positions, "BTCUSDT") }},
		{"fill id", func(r *BaselineReplayResult) { r.Points[0].Cycle.Batch.Postings[0].Fill.ExecutionID = "forged" }},
		{"postfill wallet", func(r *BaselineReplayResult) { r.Points[0].Cycle.PostFill.WalletUSDT = amount(t, "123") }},
		{"postfill position", func(r *BaselineReplayResult) {
			r.Points[0].Cycle.PostFill.Valuations[0].Position.Quantity = amount(t, "123")
		}},
		{"fill amount", func(r *BaselineReplayResult) { r.Points[0].Cycle.Batch.Postings[0].Fill.Quantity = model.Decimal{} }},
		{"cross-point source id", func(r *BaselineReplayResult) {
			r.Points[1].Accounting.FundingPostings[0].Assessment.FundingID = r.Points[0].Cycle.Prepared.Cutoff.FundingPostings[0].Assessment.FundingID
		}},
		{"weekly arm missing", func(r *BaselineReplayResult) {
			r.Points[168].Accounting = &r.Points[168].Cycle.Prepared.Cutoff
			r.Points[168].Cycle = nil
		}},
		{"fill side", func(r *BaselineReplayResult) { r.Points[0].Cycle.Batch.Postings[0].Fill.Side = model.FillSell }},
		{"late cycle amount", func(r *BaselineReplayResult) { r.Points[168].Cycle.Batch.FeesUSDT = model.Decimal{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := journalReplayFixture(t)
			test.mutate(&result)
			got, err := EncodeReplayJournal(result)
			if !errors.Is(err, ErrReplayJournal) || !reflect.DeepEqual(got, EncodedReplayJournal{}) {
				t.Fatalf("partial %v", err)
			}
		})
	}
}

// TestJournalExactUTC сохраняет байты при эквивалентном представлении момента.
func TestJournalExactUTC(t *testing.T) {
	result := journalReplayFixture(t)
	before, err := EncodeReplayJournal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.Identity.Start = result.Identity.Start.In(time.FixedZone("other", 3600))
	result.Identity.End = result.Identity.End.In(time.FixedZone("other", 3600))
	after, err := EncodeReplayJournal(result)
	if err != nil || !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("timezone")
	}
}

// TestJournalMixedCycle сохраняет отдельные знаки комиссии и PnL без повторного расчёта.
func TestJournalMixedCycle(t *testing.T) {
	input := cycleFixture(t, "now")
	prepared, err := PrepareBaselineCycle(input)
	if err != nil {
		t.Fatal(err)
	}
	opens := []NextOpenReference{}
	for _, symbol := range input.Config.Values().Symbols {
		opens = append(opens, NextOpenReference{Symbol: symbol, Price: amount(t, "100")})
	}
	batch, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := journalCycleRows(prepared.RunID, input.Kind, input.At, prepared, batch)
	if err != nil || len(rows) != 3 || rows[0].Role != "plan" || rows[1].Role != "reduce" || rows[2].Role != "increase" {
		t.Fatalf("rows %v", err)
	}
	assertDecimal(t, rows[0].Plan.FeesUSDT.value, "2.999")
	assertDecimal(t, rows[0].Plan.WalletUSDT.value, "8967.001")
	assertDecimal(t, rows[1].Fill.Quantity.value, "10.14")
	assertDecimal(t, rows[1].Fill.FeeUSDT.value, "1.014")
	assertDecimal(t, rows[2].Fill.Quantity.value, "19.85")
	assertDecimal(t, rows[2].Fill.FeeUSDT.value, "1.985")
	for i, posting := range batch.Postings {
		if rows[i+1].Fill.ExecutionID != posting.Fill.ExecutionID || rows[i+1].Fill.Side != posting.Fill.Side || !sameApplyDecimal(rows[i+1].Fill.RealizedPnLUSDT.value, posting.RealizedPnLUSDT) || !sameApplyDecimal(rows[i+1].Fill.CashDeltaUSDT.value, posting.CashDeltaUSDT) {
			t.Fatal("posting changed")
		}
	}
	batch.Postings[1].Fill.ExecutedAt = input.At.Add(time.Nanosecond)
	if _, err := journalCycleRows(prepared.RunID, input.Kind, input.At, prepared, batch); !errors.Is(err, ErrReplayJournal) {
		t.Fatal("late time accepted")
	}
}

// flatJournalVector создаёт точный плоский запуск для независимого wire-вектора.
func flatJournalVector(t *testing.T, shift time.Duration) BaselineReplayResult {
	t.Helper()
	start := time.Date(2025, 7, 8, 0, 0, 0, 0, time.UTC).Add(shift)
	id := BaselineReplayIdentity{ConfigSHA256: strings.Repeat("a", 64), DatasetSHA256: strings.Repeat("b", 64), ModelSHA256: strings.Repeat("c", 64), Start: start, End: start.Add(time.Hour), Symbols: []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, StartPolicy: "next_monday"}
	id.RunID = baselineIdentity(baselineReplayIdentity{"momentum/baseline-replay/v1", id.ConfigSHA256, id.DatasetSHA256, id.ModelSHA256, journalTime(id.Start), journalTime(id.End)})
	result := BaselineReplayResult{Identity: id, Status: "complete"}
	for i, at := range []time.Time{start, start, id.End} {
		value := AccountingResult{AsOf: at, WalletUSDT: amount(t, "100"), FundingCashDeltaUSDT: model.Zero(), UnrealizedPnLUSDT: model.Zero(), EquityUSDT: amount(t, "100")}
		for _, symbol := range id.Symbols {
			value.Valuations = append(value.Valuations, model.LongValuation{AsOf: at, Mark: model.LongMarkObservation{Symbol: symbol, ObservedAt: at, Price: amount(t, "1")}, Position: model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}, NotionalUSDT: model.Zero(), UnrealizedPnLUSDT: model.Zero()})
		}
		if i == 0 {
			result.Initial = value
		} else {
			result.Points = append(result.Points, BaselineReplayPoint{At: at, Terminal: i == 2, Accounting: &value})
		}
	}
	return result
}

// TestJournalFixedVector сверяет независимо рассчитанный полный хеш и часовую сетку.
func TestJournalFixedVector(t *testing.T) {
	result := flatJournalVector(t, 0)
	encoded, err := EncodeReplayJournal(result)
	if err != nil || encoded.SHA256() != "1543797449c75c7948a1c53a3a90047c57f208121276f9337b52a59151d08f9c" || result.Identity.RunID != "ac8439564538a0013e679e38fa04c124915eabddb8ef9e0ed749ad47a6d5cf8e" {
		t.Fatal("fixed independent vector", err)
	}
	if got, err := EncodeReplayJournal(flatJournalVector(t, time.Nanosecond)); !errors.Is(err, ErrReplayJournal) || !reflect.DeepEqual(got, EncodedReplayJournal{}) {
		t.Fatal("fractional complete run accepted", err)
	}
}
