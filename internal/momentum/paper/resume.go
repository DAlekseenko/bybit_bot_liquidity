package paper

import (
	"context"
	"database/sql"

	s "bybit_bot_liquidity/internal/momentum/storage"
)

// ResumePaperCommand передаёт полный доказанный снимок или воспроизводит прежнюю запись.
func ResumePaperCommand(ctx context.Context, db *sql.DB, in s.PaperCommandRequest, owner *s.ExecutionOwner, p RecoveryProof) (s.PaperResumeResult, error) {
	return s.ResumePaperCommand(ctx, db, in, owner, p.recoveryStamp(), p.CheckData)
}
