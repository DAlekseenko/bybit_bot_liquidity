package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// momentumInventoryMaxPages ограничивает обход курсора, чтобы цикл курсора не стал бесконечным.
const momentumInventoryMaxPages = 100

// momentumPage описывает одну страницу Bybit V5 со списком и курсором.
type momentumPage[T any] struct {
	Category       string  `json:"category"`
	NextPageCursor *string `json:"nextPageCursor"`
	List           *[]T    `json:"list"`
}

// momentumOrderItem описывает строковые поля одной активной заявки.
type momentumOrderItem struct {
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	PositionIdx *int   `json:"positionIdx"`
	OrderStatus string `json:"orderStatus"`
	Qty         string `json:"qty"`
	LeavesQty   string `json:"leavesQty"`
	Price       string `json:"price"`
	ReduceOnly  *bool  `json:"reduceOnly"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
}

// collectMomentumPages читает все страницы курсора и возвращает раннее время сервера.
func collectMomentumPages[T any](ctx context.Context, c *Client, path string, base url.Values) ([]T, time.Time, error) {
	items := make([]T, 0)
	var oldest time.Time
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < momentumInventoryMaxPages; page++ {
		query := url.Values{}
		for key, values := range base {
			query[key] = values
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var result momentumPage[T]
		server, err := c.momentumSignedGet(ctx, path, query, &result)
		if err != nil {
			return nil, time.Time{}, err
		}
		if result.Category != linearCategory || result.List == nil || result.NextPageCursor == nil {
			return nil, time.Time{}, fmt.Errorf("%w: incomplete page metadata", ErrMalformedAccountData)
		}
		if oldest.IsZero() || server.Before(oldest) {
			oldest = server
		}
		items = append(items, *result.List...)
		cursor = *result.NextPageCursor
		if cursor == "" {
			return items, oldest, nil
		}
		if seen[cursor] {
			return nil, time.Time{}, fmt.Errorf("%w: repeated page cursor", ErrMalformedAccountData)
		}
		seen[cursor] = true
	}
	return nil, time.Time{}, fmt.Errorf("%w: page limit exceeded", ErrMalformedAccountData)
}

// GetMomentumPositions читает все ненулевые USDT linear позиции счёта по курсору.
func (c *Client) GetMomentumPositions(ctx context.Context) ([]model.AccountPosition, time.Time, error) {
	base := url.Values{"category": {linearCategory}, "settleCoin": {momentumSettleCoin}, "limit": {"200"}}
	items, server, err := collectMomentumPages[momentumPositionItem](ctx, c, "/v5/position/list", base)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("get momentum positions: %w", err)
	}
	positions := make([]model.AccountPosition, 0, len(items))
	keys := map[string]bool{}
	for _, item := range items {
		position, err := parseMomentumPosition(item, server)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("get momentum positions: %w", err)
		}
		key := position.Symbol + "/" + strconv.Itoa(position.PositionIdx)
		if keys[key] {
			return nil, time.Time{}, fmt.Errorf("get momentum positions: %w: duplicate %s", ErrMalformedAccountData, key)
		}
		keys[key] = true
		positions = append(positions, position)
	}
	return positions, server, nil
}

// GetMomentumOpenOrders читает все активные USDT linear заявки счёта по курсору.
func (c *Client) GetMomentumOpenOrders(ctx context.Context) ([]model.AccountOrder, time.Time, error) {
	base := url.Values{"category": {linearCategory}, "settleCoin": {momentumSettleCoin}, "openOnly": {"0"}, "limit": {"50"}}
	items, server, err := collectMomentumPages[momentumOrderItem](ctx, c, "/v5/order/realtime", base)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("get momentum open orders: %w", err)
	}
	orders := make([]model.AccountOrder, 0, len(items))
	ids := map[string]bool{}
	for _, item := range items {
		order, err := parseMomentumOrder(item)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("get momentum open orders: %w", err)
		}
		if ids[order.OrderID] {
			return nil, time.Time{}, fmt.Errorf("get momentum open orders: %w: duplicate order", ErrMalformedAccountData)
		}
		ids[order.OrderID] = true
		orders = append(orders, order)
	}
	return orders, server, nil
}

// GetMomentumAccountInventory читает все позиции и активные заявки счёта.
func (c *Client) GetMomentumAccountInventory(ctx context.Context) (model.AccountInventory, error) {
	positions, positionTime, err := c.GetMomentumPositions(ctx)
	if err != nil {
		return model.AccountInventory{}, err
	}
	orders, orderTime, err := c.GetMomentumOpenOrders(ctx)
	if err != nil {
		return model.AccountInventory{}, err
	}
	oldest := positionTime
	if orderTime.Before(oldest) {
		oldest = orderTime
	}
	return model.AccountInventory{Positions: positions, Orders: orders, ServerTime: oldest}, nil
}

// parseMomentumOrder разбирает активную заявку с точными количествами и известным статусом.
func parseMomentumOrder(item momentumOrderItem) (model.AccountOrder, error) {
	bad := func(field string) (model.AccountOrder, error) {
		return model.AccountOrder{}, fmt.Errorf("%w: order %q: invalid %s", ErrMalformedAccountData, item.OrderID, field)
	}
	if item.OrderID == "" || len(item.OrderLinkID) > 64 {
		return bad("id")
	}
	if !markPriceSymbolPattern.MatchString(item.Symbol) {
		return bad("symbol")
	}
	if item.Side != "Buy" && item.Side != "Sell" {
		return bad("side")
	}
	if item.PositionIdx == nil || *item.PositionIdx < 0 || *item.PositionIdx > 2 {
		return bad("positionIdx")
	}
	switch item.OrderStatus {
	case "New", "PartiallyFilled", "Untriggered":
	default:
		return bad("orderStatus")
	}
	if item.ReduceOnly == nil {
		return bad("reduceOnly")
	}
	o := model.AccountOrder{OrderID: item.OrderID, OrderLinkID: item.OrderLinkID, Symbol: item.Symbol, Side: item.Side,
		Status: item.OrderStatus, PositionIdx: *item.PositionIdx, ReduceOnly: *item.ReduceOnly}
	var err error
	if o.Qty, err = model.ParsePrice(item.Qty); err != nil {
		return bad("qty")
	}
	if o.LeavesQty, err = model.ParseQty(item.LeavesQty); err != nil {
		return bad("leavesQty")
	}
	if over, _ := o.LeavesQty.Cmp(o.Qty); over > 0 {
		return bad("leavesQty")
	}
	if o.Price, err = model.ParseQty(item.Price); err != nil {
		return bad("price")
	}
	for _, f := range []struct {
		raw string
		dst *time.Time
	}{{item.CreatedTime, &o.CreatedTime}, {item.UpdatedTime, &o.UpdatedTime}} {
		ms, err := strconv.ParseInt(f.raw, 10, 64)
		if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != f.raw {
			return bad("time")
		}
		*f.dst = time.UnixMilli(ms).UTC()
	}
	return o, nil
}
