package data

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

const tradeCandleContentFormat = "momentum/trade-candle-content/v1"

// ErrTradeCandleContent обозначает невалидное содержимое хранимого ряда OHLCV.
var ErrTradeCandleContent = errors.New("invalid momentum trade candle content")

var tradeContentSymbol = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

// TradeCandleContentRow повторяет фактическую OHLCV-проекцию StoredCandle без turnover.
type TradeCandleContentRow struct {
	OpenTime                       time.Time
	Open, High, Low, Close, Volume model.Decimal
	Confirmed                      bool
}

// TradeCandleContentInput связывает отобранные caller строки с одним namespace и окном.
type TradeCandleContentInput struct {
	Environment, DatasetID, Source, Category, Symbol, IntervalCode, Endpoint string
	Start, End                                                               time.Time
	Rows                                                                     []TradeCandleContentRow
}

type tradeContentWire struct {
	Format      string                `json:"format"`
	Environment string                `json:"environment"`
	DatasetID   string                `json:"dataset_id"`
	Source      string                `json:"source"`
	Category    string                `json:"category"`
	Symbol      string                `json:"symbol"`
	Interval    string                `json:"interval_code"`
	Endpoint    string                `json:"endpoint"`
	StartMs     int64                 `json:"start_unix_ms"`
	EndMs       int64                 `json:"end_unix_ms"`
	Candles     []tradeContentWireRow `json:"candles"`
}

type tradeContentWireRow struct {
	OpenTimeMs int64  `json:"open_time_unix_ms"`
	Open       string `json:"open"`
	High       string `json:"high"`
	Low        string `json:"low"`
	Close      string `json:"close"`
	Volume     string `json:"volume"`
	Confirmed  bool   `json:"confirmed"`
}

// EncodeTradeCandleContent возвращает точные канонические байты выбранных OHLCV и SHA-256.
func EncodeTradeCandleContent(input TradeCandleContentInput) ([]byte, string, error) {
	step := int64(0)
	switch input.IntervalCode {
	case "D":
		step = 86400000
	case "60":
		step = 3600000
	}
	if step == 0 || !validTradeContentIdentity(input) || !tradeContentTime(input.Start, step) ||
		!tradeContentTime(input.End, step) || !input.Start.UTC().Before(input.End.UTC()) {
		return nil, "", ErrTradeCandleContent
	}
	rows := make([]tradeContentWireRow, 0, len(input.Rows))
	for _, row := range input.Rows {
		stamp := row.OpenTime.UTC()
		if !row.Confirmed || !tradeContentTime(stamp, step) || stamp.Before(input.Start.UTC()) || !stamp.Before(input.End.UTC()) {
			return nil, "", ErrTradeCandleContent
		}
		values, ok := tradeContentValues(row)
		if !ok {
			return nil, "", ErrTradeCandleContent
		}
		rows = append(rows, tradeContentWireRow{row.OpenTime.UnixMilli(), values[0], values[1], values[2], values[3], values[4], true})
	}
	slices.SortFunc(rows, compareTradeContentRows)
	for i := 1; i < len(rows); i++ {
		if rows[i-1].OpenTimeMs == rows[i].OpenTimeMs {
			return nil, "", ErrTradeCandleContent
		}
	}
	payload, err := json.Marshal(tradeContentWire{
		Format: tradeCandleContentFormat, Environment: input.Environment, DatasetID: input.DatasetID,
		Source: input.Source, Category: input.Category, Symbol: input.Symbol, Interval: input.IntervalCode, Endpoint: input.Endpoint,
		StartMs: input.Start.UnixMilli(), EndMs: input.End.UnixMilli(), Candles: rows,
	})
	if err != nil {
		return nil, "", ErrTradeCandleContent
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

// validTradeContentIdentity проверяет точные атрибуты исходного торгового ряда Bybit.
func validTradeContentIdentity(input TradeCandleContentInput) bool {
	id := input.DatasetID
	if input.Source != "bybit_v5" || input.Category != "linear" || !tradeContentSymbol.MatchString(input.Symbol) ||
		!utf8.ValidString(id) || utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 128 ||
		strings.Trim(id, " ") != id || strings.ContainsRune(id, 0) {
		return false
	}
	switch input.Environment {
	case "testnet":
		return input.Endpoint == "https://api-testnet.bybit.com/v5/market/kline"
	case "mainnet":
		return input.Endpoint == "https://api.bybit.com/v5/market/kline"
	default:
		return false
	}
}

// tradeContentTime проверяет точный UTC-grid timestamp в диапазоне хранилища.
func tradeContentTime(stamp time.Time, step int64) bool {
	year := stamp.UTC().Year()
	return year >= 1970 && year <= 294276 && stamp.Nanosecond()%int(time.Millisecond) == 0 && stamp.UnixMilli()%step == 0
}

// tradeContentValues проверяет положительные OHLC, неотрицательный объём и ценовые границы.
func tradeContentValues(row TradeCandleContentRow) (values [5]string, ok bool) {
	for i, value := range [...]model.Decimal{row.Open, row.High, row.Low, row.Close, row.Volume} {
		wire, err := value.WireDecimal()
		if err != nil {
			return values, false
		}
		parse := model.ParsePrice
		if i == 4 {
			parse = model.ParseQty
		}
		if _, err = parse(wire); err != nil {
			return values, false
		}
		values[i] = wire
	}
	for _, price := range [...]model.Decimal{row.Open, row.Close} {
		high, _ := row.High.Cmp(price)
		low, _ := row.Low.Cmp(price)
		if high < 0 || low > 0 {
			return values, false
		}
	}
	return values, true
}

// compareTradeContentRows упорядочивает фактические строки по времени открытия.
func compareTradeContentRows(a, b tradeContentWireRow) int {
	return cmp.Compare(a.OpenTimeMs, b.OpenTimeMs)
}
