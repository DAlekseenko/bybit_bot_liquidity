package paperwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// inputFixture создаёт полный вход без подразумеваемых финансовых defaults.
func inputFixture(t *testing.T) CycleDocument {
	t.Helper()
	cfg, err := config.Parse([]byte(`schema_version: 1
strategy_id: momentum-v1
profile: baseline_delta_1x
profile_version: v1
model_id: roc30
mode: paper
start_policy: now
symbols: [ALT123USDT, BTCUSDT, ETHUSDT]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
initial_equity_usdt: "10000"
data_source: {kind: bybit_testnet_public, environment: testnet}
`))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 1, 6, 0, 0, 0, 123000, time.UTC)
	state := PortfolioState{at, at, decimalFixture(t, "100"), decimalFixture(t, "101"), map[string]model.LongPosition{"ALT123USDT": {Quantity: decimalFixture(t, "1"), CostBasisUSDT: decimalFixture(t, "3")}}}
	d := CycleDocument{Run: RunBinding{"run-1", "paper/account", "momentum-v1", "onboarding-1", at, cfg}, Sequence: 1, Purpose: Purpose{Kind: "rebalance"}, Profile: profileFixture(t), Observation: observationFixture(t), Before: stateIn(stateOut(state)), Cutoff: stateIn(stateOut(state)), ExpectedAfter: stateIn(stateOut(state))}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		d.Profile.Parameters.Rules[symbol] = d.Profile.Parameters.Rules["ALT123USDT"]
		d.Profile.FundingSchedules[symbol] = d.Profile.FundingSchedules["ALT123USDT"]
		for _, state := range []*PortfolioState{&d.Before, &d.Cutoff, &d.ExpectedAfter} {
			state.Positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
		}
	}
	d.Fills = []model.ConfirmedFill{{ExecutionID: "fill-1", Symbol: "ALT123USDT", Side: model.FillBuy, ExecutedAt: at, Quantity: decimalFixture(t, "1"), ExecutionPrice: decimalFixture(t, "3"), FeeUSDT: model.Zero()}}
	replanInput(t, &d, nil, nil)
	return d
}

// replanInput пересобирает immutable план, разрешая точечную негативную мутацию.
func replanInput(t *testing.T, d *CycleDocument, snapshot func(*model.SnapshotInput), metadata func(*model.PlanMetadata)) {
	t.Helper()
	v := d.Run.Config.Values()
	_, hash, err := d.Run.Config.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	s := model.SnapshotInput{Environment: "paper", AccountID: d.Run.AccountID, ConfigVersion: v.ProfileVersion, CapturedAt: d.Run.ScheduledAt, Equity: d.Cutoff.EquityUSDT, Prices: map[string]model.Decimal{"ALT123USDT": decimalFixture(t, "3")}, Positions: map[string]model.Decimal{"ALT123USDT": d.Cutoff.Positions["ALT123USDT"].Quantity}}
	m := model.PlanMetadata{StrategyID: d.Run.StrategyID, RunID: d.Run.RunID, ProfileID: v.Profile, ModelID: v.ModelID, FormulaID: "arbitrary-explicit-formula", ConfigHash: hash, ScheduledAt: d.Run.ScheduledAt, EquityCutoffAt: d.Run.ScheduledAt, Revision: 1}
	for _, symbol := range v.Symbols {
		if symbol != "ALT123USDT" {
			s.Prices[symbol] = decimalFixture(t, "3")
			s.Positions[symbol] = d.Cutoff.Positions[symbol].Quantity
		}
	}
	if snapshot != nil {
		snapshot(&s)
	}
	if metadata != nil {
		metadata(&m)
	}
	snap, err := model.NewPortfolioSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	d.Plan, err = model.NewTradePlan(model.PlanInput{Snapshot: snap, Metadata: m, Targets: []model.Target{{Symbol: "ALT123USDT", Quantity: decimalFixture(t, "2")}, {Symbol: "BTCUSDT", Quantity: model.Zero()}, {Symbol: "ETHUSDT", Quantity: model.Zero()}}})
	if err != nil {
		t.Fatal(err)
	}
}

