package paperwire

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// InputRecord хранит только проверенный immutable вход, без PrepareCycle proof.
type InputRecord struct {
	payload     []byte
	header      CycleBinding
	values      CycleDocument
	observation []byte
}

// Payload возвращает отдельную копию канонического входа.
func (r InputRecord) Payload() []byte { return bytes.Clone(r.payload) }

// Hash возвращает hash входа; нулевой record не имеет identity.
func (r InputRecord) Hash() string { return r.header.InputHash }

// Header возвращает проверенные связи immutable входа.
func (r InputRecord) Header() CycleBinding { return r.header }

// Values возвращает отдельные maps, страницы, строки и fills.
func (r InputRecord) Values() CycleDocument {
	d := r.values
	d.Profile.Parameters.Rules = maps.Clone(d.Profile.Parameters.Rules)
	d.Profile.FundingSchedules = maps.Clone(d.Profile.FundingSchedules)
	d.Observation, _ = DecodeObservation(r.observation)
	d.Before, d.Cutoff, d.ExpectedAfter = stateIn(stateOut(d.Before)), stateIn(stateOut(d.Cutoff)), stateIn(stateOut(d.ExpectedAfter))
	d.Fills = slices.Clone(d.Fills)
	return d
}

// EncodeInputRecord проверяет форму, связи и ограничения до создания immutable записи.
func EncodeInputRecord(d CycleDocument) (InputRecord, error) {
	for _, s := range []PortfolioState{d.Before, d.Cutoff, d.ExpectedAfter} {
		if len(s.Positions) > MaxArrayItems {
			return InputRecord{}, resource()
		}
	}
	if len(d.Fills) > MaxActualFillRecords {
		return InputRecord{}, resource()
	}
	measured, err := MeasureObservation(d.Observation)
	if err != nil {
		return InputRecord{}, err
	}
	cfg, cfgHash, err := d.Run.Config.Snapshot()
	if err != nil {
		return InputRecord{}, errors.Join(ErrPaperWire, err)
	}
	plan, err := model.EncodeTradePlan(d.Plan)
	if err != nil {
		return InputRecord{}, errors.Join(ErrPaperWire, err)
	}
	if len(cfg) > MaxConfigBytes || len(plan) > MaxPlanBytes {
		return InputRecord{}, resource()
	}
	profile, profileHash, err := EncodeProfile(d.Profile)
	if err != nil {
		return InputRecord{}, err
	}
	if err = validateInput(d, cfgHash); err != nil {
		return InputRecord{}, err
	}
	w := inputWire{Format: "momentum/paper-cycle-input/v1", Scope: scopeWire{"paper", d.Run.AccountID, d.Run.StrategyID},
		Sequence: strconv.FormatUint(d.Sequence, 10), PreviousInputHash: d.PreviousInputHash, PreviousResolutionHash: d.PreviousResolutionHash,
		Purpose: purposeWire{d.Purpose.Kind, d.Purpose.CommandID, strconv.FormatUint(d.Purpose.ControlRevision, 10)},
		Run:     runWire{d.Run.RunID, instant{d.Run.ScheduledAt}, d.Run.OnboardingID, base64.StdEncoding.EncodeToString(cfg), cfgHash},
		Model:   profile, ModelHash: profileHash,
		PlanPayload: base64.StdEncoding.EncodeToString(plan), PlanHash: d.Plan.Hash()}
	expected, err := countInput(d, w, measured)
	if err != nil {
		return InputRecord{}, err
	}
	observation, err := EncodeObservation(d.Observation)
	if err != nil {
		return InputRecord{}, err
	}
	w.Observation, w.Before, w.Cutoff, w.ExpectedAfter, w.Fills = observation, stateOut(d.Before), stateOut(d.Cutoff), stateOut(d.ExpectedAfter), mapValues(d.Fills, fillOut)
	raw, err := encodeFields(inputFields(&w))
	if err == nil {
		err = verifyMeasurement(raw, expected)
	}
	if err != nil {
		return InputRecord{}, err
	}
	r := InputRecord{payload: raw, observation: observation, values: d, header: CycleBinding{Scope: Scope{"paper", d.Run.AccountID, d.Run.StrategyID},
		RunID: d.Run.RunID, ConfigHash: cfgHash, ModelHash: profileHash, InputHash: payloadHash(raw), PlanHash: d.Plan.Hash(),
		PreviousInputHash: d.PreviousInputHash, PreviousResolutionHash: d.PreviousResolutionHash, Sequence: d.Sequence, ScheduledAt: d.Run.ScheduledAt.UTC()}}
	r.values = r.Values()
	return r, nil
}

