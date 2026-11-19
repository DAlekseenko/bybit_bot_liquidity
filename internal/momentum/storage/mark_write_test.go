package storage

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const markSnapshotSQL = "SELECT row(open,high,low,close,confirmed,xmin::text)::text FROM momentum.mark_prices"

// TestMarkPriceWriteValues проверяет точную десятичную проекцию OHLC без PostgreSQL.
func TestMarkPriceWriteValues(t *testing.T) {
	price, err := model.ParsePrice("9007199254740993.01")
	got, writeErr := markPriceWriteValues(model.MarkPriceCandle{Open: price, High: price, Low: price, Close: price})
	if err != nil || writeErr != nil || got != ([4]string{"9007199254740993.01", "9007199254740993.01", "9007199254740993.01", "9007199254740993.01"}) {
		t.Fatalf("OHLC projection=%v parse_err=%v write_err=%v", got, err, writeErr)
	}
}

// markWriterFixture создаёт зарегистрированные namespaces и отдельный trade ряд.
func markWriterFixture(t *testing.T, year int) (*sql.DB, MarkPriceStream, model.MarkPricePage) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	key := MarkPriceStream{"testnet", "mark' fixture", "BTCUSDT", "D"}
	for _, environment := range []string{"testnet", "mainnet"} {
		_, err := db.Exec("INSERT INTO momentum.datasets VALUES ($1,$2,'bybit_v5','linear',DEFAULT)", environment, key.DatasetID)
		migrationResult(t, err, nil)
	}
	open := time.Date(year, 1, 1, 3, 0, 0, 0, time.FixedZone("offset", 3*3600))
	row := model.MarkPriceCandle{OpenTime: open, Confirmed: true}
	raw := [...]string{"9007199254740993.01", "9007199254740997.02", "0.00000000000000012345", "9007199254740995.03"}
	for i, price := range [...]*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close} {
		value, err := model.ParsePrice(raw[i])
		migrationResult(t, err, nil)
		*price = value
	}
	_, err := db.Exec("INSERT INTO momentum.candles VALUES ($1,$2,$3,$4,$5,1,2,0.5,1.5,0,TRUE)",
		key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, open)
	migrationResult(t, err, nil)
	return db, key, model.MarkPricePage{
		Environment: key.Environment, Source: "bybit_v5", Category: "linear", Symbol: key.Symbol, IntervalCode: key.IntervalCode,
		Endpoint: "https://api-testnet.bybit.com/v5/market/mark-price-kline", StartTime: open, EndTime: open,
		ServerTime: open.Add(24 * time.Hour), Candles: []model.MarkPriceCandle{row},
	}
}

// requireMarkWriteFailure проверяет safe sentinel и полное закрытие транзакции.
func requireMarkWriteFailure(t *testing.T, tx *sql.Tx, err, want error) {
	t.Helper()
	migrationResult(t, err, ErrMarkPrices)
	migrationResult(t, err, want)
	if strings.Contains(err.Error(), "mark' fixture") || strings.Contains(err.Error(), "momentum.mark_prices") {
		t.Fatal("storage input or SQL diagnostic leaked")
	}
	if tx != nil {
		migrationResult(t, tx.Commit(), sql.ErrTxDone)
	}
}

