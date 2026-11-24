package paper

import (
	"context"
	"database/sql"

	s "bybit_bot_liquidity/internal/momentum/storage"
)

// recoveryStamp переносит привязки фактического восстановления к границе storage.
func (p RecoveryProof) recoveryStamp() s.PaperRecoveryStamp {
	head, found := p.Head()
	return s.PaperRecoveryStamp{Scope: p.Scope(), ConfigHash: p.ConfigHash(), RecoveryID: p.ID(), OwnerSessionID: p.OwnerSessionID(),
		DataHash: p.DataHash(), ControlRevision: p.ControlRevision(), Head: head, HeadFound: found}
}

// ReconcilePaperOwner подтверждает восстановленное состояние владельца перед виртуальным исполнением.
func ReconcilePaperOwner(ctx context.Context, db *sql.DB, o *s.ExecutionOwner, p RecoveryProof) error {
	return s.ReconcilePaperOwner(ctx, db, o, p.recoveryStamp(), p.CheckData)
}
