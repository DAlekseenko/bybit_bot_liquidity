package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// displayStep — шаг усечения долей для человекочитаемого вида (8 знаков).
var displayStep, _ = model.ParseQty("0.00000001")

// jsonRatio — точная доля (числитель/знаменатель) и её усечённое к нулю представление.
type jsonRatio struct {
	Num        string `json:"num"`
	Den        string `json:"den"`
	Truncated8 string `json:"truncated_8dp"`
}

// jsonDrawdown — просадка в JSON; при недоступности заполнены только available и reason.
type jsonDrawdown struct {
	Available  bool       `json:"available"`
	Reason     string     `json:"reason,omitempty"`
	PeakAt     string     `json:"peak_at,omitempty"`
	TroughAt   string     `json:"trough_at,omitempty"`
	PeakUSDT   string     `json:"peak_usdt,omitempty"`
	TroughUSDT string     `json:"trough_usdt,omitempty"`
	AmountUSDT string     `json:"amount_usdt,omitempty"`
	Ratio      *jsonRatio `json:"ratio,omitempty"`
}

// jsonSummary — закрытый список полей JSON-отчёта; ничего вне него не выводится.
type jsonSummary struct {
	RunID                         string            `json:"run_id"`
	StrategyID                    string            `json:"strategy_id"`
	ProfileID                     string            `json:"profile_id"`
	ConfigHash                    string            `json:"config_hash"`
	DatasetHash                   string            `json:"dataset_hash"`
	Scenario                      string            `json:"scenario"`
	Start                         string            `json:"start"`
	End                           string            `json:"end"`
	InitialEquityUSDT             string            `json:"initial_equity_usdt"`
	FinalEquityUSDT               string            `json:"final_equity_usdt"`
	NetReturn                     jsonRatio         `json:"net_return"`
	TransferAdjustedReturn        jsonRatio         `json:"transfer_adjusted_return"`
	Totals                        map[string]string `json:"totals"`
	DailyDrawdown                 jsonDrawdown      `json:"daily_drawdown"`
	HourlyDrawdown                jsonDrawdown      `json:"hourly_drawdown"`
	TransferAdjustedDailyDrawdown jsonDrawdown      `json:"transfer_adjusted_daily_drawdown"`
	Identity                      map[string]string `json:"equity_identity"`
	FillCount                     int               `json:"fill_count"`
	EntryFillCount                int               `json:"entry_fill_count"`
	EntryIntentCount              int               `json:"entry_intent_count"`
	EntryDefinition               string            `json:"entry_definition"`
	Coverage                      jsonCoverage      `json:"coverage"`
}

// jsonCoverage — признаки полноты данных в JSON.
type jsonCoverage struct {
	FundingComplete     bool     `json:"funding_complete"`
	FundingZeroScenario bool     `json:"funding_zero_scenario"`
	MarkComplete        bool     `json:"mark_complete"`
	MarginHistory       bool     `json:"margin_history"`
	Notes               []string `json:"notes"`
}

// SummaryJSON сериализует отчёт в стабильный JSON с отступами и переводом строки в конце.
func SummaryJSON(s Summary) ([]byte, error) {
	var out jsonSummary
	var err error
	out.RunID, out.StrategyID, out.ProfileID = s.RunID, s.StrategyID, s.ProfileID
	out.ConfigHash, out.DatasetHash, out.Scenario = s.ConfigHash, s.DatasetHash, s.Scenario
	out.Start, out.End = timeText(s.Start), timeText(s.End)
	if out.InitialEquityUSDT, err = exactText(s.InitialEquityUSDT); err != nil {
		return nil, ErrInvalidReportInput
	}
	if out.FinalEquityUSDT, err = exactText(s.FinalEquityUSDT); err != nil {
		return nil, ErrInvalidReportInput
	}
	if out.NetReturn, err = ratioJSON(s.NetReturn); err != nil {
		return nil, err
	}
	if out.TransferAdjustedReturn, err = ratioJSON(s.TransferAdjustedReturn); err != nil {
		return nil, err
	}
	t := s.Totals
	if out.Totals, err = decimalMap(map[string]model.Decimal{
		"realized_pnl_usdt": t.RealizedPnLUSDT, "unrealized_pnl_start_usdt": t.UnrealizedPnLStartUSDT,
		"unrealized_pnl_end_usdt": t.UnrealizedPnLEndUSDT, "fees_usdt": t.FeesUSDT,
		"funding_usdt": t.FundingUSDT, "slippage_usdt": t.SlippageUSDT, "net_transfers_usdt": t.NetTransfersUSDT,
	}); err != nil {
		return nil, err
	}
	for _, d := range []struct {
		src Drawdown
		dst *jsonDrawdown
	}{{s.DailyDrawdown, &out.DailyDrawdown}, {s.HourlyDrawdown, &out.HourlyDrawdown},
		{s.TransferAdjustedDailyDrawdown, &out.TransferAdjustedDailyDrawdown}} {
		if *d.dst, err = drawdownJSON(d.src); err != nil {
			return nil, err
		}
	}
	if out.Identity, err = decimalMap(map[string]model.Decimal{"delta_equity_usdt": s.Identity.DeltaEquityUSDT,
		"explained_usdt": s.Identity.ExplainedUSDT, "difference_usdt": s.Identity.DifferenceUSDT}); err != nil {
		return nil, err
	}
	out.Identity["status"] = s.Identity.Status
	out.FillCount, out.EntryFillCount, out.EntryIntentCount = s.FillCount, s.EntryFillCount, s.EntryIntentCount
	out.EntryDefinition = s.EntryDefinition
	c := s.Coverage
	out.Coverage = jsonCoverage{c.FundingComplete, c.FundingZeroScenario, c.MarkComplete, c.MarginHistory,
		append([]string{}, c.Notes...)}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, ErrInvalidReportInput
	}
	return append(b, '\n'), nil
}