// TestAppendMarkPricePage проверяет точную OHLC, идемпотентность, границы и пробел.
func TestAppendMarkPricePage(t *testing.T) {
	db, key, page := markWriterFixture(t, 3000)
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendMarkPricePage(t.Context(), tx, key, page), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "0")
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, "SELECT row(open,high,low,close,confirmed)::text FROM momentum.mark_prices",
		"(9007199254740993.01,9007199254740997.02,0.00000000000000012345,9007199254740995.03,t)")
	migrationValue(t, db, "SELECT close::text FROM momentum.candles", "1.5")
	_, err := db.Exec("UPDATE momentum.mark_prices SET low=0.0000000000000001234500")
	migrationResult(t, err, nil)
	original := migrationValue(t, db, markSnapshotSQL)
	tx = candleWriterTx(t, db)
	migrationResult(t, AppendMarkPricePage(t.Context(), tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, markSnapshotSQL, original)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "1")
	if page.Candles[0].OpenTime.Location() == time.UTC {
		t.Fatal("input page was mutated")
	}
	key.Environment, key.IntervalCode = "mainnet", "60"
	page.Environment, page.IntervalCode = key.Environment, key.IntervalCode
	page.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
	row := page.Candles[0]
	page.Candles = make([]model.MarkPriceCandle, 200)
	for i := range page.Candles {
		page.Candles[i] = row
		page.Candles[i].OpenTime = row.OpenTime.Add(time.Duration(200-i) * time.Hour)
	}
	page.Candles[199].OpenTime = row.OpenTime
	page.EndTime = page.Candles[0].OpenTime.Add(99 * time.Millisecond)
	page.ServerTime = page.Candles[0].OpenTime.Add(time.Hour)
	tx = candleWriterTx(t, db)
	migrationResult(t, AppendMarkPricePage(t.Context(), tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices WHERE environment='mainnet' AND interval_code='60'", "200")
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices WHERE environment='mainnet' AND open_time='3000-01-01 01:00+00'", "0")
}

// TestAppendMarkPricePageConflict проверяет каждое immutable поле и откат всего tx.
func TestAppendMarkPricePageConflict(t *testing.T) {
	db, key, page := markWriterFixture(t, 2026)
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendMarkPricePage(t.Context(), tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	delta, err := model.ParsePrice("0.00000000000000000001")
	migrationResult(t, err, nil)
	for i, field := range []string{"open", "high", "low", "close", "legacy-unconfirmed"} {
		t.Run(field, func(t *testing.T) {
			input, newer, conflict := page, page.Candles[0], page.Candles[0]
			newer.OpenTime = newer.OpenTime.Add(24 * time.Hour)
			input.EndTime, input.ServerTime = newer.OpenTime, newer.OpenTime.Add(24*time.Hour)
			if i == 4 {
				_, err := db.Exec("UPDATE momentum.mark_prices SET confirmed=FALSE")
				migrationResult(t, err, nil)
			} else {
				price := [...]*model.Decimal{&conflict.Open, &conflict.High, &conflict.Low, &conflict.Close}[i]
				var addErr error
				*price, addErr = price.Add(delta)
				migrationResult(t, addErr, nil)
			}
			original := migrationValue(t, db, markSnapshotSQL)
			input.Candles = []model.MarkPriceCandle{newer, conflict}
			tx := candleWriterTx(t, db)
			_, err := tx.Exec("INSERT INTO momentum.datasets VALUES ('testnet','prior-marker','bybit_v5','linear',DEFAULT)")
			migrationResult(t, err, nil)
			requireMarkWriteFailure(t, tx, AppendMarkPricePage(t.Context(), tx, key, input), ErrMarkPriceConflict)
			migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "1")
			migrationValue(t, db, markSnapshotSQL, original)
			migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE dataset_id='prior-marker'", "0")
		})
	}
}

// TestAppendMarkPricePageArguments проверяет отказы provenance, цены, окна и порядка.
func TestAppendMarkPricePageArguments(t *testing.T) {
	db, key, page := markWriterFixture(t, 2026)
	for name, change := range map[string]func(*MarkPriceStream, *model.MarkPricePage){
		"dataset":     func(k *MarkPriceStream, _ *model.MarkPricePage) { k.DatasetID = " leading" },
		"symbol":      func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Symbol = "ETHUSDT" },
		"interval":    func(_ *MarkPriceStream, p *model.MarkPricePage) { p.IntervalCode = "60" },
		"environment": func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Environment = "mainnet" },
		"source":      func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Source = "wrong" },
		"category":    func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Category = "spot" },
		"trade": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.Endpoint = "https://api-testnet.bybit.com/v5/market/kline"
		},
		"origin": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
		},
		"nil":           func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles = nil },
		"empty":         func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles = p.Candles[:0] },
		"201":           func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles = make([]model.MarkPriceCandle, 201) },
		"epoch":         func(_ *MarkPriceStream, p *model.MarkPricePage) { p.StartTime = time.Unix(0, 0) },
		"start-grid":    func(_ *MarkPriceStream, p *model.MarkPricePage) { p.StartTime = p.StartTime.Add(-time.Hour) },
		"end-precision": func(_ *MarkPriceStream, p *model.MarkPricePage) { p.EndTime = p.EndTime.Add(time.Nanosecond) },
		"end-before":    func(_ *MarkPriceStream, p *model.MarkPricePage) { p.EndTime = p.EndTime.Add(-time.Millisecond) },
		"server-zero":   func(_ *MarkPriceStream, p *model.MarkPricePage) { p.ServerTime = time.Time{} },
		"unix-ms-overflow": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.ServerTime = time.UnixMilli(1<<63 - 1).Add(time.Millisecond)
		},
		"early-server": func(_ *MarkPriceStream, p *model.MarkPricePage) { p.ServerTime = p.ServerTime.Add(-time.Millisecond) },
		"row-postgres-range": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.StartTime = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC)
			p.EndTime, p.Candles[0].OpenTime = p.StartTime, p.StartTime
			p.ServerTime = p.StartTime.Add(24 * time.Hour)
		},
		"unconfirmed": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			newer := p.Candles[0]
			newer.Confirmed = false
			newer.OpenTime = newer.OpenTime.Add(24 * time.Hour)
			p.EndTime = newer.OpenTime
			p.Candles = append([]model.MarkPriceCandle{newer}, p.Candles...)
		},
		"open-grid": func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles[0].OpenTime = p.StartTime.Add(time.Hour) },
		"before-start": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.Candles[0].OpenTime = p.StartTime.Add(-24 * time.Hour)
		},
		"after-end": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.Candles[0].OpenTime = p.EndTime.Add(24 * time.Hour)
			p.ServerTime = p.ServerTime.Add(24 * time.Hour)
		},
		"zero":      func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles[0].Open = model.Zero() },
		"unset":     func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles[0].Open = model.Decimal{} },
		"bad-high":  func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles[0].High = p.Candles[0].Low },
		"bad-low":   func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles[0].Low = p.Candles[0].High },
		"duplicate": func(_ *MarkPriceStream, p *model.MarkPricePage) { p.Candles = append(p.Candles, p.Candles[0]) },
		"ascending": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			older := p.Candles[0]
			older.OpenTime = older.OpenTime.Add(-24 * time.Hour)
			p.StartTime = older.OpenTime
			p.Candles = []model.MarkPriceCandle{older, p.Candles[0]}
		},
		"negative": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			p.Candles[0].Low, _ = model.ParseAmount("-1")
		},
		"nonterminating": func(_ *MarkPriceStream, p *model.MarkPricePage) {
			one, _ := model.ParsePrice("1")
			three, _ := model.ParsePrice("3")
			p.Candles[0].Low, _ = one.Quo(three)
		},
	} {
		t.Run(name, func(t *testing.T) {
			k, p := key, page
			p.Candles = append([]model.MarkPriceCandle(nil), p.Candles...)
			change(&k, &p)
			tx := candleWriterTx(t, db)
			requireMarkWriteFailure(t, tx, AppendMarkPricePage(t.Context(), tx, k, p), ErrMarkPriceArgument)
		})
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "0")
}

