package paperwire

import (
	"bytes"
	"slices"
	"strconv"
)

// ResolutionRecord хранит декларацию результата без финансового proof.
type ResolutionRecord struct {
	payload []byte
	header  CycleBinding
	values  ResolutionDocument
}

// Payload возвращает отдельную копию канонического receipt.
func (r ResolutionRecord) Payload() []byte { return bytes.Clone(r.payload) }

// Hash возвращает hash собственных receipt bytes; нулевой record не имеет identity.
func (r ResolutionRecord) Hash() string {
	if len(r.payload) == 0 {
		return ""
	}
	return payloadHash(r.payload)
}

// Header возвращает binding supplied immutable input.
func (r ResolutionRecord) Header() CycleBinding { return r.header }

// Values возвращает отдельные массивы и карту фактического состояния.
func (r ResolutionRecord) Values() ResolutionDocument {
	d := r.values
	d.Fills = slices.Clone(d.Fills)
	d.Remaining = slices.Clone(d.Remaining)
	d.ActualAfter = stateIn(stateOut(d.ActualAfter))
	return d
}

// EncodeResolutionRecord проверяет форму и точный budget до копирования DTO.
func EncodeResolutionRecord(d ResolutionDocument, input InputRecord) (ResolutionRecord, error) {
	if len(input.payload) == 0 {
		return ResolutionRecord{}, invalid("zero input")
	}
	if len(d.Fills) > MaxActualFillRecords || len(d.Remaining) > MaxArrayItems || len(d.ActualAfter.Positions) > MaxArrayItems {
		return ResolutionRecord{}, resource()
	}
	w := resolutionWire{Format: "momentum/paper-cycle-resolution/v1", InputHash: d.InputHash, RunID: d.RunID, PlanHash: d.PlanHash,
		Disposition: d.Kind, ResolvedAt: instant{d.ResolvedAt}, ControlRevision: strconv.FormatUint(d.ControlRevision, 10), CommandID: d.CommandID}
	measured, err := countResolution(d, w)
	if err != nil {
		return ResolutionRecord{}, err
	}
	if err = validateResolution(d, input); err != nil {
		return ResolutionRecord{}, err
	}
	w.Fills, w.Remaining, w.ActualAfter = mapValues(d.Fills, fillOut), mapValues(d.Remaining, remainderOut), stateOut(d.ActualAfter)
	raw, err := encodeFields(resolutionFields(&w))
	if err == nil {
		err = verifyMeasurement(raw, measured)
	}
	if err != nil {
		return ResolutionRecord{}, err
	}
	r := ResolutionRecord{payload: raw, header: input.Header(), values: d}
	r.values = r.Values()
	return r, nil
}

// DecodeResolutionRecord ограничивает streaming decode и требует exact canonical bytes.
func DecodeResolutionRecord(raw []byte, input InputRecord) (ResolutionRecord, error) {
	if len(input.payload) == 0 {
		return ResolutionRecord{}, invalid("zero input")
	}
	if err := resolutionPreflight(raw); err != nil {
		return ResolutionRecord{}, err
	}
	var w resolutionWire
	if err := decodeFields(raw, resolutionFields(&w)); err != nil {
		return ResolutionRecord{}, err
	}
	revision, err := strconv.ParseUint(w.ControlRevision, 10, 64)
	if err != nil {
		return ResolutionRecord{}, invalid("unsigned resolution")
	}
	d := ResolutionDocument{InputHash: w.InputHash, RunID: w.RunID, PlanHash: w.PlanHash, Kind: w.Disposition,
		ResolvedAt: w.ResolvedAt.Time, ControlRevision: revision, CommandID: w.CommandID,
		Fills: mapValues(w.Fills, fillIn), Remaining: mapValues(w.Remaining, remainderIn), ActualAfter: stateIn(w.ActualAfter)}
	r, err := EncodeResolutionRecord(d, input)
	if err != nil {
		return ResolutionRecord{}, err
	}
	if w.Format != "momentum/paper-cycle-resolution/v1" || !bytes.Equal(r.payload, raw) {
		return ResolutionRecord{}, invalid("resolution canonical bytes")
	}
	return r, nil
}