// FillsCSV выводит исполнения, отсортированные по времени и ID, в стабильный CSV.
func FillsCSV(fills []model.ConfirmedFill) ([]byte, error) {
	sorted := append([]model.ConfirmedFill(nil), fills...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].ExecutedAt.Equal(sorted[j].ExecutedAt) {
			return sorted[i].ExecutedAt.Before(sorted[j].ExecutedAt)
		}
		return sorted[i].ExecutionID < sorted[j].ExecutionID
	})
	rows := [][]string{{"execution_id", "symbol", "side", "executed_at", "quantity", "execution_price", "fee_usdt"}}
	for _, f := range sorted {
		vals, err := wireAll(f.Quantity, f.ExecutionPrice, f.FeeUSDT)
		if err != nil {
			return nil, err
		}
		rows = append(rows, append([]string{f.ExecutionID, f.Symbol, string(f.Side), timeText(f.ExecutedAt)}, vals...))
	}
	return writeCSV(rows)
}

// EquityCSV выводит ряд equity в исходном порядке со скорректированной на переводы колонкой.
func EquityCSV(points []EquityPoint) ([]byte, error) {
	rows := [][]string{{"at", "equity_usdt", "cumulative_transfers_usdt", "transfer_adjusted_equity_usdt"}}
	for _, p := range points {
		adj, err := pointValue(p, true)
		if err != nil {
			return nil, err
		}
		vals, err := wireAll(p.EquityUSDT, p.CumulativeTransfersUSDT, adj)
		if err != nil {
			return nil, err
		}
		rows = append(rows, append([]string{timeText(p.At)}, vals...))
	}
	return writeCSV(rows)
}

// writeCSV пишет строки с разделителем «,» и переводом строки \n.
func writeCSV(rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(rows); err != nil {
		return nil, ErrInvalidReportInput
	}
	return buf.Bytes(), nil
}

// exactText возвращает точное значение без скрытого округления: конечную
// десятичную строку, а для бесконечной дроби — сокращённую дробь «num/den».
// Правило денежного округления ещё не утверждено (SPEC), поэтому отчёт его не вводит.
func exactText(d model.Decimal) (string, error) {
	if s, err := d.WireDecimal(); err == nil {
		return s, nil
	}
	num, den, err := d.CanonicalRatio()
	if err != nil {
		return "", ErrInvalidReportInput
	}
	return num + "/" + den, nil
}

// wireAll превращает decimal значения в точные строки.
func wireAll(values ...model.Decimal) ([]string, error) {
	out := make([]string, len(values))
	for i, v := range values {
		s, err := exactText(v)
		if err != nil {
			return nil, ErrInvalidReportInput
		}
		out[i] = s
	}
	return out, nil
}

// decimalMap сериализует именованные decimal значения.
func decimalMap(values map[string]model.Decimal) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for k, v := range values {
		s, err := exactText(v)
		if err != nil {
			return nil, ErrInvalidReportInput
		}
		out[k] = s
	}
	return out, nil
}

// drawdownJSON переводит просадку в JSON-представление.
func drawdownJSON(d Drawdown) (jsonDrawdown, error) {
	if !d.Available {
		return jsonDrawdown{Reason: d.Reason}, nil
	}
	vals, err := wireAll(d.PeakUSDT, d.TroughUSDT, d.AmountUSDT)
	if err != nil {
		return jsonDrawdown{}, err
	}
	r, err := ratioJSON(d.Ratio)
	if err != nil {
		return jsonDrawdown{}, err
	}
	return jsonDrawdown{Available: true, PeakAt: timeText(d.PeakAt), TroughAt: timeText(d.TroughAt),
		PeakUSDT: vals[0], TroughUSDT: vals[1], AmountUSDT: vals[2], Ratio: &r}, nil
}

// ratioJSON возвращает точную долю и её усечение к нулю до 8 знаков.
func ratioJSON(d model.Decimal) (jsonRatio, error) {
	num, den, err := d.CanonicalRatio()
	if err != nil {
		return jsonRatio{}, ErrInvalidReportInput
	}
	sign, _ := d.Cmp(model.Zero())
	abs := d
	if sign < 0 {
		if abs, err = model.Zero().Sub(d); err != nil {
			return jsonRatio{}, ErrInvalidReportInput
		}
	}
	floor, err := model.FloorQty(abs, displayStep)
	if err != nil {
		return jsonRatio{}, ErrInvalidReportInput
	}
	text, err := floor.WireDecimal()
	if err != nil {
		return jsonRatio{}, ErrInvalidReportInput
	}
	if sign < 0 && text != "0" {
		text = "-" + text
	}
	return jsonRatio{Num: num, Den: den, Truncated8: text}, nil
}

// timeText форматирует время в UTC RFC3339 с наносекундами без лишних нулей.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
