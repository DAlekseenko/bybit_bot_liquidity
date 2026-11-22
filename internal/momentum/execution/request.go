// Package execution содержит чистые контракты исполнения TradePlan без I/O.
package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

var (
	// ErrInvalidIntentInput обозначает несогласованные план, проекцию или правила.
	ErrInvalidIntentInput = errors.New("invalid momentum order intent input")
	// ErrIntentEnvironment обозначает попытку построить заявку вне Testnet.
	ErrIntentEnvironment = errors.New("momentum order intents are testnet only")
	// ErrInvalidWireIntent обозначает намерение, непригодное для wire-запроса.
	ErrInvalidWireIntent = errors.New("invalid momentum wire order intent")
	// ErrInvalidSlippage обозначает некорректный допуск проскальзывания.
	ErrInvalidSlippage = errors.New("invalid momentum slippage tolerance")
)

const (
	// IntentEnvironment — единственная среда, для которой строятся заявки.
	IntentEnvironment = "testnet"
	// WireCategory — категория Bybit V5 для USDT perpetual.
	WireCategory = "linear"
	// WireOrderType — тип заявки движка: только market.
	WireOrderType = "Market"
	// WirePositionIdx — индекс позиции one-way режима.
	WirePositionIdx = 0
	// MaxOrderLinkIDLen — ограничение Bybit на длину orderLinkId.
	MaxOrderLinkIDLen = 36
)

// Leg обозначает роль заявки внутри ревизии плана.
type Leg string

// Роли заявки: сокращение (Sell, reduceOnly) и увеличение (Buy).
const (
	LegReduce   Leg = "reduce"
	LegIncrease Leg = "increase"
)

// OrderIntent — нормализованное намерение market-заявки одной ревизии плана.
type OrderIntent struct {
	IntentID, OrderLinkID         string
	Environment, AccountID, RunID string
	PlanRevision                  uint64
	PlanHash                      string
	Symbol                        string
	Leg                           Leg
	Side                          model.FillSide
	ReduceOnly                    bool
	Quantity, UnplannedQty        model.Decimal
	Residual                      planner.DeltaResidual
}

// SkippedDelta описывает требуемую дельту без заявки и явную причину (dust).
type SkippedDelta struct {
	Symbol       string
	ReduceOnly   bool
	RequestedQty model.Decimal
	Reason       planner.DeltaResidual
}

// IntentBatch хранит заявки (сначала сокращения) и пропущенные дельты.
type IntentBatch struct {
	Intents []OrderIntent
	Skipped []SkippedDelta
}

// BuildOrderIntents превращает проекцию дельт TradePlan в упорядоченные заявки.
// Количество повторно сверяется с планом и правилами и никогда не округляется вверх.
func BuildOrderIntents(plan model.TradePlan, projection planner.DeltaProjection, rules map[string]planner.MarketDeltaRules) (IntentBatch, error) {
	snapshot, meta := plan.Snapshot(), plan.Metadata()
	if plan.Hash() == "" || projection.PlanHash() != plan.Hash() || meta.Revision == 0 || strings.TrimSpace(meta.RunID) == "" {
		return IntentBatch{}, ErrInvalidIntentInput
	}
	if snapshot.Environment() != IntentEnvironment {
		return IntentBatch{}, ErrIntentEnvironment
	}
	positions, prices := snapshot.Positions(), snapshot.Prices()
	pending, targets := 0, make(map[string]model.Decimal)
	for _, target := range plan.Targets() {
		targets[target.Symbol] = target.Quantity
		if cmp, err := target.Quantity.Cmp(positions[target.Symbol]); err != nil {
			return IntentBatch{}, ErrInvalidIntentInput
		} else if cmp != 0 {
			pending++
		}
	}
	var batch IntentBatch
	deltas := projection.Deltas()
	if len(deltas) != pending {
		return IntentBatch{}, ErrInvalidIntentInput
	}
	seen, increasing := make(map[string]bool, len(deltas)), false
	for _, d := range deltas {
		if seen[d.Symbol] || (d.ReduceOnly && increasing) {
			return IntentBatch{}, ErrInvalidIntentInput
		}
		seen[d.Symbol], increasing = true, increasing || !d.ReduceOnly
		target, hasTarget := targets[d.Symbol]
		rule, hasRule := rules[d.Symbol]
		if !hasTarget || !hasRule || !matchesRequest(d, positions[d.Symbol], target) {
			return IntentBatch{}, ErrInvalidIntentInput
		}
		if sign, _ := d.CandidateQty.Cmp(model.Zero()); sign == 0 {
			if d.Residual == "" {
				return IntentBatch{}, ErrInvalidIntentInput
			}
			batch.Skipped = append(batch.Skipped, SkippedDelta{d.Symbol, d.ReduceOnly, d.RequestedQty, d.Residual})
			continue
		}
		leg, side := LegIncrease, model.FillBuy
		if d.ReduceOnly {
			leg, side = LegReduce, model.FillSell
		}
		linkID, err := OrderLinkID(plan.Hash(), meta.Revision, d.Symbol, leg)
		if err != nil || !normalizedCandidate(d, prices[d.Symbol], rule) {
			return IntentBatch{}, ErrInvalidIntentInput
		}
		// IntentID стабилен для пары счёт + orderLinkId.
		id := sha256.Sum256([]byte("momentum/order-intent/v1\x00" + snapshot.AccountID() + "\x00" + linkID))
		batch.Intents = append(batch.Intents, OrderIntent{
			IntentID: hex.EncodeToString(id[:]), OrderLinkID: linkID,
			Environment: snapshot.Environment(), AccountID: snapshot.AccountID(), RunID: meta.RunID,
			PlanRevision: meta.Revision, PlanHash: plan.Hash(), Symbol: d.Symbol, Leg: leg, Side: side,
			ReduceOnly: d.ReduceOnly, Quantity: d.CandidateQty, UnplannedQty: d.UnplannedQty, Residual: d.Residual,
		})
	}
	return batch, nil
}

