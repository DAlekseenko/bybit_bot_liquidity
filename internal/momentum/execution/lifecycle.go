package execution

import (
	"errors"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrLifecycle — общая категория ошибок жизненного цикла заявки.
	ErrLifecycle = errors.New("momentum order lifecycle failed")
	// ErrLifecycleConflict означает противоречивые свидетельства биржи; нужна сверка.
	ErrLifecycleConflict = errors.New("momentum order evidence conflicts")
)

// OrderState — состояние заявки; значения совпадают с momentum.order_intents.state.
type OrderState string

// Состояния заявки: FILLED, CANCELLED и REJECTED терминальные.
const (
	StatePending         OrderState = "PENDING"
	StateSent            OrderState = "SENT"
	StateAcknowledged    OrderState = "ACKNOWLEDGED"
	StatePartiallyFilled OrderState = "PARTIALLY_FILLED"
	StateFilled          OrderState = "FILLED"
	StateCancelled       OrderState = "CANCELLED"
	StateRejected        OrderState = "REJECTED"
	StateUnknown         OrderState = "UNKNOWN"
)

// EvidenceKind — вид свидетельства о заявке.
type EvidenceKind string

const (
	// EvidenceSent — запрос отправлен, ответа ещё нет.
	EvidenceSent EvidenceKind = "SENT"
	// EvidenceTimeout — исход отправки неизвестен (таймаут, разрыв после отправки).
	EvidenceTimeout EvidenceKind = "TIMEOUT"
	// EvidenceAccepted — биржа приняла заявку и вернула OrderID.
	EvidenceAccepted EvidenceKind = "ACCEPTED"
	// EvidenceStatus — статус заявки Bybit (WS, realtime или history) и cumExecQty.
	EvidenceStatus EvidenceKind = "STATUS"
	// EvidenceFill — подтверждённое исполнение.
	EvidenceFill EvidenceKind = "FILL"
	// EvidenceNotFound — один endpoint заявку не нашёл; состояние не меняется.
	EvidenceNotFound EvidenceKind = "NOT_FOUND"
)

// Evidence — одно наблюдение о заявке.
type Evidence struct {
	Kind       EvidenceKind
	OrderID    string // обязателен для ACCEPTED, STATUS и FILL
	Status     string // статус Bybit: New, PartiallyFilled, Filled, Cancelled, PartiallyFilledCanceled, Deactivated, Rejected, Untriggered
	CumExecQty model.Decimal
	Fill       model.ConfirmedFill
}

// Lifecycle — неизменяемое состояние одной заявки. FILLED выводится только
// из подтверждённых fills на полный объём, поэтому ложный терминал невозможен.
type Lifecycle struct {
	orderLinkID string
	quantity    model.Decimal
	phase       OrderState
	orderID     string
	fills       map[string]model.ConfirmedFill
	filled      model.Decimal
	exchangeCum model.Decimal
	cancelled   bool
	rejected    bool
}

// NewLifecycle создаёт заявку в состоянии PENDING с положительным объёмом.
func NewLifecycle(orderLinkID string, quantity model.Decimal) (Lifecycle, error) {
	if c, err := quantity.Cmp(model.Zero()); err != nil || c <= 0 || orderLinkID == "" {
		return Lifecycle{}, errors.Join(ErrLifecycle, ErrInvalidIntentInput)
	}
	return Lifecycle{orderLinkID: orderLinkID, quantity: quantity, phase: StatePending, filled: model.Zero(),
		exchangeCum: model.Zero(), fills: map[string]model.ConfirmedFill{}}, nil
}

// State выводит состояние из фазы и подтверждённых fills. Lifecycle, созданный не
// через NewLifecycle (например, нулевое значение), всегда UNKNOWN.
func (l Lifecycle) State() OrderState {
	if !l.valid() {
		return StateUnknown
	}
	full, _ := l.filled.Cmp(l.quantity)
	some, _ := l.filled.Cmp(model.Zero())
	switch {
	case full == 0:
		return StateFilled
	case l.cancelled:
		return StateCancelled
	case l.rejected:
		return StateRejected
	case some > 0:
		return StatePartiallyFilled
	}
	return l.phase
}

// valid проверяет, что Lifecycle создан NewLifecycle: link задан, объёмы сравнимы.
func (l Lifecycle) valid() bool {
	qty, qErr := l.quantity.Cmp(model.Zero())
	_, fErr := l.filled.Cmp(model.Zero())
	_, cErr := l.exchangeCum.Cmp(model.Zero())
	return l.orderLinkID != "" && qErr == nil && qty > 0 && fErr == nil && cErr == nil
}

// Filled возвращает сумму подтверждённых fills.
func (l Lifecycle) Filled() model.Decimal { return l.filled }

// OrderID возвращает идентификатор биржи, если он известен.
func (l Lifecycle) OrderID() string { return l.orderID }

// AwaitingFills — биржа сообщает больше исполненного, чем подтверждено fills.
func (l Lifecycle) AwaitingFills() bool {
	c, _ := l.exchangeCum.Cmp(l.filled)
	return c > 0
}

// Settled — состояние окончательное и все исполнения подтверждены.
func (l Lifecycle) Settled() bool {
	s := l.State()
	return (s == StateFilled || s == StateCancelled || s == StateRejected) && !l.AwaitingFills()
}

