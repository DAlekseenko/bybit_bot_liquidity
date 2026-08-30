package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestLiquidationClientStreamsEventsAndHeartbeat(t *testing.T) {
	t.Parallel()

	connection := newWebSocketConnectionFake(
		websocketMessage{Operation: "subscribe", Success: boolPointer(true)},
		websocketMessage{
			Topic: "allLiquidation.BTCUSDT",
			Data: json.RawMessage(`[
				{"T":1753264800000,"s":"BTCUSDT","S":"Buy","v":"2.5","p":"50000"},
				{"T":1753264800100,"s":"BTCUSDT","S":"Sell","v":"1.5","p":"51000"}
			]`),
		},
	)
	client := NewLiquidationClient("wss://stream.example.test/v5/public/linear", nil)
	client.heartbeatPeriod = 10 * time.Millisecond
	client.dial = func(context.Context, string, http.Header) (websocketConnection, *http.Response, error) {
		return connection, nil, nil
	}

	output := make(chan exchange.Liquidation, 2)
	ctx, cancel := context.WithCancel(context.Background())
	streamError := make(chan error, 1)
	go func() {
		streamError <- client.Stream(ctx, "BTCUSDT", output)
	}()

	first := receiveLiquidation(t, output)
	second := receiveLiquidation(t, output)
	if first.Side != exchange.SideLong || first.Price != 50000 || first.Qty != 2.5 {
		t.Fatalf("first liquidation = %#v", first)
	}
	if second.Side != exchange.SideShort || second.Price != 51000 || second.Qty != 1.5 {
		t.Fatalf("second liquidation = %#v", second)
	}

	subscription := receiveCommand(t, connection.writes)
	if subscription.Operation != "subscribe" || len(subscription.Arguments) != 1 || subscription.Arguments[0] != "allLiquidation.BTCUSDT" {
		t.Fatalf("subscription = %#v", subscription)
	}
	heartbeat := receiveCommand(t, connection.writes)
	if heartbeat.Operation != "ping" {
		t.Fatalf("heartbeat = %#v", heartbeat)
	}

	cancel()
	select {
	case err := <-streamError:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stream() did not stop after cancellation")
	}
}

func TestParseLiquidationMessageRejectsInvalidData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data any
	}{
		{name: "wrong symbol", data: []map[string]any{{"T": 1, "s": "ETHUSDT", "S": "Buy", "v": "1", "p": "1"}}},
		{name: "wrong side", data: []map[string]any{{"T": 1, "s": "BTCUSDT", "S": "Unknown", "v": "1", "p": "1"}}},
		{name: "zero quantity", data: []map[string]any{{"T": 1, "s": "BTCUSDT", "S": "Buy", "v": "0", "p": "1"}}},
		{name: "invalid timestamp", data: []map[string]any{{"T": 0, "s": "BTCUSDT", "S": "Buy", "v": "1", "p": "1"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(test.data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseLiquidationMessage("BTCUSDT", data); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("parseLiquidationMessage() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestLiquidationClientValidatesArguments(t *testing.T) {
	t.Parallel()

	client := NewLiquidationClient("https://example.com", nil)
	err := client.Stream(context.Background(), "BTCUSDT", make(chan exchange.Liquidation))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Stream() error = %v, want ErrInvalidArgument", err)
	}
}

type websocketConnectionFake struct {
	reads     chan websocketMessage
	writes    chan websocketCommand
	closed    chan struct{}
	closeOnce sync.Once
}

func newWebSocketConnectionFake(messages ...websocketMessage) *websocketConnectionFake {
	connection := &websocketConnectionFake{
		reads:  make(chan websocketMessage, len(messages)),
		writes: make(chan websocketCommand, 4),
		closed: make(chan struct{}),
	}
	for _, message := range messages {
		connection.reads <- message
	}
	return connection
}

func (c *websocketConnectionFake) ReadJSON(value any) error {
	select {
	case message := <-c.reads:
		target, ok := value.(*websocketMessage)
		if !ok {
			return errors.New("unexpected read target")
		}
		*target = message
		return nil
	case <-c.closed:
		return io.EOF
	}
}

func (c *websocketConnectionFake) WriteJSON(value any) error {
	command, ok := value.(websocketCommand)
	if !ok {
		return errors.New("unexpected write value")
	}
	select {
	case c.writes <- command:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *websocketConnectionFake) SetReadLimit(int64) {}

func (c *websocketConnectionFake) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	return nil
}

func receiveLiquidation(t *testing.T, output <-chan exchange.Liquidation) exchange.Liquidation {
	t.Helper()

	select {
	case liquidation := <-output:
		return liquidation
	case <-time.After(time.Second):
		t.Fatal("liquidation was not received")
		return exchange.Liquidation{}
	}
}

func receiveCommand(t *testing.T, commands <-chan websocketCommand) websocketCommand {
	t.Helper()

	select {
	case command := <-commands:
		return command
	case <-time.After(time.Second):
		t.Fatal("websocket command was not written")
		return websocketCommand{}
	}
}

func boolPointer(value bool) *bool {
	return &value
}
