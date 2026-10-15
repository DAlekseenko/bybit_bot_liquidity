// Package telegram реализует личный read-only интерфейс через long polling.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	apiBaseURL       = "https://api.telegram.org"
	longPollSeconds  = 30
	maxResponseBytes = 1 << 20
	maxMessageLength = 4096
	maxRetryDelay    = 30 * time.Second
)

var ErrInvalidConfig = errors.New("invalid Telegram bot config")

// Config содержит env-only секрет и неизменяемый идентификатор владельца.
type Config struct {
	Token   string
	OwnerID int64
}

// Provider возвращает безопасные для Telegram публичные сведения процесса.
type Provider interface {
	Status() string
	Settings() string
	Signal(context.Context) (string, error)
}

type apiError struct {
	permanent  bool
	retryAfter time.Duration
}

// Error скрывает URL, тело ответа и транспортные детали.
func (e *apiError) Error() string { return "Telegram API request failed" }

type Bot struct {
	token    string
	ownerID  int64
	provider Provider
	client   *http.Client
	offset   int64
}

type apiResponse[T any] struct {
	OK         bool `json:"ok"`
	ErrorCode  int  `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	Result T `json:"result"`
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text string `json:"text"`
}

// New проверяет конфигурацию и отключает перенаправление запросов с токеном.
func New(cfg Config, provider Provider, client *http.Client) (*Bot, error) {
	if cfg.OwnerID <= 0 || provider == nil || !validToken(cfg.Token) {
		return nil, ErrInvalidConfig
	}
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	} else {
		copy := *client
		client = &copy
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 {
		client.Timeout = 40 * time.Second
	}
	return &Bot{token: cfg.Token, ownerID: cfg.OwnerID, provider: provider, client: client}, nil
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// validToken допускает только формат токена Telegram bot без разделителей пути.
func validToken(token string) bool {
	return tokenPattern.MatchString(token)
}

// Run обслуживает личные команды; временные ошибки получают ограниченный backoff.
func (b *Bot) Run(ctx context.Context, logf func(string, error)) error {
	delay := time.Second
	for {
		updates, err := b.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.permanent {
				return err
			}
			wait := delay
			if errors.As(err, &apiErr) && apiErr.retryAfter > 0 {
				wait = apiErr.retryAfter
			}
			if !waitContext(ctx, boundedDelay(wait)) {
				return nil
			}
			if delay < maxRetryDelay/2 {
				delay *= 2
			}
			continue
		}
		delay = time.Second
		for _, item := range updates {
			if item.ID < b.offset {
				continue
			}
			b.offset = item.ID + 1
			if !b.authorized(item.Message) {
				continue
			}
			text, respond, responseErr := b.command(ctx, item.Message.Text)
			if responseErr != nil {
				text = "Не удалось прочитать сохранённый сигнал."
			}
			if !respond {
				continue
			}
			if sendErr := b.sendMessage(ctx, item.Message.Chat.ID, text); sendErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				var apiErr *apiError
				if errors.As(sendErr, &apiErr) && apiErr.permanent {
					return sendErr
				}
				if errors.As(sendErr, &apiErr) && apiErr.retryAfter > 0 && !waitContext(ctx, boundedDelay(apiErr.retryAfter)) {
					return nil
				}
				if logf != nil {
					logf("Telegram reply failed; command will not be resent", sendErr)
				}
			}
		}
	}
}

// authorized допускает только сообщение владельца в личном чате с самим ботом.
func (b *Bot) authorized(msg *message) bool {
	return msg != nil && msg.From != nil && msg.From.ID == b.ownerID && msg.Chat.Type == "private" && msg.Chat.ID == b.ownerID
}

// command формирует ответ только для поддерживаемых команд без изменения состояния.
func (b *Bot) command(ctx context.Context, text string) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false, nil
	}
	name := strings.SplitN(fields[0], "@", 2)[0]
	switch name {
	case "/start", "/help":
		return "Команды:\n/status — состояние процесса\n/settings — публичные настройки\n/signal — последний сохранённый сигнал\n/help — эта справка", true, nil
	case "/status":
		return b.provider.Status(), true, nil
	case "/settings":
		return b.provider.Settings(), true, nil
	case "/signal":
		response, err := b.provider.Signal(ctx)
		return response, true, err
	default:
		return "Неизвестная команда. Используйте /help.", true, nil
	}
}

// getUpdates получает только сообщения начиная с монотонного offset.
func (b *Bot) getUpdates(ctx context.Context) ([]update, error) {
	query := url.Values{
		"timeout":         {strconv.Itoa(longPollSeconds)},
		"allowed_updates": {`["message"]`},
	}
	if b.offset > 0 {
		query.Set("offset", strconv.FormatInt(b.offset, 10))
	}
	var response apiResponse[[]update]
	if err := b.request(ctx, http.MethodGet, "getUpdates", query, nil, &response); err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, errorForAPI(response.ErrorCode, response.Parameters.RetryAfter)
	}
	return response.Result, nil
}

// sendMessage отправляет один ответ и намеренно не повторяет неоднозначную отправку.
func (b *Bot) sendMessage(ctx context.Context, chatID int64, text string) error {
	text = truncate(text, maxMessageLength)
	form := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}, "text": {text}}
	var response apiResponse[json.RawMessage]
	if err := b.request(ctx, http.MethodPost, "sendMessage", nil, strings.NewReader(form.Encode()), &response); err != nil {
		return err
	}
	if !response.OK {
		return errorForAPI(response.ErrorCode, response.Parameters.RetryAfter)
	}
	return nil
}

// request выполняет bounded HTTP/JSON запрос и возвращает только безопасные ошибки.
func (b *Bot) request(ctx context.Context, method, operation string, query url.Values, body io.Reader, target any) error {
	endpoint := apiBaseURL + "/bot" + b.token + "/" + operation
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return &apiError{}
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := b.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &apiError{}
		}
		return &apiError{}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusConflict {
		return errorForAPI(response.StatusCode, 0)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return &apiError{}
	}
	var envelope apiResponse[json.RawMessage]
	if json.Unmarshal(data, &envelope) != nil {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return &apiError{}
		}
		envelope.ErrorCode = response.StatusCode
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errorForAPI(response.StatusCode, envelope.Parameters.RetryAfter)
	}
	if !envelope.OK {
		return errorForAPI(envelope.ErrorCode, envelope.Parameters.RetryAfter)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &apiError{}
	}
	return nil
}

// errorForAPI классифицирует только код и retry_after, игнорируя текст ответа API.
func errorForAPI(code, retryAfterSeconds int) error {
	err := &apiError{permanent: code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusConflict}
	if code == http.StatusTooManyRequests && retryAfterSeconds > 0 {
		if retryAfterSeconds > int(maxRetryDelay/time.Second) {
			retryAfterSeconds = int(maxRetryDelay / time.Second)
		}
		err.retryAfter = time.Duration(retryAfterSeconds) * time.Second
	}
	return err
}

// boundedDelay ограничивает ожидание сервера значением retry_after.
func boundedDelay(delay time.Duration) time.Duration {
	if delay <= 0 || delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// waitContext ждёт задержку с немедленным выходом при остановке процесса.
func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// truncate сохраняет размер ответа в пределах Telegram Bot API.
func truncate(text string, limit int) string {
	if len([]rune(text)) <= limit {
		return text
	}
	return string([]rune(text)[:limit-3]) + "..."
}