// inputRecord требует успешный публичный constructor и hash exact bytes.
func inputRecord(t *testing.T, d CycleDocument) InputRecord {
	t.Helper()
	r, err := EncodeInputRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	if r.Hash() != payloadHash(r.Payload()) {
		t.Fatal("exact byte hash")
	}
	return r
}

// TestInputRoundtrip проверяет полный wire, provenance и header без финансового proof.
func TestInputRoundtrip(t *testing.T) {
	d := inputFixture(t)
	r := inputRecord(t, d)
	out, err := DecodeInputRecord(r.Payload())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Payload(), out.Payload()) || !reflect.DeepEqual(r.Header(), out.Header()) {
		t.Fatal("roundtrip identity")
	}
	expected, _ := EncodeObservation(d.Observation)
	var w inputWire
	if err := decodeFields(r.Payload(), inputFields(&w)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, w.Observation) || !bytes.Equal(expected, observationRoundtrip(t, out.Values().Observation)) {
		t.Fatal("source provenance/order changed")
	}
	cfg, hash, _ := d.Run.Config.Snapshot()
	plan, _ := model.EncodeTradePlan(d.Plan)
	if w.Run.ConfigPayload != base64.StdEncoding.EncodeToString(cfg) || w.Run.ConfigHash != hash || w.PlanPayload != base64.StdEncoding.EncodeToString(plan) || w.PlanHash != d.Plan.Hash() {
		t.Fatal("embedded identity")
	}
	h := r.Header()
	if h.Scope != (Scope{"paper", d.Run.AccountID, d.Run.StrategyID}) || h.Sequence != 1 || h.InputHash != r.Hash() || h.ConfigHash != hash || h.PlanHash != d.Plan.Hash() {
		t.Fatal("header bindings")
	}
	profile, profileHash, _ := EncodeProfile(d.Profile)
	if !bytes.Equal(profile, w.Model) || profileHash != h.ModelHash {
		t.Fatal("profile canonical object")
	}
	d.Observation = Observation{CapturedAt: d.Run.ScheduledAt, SourceClock: d.Run.ScheduledAt}
	inputRecord(t, d) // Неполная market-история допустима как record, не PreparedCycle.
}

// TestInputIsolation проверяет constructor/accessor aliases всех изменяемых вложений.
func TestInputIsolation(t *testing.T) {
	d := inputFixture(t)
	r := inputRecord(t, d)
	raw := r.Payload()
	hash := r.Hash()
	mutate := func(v *CycleDocument) {
		delete(v.Profile.Parameters.Rules, "ALT123USDT")
		delete(v.Profile.FundingSchedules, "ALT123USDT")
		delete(v.Before.Positions, "ALT123USDT")
		delete(v.Cutoff.Positions, "ALT123USDT")
		delete(v.ExpectedAfter.Positions, "ALT123USDT")
		v.Observation.TradeWindows[0].Page.Candles[0].Symbol = "MUTATED"
		v.Observation.MarkPages[0].Candles[0].Close = model.Zero()
		v.Observation.FundingPages[0].Events[0].Rate = model.Zero()
		v.Observation.TradeWindows[0].Symbol = "OTHER"
		v.Fills[0].ExecutionID = "changed"
	}
	mutate(&d)
	v := r.Values()
	mutate(&v)
	b := r.Payload()
	b[0] = '['
	h := r.Header()
	h.Scope.AccountID = "changed"
	if r.Hash() != hash || !bytes.Equal(raw, r.Payload()) {
		t.Fatal("mutation changed record")
	}
	fresh := inputRecord(t, r.Values())
	if !bytes.Equal(raw, fresh.Payload()) {
		t.Fatal("next accessor changed")
	}
	for _, empty := range []bool{false, true} {
		d = inputFixture(t)
		d.Fills = nil
		if empty {
			d.Fills = []model.ConfirmedFill{}
		}
		r = inputRecord(t, d)
		if (r.Values().Fills == nil) != (d.Fills == nil) || !bytes.Equal(r.Payload(), inputRecord(t, r.Values()).Payload()) {
			t.Fatal("nil/empty fills")
		}
	}
}

