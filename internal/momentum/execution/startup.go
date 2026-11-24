package execution

import (
	"sort"

	"bybit_bot_liquidity/internal/momentum/model"
)

// StoredIntent — намерение из хранилища с подтверждёнными в нём fills.
type StoredIntent struct {
	OrderLinkID, Symbol, PlanHash string
	Side                          model.FillSide
	State                         OrderState
	Quantity, Filled              model.Decimal // Filled — сумма fills в хранилище
	FillIDs                       map[string]bool
}

// BrokerLookup — результат поиска заявки по orderLinkId (realtime, затем history).
type BrokerLookup struct {
	Found      bool
	Status     string // статус Bybit
	CumExecQty model.Decimal
}

// BrokerFill — исполнение из истории биржи с orderLinkId заявки.
type BrokerFill struct {
	OrderLinkID string
	Fill        model.ConfirmedFill
}

// StartupInput — снимок хранилища и биржи на старте до разрешения входов.
type StartupInput struct {
	Namespace      string // префикс orderLinkId стратегии
	LatestPlanHash string
	Intents        []StoredIntent
	Lookups        map[string]BrokerLookup // по orderLinkId каждого неурегулированного намерения
	Fills          []BrokerFill
	Inventory      model.AccountInventory
	Positions      map[string]model.Decimal // позиции по ledger до починки
}

// RepairKind — действие, восстанавливающее хранилище по доказательствам биржи:
// запись пропущенного fill или состояние заявки, найденной на бирже.
type RepairKind string

// Виды починки.
const (
	RepairRecordFill RepairKind = "RECORD_FILL"
	RepairMarkState  RepairKind = "MARK_STATE"
)

// Repair — одно идемпотентное действие для storage.RecordFill или SetOrderIntentState.
type Repair struct {
	Kind        RepairKind
	OrderLinkID string
	Fill        model.ConfirmedFill
	State       OrderState
}

// FindingKind — неустранимое автоматически расхождение; любое блокирует входы.
type FindingKind string

// Виды расхождений.
const (
	FindingSendUnconfirmed  FindingKind = "send_unconfirmed"  // SENT/UNKNOWN, биржа не знает: повтор только тем же orderLinkId
	FindingMissingLookup    FindingKind = "missing_lookup"    // не выполнен поиск неурегулированной заявки
	FindingStalePlan        FindingKind = "stale_plan"        // активная заявка старой ревизии плана
	FindingOrphanOrder      FindingKind = "orphan_order"      // заявка/fill стратегии без намерения в хранилище
	FindingForeignOrder     FindingKind = "foreign_order"     // чужая активная заявка на счёте
	FindingForeignPosition  FindingKind = "foreign_position"  // позиция по символу без позиции в ledger
	FindingPositionMismatch FindingKind = "position_mismatch" // размер позиции не равен ledger с учётом починки
	FindingInvalidInput     FindingKind = "invalid_input"
	FindingOrderUnresolved  FindingKind = "order_unresolved" // принятая заявка не найдена: исход остатка неизвестен
	FindingFillsMissing     FindingKind = "fills_missing"    // биржа сообщает больше исполненного, чем известно fills
)

// Finding — расхождение с объектом, к которому оно относится.
type Finding struct {
	Kind    FindingKind
	Subject string
}

// StartupReport — итог сверки: что починить и что мешает входам.
type StartupReport struct {
	Repairs        []Repair
	Findings       []Finding
	EntriesAllowed bool
}

// ReconcileStartup сверяет хранилище с биржей без I/O: идемпотентные Repairs (применять по
// порядку: MARK_STATE, затем RECORD_FILL) и Findings, запрещающие входы. Fill не выводится из отсутствия заявки.
func ReconcileStartup(in StartupInput) StartupReport {
	var r StartupReport
	intents := make(map[string]StoredIntent, len(in.Intents))
	for _, it := range in.Intents {
		intents[it.OrderLinkID] = it
	}
	delta := map[string]model.Decimal{}
	recovered := map[string]model.Decimal{}
	seen := map[string]bool{}
	var fills []Repair
	for _, bf := range in.Fills {
		it, ok := intents[bf.OrderLinkID]
		switch {
		case !ok:
			r.find(FindingOrphanOrder, bf.OrderLinkID)
		case !it.FillIDs[bf.Fill.ExecutionID] && !seen[bf.Fill.ExecutionID]:
			seen[bf.Fill.ExecutionID] = true
			fills = append(fills, Repair{Kind: RepairRecordFill, OrderLinkID: bf.OrderLinkID, Fill: bf.Fill})
			sum, err := addQty(recovered[bf.OrderLinkID], bf.Fill.Quantity)
			if !addSigned(delta, bf.Fill) || err != nil {
				r.find(FindingInvalidInput, bf.OrderLinkID)
			}
			recovered[bf.OrderLinkID] = sum
		}
	}
	for _, it := range in.Intents {
		r.intent(in, it, recovered[it.OrderLinkID])
	}
	// Состояния до fills: RecordFill сам выводит PARTIALLY_FILLED/FILLED, а SetOrderIntentState после FILLED невозможен.
	r.Repairs = append(r.Repairs, fills...)
	for _, o := range in.Inventory.Orders {
		_, known := intents[o.OrderLinkID]
		switch {
		case !model.OrderOwnedBy(o, in.Namespace):
			r.find(FindingForeignOrder, o.Symbol+"/"+o.OrderID)
		case !known:
			r.find(FindingOrphanOrder, o.OrderLinkID)
		}
	}
	r.positions(in, delta)
	r.EntriesAllowed = len(r.Findings) == 0
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		return a.Kind < b.Kind || (a.Kind == b.Kind && a.Subject < b.Subject)
	})
	return r
}

