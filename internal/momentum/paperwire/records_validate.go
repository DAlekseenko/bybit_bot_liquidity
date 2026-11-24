package paperwire

import (
	"errors"
	"sort"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// resource сохраняет оба sentinel при превышении любого record budget.
func resource() error { return errors.Join(ErrPaperWire, ErrPaperResourceLimit) }

// hashShape проверяет lowercase hex64 без интерпретации predecessor evidence.
func hashShape(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// microInstant проверяет exact UTC-compatible микросекунды нормативного диапазона.
func microInstant(t time.Time) bool {
	return t.UTC().Year() >= 1970 && t.UTC().Year() <= 9999 && t.Nanosecond()%1000 == 0
}

// exactAmount проверяет валидность полного Rat без WireDecimal и округления.
func exactAmount(d model.Decimal, positive bool) bool {
	cmp, err := d.Cmp(model.Zero())
	return err == nil && (!positive || cmp > 0)
}

// validateState проверяет форму полной вселенной, не утверждая финансовую истинность.
func validateState(s PortfolioState, symbols []string) bool {
	if len(s.Positions) != len(symbols) || !exactAmount(s.WalletUSDT, false) || !exactAmount(s.EquityUSDT, false) {
		return false
	}
	for _, symbol := range symbols {
		p, ok := s.Positions[symbol]
		q, err := p.Quantity.Cmp(model.Zero())
		c, ce := p.CostBasisUSDT.Cmp(model.Zero())
		if !ok || err != nil || ce != nil || q < 0 || c < 0 {
			return false
		}
	}
	return true
}

// validateFills проверяет идентичность и форму заявленных исполнений без broker proof.
func validateFills(fills []model.ConfirmedFill, symbols []string) bool {
	for _, f := range fills {
		if strings.TrimSpace(f.ExecutionID) == "" || !containsSymbol(symbols, f.Symbol) || f.Side != model.FillBuy && f.Side != model.FillSell || !exactAmount(f.Quantity, true) || !exactAmount(f.ExecutionPrice, true) || !exactAmount(f.FeeUSDT, false) {
			return false
		}
	}
	return true
}

// containsSymbol проверяет принадлежность канонической вселенной конфигурации.
func containsSymbol(symbols []string, symbol string) bool {
	i := sort.SearchStrings(symbols, symbol)
	return i < len(symbols) && symbols[i] == symbol
}

// validateInput связывает identity, конфигурацию, снимок и цели без нового алгоритма.
func validateInput(d CycleDocument, configHash string) error {
	v, r, m, s := d.Run.Config.Values(), d.Run, d.Plan.Metadata(), d.Plan.Snapshot()
	if !strings.HasPrefix(r.AccountID, "paper/") || len(r.AccountID) == len("paper/") || strings.TrimSpace(r.RunID) == "" || r.StrategyID != v.StrategyID || v.Mode != "paper" || v.DataSource.Kind != "bybit_testnet_public" || v.DataSource.Environment != "testnet" || !microInstant(r.ScheduledAt) {
		return invalid("run/scope")
	}
	if d.Sequence == 0 || d.Sequence == 1 && (d.PreviousInputHash != "" || d.PreviousResolutionHash != "") || d.Sequence > 1 && (!hashShape(d.PreviousInputHash) || !hashShape(d.PreviousResolutionHash)) {
		return invalid("predecessor")
	}
	p := d.Purpose
	rebalance := p.Kind == "rebalance" && p.CommandID == "" && p.ControlRevision == 0
	closeAll := p.Kind == "close-all" && strings.TrimSpace(p.CommandID) != "" && p.ControlRevision > 0
	if !rebalance && !closeAll {
		return invalid("purpose")
	}
	if s.Environment() != "paper" || s.AccountID() != r.AccountID || s.ConfigVersion() != v.ProfileVersion || !s.CapturedAt().Equal(r.ScheduledAt) || m.StrategyID != r.StrategyID || m.RunID != r.RunID || m.ConfigHash != configHash || m.ProfileID != v.Profile || m.ModelID != v.ModelID || m.Revision != 1 || m.PreviousHash != "" || strings.TrimSpace(m.FormulaID) == "" || !m.ScheduledAt.Equal(r.ScheduledAt) || !m.EquityCutoffAt.Equal(r.ScheduledAt) || !d.Cutoff.AsOf.Equal(r.ScheduledAt) || !d.Cutoff.FundingThrough.Equal(r.ScheduledAt) {
		return invalid("plan bindings")
	}
	prices, positions, targets := s.Prices(), s.Positions(), d.Plan.Targets()
	if len(prices) != len(v.Symbols) || len(positions) != len(v.Symbols) || len(targets) != len(v.Symbols) || len(d.Profile.Parameters.Rules) != len(v.Symbols) || len(d.Profile.FundingSchedules) != len(v.Symbols) {
		return invalid("universe")
	}
	for i, symbol := range v.Symbols {
		_, rule := d.Profile.Parameters.Rules[symbol]
		_, schedule := d.Profile.FundingSchedules[symbol]
		price, priced := prices[symbol]
		position, held := positions[symbol]
		if !rule || !schedule || !priced || !held || targets[i].Symbol != symbol || !exactAmount(price, true) || !sameDecimal(position, d.Cutoff.Positions[symbol].Quantity) {
			return invalid("symbol bindings")
		}
	}
	if !sameDecimal(s.Equity(), d.Cutoff.EquityUSDT) || !validateState(d.Before, v.Symbols) || !validateState(d.Cutoff, v.Symbols) || !validateState(d.ExpectedAfter, v.Symbols) || !validateFills(d.Fills, v.Symbols) {
		return invalid("state/fills")
	}
	return nil
}

// sameDecimal сравнивает Rat с обязательной обработкой ошибки.
func sameDecimal(a, b model.Decimal) bool { c, err := a.Cmp(b); return err == nil && c == 0 }
