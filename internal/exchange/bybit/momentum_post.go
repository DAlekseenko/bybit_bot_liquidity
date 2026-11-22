package bybit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// momentumTestnetPostOrigin — единственный origin signed POST; конфигурация его не меняет.
const momentumTestnetPostOrigin = "https://api-testnet.bybit.com"

var (
	// ErrPostOutcomeUnknown означает, что POST мог быть принят биржей, но подтверждения нет.
	// Вызывающий обязан сверить состояние с биржей и не повторять мутацию вслепую.
	ErrPostOutcomeUnknown = errors.New("bybit signed post outcome is unknown")
	// ErrPostNotTestnet означает отказ signed POST вне Bybit Testnet.
	ErrPostNotTestnet = errors.New("bybit signed post is allowed only on testnet")
)

// MomentumPostResult содержит проверенный успешный ответ signed POST.
type MomentumPostResult struct {
	Result     json.RawMessage
	ServerTime time.Time
}

// PostMomentumTestnetSigned отправляет один signed JSON POST в Bybit Testnet без повторов.
// Подписываются и отправляются одни и те же байты тела; неоднозначный исход
// возвращается как ErrPostOutcomeUnknown, а не как успех или отказ.
func (c *Client) PostMomentumTestnetSigned(ctx context.Context, path string, body any) (MomentumPostResult, error) {
	if !isMomentumTestnetOrigin(c) {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w", path, ErrPostNotTestnet)
	}
	if c.apiKey == "" || c.apiSecret == "" {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w: authenticated client credentials are required", path, ErrInvalidArgument)
	}
	if err := validateMomentumPostPath(path); err != nil {
		return MomentumPostResult{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil || len(payload) < 2 || payload[0] != '{' {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w: body must encode to a JSON object", path, ErrInvalidArgument)
	}
	if err := ctx.Err(); err != nil {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w", path, err)
	}
	timestamp := strconv.FormatInt(c.now().UTC().UnixMilli(), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, momentumTestnetPostOrigin+path, bytes.NewReader(payload))
	if err != nil {
		return MomentumPostResult{}, fmt.Errorf("post %s: create request: %w", path, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-BAPI-API-KEY", c.apiKey)
	request.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	request.Header.Set("X-BAPI-RECV-WINDOW", c.recvWindow)
	request.Header.Set("X-BAPI-SIGN", signMomentumPost(c.apiSecret, timestamp, c.apiKey, c.recvWindow, payload))
	sender := &http.Client{Transport: c.httpClient.Transport, Timeout: c.httpClient.Timeout, CheckRedirect: rejectMomentumRedirect}
	response, err := sender.Do(request)
	if err != nil {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w: %w", path, ErrPostOutcomeUnknown, err)
	}
	defer response.Body.Close()
	result, err := decodeMomentumPostResponse(response)
	if err != nil {
		return MomentumPostResult{}, fmt.Errorf("post %s: %w", path, err)
	}
	return result, nil
}

// signMomentumPost считает HMAC-SHA256 Bybit V5 над timestamp+apiKey+recvWindow+body.
func signMomentumPost(secret, timestamp, apiKey, recvWindow string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + apiKey + recvWindow))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// isMomentumTestnetOrigin проверяет, что базовый URL клиента — ровно Bybit Testnet.
func isMomentumTestnetOrigin(c *Client) bool {
	base := c.baseURL
	return base.Scheme == "https" && base.Host == "api-testnet.bybit.com" && base.User == nil &&
		(base.Path == "" || base.Path == "/") && base.RawPath == "" && base.RawQuery == "" &&
		!base.ForceQuery && base.Fragment == "" && base.Opaque == ""
}

// validateMomentumPostPath допускает только простой путь /v5/... без query и обхода.
func validateMomentumPostPath(path string) error {
	if !strings.HasPrefix(path, "/v5/") || strings.Contains(path, "//") || strings.Contains(path, "..") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("%w: invalid signed post path", ErrInvalidArgument)
	}
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '-' || r == '_') {
			return fmt.Errorf("%w: invalid signed post path", ErrInvalidArgument)
		}
	}
	return nil
}

// decodeMomentumPostResponse строго разбирает ответ и классифицирует неоднозначные исходы.
func decodeMomentumPostResponse(response *http.Response) (MomentumPostResult, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		httpErr := &HTTPError{StatusCode: response.StatusCode, Status: response.Status, Body: strings.TrimSpace(string(body))}
		if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= http.StatusInternalServerError {
			return MomentumPostResult{}, fmt.Errorf("%w: %w", ErrPostOutcomeUnknown, httpErr)
		}
		return MomentumPostResult{}, httpErr
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return MomentumPostResult{}, fmt.Errorf("%w: read response: %w", ErrPostOutcomeUnknown, err)
	}
	if len(payload) > maxResponseBytes {
		return MomentumPostResult{}, fmt.Errorf("%w: %w", ErrPostOutcomeUnknown, ErrResponseTooLarge)
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.RetCode == nil {
		return MomentumPostResult{}, fmt.Errorf("%w: malformed response envelope", ErrPostOutcomeUnknown)
	}
	if *envelope.RetCode != 0 {
		apiErr := &APIError{Code: *envelope.RetCode, Message: envelope.RetMsg}
		if apiErr.Code == 10000 || apiErr.Code == 10016 {
			return MomentumPostResult{}, fmt.Errorf("%w: %w", ErrPostOutcomeUnknown, apiErr)
		}
		return MomentumPostResult{}, apiErr
	}
	serverTime := time.UnixMilli(envelope.Time).UTC()
	if envelope.Time <= 0 || serverTime.UnixMilli() != envelope.Time || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return MomentumPostResult{}, fmt.Errorf("%w: incomplete success response", ErrPostOutcomeUnknown)
	}
	return MomentumPostResult{Result: append(json.RawMessage(nil), envelope.Result...), ServerTime: serverTime}, nil
}
