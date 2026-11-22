package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const (
	momentumTestnetHost = "api-testnet.bybit.com"
	momentumSettleCoin  = "USDT"
)

var (
	// ErrUnsupportedAsset обозначает актив счёта, который read model не поддерживает.
	ErrUnsupportedAsset = errors.New("bybit account holds an unsupported asset")
	// ErrBorrowedAsset обозначает ненулевой заём на счёте.
	ErrBorrowedAsset = errors.New("bybit account has borrowed assets")
	// ErrMalformedAccountData обозначает неполные или некорректные данные счёта.
	ErrMalformedAccountData = errors.New("malformed bybit account data")
)

// momentumWalletResult описывает wallet-balance без account-wide полей.
type momentumWalletResult struct {
	List []struct {
		AccountType string               `json:"accountType"`
		Coin        []momentumWalletCoin `json:"coin"`
	} `json:"list"`
}

// momentumWalletCoin описывает строковые поля одного coin wallet-balance.
type momentumWalletCoin struct {
	Coin            string `json:"coin"`
	Equity          string `json:"equity"`
	WalletBalance   string `json:"walletBalance"`
	TotalPositionIM string `json:"totalPositionIM"`
	TotalOrderIM    string `json:"totalOrderIM"`
	Locked          string `json:"locked"`
	Bonus           string `json:"bonus"`
	BorrowAmount    string `json:"borrowAmount"`
	SpotBorrow      string `json:"spotBorrow"`
}

