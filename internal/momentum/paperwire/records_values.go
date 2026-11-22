package paperwire

import "bybit_bot_liquidity/internal/momentum/model"

type positionWire struct{ Quantity, CostBasisUSDT amount }
type stateWire struct {
	AsOf, FundingThrough   instant
	WalletUSDT, EquityUSDT amount
	Positions              map[string]positionWire
}
type fillWire struct {
	ExecutionID, Symbol               string
	Side                              model.FillSide
	ExecutedAt                        instant
	Quantity, ExecutionPrice, FeeUSDT amount
}

// stateOut переносит все позиции в отдельную карту wire-значений.
func stateOut(s PortfolioState) stateWire {
	positions := make(map[string]positionWire, len(s.Positions))
	for symbol, p := range s.Positions {
		positions[symbol] = positionWire{amount{p.Quantity}, amount{p.CostBasisUSDT}}
	}
	return stateWire{instant{s.AsOf}, instant{s.FundingThrough}, amount{s.WalletUSDT}, amount{s.EquityUSDT}, positions}
}

// stateIn восстанавливает отдельную изменяемую карту позиций.
func stateIn(s stateWire) PortfolioState {
	positions := make(map[string]model.LongPosition, len(s.Positions))
	for symbol, p := range s.Positions {
		positions[symbol] = model.LongPosition{Quantity: p.Quantity.value, CostBasisUSDT: p.CostBasisUSDT.value}
	}
	return PortfolioState{s.AsOf.Time, s.FundingThrough.Time, s.WalletUSDT.value, s.EquityUSDT.value, positions}
}

// fillOut сохраняет все поля исполнения без пересчёта денег.
func fillOut(f model.ConfirmedFill) fillWire {
	return fillWire{f.ExecutionID, f.Symbol, f.Side, instant{f.ExecutedAt}, amount{f.Quantity}, amount{f.ExecutionPrice}, amount{f.FeeUSDT}}
}

// fillIn восстанавливает значение исполнения из полного wire.
func fillIn(f fillWire) model.ConfirmedFill {
	return model.ConfirmedFill{ExecutionID: f.ExecutionID, Symbol: f.Symbol, Side: f.Side, ExecutedAt: f.ExecutedAt.Time,
		Quantity: f.Quantity.value, ExecutionPrice: f.ExecutionPrice.value, FeeUSDT: f.FeeUSDT.value}
}