// DecodeInputRecord требует exact canonical bytes и повторяет весь binding contract.
func DecodeInputRecord(raw []byte) (InputRecord, error) {
	if err := inputPreflight(raw); err != nil {
		return InputRecord{}, err
	}
	var w inputWire
	if err := decodeFields(raw, inputFields(&w)); err != nil {
		return InputRecord{}, fmt.Errorf("decode input: %w", err)
	}
	cfgBytes, err := embedded(w.Run.ConfigPayload, MaxConfigBytes)
	if err != nil {
		return InputRecord{}, err
	}
	cfg, err := config.Parse(cfgBytes)
	if err != nil {
		return InputRecord{}, errors.Join(ErrPaperWire, err)
	}
	canonical, hash, err := cfg.Snapshot()
	if err != nil || hash != w.Run.ConfigHash || !bytes.Equal(canonical, cfgBytes) {
		return InputRecord{}, invalid("config bytes/hash")
	}
	planBytes, err := embedded(w.PlanPayload, MaxPlanBytes)
	if err != nil {
		return InputRecord{}, err
	}
	plan, err := model.DecodeTradePlan(planBytes)
	if err != nil {
		return InputRecord{}, errors.Join(ErrPaperWire, err)
	}
	p, err := DecodeProfile(w.Model)
	if err != nil {
		return InputRecord{}, err
	}
	sequence, e1 := strconv.ParseUint(w.Sequence, 10, 64)
	revision, e2 := strconv.ParseUint(w.Purpose.ControlRevision, 10, 64)
	if e1 != nil || e2 != nil {
		return InputRecord{}, invalid("unsigned input")
	}
	observation, err := DecodeObservation(w.Observation)
	if err != nil {
		return InputRecord{}, err
	}
	d := CycleDocument{Run: RunBinding{w.Run.RunID, w.Scope.AccountID, w.Scope.StrategyID, w.Run.OnboardingID, w.Run.ScheduledAt.Time, cfg},
		Sequence: sequence, PreviousInputHash: w.PreviousInputHash, PreviousResolutionHash: w.PreviousResolutionHash,
		Purpose: Purpose{w.Purpose.Kind, w.Purpose.CommandID, revision}, Profile: p, Observation: observation,
		Before: stateIn(w.Before), Cutoff: stateIn(w.Cutoff), ExpectedAfter: stateIn(w.ExpectedAfter), Plan: plan, Fills: mapValues(w.Fills, fillIn)}
	r, err := EncodeInputRecord(d)
	if err != nil {
		return InputRecord{}, err
	}
	if !bytes.Equal(r.payload, raw) {
		return InputRecord{}, invalid("input canonical bytes")
	}
	return r, nil
}

// embedded ограничивает encoded length до base64 allocation и exact decoded bytes после него.
func embedded(text string, limit int) ([]byte, error) {
	if err := encodedGuard(text, limit); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, errors.Join(ErrPaperWire, err)
	}
	if len(raw) > limit {
		return nil, resource()
	}
	if base64.StdEncoding.EncodeToString(raw) != text {
		return nil, invalid("base64 canonical form")
	}
	return raw, nil
}
