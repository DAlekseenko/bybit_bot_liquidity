package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

const createBody = `{"category":"linear","symbol":"BTCUSDT","side":"Buy","orderType":"Market","qty":"0.004","positionIdx":0,"reduceOnly":false,"orderLinkId":"cm-1"}`

// orderClient возвращает клиент с ответом reply и счётчиком запросов; body сохраняет тело последнего запроса.
func orderClient(t *testing.T, status int, reply string, body *string) (*Client, *int) {
	calls := 0
	client := testPostClient(t, "https://api-testnet.bybit.com", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if body != nil {
			*body = r.URL.Path + " " + string(raw)
		}
		return timeResponse(r, status, reply), nil
	}))
	return client, &calls
}

// TestCreateMomentumOrder проверяет отправку тела как есть и ack без признаков исполнения.
func TestCreateMomentumOrder(t *testing.T) {
	var sent string
	client, calls := orderClient(t, 200, `{"retCode":0,"retMsg":"OK","result":{"orderId":"o1","orderLinkId":"cm-1"},"time":1700000000500}`, &sent)
	ack, err := client.CreateMomentumOrder(context.Background(), json.RawMessage(createBody))
	if err != nil || ack.OrderID != "o1" || ack.OrderLinkID != "cm-1" || ack.ServerTime.UnixMilli() != 1700000000500 || *calls != 1 {
		t.Fatalf("ack=%+v err=%v calls=%d", ack, err, *calls)
	}
	if sent != "/v5/order/create "+createBody {
		t.Fatalf("sent=%s", sent)
	}
	if _, err := client.CreateMomentumOrder(context.Background(), json.RawMessage(`{"category":"linear","symbol":"BTCUSDT"}`)); !errors.Is(err, ErrInvalidArgument) || *calls != 1 {
		t.Fatalf("missing link must be refused before sending: %v", err)
	}
}

// TestMomentumOrderErrors проверяет typed ошибки, неизвестный исход и отсутствие автоповтора.
func TestMomentumOrderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		status      int
		want        error
	}{
		{"reject", `{"retCode":10001,"retMsg":"params error","result":{},"time":1}`, 200, ErrOrderRejected},
		{"duplicate", `{"retCode":110072,"retMsg":"OrderLinkedID is duplicate","result":{},"time":1}`, 200, ErrOrderLinkDuplicate},
		{"server-error", `oops`, 502, ErrPostOutcomeUnknown},
		{"busy", `{"retCode":10016,"retMsg":"busy","result":{},"time":1}`, 200, ErrPostOutcomeUnknown},
		{"foreign-ack", `{"retCode":0,"retMsg":"OK","result":{"orderId":"o9","orderLinkId":"other"},"time":1}`, 200, ErrOrderAckMalformed},
		{"empty-ack", `{"retCode":0,"retMsg":"OK","result":{},"time":1}`, 200, ErrPostOutcomeUnknown},
	} {
		client, calls := orderClient(t, tc.status, tc.reply, nil)
		_, err := client.CreateMomentumOrder(context.Background(), json.RawMessage(createBody))
		if !errors.Is(err, tc.want) || *calls != 1 {
			t.Fatalf("%s: err=%v calls=%d", tc.name, err, *calls)
		}
		if tc.name == "reject" && errors.Is(err, ErrPostOutcomeUnknown) {
			t.Fatal("explicit reject must not be ambiguous")
		}
	}
}

// TestCancelMomentumOrder проверяет тело отмены, ack и отказ для неактивной заявки.
func TestCancelMomentumOrder(t *testing.T) {
	var sent string
	client, _ := orderClient(t, 200, `{"retCode":0,"retMsg":"OK","result":{"orderId":"o1","orderLinkId":"cm-1"},"time":1}`, &sent)
	if ack, err := client.CancelMomentumOrder(context.Background(), "BTCUSDT", "cm-1"); err != nil || ack.OrderID != "o1" {
		t.Fatalf("cancel=%+v %v", ack, err)
	}
	if sent != `/v5/order/cancel {"category":"linear","orderLinkId":"cm-1","symbol":"BTCUSDT"}` {
		t.Fatalf("sent=%s", sent)
	}
	client, _ = orderClient(t, 200, `{"retCode":110001,"retMsg":"order not exists or too late to cancel","result":{},"time":1}`, nil)
	if _, err := client.CancelMomentumOrder(context.Background(), "BTCUSDT", "cm-1"); !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatalf("inactive=%v", err)
	}
	if _, err := client.CancelMomentumOrder(context.Background(), "BTCUSDT", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty link=%v", err)
	}
}

// TestCreateMomentumOrderTimeout проверяет, что обрыв после отправки — неизвестный исход без повтора.
func TestCreateMomentumOrderTimeout(t *testing.T) {
	calls := 0
	client := testPostClient(t, "https://api-testnet.bybit.com", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		_, _ = io.ReadAll(r.Body)
		return nil, context.DeadlineExceeded
	}))
	if _, err := client.CreateMomentumOrder(context.Background(), json.RawMessage(createBody)); !errors.Is(err, ErrPostOutcomeUnknown) || calls != 1 {
		t.Fatalf("timeout err=%v calls=%d", err, calls)
	}
}
