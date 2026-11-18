package history

import (
	"context"
	"database/sql"
	"errors"

	"bybit_bot_liquidity/internal/momentum/storage"
)

// RunCandleImport выполняет ограниченное число шагов ранее созданного задания.
func RunCandleImport(ctx context.Context, db *sql.DB, source CandleImportPageSource, key storage.CandleImportWindow, maxSteps int) (storage.StoredCandleImport, error) {
	var last storage.StoredCandleImport
	if ctx == nil || db == nil || source == nil || maxSteps <= 0 {
		return last, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
	}
	for range maxSteps {
		job, err := StepCandleImport(ctx, db, source, key)
		if err != nil {
			return storage.StoredCandleImport{}, err
		}
		last = job
		switch job.Checkpoint.State {
		case "complete", "blocked":
			return last, nil
		}
	}
	return last, nil
}