// Apply применяет свидетельство и возвращает новое состояние; changed=false — no-op.
func (l Lifecycle) Apply(e Evidence) (next Lifecycle, changed bool, err error) {
	next = l
	if !l.valid() {
		return l, false, errors.Join(ErrLifecycle, ErrInvalidIntentInput)
	}
	switch e.Kind {
	case EvidenceSent:
		if l.phase == StatePending {
			next.phase = StateSent
		}
	case EvidenceTimeout:
		if l.phase == StatePending || l.phase == StateSent {
			next.phase = StateUnknown
		}
	case EvidenceAccepted:
		if err := next.bindOrder(e.OrderID); err != nil {
			return l, false, err
		}
		if l.phase != StateAcknowledged {
			next.phase = StateAcknowledged
		}
	case EvidenceStatus:
		if err := next.applyStatus(e); err != nil {
			return l, false, err
		}
	case EvidenceFill:
		if err := next.bindOrder(e.OrderID); err != nil {
			return l, false, err
		}
		if err := next.applyFill(e.Fill); err != nil {
			return l, false, err
		}
	case EvidenceNotFound:
	default:
		return l, false, errors.Join(ErrLifecycle, ErrInvalidIntentInput)
	}
	return next, next.differs(l), nil
}

// bindOrder фиксирует OrderID; другой OrderID для того же orderLinkId — конфликт.
func (l *Lifecycle) bindOrder(id string) error {
	if id == "" || (l.orderID != "" && l.orderID != id) {
		return errors.Join(ErrLifecycle, ErrLifecycleConflict)
	}
	l.orderID = id
	return nil
}

// applyStatus учитывает статус биржи; терминал не отменяется, исполненное не теряется.
func (l *Lifecycle) applyStatus(e Evidence) error {
	if err := l.bindOrder(e.OrderID); err != nil {
		return err
	}
	cum, err := e.CumExecQty.Cmp(l.quantity)
	executed, zeroErr := e.CumExecQty.Cmp(model.Zero())
	// После отказа биржа не может сообщать исполненный объём.
	if err != nil || zeroErr != nil || cum > 0 || executed < 0 || (l.rejected && executed > 0) {
		return errors.Join(ErrLifecycle, ErrLifecycleConflict)
	}
	if c, _ := e.CumExecQty.Cmp(l.exchangeCum); c > 0 {
		l.exchangeCum = e.CumExecQty
	}
	if l.phase == StatePending || l.phase == StateSent || l.phase == StateUnknown {
		l.phase = StateAcknowledged
	}
	switch e.Status {
	case "New", "Untriggered", "PartiallyFilled":
	case "Filled":
		if cum != 0 {
			return errors.Join(ErrLifecycle, ErrLifecycleConflict)
		}
	case "Cancelled", "PartiallyFilledCanceled", "Deactivated":
		l.cancelled = true
	case "Rejected":
		if some, _ := l.exchangeCum.Cmp(model.Zero()); some > 0 || len(l.fills) > 0 {
			return errors.Join(ErrLifecycle, ErrLifecycleConflict)
		}
		l.rejected = true
	default:
		return errors.Join(ErrLifecycle, ErrInvalidIntentInput)
	}
	return nil
}

// applyFill добавляет fill; тот же ExecutionID с теми же данными — no-op, с другими — конфликт.
func (l *Lifecycle) applyFill(f model.ConfirmedFill) error {
	if prev, ok := l.fills[f.ExecutionID]; ok {
		if !sameFill(prev, f) {
			return errors.Join(ErrLifecycle, ErrLifecycleConflict)
		}
		return nil
	}
	if !completeFill(f) || l.rejected {
		return errors.Join(ErrLifecycle, ErrLifecycleConflict)
	}
	sum, err := l.filled.Add(f.Quantity)
	if over, cmpErr := sum.Cmp(l.quantity); err != nil || cmpErr != nil || over > 0 {
		return errors.Join(ErrLifecycle, ErrLifecycleConflict)
	}
	fills := make(map[string]model.ConfirmedFill, len(l.fills)+1)
	for id, v := range l.fills {
		fills[id] = v
	}
	fills[f.ExecutionID] = f
	l.fills, l.filled = fills, sum
	if l.phase == StatePending || l.phase == StateSent || l.phase == StateUnknown {
		l.phase = StateAcknowledged
	}
	return nil
}

// completeFill проверяет обязательные поля fill: без них ledger его не примет.
func completeFill(f model.ConfirmedFill) bool {
	qty, qErr := f.Quantity.Cmp(model.Zero())
	price, pErr := f.ExecutionPrice.Cmp(model.Zero())
	_, fErr := f.FeeUSDT.Cmp(model.Zero())
	return f.ExecutionID != "" && f.Symbol != "" && (f.Side == model.FillBuy || f.Side == model.FillSell) &&
		!f.ExecutedAt.IsZero() && qErr == nil && qty > 0 && pErr == nil && price > 0 && fErr == nil
}

// differs сообщает, изменилось ли наблюдаемое состояние.
func (l Lifecycle) differs(o Lifecycle) bool {
	f, _ := l.filled.Cmp(o.filled)
	c, _ := l.exchangeCum.Cmp(o.exchangeCum)
	return l.phase != o.phase || l.orderID != o.orderID || f != 0 || c != 0 || l.cancelled != o.cancelled || l.rejected != o.rejected
}

// sameFill сравнивает экономику двух fills.
func sameFill(a, b model.ConfirmedFill) bool {
	q, _ := a.Quantity.Cmp(b.Quantity)
	p, _ := a.ExecutionPrice.Cmp(b.ExecutionPrice)
	f, _ := a.FeeUSDT.Cmp(b.FeeUSDT)
	return a.Symbol == b.Symbol && a.Side == b.Side && a.ExecutedAt.Equal(b.ExecutedAt) && q == 0 && p == 0 && f == 0
}
