package bybit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrPrivateStream — общая категория ошибок приватного потока.
	ErrPrivateStream = errors.New("bybit private stream failed")
	// ErrPrivateStreamData означает некорректное событие; поток останавливается.
	ErrPrivateStreamData = errors.New("bybit private stream event is malformed")
)

// PrivateEventKind — тип события приватного потока.
type PrivateEventKind string

const (
	// PrivateExecution — исполнение (fill) заявки.
	PrivateExecution PrivateEventKind = "EXECUTION"
	// PrivateOrder — изменение состояния заявки.
	PrivateOrder PrivateEventKind = "ORDER"
)

// PrivateEvent — типизированное событие. Повторы и нарушенный порядок
// передаются как есть: идемпотентность обеспечивает ledger по ExecutionID.
type PrivateEvent struct {
	Kind        PrivateEventKind
	OrderID     string
	OrderLinkID string
	ExecType    string              // только для EXECUTION: Trade, AdlTrade, BustTrade
	Fill        model.ConfirmedFill // только для EXECUTION
	Symbol      string
	Status      string        // только для ORDER
	CumExecQty  model.Decimal // только для ORDER
	UpdatedAt   time.Time
}

// PrivateStreamState сообщает, можно ли считать состояние из потока свежим.
// После любого (пере)подключения состояние не свежее до MarkResynced: события
// за время разрыва потеряны, и их нужно восстановить через REST. Каждое
// подключение и отключение меняет generation, поэтому resync, начатый при
// прежнем подключении, не делает свежим новое.
type PrivateStreamState struct {
	mu         sync.Mutex
	live       bool
	resynced   bool
	generation uint64
}

// Fresh — поток подключён и после подключения выполнен REST resync.
func (s *PrivateStreamState) Fresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live && s.resynced
}

// Generation возвращает номер текущего состояния подключения; его снимают до
// начала REST resync и передают в MarkResynced.
func (s *PrivateStreamState) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

// MarkResynced фиксирует REST resync, начатый при generation. Если с тех пор
// поток переподключался или отключился, resync не засчитывается (false).
func (s *PrivateStreamState) MarkResynced(generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live || s.generation != generation {
		return false
	}
	s.resynced = true
	return true
}

// set меняет признак подключения; любое изменение сбрасывает resync и меняет generation.
func (s *PrivateStreamState) set(live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live, s.resynced = live, false
	s.generation++
}

// privateItem — поля execution и order событий Bybit V5.
type privateItem struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	ExecID      string `json:"execId"`
	ExecType    string `json:"execType"`
	ExecPrice   string `json:"execPrice"`
	ExecQty     string `json:"execQty"`
	ExecFee     string `json:"execFee"`
	FeeCurrency string `json:"feeCurrency"`
	ExecTime    string `json:"execTime"`
	UpdatedTime string `json:"updatedTime"`
	OrderStatus string `json:"orderStatus"`
	CumExecQty  string `json:"cumExecQty"`
}

// parsePrivateEvents разбирает данные топика; неизвестный топик игнорируется.
func parsePrivateEvents(topic string, data json.RawMessage) ([]PrivateEvent, error) {
	if topic != "execution.linear" && topic != "order.linear" {
		return nil, nil
	}
	var items []privateItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrivateStream, ErrPrivateStreamData)
	}
	events := make([]PrivateEvent, 0, len(items))
	for _, it := range items {
		event, ok := privateEvent(topic, it)
		if !ok {
			return nil, fmt.Errorf("%w: %w: %s", ErrPrivateStream, ErrPrivateStreamData, topic)
		}
		if event.Kind != "" {
			events = append(events, event)
		}
	}
	return events, nil
}

// privateEvent проверяет одно событие; Kind=="" — событие не о сделке (например, Funding).
func privateEvent(topic string, it privateItem) (PrivateEvent, bool) {
	if it.Category != "linear" || it.Symbol == "" || it.OrderID == "" {
		return PrivateEvent{}, false
	}
	e := PrivateEvent{OrderID: it.OrderID, OrderLinkID: it.OrderLinkID, Symbol: it.Symbol}
	if topic == "order.linear" {
		ms, err := strconv.ParseInt(it.UpdatedTime, 10, 64)
		qty, qErr := model.ParseQty(it.CumExecQty)
		if err != nil || ms <= 0 || qErr != nil || it.OrderStatus == "" {
			return PrivateEvent{}, false
		}
		e.Kind, e.Status, e.CumExecQty, e.UpdatedAt = PrivateOrder, it.OrderStatus, qty, time.UnixMilli(ms).UTC()
		return e, true
	}
	switch it.ExecType {
	case "Trade", "AdlTrade", "BustTrade":
	default:
		return PrivateEvent{}, true
	}
	side := map[string]model.FillSide{"Buy": model.FillBuy, "Sell": model.FillSell}[it.Side]
	ms, err := strconv.ParseInt(it.ExecTime, 10, 64)
	qty, qErr := model.ParseQty(it.ExecQty)
	price, pErr := model.ParsePrice(it.ExecPrice)
	fee, fErr := model.ParseAmount(it.ExecFee)
	if side == "" || it.ExecID == "" || err != nil || ms <= 0 || qErr != nil || pErr != nil || fErr != nil {
		return PrivateEvent{}, false
	}
	if c, err := qty.Cmp(model.Zero()); err != nil || c <= 0 {
		return PrivateEvent{}, false
	}
	// ConfirmedFill хранит комиссию в USDT: fill с другой валютой комиссии или
	// расчётов (например, USDC-контракт) отвергается, а не записывается как USDT.
	if it.FeeCurrency != "USDT" && (it.FeeCurrency != "" || !strings.HasSuffix(it.Symbol, "USDT")) {
		return PrivateEvent{}, false
	}
	e.Kind, e.ExecType, e.UpdatedAt = PrivateExecution, it.ExecType, time.UnixMilli(ms).UTC()
	e.Fill = model.ConfirmedFill{ExecutionID: it.ExecID, Symbol: it.Symbol, Side: side, ExecutedAt: e.UpdatedAt,
		Quantity: qty, ExecutionPrice: price, FeeUSDT: fee}
	return e, true
}