// TestInputBindings проверяет таблицу всех самостоятельных identity и shape нарушений.
func TestInputBindings(t *testing.T) {
	cases := map[string]func(*CycleDocument){
		"account":            func(d *CycleDocument) { d.Run.AccountID = "live/account" },
		"account-empty":      func(d *CycleDocument) { d.Run.AccountID = "paper/" },
		"strategy":           func(d *CycleDocument) { d.Run.StrategyID = "other" },
		"run":                func(d *CycleDocument) { d.Run.RunID = "" },
		"sequence-zero":      func(d *CycleDocument) { d.Sequence = 0 },
		"first-prev-input":   func(d *CycleDocument) { d.PreviousInputHash = strings.Repeat("a", 64) },
		"first-prev-receipt": func(d *CycleDocument) { d.PreviousResolutionHash = strings.Repeat("a", 64) },
		"later-missing":      func(d *CycleDocument) { d.Sequence = 2 },
		"later-uppercase": func(d *CycleDocument) {
			d.Sequence = 2
			d.PreviousInputHash = strings.Repeat("A", 64)
			d.PreviousResolutionHash = strings.Repeat("b", 64)
		},
		"purpose":            func(d *CycleDocument) { d.Purpose.Kind = "other" },
		"rebalance-command":  func(d *CycleDocument) { d.Purpose.CommandID = "stop" },
		"rebalance-revision": func(d *CycleDocument) { d.Purpose.ControlRevision = 1 },
		"close-id":           func(d *CycleDocument) { d.Purpose = Purpose{Kind: "close-all", ControlRevision: 1} },
		"close-revision":     func(d *CycleDocument) { d.Purpose = Purpose{Kind: "close-all", CommandID: "close"} },
		"time-nanos":         func(d *CycleDocument) { d.Run.ScheduledAt = d.Run.ScheduledAt.Add(time.Nanosecond) },
		"cutoff-asof":        func(d *CycleDocument) { d.Cutoff.AsOf = d.Cutoff.AsOf.Add(time.Second) },
		"cutoff-funding":     func(d *CycleDocument) { d.Cutoff.FundingThrough = d.Cutoff.FundingThrough.Add(time.Second) },
		"cutoff-equity":      func(d *CycleDocument) { d.Cutoff.EquityUSDT = model.Zero() },
		"cutoff-quantity": func(d *CycleDocument) {
			d.Cutoff.Positions["ALT123USDT"] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
		},
		"before-universe": func(d *CycleDocument) { delete(d.Before.Positions, "ALT123USDT") },
		"after-universe":  func(d *CycleDocument) { delete(d.ExpectedAfter.Positions, "ALT123USDT") },
		"state-invalid":   func(d *CycleDocument) { d.Before.WalletUSDT = model.Decimal{} },
		"negative-basis": func(d *CycleDocument) {
			p := d.Before.Positions["ALT123USDT"]
			p.CostBasisUSDT = decimalFixture(t, "-1")
			d.Before.Positions["ALT123USDT"] = p
		},
		"profile-rules":    func(d *CycleDocument) { delete(d.Profile.Parameters.Rules, "ALT123USDT") },
		"profile-schedule": func(d *CycleDocument) { delete(d.Profile.FundingSchedules, "ALT123USDT") },
		"fill-symbol":      func(d *CycleDocument) { d.Fills[0].Symbol = "OTHERUSDT" },
		"fill-side":        func(d *CycleDocument) { d.Fills[0].Side = "INVALID" },
		"fill-id":          func(d *CycleDocument) { d.Fills[0].ExecutionID = "" },
		"fill-qty":         func(d *CycleDocument) { d.Fills[0].Quantity = model.Zero() },
		"fill-price":       func(d *CycleDocument) { d.Fills[0].ExecutionPrice = model.Zero() },
		"fill-fee":         func(d *CycleDocument) { d.Fills[0].FeeUSDT = model.Decimal{} },
		"fill-time":        func(d *CycleDocument) { d.Fills[0].ExecutedAt = time.Time{} },
		"duplicate-request": func(d *CycleDocument) {
			d.Observation.TradeWindows = append(d.Observation.TradeWindows, d.Observation.TradeWindows[0])
		},
		"invalid-utf8": func(d *CycleDocument) { d.Run.OnboardingID = string([]byte{255}) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := inputFixture(t)
			change(&d)
			if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperWire) {
				t.Fatal("accepted", err)
			}
		})
	}
	snapshots := map[string]func(*model.SnapshotInput){
		"environment": func(s *model.SnapshotInput) { s.Environment = "backtest" },
		"account":     func(s *model.SnapshotInput) { s.AccountID = "paper/other" },
		"version":     func(s *model.SnapshotInput) { s.ConfigVersion = "other" },
		"captured":    func(s *model.SnapshotInput) { s.CapturedAt = s.CapturedAt.Add(time.Second) },
		"prices":      func(s *model.SnapshotInput) { s.Prices["EXTRAUSDT"] = decimalFixture(t, "1") },
		"positions":   func(s *model.SnapshotInput) { s.Positions["EXTRAUSDT"] = model.Zero() },
	}
	for name, change := range snapshots {
		t.Run("snapshot-"+name, func(t *testing.T) {
			d := inputFixture(t)
			replanInput(t, &d, change, nil)
			if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperWire) {
				t.Fatal("accepted", err)
			}
		})
	}
	metadata := map[string]func(*model.PlanMetadata){
		"strategy": func(m *model.PlanMetadata) { m.StrategyID = "other" }, "run": func(m *model.PlanMetadata) { m.RunID = "other" },
		"config": func(m *model.PlanMetadata) { m.ConfigHash = strings.Repeat("a", 64) }, "profile": func(m *model.PlanMetadata) { m.ProfileID = "other" },
		"model": func(m *model.PlanMetadata) { m.ModelID = "other" }, "scheduled": func(m *model.PlanMetadata) { m.ScheduledAt = m.ScheduledAt.Add(time.Second) },
		"cutoff": func(m *model.PlanMetadata) { m.EquityCutoffAt = m.EquityCutoffAt.Add(time.Second) }, "revision": func(m *model.PlanMetadata) { m.Revision = 2; m.PreviousHash = strings.Repeat("a", 64) },
	}
	for name, change := range metadata {
		t.Run("metadata-"+name, func(t *testing.T) {
			d := inputFixture(t)
			replanInput(t, &d, nil, change)
			if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperWire) {
				t.Fatal("accepted", err)
			}
		})
	}
	d := inputFixture(t)
	d.Sequence = math.MaxUint64
	d.PreviousInputHash = strings.Repeat("a", 64)
	d.PreviousResolutionHash = strings.Repeat("b", 64)
	d.Purpose = Purpose{"close-all", "cmd-1", math.MaxUint64}
	inputRecord(t, d)
}

