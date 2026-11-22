package execution

import "errors"

// ErrRebalance означает несогласованные входы машины ребалансировки.
var ErrRebalance = errors.New("momentum rebalance state is inconsistent")

// Phase — фаза ребалансировки одной ревизии плана.
type Phase string

// Фазы: сначала все сокращения, затем увеличения; BLOCKED требует сверки или нового плана.
const (
	PhaseReducing   Phase = "REDUCING"
	PhaseIncreasing Phase = "INCREASING"
	PhaseDone       Phase = "DONE"
	PhaseBlocked    Phase = "BLOCKED"
)

// BlockReason — почему увеличения запрещены.
type BlockReason string

// Причины блокировки увеличений.
const (
	BlockReductionIncomplete BlockReason = "reduction_incomplete" // отклонена, отменена с остатком или исход неизвестен
	BlockIncreaseGate        BlockReason = "increase_gate"        // preflight: маржа, свежесть и т.п.
	BlockIncreaseIncomplete  BlockReason = "increase_incomplete"
)

// Rebalance — неизменяемая ребалансировка одной ревизии TradePlan.
type Rebalance struct {
	planHash             string
	revision             uint64
	reductions, increase []OrderIntent
}

// Step — решение машины: какие заявки отправить сейчас.
type Step struct {
	Phase  Phase
	Send   []OrderIntent
	Reason BlockReason
}

// NewRebalance связывает заявки одной ревизии плана; нулевые дельты в batch не попадают.
func NewRebalance(batch IntentBatch) (Rebalance, error) {
	var r Rebalance
	for i, in := range batch.Intents {
		if i == 0 {
			r.planHash, r.revision = in.PlanHash, in.PlanRevision
		}
		if in.PlanHash != r.planHash || in.PlanRevision != r.revision || in.OrderLinkID == "" {
			return Rebalance{}, ErrRebalance
		}
		switch in.Leg {
		case LegReduce:
			if len(r.increase) > 0 {
				return Rebalance{}, ErrRebalance
			}
			r.reductions = append(r.reductions, in)
		case LegIncrease:
			r.increase = append(r.increase, in)
		default:
			return Rebalance{}, ErrRebalance
		}
	}
	return r, nil
}

// Next выводит фазу только из сохранённых состояний заявок (states по orderLinkId),
// поэтому после рестарта фаза восстанавливается тем же вызовом. increaseGate — итог
// preflight перед увеличениями; при отказе увеличения не сокращаются и бюджет
// соседних символов не перераспределяется.
func (r Rebalance) Next(states map[string]Lifecycle, increaseGate error) (Step, error) {
	for link := range states {
		if !r.owns(link) {
			return Step{}, ErrRebalance
		}
	}
	pending, settled, filled := progress(r.reductions, states)
	if len(pending) > 0 || !settled {
		return Step{Phase: PhaseReducing, Send: pending}, nil
	}
	if !filled {
		return Step{Phase: PhaseBlocked, Reason: BlockReductionIncomplete}, nil
	}
	pending, settled, filled = progress(r.increase, states)
	switch {
	case len(pending) > 0 && increaseGate != nil:
		return Step{Phase: PhaseBlocked, Reason: BlockIncreaseGate}, nil
	case len(pending) > 0 || !settled:
		return Step{Phase: PhaseIncreasing, Send: pending}, nil
	case !filled:
		return Step{Phase: PhaseBlocked, Reason: BlockIncreaseIncomplete}, nil
	}
	return Step{Phase: PhaseDone}, nil
}

// progress возвращает ещё не отправленные заявки, урегулированы ли отправленные и все ли FILLED.
func progress(intents []OrderIntent, states map[string]Lifecycle) (pending []OrderIntent, settled, filled bool) {
	settled, filled = true, true
	for _, in := range intents {
		l, sent := states[in.OrderLinkID]
		if !sent || l.State() == StatePending {
			pending = append(pending, in)
			filled = false
			continue
		}
		if !l.Settled() {
			settled = false
		}
		if l.State() != StateFilled {
			filled = false
		}
	}
	return pending, settled, filled
}

// owns проверяет, что orderLinkId принадлежит этой ревизии плана.
func (r Rebalance) owns(link string) bool {
	for _, group := range [][]OrderIntent{r.reductions, r.increase} {
		for _, in := range group {
			if in.OrderLinkID == link {
				return true
			}
		}
	}
	return false
}
