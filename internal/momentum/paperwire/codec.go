package paperwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

type ruleWire struct {
	Symbol              string `json:"symbol"`
	QtyStep             amount `json:"qty_step"`
	MinQty              amount `json:"min_qty"`
	MaxMarketQty        amount `json:"max_market_qty"`
	MinIncreaseNotional amount `json:"min_increase_notional"`
}
type scheduleWire struct {
	Symbol   string  `json:"symbol"`
	Anchor   instant `json:"anchor"`
	Interval string  `json:"interval_ns"`
}
type profileWire struct {
	Format                                                              string
	FeeRate, SlippageRate                                               amount
	MarkMaxAge, SourceMaxAge, DataDeadline, Poll, RPCTimeout            string
	ExecutionReference, ExecutionTimePolicy, MarkReference, FundingMode string
	Rules                                                               []ruleWire
	Schedules                                                           []scheduleWire
}

// profileFields задаёт единственный порядок нормативных ключей модели.
func profileFields(w *profileWire) []field {
	return []field{
		{"format", &w.Format},
		{"fee_rate", &w.FeeRate},
		{"slippage_rate", &w.SlippageRate},
		{"mark_max_age_ns", &w.MarkMaxAge},
		{"source_max_age_ns", &w.SourceMaxAge},
		{"data_deadline_ns", &w.DataDeadline},
		{"poll_ns", &w.Poll},
		{"rpc_timeout_ns", &w.RPCTimeout},
		{"execution_reference", &w.ExecutionReference},
		{"execution_time_policy", &w.ExecutionTimePolicy},
		{"mark_reference", &w.MarkReference},
		{"funding_mode", &w.FundingMode},
		{"rules", &w.Rules},
		{"funding_schedules", &w.Schedules},
	}
}

// finiteAmount проверяет представимость и знак без выбора финансовых параметров.
func finiteAmount(value model.Decimal, positive bool) bool {
	cmp, err := value.Cmp(model.Zero())
	if err != nil || cmp < 0 || positive && cmp == 0 {
		return false
	}
	_, err = value.WireDecimal()
	return err == nil
}

// durationString проверяет положительность явно переданной длительности.
func durationString(value time.Duration) (string, error) {
	if value <= 0 {
		return "", ErrPaperWire
	}
	return strconv.FormatInt(int64(value), 10), nil
}

// parseDuration требует канонический положительный decimal int64.
func parseDuration(text string) (time.Duration, error) {
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != text {
		return 0, ErrPaperWire
	}
	return time.Duration(value), nil
}