// TestInputStrictDecode проверяет корневую, вложенную и embedded каноничность.
func TestInputStrictDecode(t *testing.T) {
	r := inputRecord(t, inputFixture(t))
	raw := r.Payload()
	mutations := map[string][]byte{
		"nil": nil, "empty": {}, "null": []byte("null"), "whitespace": append([]byte(" "), raw...), "trailing": append(bytes.Clone(raw), []byte("{}")...),
		"unknown":           bytes.Replace(raw, []byte(`"format":`), []byte(`"unknown":0,"format":`), 1),
		"missing":           bytes.Replace(raw, []byte(`"sequence":"1",`), nil, 1),
		"duplicate":         bytes.Replace(raw, []byte(`"sequence":"1"`), []byte(`"sequence":"1","sequence":"1"`), 1),
		"order":             bytes.Replace(raw, []byte(`"purpose":{"kind":"rebalance","command_id":"","control_revision":"0"}`), []byte(`"purpose":{"command_id":"","kind":"rebalance","control_revision":"0"}`), 1),
		"scope":             bytes.Replace(raw, []byte(`"environment":"paper"`), []byte(`"environment":"testnet"`), 1),
		"sequence-spelling": bytes.Replace(raw, []byte(`"sequence":"1"`), []byte(`"sequence":"01"`), 1),
		"ratio":             bytes.Replace(raw, []byte(`"numerator":"0","denominator":"1"`), []byte(`"numerator":"0","denominator":"2"`), 1),
		"ratio-zero":        bytes.Replace(raw, []byte(`"denominator":"1"`), []byte(`"denominator":"0"`), 1),
	}
	for name, payload := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInputRecord(payload); !errors.Is(err, ErrPaperWire) {
				t.Fatal("accepted", err)
			}
		})
	}
	for _, name := range []string{"format", "model-hash", "plan-hash", "config-hash", "config-truncated", "plan-corrupt", "profile-unknown", "nested-missing", "revision-spelling"} {
		t.Run(name, func(t *testing.T) {
			var w inputWire
			if err := decodeFields(raw, inputFields(&w)); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "format":
				w.Format = "other"
			case "model-hash":
				w.ModelHash = strings.Repeat("b", 64)
			case "plan-hash":
				w.PlanHash = strings.Repeat("b", 64)
			case "config-hash":
				w.Run.ConfigHash = strings.Repeat("b", 64)
			case "config-truncated":
				w.Run.ConfigPayload = base64.StdEncoding.EncodeToString([]byte(`{"strategy_id":"momentum-v1"}`))
			case "plan-corrupt":
				b, _ := base64.StdEncoding.DecodeString(w.PlanPayload)
				b[len(b)-1] = 'X'
				w.PlanPayload = base64.StdEncoding.EncodeToString(b)
			case "profile-unknown":
				w.Model = bytes.Replace(w.Model, []byte(`"format":`), []byte(`"other":0,"format":`), 1)
			case "nested-missing":
				w.Observation = bytes.Replace(w.Observation, []byte(`"DailyBars":7,`), nil, 1)
			case "revision-spelling":
				w.Purpose.ControlRevision = "00"
			}
			b, err := encodeFields(inputFields(&w))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeInputRecord(b); !errors.Is(err, ErrPaperWire) {
				t.Fatal("accepted", err)
			}
		})
	}
	if (InputRecord{}).Hash() != "" || (InputRecord{}).Payload() != nil {
		t.Fatal("zero record identity")
	}
}