// TestAppendMarkPricePageTransaction проверяет настройки, SQL failure и отмену.
func TestAppendMarkPricePageTransaction(t *testing.T) {
	db, key, page := markWriterFixture(t, 2026)
	ctx := context.Background()
	requireMarkWriteFailure(t, nil, AppendMarkPricePage(ctx, nil, key, page), ErrMarkPriceTx)
	for _, opt := range []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelReadCommitted, ReadOnly: true}} {
		tx := candleWriterTx(t, db, opt)
		requireMarkWriteFailure(t, tx, AppendMarkPricePage(ctx, tx, key, page), ErrMarkPriceTx)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	tx := candleWriterTx(t, db)
	requireMarkWriteFailure(t, tx, AppendMarkPricePage(canceled, tx, key, page), context.Canceled)
	missing := key
	missing.DatasetID = "absent"
	tx = candleWriterTx(t, db)
	requireMarkWriteFailure(t, tx, AppendMarkPricePage(ctx, tx, missing, page), ErrMarkPriceDataset)
	_, err := db.Exec(`CREATE FUNCTION momentum.fail_mark() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF NEW.open_time = '2026-01-01 00:00+00' THEN RAISE EXCEPTION 'sensitive_sql_marker'; END IF; RETURN NEW; END; $$;
	 CREATE TRIGGER stop_mark BEFORE INSERT ON momentum.mark_prices FOR EACH ROW EXECUTE FUNCTION momentum.fail_mark()`)
	migrationResult(t, err, nil)
	newer := page.Candles[0]
	newer.OpenTime = newer.OpenTime.Add(24 * time.Hour)
	page.Candles = append([]model.MarkPriceCandle{newer}, page.Candles...)
	page.EndTime, page.ServerTime = newer.OpenTime, newer.OpenTime.Add(24*time.Hour)
	tx = candleWriterTx(t, db)
	requireMarkWriteFailure(t, tx, AppendMarkPricePage(ctx, tx, key, page), ErrMarkPrices)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "0")
}

// TestAppendMarkPricePageConcurrent проверяет реальные конкурирующие вставки.
func TestAppendMarkPricePageConcurrent(t *testing.T) {
	db, key, page := markWriterFixture(t, 2026)
	ctx := context.Background()
	for _, variant := range []string{"exact", "divergent", "rollback", "deadline"} {
		t.Run(variant, func(t *testing.T) {
			input, k := page, key
			k.Symbol = strings.ToUpper(variant) + "USDT"
			input.Symbol = k.Symbol
			input.Candles = append([]model.MarkPriceCandle(nil), input.Candles...)
			winner, contender := candleWriterTx(t, db), candleWriterTx(t, db)
			var pid int
			migrationResult(t, contender.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			migrationResult(t, AppendMarkPricePage(ctx, winner, k, input), nil)
			if variant == "divergent" {
				input.Candles[0].Close = input.Candles[0].High
			}
			call, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- AppendMarkPricePage(call, contender, k, input) }()
			for {
				var waiting bool
				err := db.QueryRowContext(call, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting)
				migrationResult(t, err, nil)
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			switch variant {
			case "rollback":
				migrationResult(t, winner.Rollback(), nil)
			case "deadline":
			default:
				migrationResult(t, winner.Commit(), nil)
			}
			got := <-result
			switch variant {
			case "divergent":
				requireMarkWriteFailure(t, contender, got, ErrMarkPriceConflict)
			case "deadline":
				requireMarkWriteFailure(t, contender, got, context.DeadlineExceeded)
				migrationResult(t, winner.Rollback(), nil)
			default:
				migrationResult(t, got, nil)
				migrationResult(t, contender.Commit(), nil)
			}
		})
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "3")
}