// matchesRequest проверяет, что дельта равна точной разнице позиции и цели плана.
func matchesRequest(d planner.PositionDelta, current, target model.Decimal) bool {
	from, to := current, target
	if d.ReduceOnly {
		from, to = target, current
	}
	requested, err := to.Sub(from)
	if err != nil {
		return false
	}
	positive, err1 := requested.Cmp(model.Zero())
	same, err2 := requested.Cmp(d.RequestedQty)
	upper, err3 := d.CandidateQty.Cmp(requested)
	sum, err4 := d.CandidateQty.Add(d.UnplannedQty)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || positive <= 0 || same != 0 || upper > 0 {
		return false
	}
	total, err := sum.Cmp(requested)
	return err == nil && total == 0
}

// normalizedCandidate проверяет шаг, min/max Qty и min notional увеличения.
func normalizedCandidate(d planner.PositionDelta, price model.Decimal, rule planner.MarketDeltaRules) bool {
	floored, err := model.FloorQty(d.CandidateQty, rule.QtyStep)
	if err != nil {
		return false
	}
	aligned, err1 := floored.Cmp(d.CandidateQty)
	low, err2 := d.CandidateQty.Cmp(rule.MinQty)
	high, err3 := d.CandidateQty.Cmp(rule.MaxMarketQty)
	_, err4 := d.CandidateQty.WireDecimal()
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || aligned != 0 || low < 0 || high > 0 {
		return false
	}
	if d.ReduceOnly {
		return true
	}
	ok, err := model.MeetsMinNotional(d.CandidateQty, price, rule.MinIncreaseNotional)
	return err == nil && ok
}

// OrderLinkID детерминированно выводит orderLinkId из hash и ревизии плана,
// символа и роли заявки: "cm-" и 32 hex-символа, в пределах 36 символов Bybit.
func OrderLinkID(planHash string, revision uint64, symbol string, leg Leg) (string, error) {
	raw, err := hex.DecodeString(planHash)
	if err != nil || len(raw) != sha256.Size || planHash != strings.ToLower(planHash) || revision == 0 ||
		strings.TrimSpace(symbol) == "" || (leg != LegReduce && leg != LegIncrease) {
		return "", ErrInvalidIntentInput
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"momentum/order-link/v1", planHash,
		strconv.FormatUint(revision, 10), symbol, string(leg)}, "\x00")))
	return "cm-" + hex.EncodeToString(sum[:16]), nil
}

// SlippageType — тип допуска проскальзывания market-заявки Bybit.
type SlippageType string

const (
	SlippageTickSize SlippageType = "TickSize"
	SlippagePercent  SlippageType = "Percent"
)

// Slippage задаёт допуск проскальзывания; пустой Type означает отсутствие поля.
// Численное значение — решение владельца (D-04), умолчания нет.
type Slippage struct {
	Type      SlippageType
	Tolerance model.Decimal
}

// createOrderWire фиксирует поля и порядок тела POST /v5/order/create; SL/TP нет.
type createOrderWire struct {
	Category              string `json:"category"`
	Symbol                string `json:"symbol"`
	Side                  string `json:"side"`
	OrderType             string `json:"orderType"`
	Qty                   string `json:"qty"`
	PositionIdx           int    `json:"positionIdx"`
	ReduceOnly            bool   `json:"reduceOnly"`
	OrderLinkID           string `json:"orderLinkId"`
	SlippageToleranceType string `json:"slippageToleranceType,omitempty"`
	SlippageTolerance     string `json:"slippageTolerance,omitempty"`
}

// EncodeCreateOrder сериализует намерение в тело Bybit V5 create order.
// Сторона, reduceOnly и orderLinkId повторно сверяются с ролью заявки.
func EncodeCreateOrder(intent OrderIntent, slippage Slippage) ([]byte, error) {
	qty, err := intent.Quantity.WireDecimal()
	if positive, _ := intent.Quantity.Cmp(model.Zero()); err != nil || positive <= 0 {
		return nil, ErrInvalidWireIntent
	}
	if intent.Environment != IntentEnvironment {
		return nil, ErrIntentEnvironment
	}
	side := map[model.FillSide]string{model.FillBuy: "Buy", model.FillSell: "Sell"}[intent.Side]
	consistent := (intent.Leg == LegReduce && intent.Side == model.FillSell && intent.ReduceOnly) ||
		(intent.Leg == LegIncrease && intent.Side == model.FillBuy && !intent.ReduceOnly)
	linkID, err := OrderLinkID(intent.PlanHash, intent.PlanRevision, intent.Symbol, intent.Leg)
	if err != nil || !consistent || linkID != intent.OrderLinkID {
		return nil, ErrInvalidWireIntent
	}
	body := createOrderWire{Category: WireCategory, Symbol: intent.Symbol, Side: side, OrderType: WireOrderType,
		Qty: qty, PositionIdx: WirePositionIdx, ReduceOnly: intent.ReduceOnly, OrderLinkID: linkID}
	if slippage.Type != "" {
		tolerance, err := slippage.Tolerance.WireDecimal()
		positive, _ := slippage.Tolerance.Cmp(model.Zero())
		if (slippage.Type != SlippageTickSize && slippage.Type != SlippagePercent) || err != nil || positive <= 0 {
			return nil, ErrInvalidSlippage
		}
		body.SlippageToleranceType, body.SlippageTolerance = string(slippage.Type), tolerance
	}
	return json.Marshal(body)
}
