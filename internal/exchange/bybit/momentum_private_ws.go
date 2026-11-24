package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// TestnetPrivateWebSocketURL — единственный адрес приватного потока: Mainnet не поддерживается.
const TestnetPrivateWebSocketURL = "wss://stream-testnet.bybit.com/v5/private"

// ErrPrivateStreamAuth означает отказ биржи в аутентификации или подписке.
var ErrPrivateStreamAuth = errors.New("bybit private stream authentication rejected")

// PrivateStreamClient читает приватные топики execution и order Testnet.
type PrivateStreamClient struct {
	url               string
	apiKey, apiSecret string
	dial              websocketDial
	heartbeat         time.Duration
	handshake         time.Duration
	now               func() time.Time
	State             *PrivateStreamState
}

// NewTestnetPrivateStream создаёт клиент Testnet; ключи берутся вызывающим из env.
func NewTestnetPrivateStream(apiKey, apiSecret string, dialer *websocket.Dialer) (*PrivateStreamClient, error) {
	apiKey, apiSecret = strings.TrimSpace(apiKey), strings.TrimSpace(apiSecret)
	if apiKey == "" || apiSecret == "" {
		return nil, fmt.Errorf("%w: %w: api key and secret are required", ErrPrivateStream, ErrInvalidArgument)
	}
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	return &PrivateStreamClient{url: TestnetPrivateWebSocketURL, apiKey: apiKey, apiSecret: apiSecret,
		dial: func(ctx context.Context, u string, h http.Header) (websocketConnection, *http.Response, error) {
			return dialer.DialContext(ctx, u, h)
		}, heartbeat: defaultHeartbeatPeriod, handshake: 10 * time.Second, now: time.Now, State: &PrivateStreamState{}}, nil
}

// String скрывает секреты при логировании.
func (c *PrivateStreamClient) String() string { return "PrivateStreamClient{testnet}" }

// Run держит поток, переподключаясь через backoff до отмены ctx.
func (c *PrivateStreamClient) Run(ctx context.Context, backoff time.Duration, out chan<- PrivateEvent) error {
	for {
		err := c.Stream(ctx, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrPrivateStreamAuth) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// Stream выполняет одно подключение: auth, подписку и чтение до ошибки или отмены.
// Auth и подписка ограничены таймаутом: молчащий сервер закрывается, и Run переподключается.
func (c *PrivateStreamClient) Stream(ctx context.Context, out chan<- PrivateEvent) error {
	if out == nil {
		return fmt.Errorf("%w: %w: output channel is required", ErrPrivateStream, ErrInvalidArgument)
	}
	conn, resp, err := c.dial(ctx, c.url, http.Header{"User-Agent": {"momentum-bot"}})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("%w: connect: %w", ErrPrivateStream, err)
	}
	defer conn.Close()
	defer c.State.set(false)
	conn.SetReadLimit(maxWebSocketMessage)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()
	handshakeDone := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		select {
		case <-handshakeDone:
		case <-stop:
		case <-time.After(c.handshake):
			timedOut.Store(true)
			conn.Close()
		}
	}()
	// Bybit ждёт expires числом JSON; подписывается его десятичная запись.
	expires := c.now().Add(10 * time.Second).UnixMilli()
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	err = c.command(ctx, conn, "auth", c.apiKey, expires, hex.EncodeToString(mac.Sum(nil)))
	if err == nil {
		err = c.command(ctx, conn, "subscribe", "execution.linear", "order.linear")
	}
	close(handshakeDone)
	if err != nil {
		if timedOut.Load() {
			return fmt.Errorf("%w: %w", ErrPrivateStream, errPrivateHandshakeTimeout)
		}
		return err
	}
	c.State.set(true)
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	beatErr := make(chan error, 1)
	go c.beat(conn, stop, &lastRead, beatErr)
	for {
		var msg websocketMessage
		err := conn.ReadJSON(&msg)
		if err == nil {
			lastRead.Store(time.Now().UnixNano())
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case err = <-beatErr:
			default:
			}
			return fmt.Errorf("%w: read: %w", ErrPrivateStream, err)
		}
		if msg.Operation != "" {
			continue
		}
		events, err := parsePrivateEvents(msg.Topic, msg.Data)
		if err != nil {
			return err
		}
		// Отправка учитывает и сбой heartbeat: при полном out и паузе потребителя
		// закрытие сокета не разблокирует send, а Run должен переподключиться.
		for _, event := range events {
			select {
			case out <- event:
			case <-ctx.Done():
				return ctx.Err()
			case err := <-beatErr:
				return fmt.Errorf("%w: heartbeat: %w", ErrPrivateStream, err)
			}
		}
	}
}

// privateCommand — команда с разнотипными аргументами (auth передаёт expires числом).
type privateCommand struct {
	Operation string `json:"op"`
	Arguments []any  `json:"args"`
}

// command отправляет auth/subscribe и ждёт подтверждения той же операции.
func (c *PrivateStreamClient) command(ctx context.Context, conn websocketConnection, op string, args ...any) error {
	if err := conn.WriteJSON(privateCommand{Operation: op, Arguments: args}); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrPrivateStream, op, err)
	}
	for {
		var msg websocketMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s response: %w", ErrPrivateStream, op, err)
		}
		if msg.Operation == op {
			if msg.Success == nil || !*msg.Success {
				return fmt.Errorf("%w: %w: %s", ErrPrivateStream, ErrPrivateStreamAuth, op)
			}
			return nil
		}
	}
}

// errPrivateHandshakeTimeout означает, что сервер не подтвердил auth или подписку вовремя.
var errPrivateHandshakeTimeout = errors.New("private stream handshake timed out")

// errPrivateStreamSilent означает, что сервер не отвечает дольше двух периодов heartbeat.
var errPrivateStreamSilent = errors.New("no messages or pong within heartbeat timeout")

// beat отправляет ping и следит за ответами. Если входящих сообщений (включая
// pong) нет дольше двух периодов heartbeat, соединение считается полуоткрытым:
// свежесть сразу снимается, соединение закрывается, и Run переподключается.
// Ошибка записи ping обрабатывается так же.
func (c *PrivateStreamClient) beat(conn websocketConnection, stop <-chan struct{}, lastRead *atomic.Int64, failed chan<- error) {
	ticker := time.NewTicker(c.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			err := errPrivateStreamSilent
			if time.Since(time.Unix(0, lastRead.Load())) <= 2*c.heartbeat {
				if err = conn.WriteJSON(websocketCommand{Operation: "ping"}); err == nil {
					continue
				}
			}
			c.State.set(false)
			failed <- err
			conn.Close()
			return
		}
	}
}