// payloadHash вычисляет идентичность канонических байтов без самоссылочного поля.
func payloadHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EncodeProfile сохраняет явно заданную конечную модель без numeric defaults.
func EncodeProfile(p Profile) ([]byte, string, error) {
	one, _ := model.ParseAmount("1")
	slip, err := p.Parameters.SlippageRate.Cmp(one)
	if err != nil || slip >= 0 || !finiteAmount(p.Parameters.FeeRate, false) || !finiteAmount(p.Parameters.SlippageRate, false) || p.ExecutionReference != "closed-hourly-trade-close/v1" || p.DataDeadline > 7*24*time.Hour {
		return nil, "", invalid("profile")
	}
	if len(p.Parameters.Rules) == 0 || len(p.Parameters.Rules) != len(p.FundingSchedules) {
		return nil, "", invalid("profile")
	}
	if len(p.Parameters.Rules) > MaxArrayItems {
		return nil, "", errors.Join(invalid("profile rule count"), ErrPaperResourceLimit)
	}
	// Канонический корень занимает 42 tokens; rule с четырьмя ratios — 32,
	// schedule — 8. Деление проверяет весь документ без переполнения и копий.
	const profileBaseTokens, profileSymbolTokens = 42, 40
	if len(p.Parameters.Rules) > (MaxJSONTokens-profileBaseTokens)/profileSymbolTokens {
		return nil, "", errors.Join(invalid("profile token count"), ErrPaperResourceLimit)
	}
	values := []time.Duration{p.Parameters.MarkMaxAge, p.SourceMaxAge, p.DataDeadline, p.Poll, p.RPCTimeout}
	durations := make([]string, len(values))
	for i, value := range values {
		durations[i], err = durationString(value)
		if err != nil {
			return nil, "", err
		}
	}
	symbols := make([]string, 0, len(p.Parameters.Rules))
	for symbol := range p.Parameters.Rules {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	w := profileWire{Format: "momentum/paper-model/v1", FeeRate: amount{p.Parameters.FeeRate}, SlippageRate: amount{p.Parameters.SlippageRate},
		MarkMaxAge: durations[0], SourceMaxAge: durations[1], DataDeadline: durations[2], Poll: durations[3], RPCTimeout: durations[4],
		ExecutionReference: p.ExecutionReference, ExecutionTimePolicy: "plan-scheduled-at/v1", MarkReference: "previous-closed-hour-mark/v1", FundingMode: "observed",
		Rules: make([]ruleWire, 0, len(symbols)), Schedules: make([]scheduleWire, 0, len(symbols))}
	encodedSize := 0
	for _, symbol := range symbols {
		rule := p.Parameters.Rules[symbol]
		schedule, ok := p.FundingSchedules[symbol]
		interval, intervalErr := durationString(schedule.Interval)
		if !profileSymbol(symbol) || !ok || intervalErr != nil || schedule.Interval%time.Millisecond != 0 || schedule.Anchor.Nanosecond()%int(time.Millisecond) != 0 || !finiteAmount(rule.QtyStep, true) || !finiteAmount(rule.MinQty, true) || !finiteAmount(rule.MaxMarketQty, true) || !finiteAmount(rule.MinIncreaseNotional, false) {
			return nil, "", invalid("profile")
		}
		if len(symbol) > MaxCanonicalBytes/2 {
			return nil, "", errors.Join(invalid("profile symbols"), ErrPaperResourceLimit)
		}
		encodedRule := ruleWire{symbol, amount{rule.QtyStep}, amount{rule.MinQty}, amount{rule.MaxMarketQty}, amount{rule.MinIncreaseNotional}}
		encodedSchedule := scheduleWire{symbol, instant{schedule.Anchor}, interval}
		ruleBytes, ruleErr := json.Marshal(encodedRule)
		scheduleBytes, scheduleErr := json.Marshal(encodedSchedule)
		if ruleErr != nil || scheduleErr != nil {
			return nil, "", invalid("profile schedule")
		}
		encodedSize += len(ruleBytes) + len(scheduleBytes) + 2
		if encodedSize > MaxCanonicalBytes {
			return nil, "", errors.Join(invalid("profile bytes"), ErrPaperResourceLimit)
		}
		w.Rules = append(w.Rules, encodedRule)
		w.Schedules = append(w.Schedules, encodedSchedule)
	}
	raw, err := encodeFields(profileFields(&w))
	if err != nil {
		return nil, "", err
	}
	return raw, payloadHash(raw), nil
}

// DecodeProfile отвергает любое неканоническое или несовместимое представление.
func DecodeProfile(raw []byte) (Profile, error) {
	var w profileWire
	if err := decodeFields(raw, profileFields(&w)); err != nil {
		return Profile{}, fmt.Errorf("decode profile: %w", err)
	}
	p := Profile{ExecutionReference: w.ExecutionReference, FundingSchedules: make(map[string]FundingSchedule)}
	p.Parameters.FeeRate, p.Parameters.SlippageRate = w.FeeRate.value, w.SlippageRate.value
	durations := []*time.Duration{&p.Parameters.MarkMaxAge, &p.SourceMaxAge, &p.DataDeadline, &p.Poll, &p.RPCTimeout}
	for i, text := range []string{w.MarkMaxAge, w.SourceMaxAge, w.DataDeadline, w.Poll, w.RPCTimeout} {
		value, err := parseDuration(text)
		if err != nil {
			return Profile{}, fmt.Errorf("decode profile: %w", err)
		}
		*durations[i] = value
	}
	p.Parameters.Rules = make(map[string]planner.MarketDeltaRules, len(w.Rules))
	for _, r := range w.Rules {
		if _, exists := p.Parameters.Rules[r.Symbol]; exists {
			return Profile{}, ErrPaperWire
		}
		p.Parameters.Rules[r.Symbol] = planner.MarketDeltaRules{QtyStep: r.QtyStep.value, MinQty: r.MinQty.value, MaxMarketQty: r.MaxMarketQty.value, MinIncreaseNotional: r.MinIncreaseNotional.value}
	}
	for _, s := range w.Schedules {
		interval, err := parseDuration(s.Interval)
		if err != nil {
			return Profile{}, fmt.Errorf("decode profile: %w", err)
		}
		if _, exists := p.FundingSchedules[s.Symbol]; exists {
			return Profile{}, ErrPaperWire
		}
		p.FundingSchedules[s.Symbol] = FundingSchedule{s.Anchor.Time, interval}
	}
	canonical, _, err := EncodeProfile(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Profile{}, ErrPaperWire
	}
	return p, nil
}

// profileSymbol проверяет один переданный символ без фиксированного размера вселенной.
func profileSymbol(symbol string) bool {
	base, ok := strings.CutSuffix(symbol, "USDT")
	if !ok || base == "" {
		return false
	}
	for _, c := range base {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
