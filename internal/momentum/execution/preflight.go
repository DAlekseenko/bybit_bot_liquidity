// Package execution содержит чистые контракты исполнения TradePlan без I/O.
package execution

import (
	"slices"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// PreflightReason обозначает причину запрета входа.
type PreflightReason string

// Причины запрета входа; inventory и маржа проверяются до любых торговых вызовов.
const (
	ReasonInvalidParameters  PreflightReason = "invalid_parameters"
	ReasonStaleData          PreflightReason = "stale_data"
	ReasonMarginMode         PreflightReason = "margin_mode"
	ReasonPositionMode       PreflightReason = "position_mode"
	ReasonUnknownPosition    PreflightReason = "unknown_position"
	ReasonPositionMismatch   PreflightReason = "position_mismatch"
	ReasonLeverageMismatch   PreflightReason = "leverage_mismatch"
	ReasonAutoAddMargin      PreflightReason = "auto_add_margin"
	ReasonPositionStatus     PreflightReason = "position_status"
	ReasonUnknownOrder       PreflightReason = "unknown_order"
	ReasonOwnedOrderOpen     PreflightReason = "owned_order_open"
	ReasonUnsupportedSymbol  PreflightReason = "unsupported_symbol"
	ReasonInstrumentStatus   PreflightReason = "instrument_status"
	ReasonMissingMark        PreflightReason = "missing_mark"
	ReasonUnavailableBalance PreflightReason = "unavailable_balance"
	ReasonInsufficientMargin PreflightReason = "insufficient_margin"
	isolatedMarginMode                       = "ISOLATED_MARGIN"
	tradingInstrumentStatus                  = "Trading"
	oneWayPositionIdx                        = 0
)

// PreflightDenial связывает причину запрета с субъектом (символ, заявка, источник).
type PreflightDenial struct {
	Reason  PreflightReason
	Subject string
}

// PreflightParams задаёт явные параметры владельца; умолчаний нет (D-04/D-06).
type PreflightParams struct {
	// Now — время проверки в UTC.
	Now time.Time
	// MaxDataAge — максимальный возраст любого чтения биржи (D-04).
	MaxDataAge time.Duration
	// TargetLeverage — требуемое плечо каждой позиции стратегии.
	TargetLeverage model.Decimal
	// MarginBufferRate — неотрицательный запас к требуемой марже (fee/slippage, D-04).
	MarginBufferRate model.Decimal
	// OrderLinkNamespace — префикс LinkID заявок стратегии.
	OrderLinkNamespace string
}

// MarkObservation хранит mark price символа и время его чтения.
type MarkObservation struct {
	Price model.Decimal
	Time  time.Time
}

// PreflightInput содержит проверенные read models и проекцию плана.
type PreflightInput struct {
	Mode      model.AccountMode
	Balance   model.IsolatedBalance
	Inventory model.AccountInventory
	// Symbols — символы, которыми управляет стратегия; прочие позиции неизвестны.
	Symbols []string
	// Expected — ожидаемые ledger количества по символам стратегии (отсутствие = 0).
	Expected map[string]model.Decimal
	// InstrumentStatus — статус инструмента биржи по символу.
	InstrumentStatus map[string]string
	Marks            map[string]MarkObservation
	Deltas           []planner.PositionDelta
}

// PreflightResult описывает решение allow/deny и рассчитанную требуемую маржу.
type PreflightResult struct {
	Allowed        bool
	Denials        []PreflightDenial
	RequiredMargin model.Decimal
}

// Preflight проверяет счёт перед входом без I/O, setter или trade вызовов.
func Preflight(in PreflightInput, p PreflightParams) PreflightResult {
	var denials []PreflightDenial
	deny := func(reason PreflightReason, subject string) {
		denials = append(denials, PreflightDenial{Reason: reason, Subject: subject})
	}
	lev, errLev := p.TargetLeverage.Cmp(model.Zero())
	buf, errBuf := p.MarginBufferRate.Cmp(model.Zero())
	if p.Now.IsZero() || p.MaxDataAge <= 0 || errLev != nil || lev <= 0 || errBuf != nil || buf < 0 || p.OrderLinkNamespace == "" || len(in.Symbols) == 0 {
		return PreflightResult{Denials: []PreflightDenial{{Reason: ReasonInvalidParameters}}}
	}
	fresh := func(at time.Time, subject string) {
		if at.IsZero() || at.After(p.Now) || p.Now.Sub(at) > p.MaxDataAge {
			deny(ReasonStaleData, subject)
		}
	}
	fresh(in.Mode.ServerTime, "account_mode")
	fresh(in.Balance.ServerTime, "balance")
	fresh(in.Inventory.ServerTime, "inventory")
	if in.Mode.MarginMode != isolatedMarginMode {
		deny(ReasonMarginMode, in.Mode.MarginMode)
	}
	checkPositions(in, p, deny)
	for _, order := range in.Inventory.Orders {
		if model.OrderOwnedBy(order, p.OrderLinkNamespace) {
			deny(ReasonOwnedOrderOpen, order.OrderID)
		} else {
			deny(ReasonUnknownOrder, order.OrderID)
		}
	}
	required := requiredMargin(in, p, deny, fresh)
	enough, errAvail := in.Balance.Available.Cmp(required)
	if in.Balance.Coin != "USDT" || errAvail != nil {
		deny(ReasonUnavailableBalance, "USDT")
	} else if positive, _ := in.Balance.Available.Cmp(model.Zero()); hasIncrease(in.Deltas) && (enough < 0 || positive <= 0) {
		deny(ReasonInsufficientMargin, "USDT")
	}
	return PreflightResult{Allowed: len(denials) == 0, Denials: denials, RequiredMargin: required}
}

// checkPositions сверяет все позиции счёта с символами и ожидаемым ledger количеством.
func checkPositions(in PreflightInput, p PreflightParams, deny func(PreflightReason, string)) {
	seen := map[string]bool{}
	for _, pos := range in.Inventory.Positions {
		if pos.PositionIdx != oneWayPositionIdx {
			deny(ReasonPositionMode, pos.Symbol)
		}
		if !slices.Contains(in.Symbols, pos.Symbol) {
			deny(ReasonUnknownPosition, pos.Symbol)
			continue
		}
		seen[pos.Symbol] = true
		expected := expectedQty(in, pos.Symbol)
		if eq, err := pos.Size.Cmp(expected); err != nil || eq != 0 || (pos.Side != "Buy" && pos.Side != "") {
			deny(ReasonPositionMismatch, pos.Symbol)
		}
		if eq, err := pos.Leverage.Cmp(p.TargetLeverage); err != nil || eq != 0 {
			deny(ReasonLeverageMismatch, pos.Symbol)
		}
		if pos.AutoAddMargin {
			deny(ReasonAutoAddMargin, pos.Symbol)
		}
		if pos.Status != "Normal" {
			deny(ReasonPositionStatus, pos.Symbol)
		}
	}
	for _, symbol := range in.Symbols {
		if sign, err := expectedQty(in, symbol).Cmp(model.Zero()); !seen[symbol] && (err != nil || sign != 0) {
			deny(ReasonPositionMismatch, symbol)
		}
	}
}

// expectedQty возвращает ожидаемое количество символа; отсутствие означает ноль.
func expectedQty(in PreflightInput, symbol string) model.Decimal {
	if qty, ok := in.Expected[symbol]; ok {
		return qty
	}
	return model.Zero()
}

// requiredMargin суммирует candidateQty*mark/leverage*(1+buffer) по увеличениям.
func requiredMargin(in PreflightInput, p PreflightParams, deny func(PreflightReason, string), fresh func(time.Time, string)) model.Decimal {
	total := model.Zero()
	one, _ := model.ParseAmount("1")
	factor, err := one.Add(p.MarginBufferRate)
	if err != nil {
		deny(ReasonInvalidParameters, "margin_buffer")
		return total
	}
	for _, delta := range in.Deltas {
		if !slices.Contains(in.Symbols, delta.Symbol) {
			deny(ReasonUnsupportedSymbol, delta.Symbol)
			continue
		}
		if in.InstrumentStatus[delta.Symbol] != tradingInstrumentStatus {
			deny(ReasonInstrumentStatus, delta.Symbol)
		}
		if delta.ReduceOnly {
			continue
		}
		mark, ok := in.Marks[delta.Symbol]
		if positive, err := mark.Price.Cmp(model.Zero()); !ok || err != nil || positive <= 0 {
			deny(ReasonMissingMark, delta.Symbol)
			continue
		}
		fresh(mark.Time, "mark:"+delta.Symbol)
		notional, err := delta.CandidateQty.Mul(mark.Price)
		if err == nil {
			notional, err = notional.Quo(p.TargetLeverage)
		}
		if err == nil {
			notional, err = notional.Mul(factor)
		}
		if err == nil {
			total, err = total.Add(notional)
		}
		if err != nil {
			deny(ReasonInvalidParameters, delta.Symbol)
		}
	}
	return total
}

// hasIncrease сообщает, содержит ли проекция хотя бы одно увеличение позиции.
func hasIncrease(deltas []planner.PositionDelta) bool {
	for _, delta := range deltas {
		if !delta.ReduceOnly {
			return true
		}
	}
	return false
}
