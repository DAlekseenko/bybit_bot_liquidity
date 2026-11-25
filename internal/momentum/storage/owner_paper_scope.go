package storage

import (
	"context"
	"database/sql"
)

// PermitPaperScope проверяет принадлежность handle до подготовки виртуального запуска.
func (o *ExecutionOwner) PermitPaperScope(ctx context.Context, db *sql.DB, scope ControlScope) error {
	if o == nil || db == nil || o.db != db || !paperControlScope(scope) {
		return ErrOwnerArgument
	}
	id := o.id
	if scope != (ControlScope{id.Environment, id.AccountID, id.StrategyID}) {
		return ErrOwnerArgument
	}
	return o.Permit(ctx)
}
