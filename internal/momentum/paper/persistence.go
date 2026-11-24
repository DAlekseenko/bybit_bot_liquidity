package paper

import "bybit_bot_liquidity/internal/momentum/storage"

// CheckPaperFill связывает каждую фактическую проводку с исходным намерением проекции.
func CheckPaperFill(p PreparedCycle) storage.PaperFillCheck {
	return func(f storage.PaperRunFacts) error {
		if !f.InputFound || p.Binding() != f.Input.Header() || p.Hash() != f.Input.Hash() {
			return ErrPaperProgress
		}
		fills, err := f.ConfirmedFills()
		if err != nil {
			return err
		}
		progress, err := ProjectProgress(p, fills)
		if err != nil {
			return err
		}
		rows := f.Fills()
		for i, fill := range progress.fills {
			link, err := storage.PaperIntentIdentity(p.Hash(), progress.indices[i])
			if err != nil || rows[fill.ExecutionID].OrderLinkID != link {
				return ErrPaperProgress
			}
		}
		return nil
	}
}
