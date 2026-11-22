// Package risk содержит чистый risk controller: по текущему режиму управления,
// снимку equity и флагам сверки он решает, нужно ли приостановить входы или
// остановить стратегию. Пакет не выполняет I/O, не вызывает API биржи и не
// закрывает позиции: HALT запрещает новые входы, а не означает close_all.
package risk

import (
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrInvalidInput обозначает некорректные параметры или вход; вызывающий код
// обязан трактовать её как отказ (fail closed), а не как разрешение входов.
var ErrInvalidInput = errors.New("invalid momentum risk controller input")

// Mode — режим управления; значения совпадают с control_revisions.mode.
type Mode string

const (
	// ModeActive разрешает новые входы.
	ModeActive Mode = "ACTIVE"
	// ModePausedEntries запрещает новые входы до решения оператора.
	ModePausedEntries Mode = "PAUSED_ENTRIES"
	// ModeHalted — липкая остановка; выход только через RESUME оператора.
	ModeHalted Mode = "HALTED"
)

// Action — переход, предложенный контроллером; значения совпадают с
// control_revisions.action. Пустое значение означает отсутствие перехода.
type Action string

const (
	// ActionNone — режим не меняется.
	ActionNone Action = ""
	// ActionPauseEntries переводит ACTIVE в PAUSED_ENTRIES.
	ActionPauseEntries Action = "PAUSE_ENTRIES"
	// ActionHalt переводит стратегию в HALTED.
	ActionHalt Action = "HALT"
)

// Reason — причина перехода; значения совпадают с risk_events.kind.
type Reason string

const (
	// ReasonEquityStop — свежая equity не выше порога остановки.
	ReasonEquityStop Reason = "EQUITY_STOP"
	// ReasonStaleData — снимок equity отсутствует, устарел или помечен несвежим.
	ReasonStaleData Reason = "STALE_DATA"
	// ReasonUnknownPosition — на бирже есть позиция, неизвестная ledger.
	ReasonUnknownPosition Reason = "UNKNOWN_POSITION"
	// ReasonUnknownOrder — есть заявка неизвестного или UNKNOWN состояния.
	ReasonUnknownOrder Reason = "UNKNOWN_ORDER"
	// ReasonLockLost — потеряна блокировка владельца исполнения.
	ReasonLockLost Reason = "LOCK_LOST"
	// ReasonReconciliationMismatch — сверка с биржей выявила расхождение.
	ReasonReconciliationMismatch Reason = "RECONCILIATION_MISMATCH"
)

// EquityStop — порог остановки по equity. Значение и включение — решение
// владельца (D-05), по умолчанию (нулевое значение) stop выключен.
type EquityStop struct {
	Enabled   bool
	Threshold model.Decimal
}

// Params — явные параметры контроллера без умолчаний (D-04/D-05).
type Params struct {
	EquityStop EquityStop
	// MaxSnapshotAge — максимальный возраст снимка equity; должен быть > 0.
	MaxSnapshotAge time.Duration
}

// EquitySnapshot — последний снимок equity стратегии.
type EquitySnapshot struct {
	ObservedAt time.Time
	EquityUSDT model.Decimal
	DataFresh  bool
}

// Input — наблюдаемое состояние на момент Now. Nil Equity означает отсутствие снимка.
type Input struct {
	Mode                   Mode
	Now                    time.Time
	Equity                 *EquitySnapshot
	UnknownPosition        bool
	UnknownOrder           bool
	LockLost               bool
	ReconciliationMismatch bool
}

// StopEvidence — данные для risk_events.EQUITY_STOP (equity <= threshold).
type StopEvidence struct {
	ThresholdUSDT, EquityUSDT model.Decimal
}

// Decision — результат оценки. Mode — режим после применения Action; Reasons
// перечисляют причины перехода в стабильном порядке и пусты при ActionNone.
// Stop заполнен, только если среди причин есть ReasonEquityStop.
type Decision struct {
	Action  Action
	Mode    Mode
	Reasons []Reason
	Stop    *StopEvidence
}

// EntriesAllowed сообщает, разрешены ли новые входы после решения.
func (d Decision) EntriesAllowed() bool { return d.Mode == ModeActive }

// Evaluate чисто и детерминированно оценивает вход. Из HALTED переход не
// предлагается никогда (выход — только RESUME оператора), из PAUSED_ENTRIES
// возможен только HALT; восстановление данных или equity режим не ослабляет.
// Повторная оценка того же состояния после применения перехода даёт ActionNone.
func Evaluate(params Params, in Input) (Decision, error) {
	if err := validate(params, in); err != nil {
		return Decision{}, err
	}
	reasons, stop := detect(params, in)
	action := ActionNone
	switch {
	case in.Mode == ModeHalted:
	case stop != nil:
		action = ActionHalt
	case in.Mode == ModeActive && len(reasons) > 0:
		action = ActionPauseEntries
	}
	switch action {
	case ActionHalt:
		return Decision{Action: action, Mode: ModeHalted, Reasons: reasons, Stop: stop}, nil
	case ActionPauseEntries:
		return Decision{Action: action, Mode: ModePausedEntries, Reasons: reasons}, nil
	}
	return Decision{Action: ActionNone, Mode: in.Mode}, nil
}

// validate отклоняет неизвестный режим, пустое время и неполные параметры.
func validate(params Params, in Input) error {
	if in.Mode != ModeActive && in.Mode != ModePausedEntries && in.Mode != ModeHalted {
		return ErrInvalidInput
	}
	if in.Now.IsZero() || params.MaxSnapshotAge <= 0 {
		return ErrInvalidInput
	}
	if params.EquityStop.Enabled {
		if _, err := params.EquityStop.Threshold.Cmp(model.Zero()); err != nil {
			return ErrInvalidInput
		}
	}
	if in.Equity != nil {
		if _, err := in.Equity.EquityUSDT.Cmp(model.Zero()); err != nil || in.Equity.ObservedAt.IsZero() {
			return ErrInvalidInput
		}
	}
	return nil
}

// detect собирает причины в порядке Reason-констант. Stop проверяется только
// на свежем снимке: несвежие данные блокируют входы, но не вызывают HALT.
// Decimal уже проверены validate, поэтому ошибка Cmp здесь невозможна.
func detect(params Params, in Input) ([]Reason, *StopEvidence) {
	var reasons []Reason
	var stop *StopEvidence
	fresh := in.Equity != nil && in.Equity.DataFresh && !in.Equity.ObservedAt.After(in.Now) &&
		in.Now.Sub(in.Equity.ObservedAt) <= params.MaxSnapshotAge
	if fresh && params.EquityStop.Enabled {
		if c, _ := in.Equity.EquityUSDT.Cmp(params.EquityStop.Threshold); c <= 0 {
			reasons = append(reasons, ReasonEquityStop)
			stop = &StopEvidence{ThresholdUSDT: params.EquityStop.Threshold, EquityUSDT: in.Equity.EquityUSDT}
		}
	}
	flags := []struct {
		on     bool
		reason Reason
	}{
		{!fresh, ReasonStaleData},
		{in.UnknownPosition, ReasonUnknownPosition},
		{in.UnknownOrder, ReasonUnknownOrder},
		{in.LockLost, ReasonLockLost},
		{in.ReconciliationMismatch, ReasonReconciliationMismatch},
	}
	for _, f := range flags {
		if f.on {
			reasons = append(reasons, f.reason)
		}
	}
	return reasons, stop
}
