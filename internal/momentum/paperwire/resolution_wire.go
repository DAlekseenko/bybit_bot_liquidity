package paperwire

import (
	"bytes"
	"encoding/json"

	"bybit_bot_liquidity/internal/momentum/model"
)

type resolutionWire struct {
	Format, InputHash, RunID, PlanHash, Disposition string
	ResolvedAt                                      instant
	ControlRevision, CommandID                      string
	Fills                                           []fillWire
	Remaining                                       []remainderWire
	ActualAfter                                     stateWire
}
type remainderWire struct {
	IntentIndex                     int
	Symbol                          string
	Side                            model.FillSide
	Requested, Confirmed, Remaining amount
}

// resolutionFields задаёт нормативный порядок корневых полей receipt.
func resolutionFields(w *resolutionWire) []field {
	return []field{{"format", &w.Format}, {"input_sha256", &w.InputHash}, {"run_id", &w.RunID}, {"plan_sha256", &w.PlanHash},
		{"disposition", &w.Disposition}, {"resolved_at", &w.ResolvedAt}, {"control_revision", &w.ControlRevision},
		{"command_id", &w.CommandID}, {"fills", &w.Fills}, {"remaining", &w.Remaining}, {"actual_after", &w.ActualAfter}}
}

// remainderOut сохраняет полную форму остатка, включая ненулевое remaining.
func remainderOut(r IntentRemainder) remainderWire {
	return remainderWire{r.IntentIndex, r.Symbol, r.Side, amount{r.Requested}, amount{r.Confirmed}, amount{r.Remaining}}
}

// remainderIn восстанавливает остаток без финансового пересчёта.
func remainderIn(r remainderWire) IntentRemainder {
	return IntentRemainder{r.IntentIndex, r.Symbol, r.Side, r.Requested.value, r.Confirmed.value, r.Remaining.value}
}

// countResolution считает точный receipt до stateOut/mapValues/clone.
func countResolution(d ResolutionDocument, w resolutionWire) (recordMeter, error) {
	m := recordMeter{}
	fields := resolutionFields(&w)
	m.add(2+len(fields)-1+len(fields), 2)
	m.depth = 1
	for _, f := range fields {
		m.text(f.name)
		switch f.name {
		case "resolved_at":
			m.value(d.ResolvedAt)
		case "actual_after":
			m.state(d.ActualAfter)
		case "fills":
			if d.Fills == nil {
				m.add(4, 1)
				break
			}
			m.array(len(d.Fills), func(i int) {
				x := d.Fills[i]
				m.object(field{"ExecutionID", x.ExecutionID}, field{"Symbol", x.Symbol}, field{"Side", string(x.Side)}, field{"ExecutedAt", x.ExecutedAt}, field{"Quantity", x.Quantity}, field{"ExecutionPrice", x.ExecutionPrice}, field{"FeeUSDT", x.FeeUSDT})
			})
		case "remaining":
			if d.Remaining == nil {
				m.add(4, 1)
				break
			}
			m.array(len(d.Remaining), func(i int) {
				x := d.Remaining[i]
				m.object(field{"IntentIndex", x.IntentIndex}, field{"Symbol", x.Symbol}, field{"Side", string(x.Side)}, field{"Requested", x.Requested}, field{"Confirmed", x.Confirmed}, field{"Remaining", x.Remaining})
			})
		default:
			m.text(*f.value.(*string))
		}
	}
	return m, m.err
}

// resolutionPreflight ограничивает receipt массивы до типизированного unmarshal.
func resolutionPreflight(raw []byte) error {
	if err := preflight(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return scanResolution(d, "")
}

// scanResolution считает fills/remaining без создания DTO arrays.
func scanResolution(d *json.Decoder, path string) error {
	t, err := d.Token()
	if err != nil {
		return ErrPaperWire
	}
	c, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	count := 0
	for d.More() {
		child := path
		if c == '{' {
			k, e := d.Token()
			if e != nil {
				return ErrPaperWire
			}
			child = path + "/" + k.(string)
		} else {
			count++
			if path == "/fills" && count > MaxActualFillRecords || path == "/remaining" && count > MaxArrayItems {
				return resource()
			}
		}
		if err := scanResolution(d, child); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
