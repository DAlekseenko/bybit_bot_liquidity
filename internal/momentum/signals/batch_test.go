package signals

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// batchFixture задаёт ровно93 фактических closes с positive/negative/flat ROC.
func batchFixture(t *testing.T) (config.Config, time.Time, []Window) {
	t.Helper()
	cfg, err := config.Load("../../../configs/momentum.paper.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var windows []Window
	for i, symbol := range cfg.Values().Symbols {
		window := Window{Symbol: symbol, Environment: "testnet", Source: "bybit_v5", Category: "linear", Endpoint: "https://api-testnet.bybit.com/v5/market/kline",
			StartMS: cutoff.AddDate(0, 0, -31).UnixMilli(), EndMS: cutoff.UnixMilli() - 1, ServerTime: cutoff.Add(time.Duration(i+1) * time.Hour)}
		for day := range 31 {
			price, err := model.ParsePrice("100000000000000000")
			if day == 30 {
				price, err = model.ParsePrice([]string{"100000000000000001", "99999999999999999", "100000000000000000"}[i])
			}
			if err != nil {
				t.Fatal(err)
			}
			window.Candles = append(window.Candles, data.DailyClose{Symbol: symbol, OpenTime: cutoff.AddDate(0, 0, day-31), Close: price, Confirmed: true})
		}
		windows = append(windows, window)
	}
	return cfg, cutoff, windows
}

// requireBatchError проверяет fail-closed zero value без частичных данных.
func requireBatchError(t *testing.T, batch Batch, err error) {
	t.Helper()
	if !errors.Is(err, ErrBatch) || batch.Hash() != "" || len(batch.Payload()) != 0 {
		t.Fatalf("partial batch or missing sentinel: %+v %v", batch, err)
	}
}

// TestBatchValue проверяет canonical payload, точность, ownership и детерминизм.
func TestBatchValue(t *testing.T) {
	cfg, cutoff, windows := batchFixture(t)
	before, _ := json.Marshal(windows)
	batch, err := NewBatch(cfg, cutoff, windows)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(windows)
	sum := sha256.Sum256(batch.Payload())
	if !bytes.Equal(before, after) || batch.Hash() != hex.EncodeToString(sum[:]) {
		t.Fatal("input changed or incorrect content hash")
	}
	var document wireBatch
	if json.Unmarshal(batch.Payload(), &document) != nil || len(document.Windows) != 3 || len(document.Signals) != 3 ||
		!document.Signals[0].Active || document.Signals[1].Active || document.Signals[2].Active || document.Signals[0].Reason != "roc30_positive" || document.Signals[2].Reason != "roc30_nonpositive" {
		t.Fatalf("wrong exact ROC: %+v", document.Signals)
	}
	snapshot, configHash, _ := cfg.Snapshot()
	if !bytes.Equal(document.Config, snapshot) || document.ConfigHash != configHash || document.Version != "momentum/signal-batch/v1" || document.Algorithm != "momentum/roc30/v1" || document.CutoffMS != cutoff.UnixMilli() {
		t.Fatal("lost exact safe config or algorithm/cutoff identity")
	}
	for i, window := range document.Windows {
		input := windows[i]
		if window.Symbol != input.Symbol || window.Environment != input.Environment || window.Source != input.Source ||
			window.Category != input.Category || window.Endpoint != input.Endpoint || window.StartMS != input.StartMS ||
			window.EndMS != input.EndMS || window.ServerMS != input.ServerTime.UnixMilli() || len(window.Candles) != 31 {
			t.Fatal("lost actual source/request/clock evidence")
		}
		for j, candle := range window.Candles {
			text, _ := input.Candles[j].Close.WireDecimal()
			if candle.OpenMS != input.Candles[j].OpenTime.UnixMilli() || candle.Close != text || candle.Confirmed != input.Candles[j].Confirmed {
				t.Fatal("lost one of93 actual replay inputs")
			}
		}
	}
	windows[0], windows[2] = windows[2], windows[0]
	windows[0].ServerTime = windows[0].ServerTime.In(time.FixedZone("other", 3*3600))
	windows[1].Candles[4].OpenTime = windows[1].Candles[4].OpenTime.In(time.FixedZone("other", 3*3600))
	again, err := NewBatch(cfg, cutoff.In(time.FixedZone("other", 3*3600)), windows)
	if err != nil || again.Hash() != batch.Hash() || !bytes.Equal(again.Payload(), batch.Payload()) {
		t.Fatal("ordering/location changed canonical value")
	}
	leaked := batch.Payload()
	leaked[0] = 'x'
	windows[0].Candles[0].Close = model.Zero()
	windows[0].Source = "mutated"
	if bytes.Equal(leaked, batch.Payload()) || !bytes.Equal(batch.Payload(), again.Payload()) {
		t.Fatal("mutable input/getter alias escaped")
	}
	cfg2, cutoff2, windows2 := batchFixture(t)
	windows2[0].ServerTime = windows2[0].ServerTime.Add(time.Millisecond)
	distinct, err := NewBatch(cfg2, cutoff2, windows2)
	if err != nil || distinct.Hash() == batch.Hash() {
		t.Fatal("distinct actual clock observation lost")
	}
	cfg2, cutoff2, windows2 = batchFixture(t)
	windows2[0].Candles[1].Close, _ = model.ParsePrice("100000000000000000.0")
	equivalent, err := NewBatch(cfg2, cutoff2, windows2)
	if err != nil || equivalent.Hash() != batch.Hash() {
		t.Fatal("numeric-equivalent input changed canonical value")
	}
}

// TestBatchArguments сохраняет все source/config/time/quality/representation guards.
func TestBatchArguments(t *testing.T) {
	cfg, cutoff, windows := batchFixture(t)
	batch, err := NewBatch(config.Config{}, cutoff, windows)
	requireBatchError(t, batch, err)
	for _, stamp := range []time.Time{{}, time.Unix(-1, 0), time.Unix(0, 0), time.UnixMilli(31 * dayMS), cutoff.Add(time.Nanosecond), cutoff.Add(time.Hour), time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC), time.Unix(9223372036854775807, 0)} {
		batch, err = NewBatch(cfg, stamp, windows)
		requireBatchError(t, batch, err)
	}
	batch, err = NewBatch(cfg, cutoff, nil)
	requireBatchError(t, batch, err)
	for _, count := range []int{0, 1, 2, 4} {
		candidate := append(append([]Window(nil), windows...), windows[0])[:count]
		batch, err = NewBatch(cfg, cutoff, candidate)
		requireBatchError(t, batch, err)
	}
	for _, edit := range []func(*Window){
		func(w *Window) { w.Symbol = "UNKNOWNUSDT" }, func(w *Window) { w.Symbol = "ETHUSDT" },
		func(w *Window) { w.Environment = "mainnet" }, func(w *Window) { w.Source = "other" }, func(w *Window) { w.Category = "spot" },
		func(w *Window) { w.StartMS++ }, func(w *Window) { w.EndMS++ },
		func(w *Window) { w.ServerTime = time.Time{} }, func(w *Window) { w.ServerTime = w.ServerTime.AddDate(0, 0, -1) },
		func(w *Window) { w.ServerTime = w.ServerTime.AddDate(0, 0, 1) }, func(w *Window) { w.ServerTime = w.ServerTime.Add(time.Nanosecond) },
		func(w *Window) { w.Candles = nil }, func(w *Window) { w.Candles = w.Candles[:30] },
		func(w *Window) { w.Candles = append(w.Candles, w.Candles[30]) },
		func(w *Window) { w.Candles[4].OpenTime = w.Candles[4].OpenTime.Add(time.Nanosecond) },
	} {
		cfg, cutoff, windows = batchFixture(t)
		edit(&windows[0])
		batch, err = NewBatch(cfg, cutoff, windows)
		requireBatchError(t, batch, err)
	}
	for _, tc := range []struct {
		edit func(*Window)
		want error
	}{
		{func(w *Window) { w.Candles[4].Symbol = "OTHER" }, data.ReasonMixedSymbol},
		{func(w *Window) { w.Candles[30].Confirmed = false }, data.ReasonUnconfirmed},
		{func(w *Window) {
			for i := range w.Candles {
				w.Candles[i].OpenTime = w.Candles[i].OpenTime.AddDate(0, 0, -1)
			}
		}, data.ReasonDifferentEnd},
		{func(w *Window) {
			for i := range w.Candles {
				for k := range windows {
					windows[k].Candles[i].OpenTime = windows[k].Candles[i].OpenTime.AddDate(0, 0, -1)
				}
			}
		}, ErrBatch},
	} {
		cfg, cutoff, windows = batchFixture(t)
		tc.edit(&windows[0])
		batch, err = NewBatch(cfg, cutoff, windows)
		requireBatchError(t, batch, err)
		if (tc.want == ErrBatch && err != tc.want) || (tc.want != ErrBatch && !errors.Is(err, tc.want)) {
			t.Fatal("unchanged common-window failure was not propagated")
		}
	}
	for _, endpoint := range []string{"", "http://api-testnet.bybit.com/v5/market/kline", "https://api.bybit.com/v5/market/kline", "https://api-testnet.bybit.com:443/v5/market/kline", "https://api-testnet.bybit.com/v5/market/kline/", "https://u:p@api-testnet.bybit.com/v5/market/kline", "https://api-testnet.bybit.com/v5/market/kline?x=1", "https://api-testnet.bybit.com.evil/v5/market/kline"} {
		cfg, cutoff, windows = batchFixture(t)
		windows[0].Endpoint = endpoint
		batch, err = NewBatch(cfg, cutoff, windows)
		requireBatchError(t, batch, err)
	}
	one, _ := model.ParsePrice("1")
	three, _ := model.ParsePrice("3")
	third, _ := one.Quo(three)
	negative, _ := model.ParseAmount("-1")
	tiny, _ := model.ParsePrice("0." + strings.Repeat("0", 63) + "1")
	precise, _ := tiny.Mul(tiny)
	large, _ := model.ParsePrice(strings.Repeat("9", 128))
	wide, _ := large.Add(one)
	for _, price := range []model.Decimal{{}, model.Zero(), negative, third, precise, wide} {
		cfg, cutoff, windows = batchFixture(t)
		windows[1].Candles[4].Close = price
		batch, err = NewBatch(cfg, cutoff, windows)
		requireBatchError(t, batch, err)
	}
	cfg, cutoff, windows = batchFixture(t)
	values := cfg.Values()
	values.Mode = "backtest"
	values.DataSource = config.DataSource{Kind: "frozen_dataset", Environment: "mainnet", DatasetID: "d", ManifestSHA256: strings.Repeat("0", 64)}
	raw, _ := json.Marshal(values)
	backtest, parseErr := config.Parse(raw)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	batch, err = NewBatch(backtest, cutoff, windows)
	requireBatchError(t, batch, err)
}

