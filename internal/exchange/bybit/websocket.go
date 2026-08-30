package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/exchange"

	"github.com/gorilla/websocket"
)

const (
	liquidationTopicPrefix = "allLiquidation."
	defaultHeartbeatPeriod = 20 * time.Second
	maxWebSocketMessage    = 1 << 20
)

// LiquidationClient читает публичный Bybit V5 WebSocket topic ликвидаций.
type LiquidationClient struct {
	websocketURL    string
	dial            websocketDial
	heartbeatPeriod time.Duration
}

func NewLiquidationClient(websocketURL string, dialer *websocket.Dialer) *LiquidationClient {
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	return &LiquidationClient{
		websocketURL: strings.TrimSpace(websocketURL),
		dial: func(ctx context.Context, url string, header http.Header) (websocketConnection, *http.Response, error) {
			return dialer.DialContext(ctx, url, header)
		},
		heartbeatPeriod: defaultHeartbeatPeriod,
	}
}

// Stream подключается один раз и передаёт события до ошибки или отмены context.
// Повторное подключение остаётся ответственностью вызывающего кода.
func (c *LiquidationClient) Stream(ctx context.Context, symbol string, output chan<- exchange.Liquidation) error {
	if err := validateSymbol(symbol); err != nil {
		return err
	}
	if output == nil {
		return fmt.Errorf("%w: liquidation output channel is required", ErrInvalidArgument)
	}
	if err := validateWebSocketURL(c.websocketURL); err != nil {
		return err
	}

	connection, response, err := c.dial(ctx, c.websocketURL, http.Header{"User-Agent": {"bybit-liquidity-bot"}})
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect liquidation websocket: %w", err)
	}
	defer connection.Close()
	connection.SetReadLimit(maxWebSocketMessage)

	topic := liquidationTopicPrefix + symbol
	if err := connection.WriteJSON(websocketCommand{Operation: "subscribe", Arguments: []string{topic}}); err != nil {
		return fmt.Errorf("subscribe liquidation websocket: %w", err)
	}

	stopHeartbeat := make(chan struct{})
	heartbeatError := make(chan error, 1)
	go c.heartbeat(connection, stopHeartbeat, heartbeatError)
	defer close(stopHeartbeat)

	stopOnCancel := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-stopOnCancel:
		}
	}()
	defer close(stopOnCancel)

	for {
		var message websocketMessage
		if err := connection.ReadJSON(&message); err != nil {
			select {
			case heartbeatErr := <-heartbeatError:
				return heartbeatErr
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read liquidation websocket: %w", err)
		}

		if message.Operation != "" {
			if message.Operation == "subscribe" && message.Success != nil && !*message.Success {
				return fmt.Errorf("subscribe liquidation websocket: %s", message.ReturnMessage)
			}
			continue
		}
		if message.Topic != topic {
			continue
		}

		liquidations, err := parseLiquidationMessage(symbol, message.Data)
		if err != nil {
			return err
		}
		for _, liquidation := range liquidations {
			select {
			case output <- liquidation:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (c *LiquidationClient) heartbeat(connection websocketConnection, stop <-chan struct{}, heartbeatError chan<- error) {
	ticker := time.NewTicker(c.heartbeatPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := connection.WriteJSON(websocketCommand{Operation: "ping"}); err != nil {
				select {
				case heartbeatError <- fmt.Errorf("write liquidation websocket heartbeat: %w", err):
				default:
				}
				connection.Close()
				return
			}
		}
	}
}

type websocketConnection interface {
	ReadJSON(value any) error
	WriteJSON(value any) error
	SetReadLimit(limit int64)
	Close() error
}

type websocketDial func(context.Context, string, http.Header) (websocketConnection, *http.Response, error)

type websocketCommand struct {
	Operation string   `json:"op"`
	Arguments []string `json:"args,omitempty"`
}

type websocketMessage struct {
	Topic         string          `json:"topic"`
	Operation     string          `json:"op"`
	Success       *bool           `json:"success"`
	ReturnMessage string          `json:"ret_msg"`
	Data          json.RawMessage `json:"data"`
}

type liquidationItem struct {
	Timestamp int64  `json:"T"`
	Symbol    string `json:"s"`
	Side      string `json:"S"`
	Quantity  string `json:"v"`
	Price     string `json:"p"`
}

func parseLiquidationMessage(expectedSymbol string, data json.RawMessage) ([]exchange.Liquidation, error) {
	var items []liquidationItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse liquidation websocket data: %w", err)
	}

	result := make([]exchange.Liquidation, 0, len(items))
	for index, item := range items {
		if item.Symbol != expectedSymbol {
			return nil, fmt.Errorf("parse liquidation item %d: %w: unexpected symbol %q", index, ErrInvalidArgument, item.Symbol)
		}
		side, err := parseLiquidationSide(item.Side)
		if err != nil {
			return nil, fmt.Errorf("parse liquidation item %d: %w", index, err)
		}
		price, err := parsePositiveNumber("price", item.Price)
		if err != nil {
			return nil, fmt.Errorf("parse liquidation item %d: %w", index, err)
		}
		quantity, err := parsePositiveNumber("quantity", item.Quantity)
		if err != nil {
			return nil, fmt.Errorf("parse liquidation item %d: %w", index, err)
		}
		if item.Timestamp <= 0 {
			return nil, fmt.Errorf("parse liquidation item %d: %w: invalid timestamp", index, ErrInvalidArgument)
		}

		result = append(result, exchange.Liquidation{
			Symbol: item.Symbol,
			Side:   side,
			Price:  price,
			Qty:    quantity,
			Time:   time.UnixMilli(item.Timestamp).UTC(),
		})
	}
	return result, nil
}

func parseLiquidationSide(value string) (exchange.Side, error) {
	switch value {
	case "Buy":
		return exchange.SideLong, nil
	case "Sell":
		return exchange.SideShort, nil
	default:
		return "", fmt.Errorf("%w: unsupported liquidation side %q", ErrInvalidArgument, value)
	}
}

func parsePositiveNumber(name, value string) (float64, error) {
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%w: invalid liquidation %s %q", ErrInvalidArgument, name, value)
	}
	return number, nil
}

func validateWebSocketURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return fmt.Errorf("%w: websocket URL must be absolute ws or wss URL", ErrInvalidArgument)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: websocket URL must not include user info", ErrInvalidArgument)
	}
	return nil
}
