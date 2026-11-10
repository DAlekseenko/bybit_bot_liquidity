// Package signals строит чистые неизменяемые наблюдения Momentum без исполнения.
package signals

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrBatch обозначает невалидное либо неканоническое наблюдение без частичного результата.
var ErrBatch = errors.New("invalid momentum signal batch")

const dayMS int64 = 86400000

// Window задаёт фактические source metadata, логические request bounds и 31 ascending close.
type Window struct {
	Symbol, Environment, Source, Category, Endpoint string
	StartMS, EndMS                                  int64
	ServerTime                                      time.Time
	Candles                                         []data.DailyClose
}

// Batch хранит только неизменяемые канонические байты и их content hash.
type Batch struct{ payload, hash string }

type wireClose struct {
	OpenMS    int64
	Close     string
	Confirmed bool
}

type wireWindow struct {
	Symbol, Environment, Source, Category, Endpoint string
	StartMS, EndMS, ServerMS                        int64
	Candles                                         []wireClose
}

type wireSignal struct {
	Symbol string
	Active bool
	Reason string
}

type wireBatch struct {
	Version, Algorithm string
	Config             json.RawMessage
	ConfigHash         string
	CutoffMS           int64
	Windows            []wireWindow
	Signals            []wireSignal
}

// exactMS отклоняет truncation/overflow и неподдерживаемые физические UTC timestamps.
func exactMS(stamp time.Time) (int64, bool) {
	ms := stamp.UnixMilli()
	return ms, ms >= 0 && stamp.UTC().Year() <= 294276 && time.UnixMilli(ms).Equal(stamp)
}

// NewBatch проверяет весь реальный вход и вычисляет неизменяемые ROC30-флаги.
func NewBatch(cfg config.Config, cutoff time.Time, windows []Window) (Batch, error) {
	snapshot, configHash, err := cfg.Snapshot()
	if err != nil {
		return Batch{}, ErrBatch
	}
	checked, err := config.Parse(snapshot)
	if err != nil {
		return Batch{}, ErrBatch
	}
	values := checked.Values()
	c, valid := exactMS(cutoff)
	if !valid || c%dayMS != 0 || c <= 31*dayMS || len(windows) != 3 || values.Mode != "paper" ||
		values.DataSource.Kind != "bybit_testnet_public" || values.DataSource.Environment != "testnet" {
		return Batch{}, ErrBatch
	}
	bySymbol := make(map[string]Window, 3)
	for _, window := range windows {
		if _, duplicate := bySymbol[window.Symbol]; duplicate {
			return Batch{}, ErrBatch
		}
		bySymbol[window.Symbol] = window
	}
	document := wireBatch{Version: "momentum/signal-batch/v1", Algorithm: "momentum/roc30/v1", Config: snapshot, ConfigHash: configHash, CutoffMS: c}
	series := make(map[string][]data.DailyClose, 3)
	for _, symbol := range values.Symbols {
		window, found := bySymbol[symbol]
		server, valid := exactMS(window.ServerTime)
		if !found || !valid || server/dayMS*dayMS != c || window.Environment != "testnet" || window.Source != "bybit_v5" ||
			window.Category != "linear" || window.Endpoint != "https://api-testnet.bybit.com/v5/market/kline" ||
			window.StartMS != c-31*dayMS || window.EndMS != c-1 || len(window.Candles) != 31 {
			return Batch{}, ErrBatch
		}
		encoded := wireWindow{Symbol: symbol, Environment: window.Environment, Source: window.Source, Category: window.Category,
			Endpoint: window.Endpoint, StartMS: window.StartMS, EndMS: window.EndMS, ServerMS: server}
		for _, candle := range window.Candles {
			open, valid := exactMS(candle.OpenTime)
			if !valid {
				return Batch{}, ErrBatch
			}
			text, err := candle.Close.WireDecimal()
			if err != nil {
				return Batch{}, ErrBatch
			}
			price, err := model.ParsePrice(text)
			if err != nil {
				return Batch{}, ErrBatch
			}
			if cmp, err := price.Cmp(candle.Close); err != nil || cmp != 0 {
				return Batch{}, ErrBatch
			}
			encoded.Candles = append(encoded.Candles, wireClose{open, text, candle.Confirmed})
			series[symbol] = append(series[symbol], data.DailyClose{Symbol: candle.Symbol, OpenTime: time.UnixMilli(open).UTC(), Close: price, Confirmed: candle.Confirmed})
		}
		document.Windows = append(document.Windows, encoded)
	}
	flags, err := planner.ROC30(values.Symbols, series, time.UnixMilli(c).UTC())
	if err != nil {
		return Batch{}, errors.Join(ErrBatch, err)
	}
	for _, rows := range series {
		if rows[len(rows)-1].OpenTime.UnixMilli() != c-dayMS {
			return Batch{}, ErrBatch
		}
	}
	for _, symbol := range values.Symbols {
		reason := "roc30_nonpositive"
		if flags[symbol] {
			reason = "roc30_positive"
		}
		document.Signals = append(document.Signals, wireSignal{symbol, flags[symbol], reason})
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return Batch{}, ErrBatch
	}
	hash := sha256.Sum256(payload)
	return Batch{string(payload), hex.EncodeToString(hash[:])}, nil
}

// Payload возвращает отдельную копию канонического документа без изменяемых aliases.
func (b Batch) Payload() []byte { return []byte(b.payload) }

// Hash возвращает SHA256 точных канонических байтов, пустой для zero value.
func (b Batch) Hash() string { return b.hash }

// DecodeBatch повторно вычисляет ROC30 из сохранённых входов и принимает только canonical v1.
func DecodeBatch(payload []byte) (Batch, error) {
	var document wireBatch
	if json.Unmarshal(payload, &document) != nil {
		return Batch{}, ErrBatch
	}
	cfg, err := config.Parse(document.Config)
	if err != nil {
		return Batch{}, ErrBatch
	}
	windows := make([]Window, 0, len(document.Windows))
	for _, encoded := range document.Windows {
		window := Window{Symbol: encoded.Symbol, Environment: encoded.Environment, Source: encoded.Source, Category: encoded.Category,
			Endpoint: encoded.Endpoint, StartMS: encoded.StartMS, EndMS: encoded.EndMS, ServerTime: time.UnixMilli(encoded.ServerMS).UTC()}
		for _, encodedClose := range encoded.Candles {
			close, err := model.ParsePrice(encodedClose.Close)
			if err != nil {
				return Batch{}, ErrBatch
			}
			window.Candles = append(window.Candles, data.DailyClose{Symbol: encoded.Symbol, OpenTime: time.UnixMilli(encodedClose.OpenMS).UTC(), Close: close, Confirmed: encodedClose.Confirmed})
		}
		windows = append(windows, window)
	}
	batch, err := NewBatch(cfg, time.UnixMilli(document.CutoffMS).UTC(), windows)
	if err != nil || !bytes.Equal(batch.Payload(), payload) {
		return Batch{}, ErrBatch
	}
	return batch, nil
}