// momentumPositionItem описывает строковые поля одной позиции position/list.
type momentumPositionItem struct {
	PositionIdx    *int   `json:"positionIdx"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Size           string `json:"size"`
	AvgPrice       string `json:"avgPrice"`
	MarkPrice      string `json:"markPrice"`
	Leverage       string `json:"leverage"`
	PositionIM     string `json:"positionIM"`
	AutoAddMargin  *int   `json:"autoAddMargin"`
	PositionStatus string `json:"positionStatus"`
	UpdatedTime    string `json:"updatedTime"`
}

// momentumPrivateReader возвращает копию клиента только для signed Testnet чтений.
func (c *Client) momentumPrivateReader() (*Client, error) {
	b := c.baseURL
	if b.Scheme != "https" || b.Host != momentumTestnetHost || b.User != nil || b.Path != "" || b.RawPath != "" ||
		b.RawQuery != "" || b.ForceQuery || b.Fragment != "" || b.Opaque != "" {
		return nil, fmt.Errorf("%w: private reads are limited to Bybit Testnet", ErrInvalidArgument)
	}
	if c.apiKey == "" || c.apiSecret == "" {
		return nil, fmt.Errorf("%w: authenticated client credentials are required", ErrInvalidArgument)
	}
	reader := *c
	reader.httpClient = &http.Client{Transport: c.httpClient.Transport, Timeout: c.httpClient.Timeout, CheckRedirect: rejectMomentumRedirect}
	return &reader, nil
}

// momentumSignedGet выполняет signed Testnet GET и возвращает проверенное время сервера.
func (c *Client) momentumSignedGet(ctx context.Context, path string, query url.Values, result any) (time.Time, error) {
	reader, err := c.momentumPrivateReader()
	if err != nil {
		return time.Time{}, err
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	stamp, err := reader.signedGet(ctx, path, query, result)
	if err != nil {
		return time.Time{}, err
	}
	if stamp <= 0 {
		return time.Time{}, fmt.Errorf("%w: invalid server time", ErrMalformedAccountData)
	}
	return time.UnixMilli(stamp).UTC(), nil
}

// GetMomentumAccountMode читает режим Unified account с временем сервера.
func (c *Client) GetMomentumAccountMode(ctx context.Context) (model.AccountMode, error) {
	var result accountInfoResult
	server, err := c.momentumSignedGet(ctx, "/v5/account/info", url.Values{}, &result)
	if err != nil {
		return model.AccountMode{}, fmt.Errorf("get momentum account mode: %w", err)
	}
	info, err := parseAccountInfo(result)
	if err != nil {
		return model.AccountMode{}, fmt.Errorf("get momentum account mode: %w: %v", ErrMalformedAccountData, err)
	}
	return model.AccountMode{UnifiedMarginStatus: info.UnifiedMarginStatus, MarginMode: string(info.MarginMode), ServerTime: server}, nil
}

// GetMomentumIsolatedBalance читает USDT coin баланс и отклоняет прочие активы и займы.
func (c *Client) GetMomentumIsolatedBalance(ctx context.Context) (model.IsolatedBalance, error) {
	var result momentumWalletResult
	server, err := c.momentumSignedGet(ctx, "/v5/account/wallet-balance", url.Values{"accountType": {unifiedAccountType}}, &result)
	if err != nil {
		return model.IsolatedBalance{}, fmt.Errorf("get momentum isolated balance: %w", err)
	}
	if len(result.List) != 1 || result.List[0].AccountType != unifiedAccountType {
		return model.IsolatedBalance{}, fmt.Errorf("get momentum isolated balance: %w: expected one unified account", ErrMalformedAccountData)
	}
	coins := result.List[0].Coin
	for _, coin := range coins {
		if coin.Coin != momentumSettleCoin {
			return model.IsolatedBalance{}, fmt.Errorf("get momentum isolated balance: %w: %q", ErrUnsupportedAsset, coin.Coin)
		}
	}
	if len(coins) != 1 {
		return model.IsolatedBalance{}, fmt.Errorf("get momentum isolated balance: %w: expected one USDT coin", ErrMalformedAccountData)
	}
	balance, err := parseMomentumCoin(coins[0])
	if err != nil {
		return model.IsolatedBalance{}, fmt.Errorf("get momentum isolated balance: %w", err)
	}
	balance.ServerTime = server
	return balance, nil
}

// parseMomentumCoin разбирает точные поля coin; пустое поле — ошибка, а не ноль.
func parseMomentumCoin(coin momentumWalletCoin) (model.IsolatedBalance, error) {
	b := model.IsolatedBalance{Coin: coin.Coin}
	var borrow, spotBorrow model.Decimal
	fields := []struct {
		name   string
		raw    string
		signed bool
		dst    *model.Decimal
	}{
		{"equity", coin.Equity, true, &b.Equity},
		{"walletBalance", coin.WalletBalance, true, &b.WalletBalance},
		{"totalPositionIM", coin.TotalPositionIM, false, &b.TotalPositionIM},
		{"totalOrderIM", coin.TotalOrderIM, false, &b.TotalOrderIM},
		{"locked", coin.Locked, false, &b.Locked},
		{"bonus", coin.Bonus, false, &b.Bonus},
		{"borrowAmount", coin.BorrowAmount, false, &borrow},
		{"spotBorrow", coin.SpotBorrow, false, &spotBorrow},
	}
	for _, f := range fields {
		parse := model.ParseQty
		if f.signed {
			parse = model.ParseAmount
		}
		value, err := parse(f.raw)
		if err != nil {
			return model.IsolatedBalance{}, fmt.Errorf("%w: invalid %s", ErrMalformedAccountData, f.name)
		}
		*f.dst = value
	}
	for _, debt := range []model.Decimal{borrow, spotBorrow} {
		if sign, err := debt.Cmp(model.Zero()); err != nil || sign != 0 {
			return model.IsolatedBalance{}, ErrBorrowedAsset
		}
	}
	available := b.WalletBalance
	for _, part := range []model.Decimal{b.TotalPositionIM, b.TotalOrderIM, b.Locked, b.Bonus} {
		next, err := available.Sub(part)
		if err != nil {
			return model.IsolatedBalance{}, fmt.Errorf("%w: available arithmetic", ErrMalformedAccountData)
		}
		available = next
	}
	b.Available = available
	return b, nil
}

// parseMomentumPosition разбирает позицию счёта с точными полями и согласованностью side/size.
func parseMomentumPosition(item momentumPositionItem, server time.Time) (model.AccountPosition, error) {
	bad := func(field string) (model.AccountPosition, error) {
		return model.AccountPosition{}, fmt.Errorf("%w: position %q: invalid %s", ErrMalformedAccountData, item.Symbol, field)
	}
	if !markPriceSymbolPattern.MatchString(item.Symbol) {
		return bad("symbol")
	}
	if item.PositionIdx == nil || *item.PositionIdx < 0 || *item.PositionIdx > 2 {
		return bad("positionIdx")
	}
	if item.AutoAddMargin == nil || (*item.AutoAddMargin != 0 && *item.AutoAddMargin != 1) {
		return bad("autoAddMargin")
	}
	switch item.PositionStatus {
	case "Normal", "Liq", "Adl":
	default:
		return bad("positionStatus")
	}
	p := model.AccountPosition{Symbol: item.Symbol, PositionIdx: *item.PositionIdx, Side: item.Side,
		AutoAddMargin: *item.AutoAddMargin == 1, Status: item.PositionStatus, ServerTime: server}
	var err error
	if p.Size, err = model.ParseQty(item.Size); err != nil {
		return bad("size")
	}
	if p.AvgPrice, err = model.ParseQty(item.AvgPrice); err != nil {
		return bad("avgPrice")
	}
	if p.MarkPrice, err = model.ParsePrice(item.MarkPrice); err != nil {
		return bad("markPrice")
	}
	if p.Leverage, err = model.ParsePrice(item.Leverage); err != nil {
		return bad("leverage")
	}
	if p.PositionIM, err = model.ParseQty(item.PositionIM); err != nil {
		return bad("positionIM")
	}
	updated, err := strconv.ParseInt(item.UpdatedTime, 10, 64)
	if err != nil || updated <= 0 || strconv.FormatInt(updated, 10) != item.UpdatedTime {
		return bad("updatedTime")
	}
	p.UpdatedTime = time.UnixMilli(updated).UTC()
	sign, _ := p.Size.Cmp(model.Zero())
	if (sign == 0) != (p.Side == "") || (p.Side != "" && p.Side != "Buy" && p.Side != "Sell") {
		return bad("side")
	}
	return p, nil
}