// TestBatchCodec проверяет offline replay и строгую canonical/hash целостность документа.
func TestBatchCodec(t *testing.T) {
	cfg, cutoff, windows := batchFixture(t)
	batch, err := NewBatch(cfg, cutoff, windows)
	if err != nil {
		t.Fatal(err)
	}
	decodePayload := batch.Payload()
	decoded, err := DecodeBatch(decodePayload)
	if err != nil || decoded.Hash() != batch.Hash() || !bytes.Equal(decoded.Payload(), batch.Payload()) {
		t.Fatal("canonical replay changed actual signals/provenance")
	}
	var original wireBatch
	if json.Unmarshal(batch.Payload(), &original) != nil {
		t.Fatal("bad fixture payload")
	}
	for _, edit := range []func(*wireBatch){
		func(d *wireBatch) { d.Version = "v2" }, func(d *wireBatch) { d.Algorithm = "other" }, func(d *wireBatch) { d.ConfigHash = strings.Repeat("0", 64) },
		func(d *wireBatch) { d.CutoffMS++ }, func(d *wireBatch) { d.Config = nil }, func(d *wireBatch) { d.Windows = nil },
		func(d *wireBatch) { d.Windows[0].Environment = "mainnet" }, func(d *wireBatch) { d.Windows[0].ServerMS -= dayMS },
		func(d *wireBatch) { d.Windows[0].Candles[0].Close = "10.00" }, func(d *wireBatch) { d.Windows[0].Candles[0].Close = "1/3" },
		func(d *wireBatch) { d.Windows[0].Candles[0].Confirmed = false }, func(d *wireBatch) { d.Windows[0].Candles[0].OpenMS++ },
		func(d *wireBatch) { d.Signals[0].Active = !d.Signals[0].Active }, func(d *wireBatch) { d.Signals[0].Reason = "fabricated" },
	} {
		var changed wireBatch
		_ = json.Unmarshal(batch.Payload(), &changed)
		edit(&changed)
		raw, _ := json.Marshal(changed)
		got, err := DecodeBatch(raw)
		requireBatchError(t, got, err)
	}
	for _, raw := range [][]byte{nil, {}, []byte("null"), append(batch.Payload(), ' '), append(batch.Payload(), batch.Payload()...), []byte(strings.Replace(string(batch.Payload()), "{", "{\"Unknown\":1,", 1)), []byte(strings.Replace(string(batch.Payload()), "{", "{\"Version\":\"ignored\",", 1))} {
		got, err := DecodeBatch(raw)
		requireBatchError(t, got, err)
	}
	copyPayload := decoded.Payload()
	copyPayload[0] = 'x'
	decodePayload[0] = 'x'
	if reflect.DeepEqual(copyPayload, decoded.Payload()) || !bytes.Equal(decoded.Payload(), batch.Payload()) || decoded.Hash() != batch.Hash() {
		t.Fatal("decoded payload leaked mutable alias")
	}
}
