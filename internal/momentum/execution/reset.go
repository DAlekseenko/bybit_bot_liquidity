package execution

// ResetPhase — фаза weekly reset: закрытие, подтверждение нуля, opening.
type ResetPhase string

// Фазы reset.
const (
	ResetClosing      ResetPhase = "CLOSING"       // отправлять/ждать закрывающие заявки
	ResetAwaitingFlat ResetPhase = "AWAITING_FLAT" // закрытия исполнены, ждём подтверждения нулевых позиций биржей
	ResetPlanOpening  ResetPhase = "PLAN_OPENING"  // снять post-close equity и сохранить opening-ревизию
	ResetOpening      ResetPhase = "OPENING"       // отправлять/ждать открывающие заявки
	ResetDone         ResetPhase = "DONE"
	ResetBlocked      ResetPhase = "BLOCKED" // opening запрещён до сверки или нового цикла
)

// ResetInput — сохранённое состояние цикла; всё выводится из него после рестарта.
type ResetInput struct {
	Close         Rebalance
	CloseStates   map[string]Lifecycle
	BrokerFlat    bool       // биржа подтвердила нулевые позиции всех символов стратегии
	Opening       *Rebalance // сохранённая opening-ревизия; nil — ещё не создана
	OpeningStates map[string]Lifecycle
	IncreaseGate  error // итог preflight перед opening
}

// ResetStep — решение по циклу reset.
type ResetStep struct {
	Phase  ResetPhase
	Send   []OrderIntent
	Reason BlockReason
}

// NextReset выводит шаг weekly reset. Позиции закрываются всегда, даже если
// сигнал не изменился. Opening планируется только после исполненных закрытий и
// подтверждённого биржей нуля; частичное закрытие, dust или неизвестный исход
// запрещают opening. Сохранённая opening-ревизия не пересоздаётся: при её
// наличии фаза PLAN_OPENING не возвращается. Post-close equity снимается после
// фактических комиссий закрытия, поэтому они уменьшают бюджет opening.
func NextReset(in ResetInput) (ResetStep, error) {
	if len(in.Close.increase) > 0 {
		return ResetStep{}, ErrRebalance
	}
	closing, err := in.Close.Next(in.CloseStates, nil)
	if err != nil {
		return ResetStep{}, err
	}
	switch {
	case closing.Phase == PhaseReducing:
		return ResetStep{Phase: ResetClosing, Send: closing.Send}, nil
	case closing.Phase == PhaseBlocked:
		return ResetStep{Phase: ResetBlocked, Reason: closing.Reason}, nil
	case in.Close.skippedReduction:
		return ResetStep{Phase: ResetBlocked, Reason: BlockReductionIncomplete}, nil
	case !in.BrokerFlat:
		return ResetStep{Phase: ResetAwaitingFlat}, nil
	case in.Opening == nil:
		return ResetStep{Phase: ResetPlanOpening}, nil
	}
	if in.Opening.planHash == in.Close.planHash && in.Opening.revision == in.Close.revision {
		return ResetStep{}, ErrRebalance
	}
	opening, err := in.Opening.Next(in.OpeningStates, in.IncreaseGate)
	if err != nil {
		return ResetStep{}, err
	}
	switch opening.Phase {
	case PhaseDone:
		return ResetStep{Phase: ResetDone}, nil
	case PhaseBlocked:
		return ResetStep{Phase: ResetBlocked, Reason: opening.Reason}, nil
	}
	return ResetStep{Phase: ResetOpening, Send: opening.Send}, nil
}