// intent сверяет одно неурегулированное намерение с поиском на бирже и
// проверяет, что исполненный биржей объём покрыт известными fills.
func (r *StartupReport) intent(in StartupInput, it StoredIntent, recovered model.Decimal) {
	if it.State == StateFilled || it.State == StateCancelled || it.State == StateRejected || it.State == StatePending {
		return
	}
	lookup, ok := in.Lookups[it.OrderLinkID]
	switch {
	case !ok:
		r.find(FindingMissingLookup, it.OrderLinkID)
		return
	case !lookup.Found:
		if it.State == StateSent || it.State == StateUnknown {
			r.find(FindingSendUnconfirmed, it.OrderLinkID)
		} else {
			r.find(FindingOrderUnresolved, it.OrderLinkID)
		}
		return
	}
	known, err := addQty(it.Filled, recovered)
	if c, cmpErr := lookup.CumExecQty.Cmp(known); err != nil || cmpErr != nil || c > 0 {
		r.find(FindingFillsMissing, it.OrderLinkID)
		return
	}
	state := StateAcknowledged
	switch lookup.Status {
	case "Cancelled", "PartiallyFilledCanceled", "Deactivated":
		state = StateCancelled
	case "Rejected":
		state = StateRejected
	case "New", "PartiallyFilled", "Untriggered":
		if it.PlanHash != in.LatestPlanHash {
			r.find(FindingStalePlan, it.OrderLinkID)
		}
	}
	// PARTIALLY_FILLED не понижается до ACKNOWLEDGED, но подтверждённая отмена допустима.
	if state != it.State && (it.State != StatePartiallyFilled || state != StateAcknowledged) {
		r.Repairs = append(r.Repairs, Repair{Kind: RepairMarkState, OrderLinkID: it.OrderLinkID, State: state})
	}
}

// positions сравнивает размеры позиций биржи с ledger плюс записываемые fills.
func (r *StartupReport) positions(in StartupInput, delta map[string]model.Decimal) {
	want := map[string]model.Decimal{}
	for symbol, qty := range in.Positions {
		want[symbol] = qty
	}
	for symbol, d := range delta {
		sum, err := addQty(want[symbol], d)
		if err != nil {
			r.find(FindingInvalidInput, symbol)
			continue
		}
		want[symbol] = sum
	}
	seen := map[string]bool{}
	for _, p := range in.Inventory.Positions {
		if c, err := p.Size.Cmp(model.Zero()); err != nil || c == 0 {
			continue
		}
		seen[p.Symbol] = true
		expected, ok := want[p.Symbol]
		size := p.Size
		if p.Side == "Sell" {
			size, _ = model.Zero().Sub(p.Size)
		}
		if c, err := size.Cmp(expected); !ok {
			r.find(FindingForeignPosition, p.Symbol)
		} else if err != nil || c != 0 {
			r.find(FindingPositionMismatch, p.Symbol)
		}
	}
	for symbol, qty := range want {
		if c, err := qty.Cmp(model.Zero()); !seen[symbol] && (err != nil || c != 0) {
			r.find(FindingPositionMismatch, symbol)
		}
	}
}

// find добавляет расхождение.
func (r *StartupReport) find(kind FindingKind, subject string) {
	r.Findings = append(r.Findings, Finding{Kind: kind, Subject: subject})
}

// addQty складывает количества; нулевое значение Decimal трактуется как ноль.
func addQty(a, b model.Decimal) (model.Decimal, error) {
	if _, err := a.Cmp(model.Zero()); err != nil {
		a = model.Zero()
	}
	if _, err := b.Cmp(model.Zero()); err != nil {
		b = model.Zero()
	}
	return a.Add(b)
}

// addSigned прибавляет к позиции символа количество fill со знаком стороны.
func addSigned(delta map[string]model.Decimal, f model.ConfirmedFill) bool {
	qty, err := f.Quantity, error(nil)
	if f.Side == model.FillSell {
		qty, err = model.Zero().Sub(f.Quantity)
	} else if f.Side != model.FillBuy {
		return false
	}
	if err != nil {
		return false
	}
	sum, err := addQty(delta[f.Symbol], qty)
	delta[f.Symbol] = sum
	return err == nil
}
