package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakePrivateServer поднимает WS-сервер; script вызывается на каждое подключение с его номером.
func fakePrivateServer(t *testing.T, script func(n int, conn *websocket.Conn)) (*PrivateStreamClient, *int32) {
	t.Helper()
	var count int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		script(int(atomic.AddInt32(&count, 1)), conn)
	}))
	t.Cleanup(server.Close)
	client, err := NewTestnetPrivateStream("key", "secret-marker", nil)
	if err != nil {
		t.Fatal(err)
	}
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return client, &count
}

// handshake проверяет подпись auth и подписку, отвечая authOK на auth.
func handshake(t *testing.T, conn *websocket.Conn, authOK bool) bool {
	var auth struct {
		Operation string            `json:"op"`
		Arguments []json.RawMessage `json:"args"`
	}
	var key, signature string
	var expires int64
	if conn.ReadJSON(&auth) != nil || auth.Operation != "auth" || len(auth.Arguments) != 3 ||
		json.Unmarshal(auth.Arguments[0], &key) != nil || json.Unmarshal(auth.Arguments[1], &expires) != nil ||
		json.Unmarshal(auth.Arguments[2], &signature) != nil {
		t.Errorf("bad auth (expires must be a JSON number) %+v", auth)
		return false
	}
	mac := hmac.New(sha256.New, []byte("secret-marker"))
	mac.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	if key != "key" || signature != hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("bad signature %+v", auth)
	}
	_ = conn.WriteJSON(map[string]any{"op": "auth", "success": authOK})
	if !authOK {
		return false
	}
	var sub websocketCommand
	if conn.ReadJSON(&sub) != nil || sub.Operation != "subscribe" || strings.Join(sub.Arguments, ",") != "execution.linear,order.linear" {
		t.Errorf("bad subscribe %+v", sub)
	}
	_ = conn.WriteJSON(map[string]any{"op": "subscribe", "success": true})
	return true
}

// execution формирует сообщение топика execution.linear.
func execution(id, execType, at string) map[string]any {
	return map[string]any{"topic": "execution.linear", "data": []map[string]string{{"category": "linear", "symbol": "BTCUSDT",
		"side": "Buy", "orderId": "o1", "orderLinkId": "l1", "execId": id, "execType": execType, "execPrice": "90000.5",
		"execQty": "0.004", "execFee": "-0.01", "execTime": at}}}
}

// TestPrivateStreamEvents проверяет auth, типизированные события, повторы, порядок и флаг свежести.
func TestPrivateStreamEvents(t *testing.T) {
	release := make(chan struct{})
	client, _ := fakePrivateServer(t, func(_ int, conn *websocket.Conn) {
		if !handshake(t, conn, true) {
			return
		}
		for _, m := range []map[string]any{execution("e2", "Trade", "1700000002000"), execution("e2", "Trade", "1700000002000"),
			execution("e1", "Trade", "1700000001000"), execution("f1", "Funding", "1700000003000"),
			{"topic": "order.linear", "data": []map[string]string{{"category": "linear", "symbol": "BTCUSDT", "orderId": "o1",
				"orderLinkId": "l1", "orderStatus": "PartiallyFilled", "cumExecQty": "0.008", "updatedTime": "1700000004000"}}}} {
			_ = conn.WriteJSON(m)
		}
		<-release
	})
	out := make(chan PrivateEvent, 8)
	done := make(chan error, 1)
	go func() { done <- client.Stream(context.Background(), out) }()
	var got []string
	for i := 0; i < 4; i++ {
		e := <-out
		got = append(got, string(e.Kind)+":"+e.Fill.ExecutionID+e.Status)
		if i == 0 {
			if client.State.Fresh() {
				t.Fatal("stream must not be fresh before REST resync")
			}
			if !client.State.MarkResynced(client.State.Generation()) || !client.State.Fresh() {
				t.Fatal("resynced live stream must be fresh")
			}
			if fee, _ := e.Fill.FeeUSDT.WireDecimal(); fee != "-0.01" || !e.Fill.ExecutedAt.Equal(time.UnixMilli(1700000002000).UTC()) {
				t.Fatalf("fill=%+v", e.Fill)
			}
		}
	}
	if strings.Join(got, ",") != "EXECUTION:e2,EXECUTION:e2,EXECUTION:e1,ORDER:PartiallyFilled" {
		t.Fatalf("events=%v", got)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrPrivateStream) || client.State.Fresh() {
		t.Fatalf("disconnect: err=%v fresh=%v", err, client.State.Fresh())
	}
}

