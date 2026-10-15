package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip выполняет тестовый HTTP-запрос без сетевого соединения.
func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type testProvider struct {
	signal    string
	signalErr error
}

// Status возвращает фиксированный статус для тестов команд.
func (p testProvider) Status() string { return "status" }

// Settings возвращает фиксированные настройки для тестов команд.
func (p testProvider) Settings() string { return "settings" }

// Signal возвращает фиксированный сигнал для тестов команд.
func (p testProvider) Signal(context.Context) (string, error) { return p.signal, p.signalErr }

// TestNewRejectsInvalidCredentials проверяет положительный owner ID и безопасный формат токена.
func TestNewRejectsInvalidCredentials(t *testing.T) {
	for _, cfg := range []Config{{Token: "", OwnerID: 1}, {Token: "123:abc/path", OwnerID: 1}, {Token: "123:abc", OwnerID: 0}} {
		if _, err := New(cfg, testProvider{}, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New(%#v) error = %v", cfg, err)
		}
	}
}

// TestAuthorizationAndCommands проверяет owner-private доступ и весь read-only набор команд.
func TestAuthorizationAndCommands(t *testing.T) {
	provider := testProvider{signal: "saved signal"}
	bot, err := New(Config{Token: "123:secret", OwnerID: 7}, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	private := &message{From: &struct {
		ID int64 `json:"id"`
	}{ID: 7}, Chat: struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}{ID: 7, Type: "private"}}
	if !bot.authorized(private) {
		t.Fatal("owner private message was rejected")
	}
	for _, denied := range []*message{nil, {Chat: private.Chat}, {From: private.From, Chat: struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}{ID: 7, Type: "group"}}, {From: &struct {
		ID int64 `json:"id"`
	}{ID: 8}, Chat: private.Chat}, {From: private.From, Chat: struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}{ID: 8, Type: "private"}}} {
		if bot.authorized(denied) {
			t.Fatalf("unauthorized message accepted: %#v", denied)
		}
	}
	for input, want := range map[string]string{"/start": "Команды:", "/help": "Команды:", "/status": "status", "/status@operator": "status", "/settings": "settings", "/signal": "saved signal", "/unknown": "Неизвестная команда."} {
		got, respond, err := bot.command(context.Background(), input)
		if err != nil || !respond || !strings.HasPrefix(got, want) {
			t.Fatalf("command(%q) = %q, %t, %v", input, got, respond, err)
		}
	}
	if _, respond, err := bot.command(context.Background(), " "); err != nil || respond {
		t.Fatalf("empty command = %t, %v", respond, err)
	}
}

