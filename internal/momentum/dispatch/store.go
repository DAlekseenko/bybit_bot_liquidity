package dispatch

import (
	"context"
	"database/sql"

	"bybit_bot_liquidity/internal/momentum/execution"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// DBStore реализует Store через функции storage для одной пары environment/account.
type DBStore struct {
	DB                     *sql.DB
	Environment, AccountID string
}

// RecordIntent сохраняет намерение в PENDING через storage.RecordOrderIntent.
func (s DBStore) RecordIntent(ctx context.Context, in execution.OrderIntent) error {
	return storage.RecordOrderIntent(ctx, s.DB, storage.OrderIntent{IntentID: in.IntentID, Environment: in.Environment,
		AccountID: in.AccountID, OrderLinkID: in.OrderLinkID, RunID: in.RunID, PlanRevision: in.PlanRevision,
		PlanHash: in.PlanHash, Symbol: in.Symbol, Side: in.Side, ReduceOnly: in.ReduceOnly, Quantity: in.Quantity})
}

// MarkState сохраняет состояние через storage.SetOrderIntentState.
func (s DBStore) MarkState(ctx context.Context, orderLinkID string, state execution.OrderState) error {
	return storage.SetOrderIntentState(ctx, s.DB, s.Environment, s.AccountID, orderLinkID, string(state))
}

// Acknowledge делегирует storage.AcknowledgeOrderIntent.
func (s DBStore) Acknowledge(ctx context.Context, orderLinkID string) error {
	_, err := storage.AcknowledgeOrderIntent(ctx, s.DB, s.Environment, s.AccountID, orderLinkID)
	return err
}

// MarkUnknown делегирует storage.MarkOrderIntentUnknown.
func (s DBStore) MarkUnknown(ctx context.Context, orderLinkID string) error {
	_, err := storage.MarkOrderIntentUnknown(ctx, s.DB, s.Environment, s.AccountID, orderLinkID)
	return err
}