// TestPrivateStreamReconnect проверяет переподключение, сброс свежести и отмену.
func TestPrivateStreamReconnect(t *testing.T) {
	client, count := fakePrivateServer(t, func(n int, conn *websocket.Conn) {
		if handshake(t, conn, true) {
			_ = conn.WriteJSON(execution("r"+string(rune('0'+n)), "Trade", "1700000001000"))
			if n > 1 {
				time.Sleep(time.Second)
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan PrivateEvent, 4)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, 10*time.Millisecond, out) }()
	first, second := <-out, <-out
	if first.Fill.ExecutionID != "r1" || second.Fill.ExecutionID != "r2" || atomic.LoadInt32(count) != 2 {
		t.Fatalf("events %s %s", first.Fill.ExecutionID, second.Fill.ExecutionID)
	}
	if client.State.Fresh() {
		t.Fatal("reconnected stream must require resync")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run=%v", err)
	}
}

// TestPrivateStreamFailures проверяет отказ auth без утечки секрета, битые события и аргументы.
func TestPrivateStreamFailures(t *testing.T) {
	client, _ := fakePrivateServer(t, func(_ int, conn *websocket.Conn) { handshake(t, conn, false) })
	err := client.Run(context.Background(), time.Millisecond, make(chan PrivateEvent, 1))
	if !errors.Is(err, ErrPrivateStreamAuth) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("auth=%v", err)
	}
	bad, _ := fakePrivateServer(t, func(_ int, conn *websocket.Conn) {
		if handshake(t, conn, true) {
			m := execution("x", "Trade", "1700000001000")
			m["data"].([]map[string]string)[0]["execQty"] = "0"
			_ = conn.WriteJSON(m)
			time.Sleep(time.Second)
		}
	})
	if err := bad.Stream(context.Background(), make(chan PrivateEvent, 1)); !errors.Is(err, ErrPrivateStreamData) {
		t.Fatalf("malformed=%v", err)
	}
	if _, err := NewTestnetPrivateStream(" ", "s", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty key=%v", err)
	}
	if err := bad.Stream(context.Background(), nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("nil out=%v", err)
	}
	if strings.Contains(bad.String(), "secret") || TestnetPrivateWebSocketURL != "wss://stream-testnet.bybit.com/v5/private" {
		t.Fatal("client must not expose secrets and must target Testnet only")
	}
}

// TestPrivateStreamSilentServer проверяет, что молчащий сервер снимает свежесть и рвёт соединение.
func TestPrivateStreamSilentServer(t *testing.T) {
	release := make(chan struct{})
	client, _ := fakePrivateServer(t, func(_ int, conn *websocket.Conn) {
		if handshake(t, conn, true) {
			<-release
		}
	})
	defer close(release)
	client.heartbeat = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- client.Stream(context.Background(), make(chan PrivateEvent, 1)) }()
	for !client.State.MarkResynced(client.State.Generation()) {
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPrivateStream) || client.State.Fresh() {
			t.Fatalf("silent server: err=%v fresh=%v", err, client.State.Fresh())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream must give up on a server that stops answering")
	}
}

// TestPrivateStreamHandshakeTimeout проверяет, что молчащий при auth сервер закрывается и Run переподключается.
func TestPrivateStreamHandshakeTimeout(t *testing.T) {
	client, count := fakePrivateServer(t, func(_ int, conn *websocket.Conn) {
		var auth json.RawMessage
		_ = conn.ReadJSON(&auth)
		time.Sleep(500 * time.Millisecond)
	})
	client.handshake = 30 * time.Millisecond
	err := client.Stream(context.Background(), make(chan PrivateEvent, 1))
	if !errors.Is(err, ErrPrivateStream) || errors.Is(err, ErrPrivateStreamAuth) {
		t.Fatalf("stalled handshake=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := client.Run(ctx, 10*time.Millisecond, make(chan PrivateEvent, 1)); !errors.Is(err, context.DeadlineExceeded) || atomic.LoadInt32(count) < 3 {
		t.Fatalf("run must keep reconnecting: %v, connections=%d", err, atomic.LoadInt32(count))
	}
}

// TestPrivateStreamBackpressureHeartbeat проверяет, что сбой heartbeat завершает Stream при заблокированном out.
func TestPrivateStreamBackpressureHeartbeat(t *testing.T) {
	release := make(chan struct{})
	client, _ := fakePrivateServer(t, func(_ int, conn *websocket.Conn) {
		if handshake(t, conn, true) {
			_ = conn.WriteJSON(execution("e1", "Trade", "1700000001000"))
			<-release
		}
	})
	defer close(release)
	client.heartbeat = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- client.Stream(context.Background(), make(chan PrivateEvent)) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPrivateStream) || client.State.Fresh() {
			t.Fatalf("blocked consumer: err=%v fresh=%v", err, client.State.Fresh())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat failure must end Stream even when out is blocked")
	}
}
