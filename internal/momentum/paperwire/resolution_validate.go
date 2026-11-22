package paperwire

import (
	"strings"

	"bybit_bot_liquidity/internal/momentum/model"
)

// validateResolution проверяет декларативную форму, не доказывая финансовую истинность.
func validateResolution(d ResolutionDocument, input InputRecord) error {
	h := input.Header()
	if d.InputHash != h.InputHash || d.RunID != h.RunID || d.PlanHash != h.PlanHash || !microInstant(d.ResolvedAt) || d.ResolvedAt.Before(h.ScheduledAt) {
		return invalid("resolution binding/time")
	}
	completed := d.Kind == "COMPLETED" && d.CommandID == "" && d.ControlRevision == 0
	aborted := d.Kind == "ABORTED" && strings.TrimSpace(d.CommandID) != "" && d.ControlRevision > 0
	if !completed && !aborted {
		return invalid("resolution disposition/control")
	}
	symbols := input.values.Run.Config.Values().Symbols
	if !validateState(d.ActualAfter, symbols) || !validateFills(d.Fills, symbols) {
		return invalid("resolution state/fills")
	}
	seen := make(map[int]bool, len(d.Remaining))
	for _, r := range d.Remaining {
		if r.IntentIndex < 0 || seen[r.IntentIndex] || !containsSymbol(symbols, r.Symbol) || r.Side != model.FillBuy && r.Side != model.FillSell {
			return invalid("resolution remainder identity")
		}
		seen[r.IntentIndex] = true
		for _, value := range []model.Decimal{r.Requested, r.Confirmed, r.Remaining} {
			cmp, err := value.Cmp(model.Zero())
			if err != nil || cmp < 0 {
				return invalid("resolution remainder amount")
			}
		}
		if completed && !sameDecimal(r.Remaining, model.Zero()) {
			return invalid("completed residual")
		}
	}
	return nil
}