// TestRunFiltersUpdatesAdvancesOffsetAndDoesNotRepeatSends проверяет авторизацию, дубликаты и неоднозначную отправку.
func TestRunFiltersUpdatesAdvancesOffsetAndDoesNotRepeatSends(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls, sends int
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/bot123:secret/getUpdates" {
			polls++
			if polls == 1 {
				return jsonResponse(200, `{"ok":true,"result":[{"update_id":5,"message":{"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"/status"}},{"update_id":5,"message":{"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"/status"}},{"update_id":6,"message":{"from":{"id":8},"chat":{"id":8,"type":"private"},"text":"/status"}},{"update_id":7,"message":{"chat":{"id":7,"type":"group"},"text":"/status"}}]}`), nil
			}
			if request.URL.Query().Get("offset") != "8" {
				t.Errorf("next offset = %q", request.URL.Query().Get("offset"))
			}
			cancel()
			return nil, errors.New("cancelled")
		}
		sends++
		return jsonResponse(500, "temporary failure"), nil
	})}
	bot, err := New(Config{Token: "123:secret", OwnerID: 7}, testProvider{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.Run(ctx, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if polls != 2 || sends != 1 {
		t.Fatalf("polls=%d sends=%d", polls, sends)
	}
}

// TestRunCancelsDuringRateLimitBackoff проверяет обработку 429 и быструю отмену без сети.
func TestRunCancelsDuringRateLimitBackoff(t *testing.T) {
	if api := errorForAPI(http.StatusTooManyRequests, int(^uint(0)>>1)); api == nil {
		t.Fatal("missing rate-limit error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		return jsonResponse(429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`), nil
	})}
	bot, err := New(Config{Token: "123:secret", OwnerID: 7}, testProvider{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.Run(ctx, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

// TestRequestSanitizesFailures проверяет, что ошибки транспорта, чтения и API скрывают credentials.
func TestRequestSanitizesFailures(t *testing.T) {
	for name, transport := range map[string]http.RoundTripper{
		"transport": roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("bad https://api.telegram.org/bot123:secret")
		}),
		"read": roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: failingBody{}}, nil
		}),
		"decode": roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(200, "not-json-token-secret"), nil }),
		"api": roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(200, `{"ok":false,"error_code":401,"description":"secret"}`), nil
		}),
	} {
		t.Run(name, func(t *testing.T) {
			bot, err := New(Config{Token: "123:secret", OwnerID: 7}, testProvider{}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			err = bot.request(context.Background(), http.MethodGet, "getUpdates", nil, nil, &apiResponse[[]update]{})
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "api.telegram.org") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
	if !errorForAPI(http.StatusUnauthorized, 0).(*apiError).permanent || !errorForAPI(http.StatusForbidden, 0).(*apiError).permanent || !errorForAPI(http.StatusConflict, 0).(*apiError).permanent || errorForAPI(http.StatusTooManyRequests, 1).(*apiError).permanent {
		t.Fatal("API error classification is incorrect")
	}
}

// TestRequestKeepsOtherClientErrorsRetryable проверяет временную классификацию остальных 4xx.
func TestRequestKeepsOtherClientErrorsRetryable(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(status, `{"ok":false}`), nil
			})}
			bot, err := New(Config{Token: "123:secret", OwnerID: 7}, testProvider{}, client)
			if err != nil {
				t.Fatal(err)
			}
			err = bot.request(context.Background(), http.MethodGet, "getUpdates", nil, nil, &apiResponse[[]update]{})
			var apiErr *apiError
			if !errors.As(err, &apiErr) || apiErr.permanent {
				t.Fatalf("HTTP %d error should remain retryable: %v", status, err)
			}
		})
	}
}

// TestRequestUsesPermanentStatusBeforeBody проверяет 401/403/409 независимо от тела ответа.
func TestRequestUsesPermanentStatusBeforeBody(t *testing.T) {
	bodyCases := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"description":"secret`},
		{name: "oversized", body: strings.Repeat("secret", maxResponseBytes/len("secret")+1)},
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict} {
		for _, bodyCase := range bodyCases {
			t.Run(http.StatusText(status)+"/"+bodyCase.name, func(t *testing.T) {
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(status, bodyCase.body), nil
				})}
				bot, err := New(Config{Token: "123:secret", OwnerID: 7}, testProvider{}, client)
				if err != nil {
					t.Fatal(err)
				}
				err = bot.request(context.Background(), http.MethodGet, "getUpdates", nil, nil, &apiResponse[[]update]{})
				var apiErr *apiError
				if !errors.As(err, &apiErr) || !apiErr.permanent || err.Error() != "Telegram API request failed" || strings.Contains(err.Error(), "secret") {
					t.Fatalf("HTTP %d status was not handled safely: %v", status, err)
				}
			})
		}
	}
}

type failingBody struct{}

// Read сообщает ошибку чтения ответа, не раскрывая тело.
func (failingBody) Read([]byte) (int, error) { return 0, errors.New("secret read failure") }

// Close реализует обязательный io.Closer для тестового тела.
func (failingBody) Close() error { return nil }

// jsonResponse создаёт HTTP-ответ, использующий только память теста.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
