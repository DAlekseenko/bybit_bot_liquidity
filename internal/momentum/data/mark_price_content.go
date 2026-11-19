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

const markPriceContentFormat = "momentum/mark-price-content/v1"

// ErrMarkPriceContent обозначает невалидное содержимое хранимого ряда mark OHLC.
var ErrMarkPriceContent = errors.New("invalid momentum mark price content")

var markContentSymbol = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

// MarkPriceContentInput связывает отобранные caller строки с одним namespace и окном.
// Caller отвечает за provenance, состав и согласованный snapshot фактических mark OHLC.
// Нейтральный тип строк не доказывает их принадлежность объявленному источнику.
type MarkPriceContentInput struct {
	Environment, DatasetID, Source, Category, Symbol, IntervalCode, Endpoint string
	Start, End                                                               time.Time
	Rows                                                                     []model.MarkPriceCandle
}

type markContentWire struct {
	Format      string               `json:"format"`
	Environment string               `json:"environment"`
	DatasetID   string               `json:"dataset_id"`
	Source      string               `json:"source"`
	Category    string               `json:"category"`
	Symbol      string               `json:"symbol"`
	Interval    string               `json:"interval_code"`
	Endpoint    string               `json:"endpoint"`
	StartMs     int64                `json:"start_unix_ms"`
	EndMs       int64                `json:"end_unix_ms"`
	Candles     []markContentWireRow `json:"candles"`
}

type markContentWireRow struct {
	OpenTimeMs int64  `json:"open_time_unix_ms"`
	Open       string `json:"open"`
	High       string `json:"high"`
	Low        string `json:"low"`
	Close      string `json:"close"`
	Confirmed  bool   `json:"confirmed"`
}

// EncodeMarkPriceContent возвращает точные канонические байты выбранных mark OHLC и SHA-256.
// Успех не доказывает полноту, coverage, immutable membership или готовность replay.
// Confirmed передаёт caller; функция не выводит закрытие свечи по текущим часам.
func EncodeMarkPriceContent(input MarkPriceContentInput) ([]byte, string, error) {
	step := int64(0)
	switch input.IntervalCode {
	case "D":
		step = 86400000
	case "60":
		step = 3600000
	}
	if step == 0 || !validMarkContentIdentity(input) || !markContentTime(input.Start, step) ||
		!markContentTime(input.End, step) || !input.Start.UTC().Before(input.End.UTC()) {
		return nil, "", ErrMarkPriceContent
	}
	rows := make([]markContentWireRow, 0, len(input.Rows))
	for _, row := range input.Rows {
		stamp := row.OpenTime.UTC()
		if !row.Confirmed || !markContentTime(stamp, step) || stamp.Before(input.Start.UTC()) || !stamp.Before(input.End.UTC()) {
			return nil, "", ErrMarkPriceContent
		}
		values, ok := markContentValues(row)
		if !ok {
			return nil, "", ErrMarkPriceContent
		}
		rows = append(rows, markContentWireRow{row.OpenTime.UnixMilli(), values[0], values[1], values[2], values[3], true})
	}
	slices.SortFunc(rows, compareMarkContentRows)
	for i := 1; i < len(rows); i++ {
		if rows[i-1].OpenTimeMs == rows[i].OpenTimeMs {
			return nil, "", ErrMarkPriceContent
		}
	}
	payload, err := json.Marshal(markContentWire{
		Format: markPriceContentFormat, Environment: input.Environment, DatasetID: input.DatasetID,
		Source: input.Source, Category: input.Category, Symbol: input.Symbol, Interval: input.IntervalCode, Endpoint: input.Endpoint,
		StartMs: input.Start.UnixMilli(), EndMs: input.End.UnixMilli(), Candles: rows,
	})
	if err != nil {
		return nil, "", ErrMarkPriceContent
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

// validMarkContentIdentity проверяет точные атрибуты исходного ряда mark price Bybit.
func validMarkContentIdentity(input MarkPriceContentInput) bool {
	id := input.DatasetID
	if input.Source != "bybit_v5" || input.Category != "linear" || !markContentSymbol.MatchString(input.Symbol) ||
		!utf8.ValidString(id) || utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 128 ||
		strings.Trim(id, " ") != id || strings.ContainsRune(id, 0) {
		return false
	}
	switch input.Environment {
	case "testnet":
		return input.Endpoint == "https://api-testnet.bybit.com/v5/market/mark-price-kline"
	case "mainnet":
		return input.Endpoint == "https://api.bybit.com/v5/market/mark-price-kline"
	default:
		return false
	}
}

// markContentTime проверяет точный UTC-grid timestamp в диапазоне хранилища.
func markContentTime(stamp time.Time, step int64) bool {
	year := stamp.UTC().Year()
	return year >= 1970 && year <= 294276 && stamp.Nanosecond()%int(time.Millisecond) == 0 && stamp.UnixMilli()%step == 0
}

// markContentValues проверяет положительные mark OHLC и ценовые границы.
func markContentValues(row model.MarkPriceCandle) (values [4]string, ok bool) {
	for i, value := range [...]model.Decimal{row.Open, row.High, row.Low, row.Close} {
		wire, err := value.WireDecimal()
		if err != nil {
			return values, false
		}
		if _, err = model.ParsePrice(wire); err != nil {
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

// compareMarkContentRows упорядочивает фактические строки по времени открытия.
func compareMarkContentRows(a, b markContentWireRow) int {
	return cmp.Compare(a.OpenTimeMs, b.OpenTimeMs)
}