// TestInputFullRat проверяет бесконечную decimal-дробь и весь4096bit диапазон.
func TestInputFullRat(t *testing.T) {
	d := inputFixture(t)
	n := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4096), big.NewInt(1))
	den := new(big.Int).Lsh(big.NewInt(1), 4095)
	huge, err := reducedAmount(n, den)
	if err != nil {
		t.Fatal(err)
	}
	third, err := decimalFixture(t, "1").Quo(decimalFixture(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	d.Before.WalletUSDT = huge
	d.ExpectedAfter.EquityUSDT = huge
	p := d.Cutoff.Positions["ALT123USDT"]
	p.CostBasisUSDT = third
	d.Cutoff.Positions["ALT123USDT"] = p
	r := inputRecord(t, d)
	out, err := DecodeInputRecord(r.Payload())
	if err != nil {
		t.Fatal(err)
	}
	if !sameDecimal(out.Values().Cutoff.Positions["ALT123USDT"].CostBasisUSDT, third) || !sameDecimal(out.Values().Before.WalletUSDT, huge) || !bytes.Equal(r.Payload(), out.Payload()) {
		t.Fatal("ratio truncated")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(r.Payload(), &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root["input_sha256"]; exists {
		t.Fatal("self hash")
	}
}

// TestInputConfigAndModel проверяет independent runtime model hash и canonical config bindings.
func TestInputConfigAndModel(t *testing.T) {
	d := inputFixture(t)
	a := inputRecord(t, d)
	d.Profile.Parameters.FeeRate = decimalFixture(t, "0.001")
	b := inputRecord(t, d)
	if a.Header().ModelHash == b.Header().ModelHash || a.Header().ConfigHash != b.Header().ConfigHash || a.Header().PlanHash != b.Header().PlanHash {
		t.Fatal("runtime model identity conflated with config ModelID")
	}
	for _, change := range []string{"strategy", "version", "model", "profile", "symbols", "backtest"} {
		t.Run(change, func(t *testing.T) {
			d := inputFixture(t)
			raw, _, _ := d.Run.Config.Snapshot()
			switch change {
			case "strategy":
				raw = bytes.Replace(raw, []byte(`"strategy_id":"momentum-v1"`), []byte(`"strategy_id":"other"`), 1)
			case "version":
				raw = bytes.Replace(raw, []byte(`"profile_version":"v1"`), []byte(`"profile_version":"v2"`), 1)
			case "model":
				raw = bytes.Replace(raw, []byte(`"model_id":"roc30"`), []byte(`"model_id":"other"`), 1)
			case "profile":
				raw = bytes.Replace(raw, []byte(`"profile":"baseline_delta_1x"`), []byte(`"profile":"isolated_weekly_reset"`), 1)
			case "symbols":
				raw = bytes.Replace(raw, []byte(`"BTCUSDT"`), []byte(`"OTHERUSDT"`), 1)
			case "backtest":
				raw = bytes.Replace(raw, []byte(`"mode":"paper"`), []byte(`"mode":"backtest"`), 1)
				raw = bytes.Replace(raw, []byte(`"kind":"bybit_testnet_public","environment":"testnet"`), []byte(`"kind":"frozen_dataset","environment":"testnet","dataset_id":"dataset","manifest_sha256":"`+strings.Repeat("a", 64)+`"`), 1)
			}
			cfg, err := config.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			d.Run.Config = cfg
			if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperWire) {
				t.Fatal("config mismatch accepted", err)
			}
		})
	}
	d = inputFixture(t)
	plan, err := model.NewTradePlan(model.PlanInput{Snapshot: d.Plan.Snapshot(), Metadata: d.Plan.Metadata(), Targets: d.Plan.Targets()[:1]})
	if err != nil {
		t.Fatal(err)
	}
	d.Plan = plan
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperWire) {
		t.Fatal("partial target universe", err)
	}
}

// TestInputRatDecodeFailures проверяет malformed/overflow ratios через реальный Input decoder.
func TestInputRatDecodeFailures(t *testing.T) {
	r := inputRecord(t, inputFixture(t))
	for _, ratio := range []string{`{"numerator":"1","denominator":"0"}`, `{"numerator":"2","denominator":"6"}`, `{"numerator":"01","denominator":"1"}`, `{"numerator":"1.0","denominator":"1"}`, `{"numerator":"` + new(big.Int).Lsh(big.NewInt(1), 4096).String() + `","denominator":"1"}`} {
		var w inputWire
		if err := decodeFields(r.Payload(), inputFields(&w)); err != nil {
			t.Fatal(err)
		}
		raw, err := encodeFields(inputFields(&w))
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte(`"WalletUSDT":{"numerator":"100","denominator":"1"}`), []byte(`"WalletUSDT":`+ratio), 1)
		if _, err := DecodeInputRecord(raw); !errors.Is(err, ErrPaperWire) {
			t.Fatal("accepted bad Rat", err)
		}
	}
}
