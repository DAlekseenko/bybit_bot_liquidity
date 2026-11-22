package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/momentum/execution"
	"bybit_bot_liquidity/internal/momentum/model"
)

// fakeStore записывает вызовы и может отказывать на заданном шаге.
type fakeStore struct {
	calls      []string
	failRecord bool
	failState  execution.OrderState
}

// RecordIntent фиксирует вызов или отказывает.
func (s *fakeStore) RecordIntent(context.Context, execution.OrderIntent) error {
	s.calls = append(s.calls, "record")
	if s.failRecord {
		return errors.New("db unavailable")
	}
	return nil
}

// MarkState фиксирует состояние или отказывает на failState.
func (s *fakeStore) MarkState(_ context.Context, _ string, state execution.OrderState) error {
	s.calls = append(s.calls, string(state))
	if state == s.failState {
		return errors.New("db unavailable")
	}
	return nil
}

// fakeOwner возвращает заданную ошибку права отправки.
type fakeOwner struct{ err error }

// Permit возвращает заданную ошибку.
func (o fakeOwner) Permit(context.Context) error { return o.err }

// fakeExchange отвечает по очереди заданными ошибками create и наличием заявки при lookup.
type fakeExchange struct {
	createErrs []error
	found      []bool
	sends      int
	lookups    int
	bodies     []string
}

// CreateMomentumOrder возвращает следующую ошибку или ack.
func (e *fakeExchange) CreateMomentumOrder(_ context.Context, body json.RawMessage) (bybit.OrderAck, error) {
	e.sends++
	e.bodies = append(e.bodies, string(body))
	if len(e.createErrs) > 0 {
		err := e.createErrs[0]
		e.createErrs = e.createErrs[1:]
		if err != nil {
			return bybit.OrderAck{}, err
		}
	}
	return bybit.OrderAck{OrderID: "o1"}, nil
}

// LookupMomentumOrder возвращает следующее значение found.
func (e *fakeExchange) LookupMomentumOrder(_ context.Context, _, _ string, sentAt time.Time) (bybit.OrderRecord, bool, error) {
	e.lookups++
	if sentAt.IsZero() {
		return bybit.OrderRecord{}, false, errors.New("send time required")
	}
	found := len(e.found) > 0 && e.found[0]
	if len(e.found) > 0 {
		e.found = e.found[1:]
	}
	return bybit.OrderRecord{OrderID: "o1", Status: "New"}, found, nil
}

// testIntent строит валидное намерение покупки на Testnet.
func testIntent(t *testing.T) execution.OrderIntent {
	t.Helper()
	hash := strings.Repeat("ab", 32)
	link, err := execution.OrderLinkID(hash, 1, "BTCUSDT", execution.LegIncrease)
	qty, qErr := model.ParseQty("0.004")
	if err != nil || qErr != nil {
		t.Fatal(err, qErr)
	}
	return execution.OrderIntent{IntentID: "i1", OrderLinkID: link, Environment: "testnet", AccountID: "acc", RunID: "run",
		PlanRevision: 1, PlanHash: hash, Symbol: "BTCUSDT", Leg: execution.LegIncrease, Side: model.FillBuy, Quantity: qty}
}

// TestSendBoundaries проверяет, что сбой любой границы до отправки не отправляет заявку.
func TestSendBoundaries(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		store *fakeStore
		owner fakeOwner
	}{
		"db-before-send": {&fakeStore{failRecord: true}, fakeOwner{}},
		"lock-lost":      {&fakeStore{}, fakeOwner{errors.New("ownership lost")}},
		"db-mark-sent":   {&fakeStore{failState: execution.StateSent}, fakeOwner{}},
	} {
		ex := &fakeExchange{}
		_, err := Dispatcher{Store: tc.store, Owner: tc.owner, Exchange: ex}.Send(ctx, testIntent(t))
		if !errors.Is(err, ErrNotSent) || ex.sends != 0 {
			t.Fatalf("%s: err=%v sends=%d", name, err, ex.sends)
		}
	}
	bad := testIntent(t)
	bad.OrderLinkID = "foreign"
	ex := &fakeExchange{}
	if _, err := (Dispatcher{Store: &fakeStore{}, Owner: fakeOwner{}, Exchange: ex}).Send(ctx, bad); !errors.Is(err, ErrNotSent) || ex.sends != 0 {
		t.Fatalf("invalid intent sent: %v", err)
	}
}

// TestSendOutcomes проверяет ack, отказ, неизвестный исход и дубликат orderLinkId.
func TestSendOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		err   error
		found []bool
		state execution.OrderState
		calls string
	}{
		{"ack", nil, nil, execution.StateAcknowledged, "record,SENT,ACKNOWLEDGED"},
		{"reject", bybit.ErrOrderRejected, nil, execution.StateRejected, "record,SENT,REJECTED"},
		{"unknown-absent", bybit.ErrPostOutcomeUnknown, []bool{false}, execution.StateUnknown, "record,SENT,UNKNOWN"},
		{"unknown-found", bybit.ErrPostOutcomeUnknown, []bool{true}, execution.StateAcknowledged, "record,SENT,UNKNOWN,ACKNOWLEDGED"},
		{"duplicate", bybit.ErrOrderLinkDuplicate, []bool{true}, execution.StateAcknowledged, "record,SENT,ACKNOWLEDGED"},
	} {
		store, ex := &fakeStore{}, &fakeExchange{createErrs: []error{tc.err}, found: tc.found}
		out, _ := Dispatcher{Store: store, Owner: fakeOwner{}, Exchange: ex}.Send(ctx, testIntent(t))
		if out.State != tc.state || strings.Join(store.calls, ",") != tc.calls || ex.sends != 1 {
			t.Fatalf("%s: state=%s calls=%v sends=%d", tc.name, out.State, store.calls, ex.sends)
		}
	}
}

// TestRetryAndResidual проверяет повтор того же orderLinkId только при подтверждённом отсутствии и блок остатка.
func TestRetryAndResidual(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExchange{found: []bool{true}}
	d := Dispatcher{Store: &fakeStore{}, Owner: fakeOwner{}, Exchange: ex}
	if out, err := d.Retry(ctx, testIntent(t), time.Unix(1700000000, 0)); err != nil || out.State != execution.StateAcknowledged || ex.sends != 0 {
		t.Fatalf("found order must not be resent: %+v %v", out, err)
	}
	ex = &fakeExchange{found: []bool{false}}
	d.Exchange = ex
	if out, err := d.Retry(ctx, testIntent(t), time.Unix(1700000000, 0)); err != nil || out.State != execution.StateAcknowledged || ex.sends != 1 ||
		!strings.Contains(ex.bodies[0], testIntent(t).OrderLinkID) {
		t.Fatalf("absent order must be resent once with the same link: %+v %v %d", out, err, ex.sends)
	}
	qty, _ := model.ParseQty("0.004")
	unknown, _ := execution.NewLifecycle("l1", qty)
	unknown, _, _ = unknown.Apply(execution.Evidence{Kind: execution.EvidenceTimeout})
	if err := ResidualAllowed([]execution.Lifecycle{unknown}); !errors.Is(err, ErrResidualBlocked) {
		t.Fatalf("unknown must block residual: %v", err)
	}
	cancelled, _, _ := unknown.Apply(execution.Evidence{Kind: execution.EvidenceStatus, OrderID: "o1", Status: "Cancelled", CumExecQty: model.Zero()})
	if err := ResidualAllowed([]execution.Lifecycle{cancelled}); err != nil {
		t.Fatalf("settled cancel must allow residual: %v", err)
	}
}
