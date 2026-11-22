// Package dispatch отправляет заявки Momentum на Testnet с durable намерением:
// намерение фиксируется до отправки, отправка разрешена только владельцу
// исполнения, неоднозначный исход сверяется по тому же orderLinkId.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/momentum/execution"
)

var (
	// ErrDispatch — общая категория ошибок отправки.
	ErrDispatch = errors.New("momentum dispatch failed")
	// ErrNotSent означает, что заявка гарантированно не отправлялась.
	ErrNotSent = errors.New("momentum order was not sent")
	// ErrResidualBlocked означает, что по символу есть неурегулированная заявка.
	ErrResidualBlocked = errors.New("momentum residual blocked by unsettled order")
)

// Store сохраняет намерения и их состояния (реализация — storage).
type Store interface {
	RecordIntent(ctx context.Context, intent execution.OrderIntent) error
	MarkState(ctx context.Context, orderLinkID string, state execution.OrderState) error
}

// Permit разрешает одну отправку; реализует storage.ExecutionOwner.
type Permit interface {
	Permit(ctx context.Context) error
}

// Exchange — нужные диспетчеру операции Testnet-адаптера bybit.Client.
type Exchange interface {
	CreateMomentumOrder(ctx context.Context, request json.RawMessage) (bybit.OrderAck, error)
	LookupMomentumOrder(ctx context.Context, symbol, orderLinkID string) (bybit.OrderRecord, bool, error)
}

// Dispatcher связывает хранилище, право владельца и биржу.
type Dispatcher struct {
	Store    Store
	Owner    Permit
	Exchange Exchange
	Slippage execution.Slippage
}

// Outcome — итог отправки или сверки; Record заполнен, если заявку нашли на бирже.
type Outcome struct {
	State  execution.OrderState
	Ack    *bybit.OrderAck
	Record *bybit.OrderRecord
}

// Send фиксирует намерение, проверяет право, помечает SENT и отправляет заявку
// один раз. Любой сбой до отправки возвращает ErrNotSent. Неизвестный исход
// помечается UNKNOWN и сразу сверяется по тому же orderLinkId.
func (d Dispatcher) Send(ctx context.Context, intent execution.OrderIntent) (Outcome, error) {
	body, err := execution.EncodeCreateOrder(intent, d.Slippage)
	if err != nil {
		return Outcome{}, errors.Join(ErrDispatch, ErrNotSent, err)
	}
	if err := d.Store.RecordIntent(ctx, intent); err != nil {
		return Outcome{}, errors.Join(ErrDispatch, ErrNotSent, err)
	}
	if err := d.Owner.Permit(ctx); err != nil {
		return Outcome{}, errors.Join(ErrDispatch, ErrNotSent, err)
	}
	if err := d.Store.MarkState(ctx, intent.OrderLinkID, execution.StateSent); err != nil {
		return Outcome{}, errors.Join(ErrDispatch, ErrNotSent, err)
	}
	ack, err := d.Exchange.CreateMomentumOrder(ctx, body)
	switch {
	case err == nil:
		return Outcome{State: execution.StateAcknowledged, Ack: &ack},
			d.mark(ctx, intent.OrderLinkID, execution.StateAcknowledged)
	case errors.Is(err, bybit.ErrOrderLinkDuplicate):
		return d.Reconcile(ctx, intent)
	case errors.Is(err, bybit.ErrOrderRejected):
		if markErr := d.mark(ctx, intent.OrderLinkID, execution.StateRejected); markErr != nil {
			return Outcome{State: execution.StateRejected}, markErr
		}
		return Outcome{State: execution.StateRejected}, errors.Join(ErrDispatch, err)
	}
	if markErr := d.mark(ctx, intent.OrderLinkID, execution.StateUnknown); markErr != nil {
		return Outcome{State: execution.StateUnknown}, markErr
	}
	return d.Reconcile(ctx, intent)
}

// Reconcile ищет заявку по тому же orderLinkId. Найдена — ACKNOWLEDGED (дальнейший
// статус ведёт Lifecycle); не найдена — остаётся UNKNOWN, fill не выводится.
func (d Dispatcher) Reconcile(ctx context.Context, intent execution.OrderIntent) (Outcome, error) {
	record, found, err := d.Exchange.LookupMomentumOrder(ctx, intent.Symbol, intent.OrderLinkID)
	if err != nil || !found {
		if err != nil {
			err = errors.Join(ErrDispatch, err)
		}
		return Outcome{State: execution.StateUnknown}, err
	}
	return Outcome{State: execution.StateAcknowledged, Record: &record}, d.mark(ctx, intent.OrderLinkID, execution.StateAcknowledged)
}

// Retry повторяет отправку UNKNOWN-заявки с тем же orderLinkId, только если сверка
// подтвердила, что биржа её не знает. Биржа отвергает дубликат orderLinkId, поэтому
// повтор не создаёт вторую заявку.
func (d Dispatcher) Retry(ctx context.Context, intent execution.OrderIntent) (Outcome, error) {
	outcome, err := d.Reconcile(ctx, intent)
	if err != nil || outcome.State != execution.StateUnknown {
		return outcome, err
	}
	return d.Send(ctx, intent)
}

// mark сохраняет состояние после ответа биржи; сбой не отменяет факт отправки.
func (d Dispatcher) mark(ctx context.Context, orderLinkID string, state execution.OrderState) error {
	if err := d.Store.MarkState(ctx, orderLinkID, state); err != nil {
		return errors.Join(ErrDispatch, err)
	}
	return nil
}

// ResidualAllowed разрешает новую заявку по символу, только если все прежние
// заявки урегулированы: UNKNOWN или ожидающие fills блокируют новый остаток.
// Остаток после урегулированной заявки планируется новой revision TradePlan и
// поэтому получает новый orderLinkId (execution.OrderLinkID зависит от revision).
func ResidualAllowed(previous []execution.Lifecycle) error {
	for _, l := range previous {
		if !l.Settled() {
			return errors.Join(ErrDispatch, ErrResidualBlocked)
		}
	}
	return nil
}
