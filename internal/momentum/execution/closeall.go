package execution

import (
	"sort"

	"bybit_bot_liquidity/internal/momentum/model"
)

// CloseAllPhase — фаза команды close_all_and_halt.
type CloseAllPhase string

// Фазы: сначала halt, потом отмена своих заявок, закрытие, проверка нуля.
const (
	CloseAllPersistHalt CloseAllPhase = "PERSIST_HALT" // сохранить HALTED до любых действий
	CloseAllCancelling  CloseAllPhase = "CANCELLING"   // отменить свои активные заявки
	CloseAllClosing     CloseAllPhase = "CLOSING"      // отправить reduce-only закрытия
	CloseAllVerifying   CloseAllPhase = "VERIFYING"    // ждать fills/подтверждения нуля
	CloseAllClosed      CloseAllPhase = "CLOSED"
	CloseAllBlocked     CloseAllPhase = "BLOCKED" // нужна ручная сверка; не CLOSED
)

// CloseAllInput — состояние счёта и уже отправленных закрытий.
type CloseAllInput struct {
	Halted    bool // control_revisions уже HALTED
	Namespace string
	Symbols   []string // universe стратегии: закрываются только эти позиции
	Inventory model.AccountInventory
	MinQty    map[string]model.Decimal // минимальный объём закрытия по символу
	Closes    map[string]Lifecycle     // последнее закрытие по символу
}

// CloseOrder — reduce-only закрытие позиции символа на её текущий размер.
type CloseOrder struct {
	Symbol   string
	Quantity model.Decimal
}

// CloseAllStep — решение команды; повтор с тем же состоянием даёт тот же шаг.
type CloseAllStep struct {
	Phase    CloseAllPhase
	Cancel   []model.AccountOrder
	Close    []CloseOrder
	Findings []Finding
}

// NextCloseAll выводит шаг close_all_and_halt. HALTED сохраняется до действий;
// свои активные заявки отменяются до закрытий; закрываются только позиции
// universe стратегии, чужие позиции и заявки лишь попадают в Findings. CLOSED —
// только когда биржа подтверждает нулевые позиции, своих заявок нет, все
// закрытия урегулированы и расхождений нет; dust и неизвестный исход не CLOSED.
// Закрытие не повторяется, пока предыдущее по символу не урегулировано.
func NextCloseAll(in CloseAllInput) CloseAllStep {
	if !in.Halted {
		return CloseAllStep{Phase: CloseAllPersistHalt}
	}
	var step CloseAllStep
	universe := map[string]bool{}
	for _, s := range in.Symbols {
		universe[s] = true
	}
	for _, o := range in.Inventory.Orders {
		if model.OrderOwnedBy(o, in.Namespace) {
			step.Cancel = append(step.Cancel, o)
		} else {
			step.find(FindingForeignOrder, o.Symbol+"/"+o.OrderID)
		}
	}
	if len(step.Cancel) > 0 {
		step.Phase = CloseAllCancelling
		step.sortFindings()
		return step
	}
	inFlight := false
	for _, l := range in.Closes {
		if !l.Settled() {
			inFlight = true
		}
	}
	for _, p := range in.Inventory.Positions {
		size, err := p.Size.Cmp(model.Zero())
		if err != nil || size == 0 {
			continue
		}
		if !universe[p.Symbol] {
			step.find(FindingForeignPosition, p.Symbol)
			continue
		}
		if l, ok := in.Closes[p.Symbol]; ok && !l.Settled() {
			continue
		}
		if min, ok := in.MinQty[p.Symbol]; !ok || p.Side != "Buy" {
			step.find(FindingPositionMismatch, p.Symbol)
		} else if below, err := p.Size.Cmp(min); err != nil || below < 0 {
			step.find(FindingPositionMismatch, p.Symbol+"/dust")
		} else {
			step.Close = append(step.Close, CloseOrder{Symbol: p.Symbol, Quantity: p.Size})
		}
	}
	step.sortFindings()
	switch {
	case len(step.Close) > 0:
		step.Phase = CloseAllClosing
	case inFlight:
		step.Phase = CloseAllVerifying
	case len(step.Findings) > 0:
		step.Phase = CloseAllBlocked
	default:
		step.Phase = CloseAllClosed
	}
	return step
}

// find добавляет расхождение.
func (s *CloseAllStep) find(kind FindingKind, subject string) {
	s.Findings = append(s.Findings, Finding{Kind: kind, Subject: subject})
}

// sortFindings упорядочивает расхождения для детерминированного отчёта.
func (s *CloseAllStep) sortFindings() {
	sort.Slice(s.Findings, func(i, j int) bool {
		a, b := s.Findings[i], s.Findings[j]
		return a.Kind < b.Kind || (a.Kind == b.Kind && a.Subject < b.Subject)
	})
}
