package paper

import (
	"bytes"

	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// BuildPaperResolution строит канонический итог из текущих исполнений и сверяет сохранённый receipt.
func BuildPaperResolution(p PreparedCycle, d ResolutionDisposition) storage.PaperResolutionBuilder {
	return func(f storage.PaperRunFacts) (paperwire.ResolutionRecord, error) {
		if err := CheckPaperFill(p)(f); err != nil {
			return paperwire.ResolutionRecord{}, err
		}
		fills, err := f.ConfirmedFills()
		if err != nil {
			return paperwire.ResolutionRecord{}, err
		}
		fresh, err := SealResolution(p, fills, d)
		if err != nil {
			return paperwire.ResolutionRecord{}, err
		}
		if f.Completed {
			saved, err := DecodeResolution([]byte(f.Completion.Reason), p)
			if err != nil {
				return paperwire.ResolutionRecord{}, err
			}
			if !bytes.Equal(saved.Payload(), fresh.Payload()) {
				return paperwire.ResolutionRecord{}, ErrPaperProgress
			}
		}
		return fresh.record, nil
	}
}
