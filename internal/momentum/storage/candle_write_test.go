package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// candleWriterFixture создаёт проверенные namespaces и страницу с точной OHLCV моделью.
func candleWriterFixture(t *testing.T) (*sql.DB, CandleStream, model.CandlePage) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	key := CandleStream{"testnet", "quoted' fixture", "BTCUSDT", "D"}
	for _, environment := range []string{"testnet", "mainnet"} {
		_, err := db.Exec(`INSERT INTO momentum.datasets (environment,dataset_id,source,category) VALUES ($1,$2,'bybit_v5','linear')`, environment, key.DatasetID)
		migrationResult(t, err, nil)
	}
	stamp := time.Unix(3*86400, 0).UTC()
	row := model.Candle{Symbol: key.Symbol, IntervalCode: key.IntervalCode, OpenTime: stamp, Confirmed: true}
	for i, d := range []*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close, &row.Volume} {
		value, err := model.ParseQty([]string{"9007199254740993.01", "9007199254740998.02", "0.000001", "9007199254740995.03", "0"}[i])
		migrationResult(t, err, nil)
		*d = value
	}
	page := model.CandlePage{Environment: key.Environment, Source: "bybit_v5", Category: "linear",
		Endpoint: "https://api-testnet.bybit.com/v5/market/kline", ServerTime: stamp.Add(24 * time.Hour), Candles: []model.Candle{row}}
	return db, key, page
}

// candleWriterTx начинает обязательную write транзакцию и гарантирует cleanup.
func candleWriterTx(t *testing.T, db *sql.DB, options ...sql.TxOptions) *sql.Tx {
	t.Helper()
	opt := sql.TxOptions{Isolation: sql.LevelReadCommitted}
	if len(options) != 0 {
		opt = options[0]
	}
	tx, err := db.BeginTx(context.Background(), &opt)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// requireCandleWriteFailure проверяет безопасную причину и закрытую транзакцию.
func requireCandleWriteFailure(t *testing.T, tx *sql.Tx, err, want error) {
	t.Helper()
	if !errors.Is(err, ErrCandles) || !errors.Is(err, want) {
		t.Fatalf("write outcome: %v, want %v", err, want)
	}
	for _, forbidden := range []string{"sensitive_sql_marker", "postgres://", "pq:", "momentum.candles"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("raw SQL diagnostic leaked")
		}
	}
	if tx != nil {
		migrationResult(t, tx.Commit(), sql.ErrTxDone)
	}
}

// TestAppendCandlePage проверяет точность D/60, scope, предел и отсутствие rewrite.
func TestAppendCandlePage(t *testing.T) {
	db, dKey, page := candleWriterFixture(t)
	ctx := context.Background()
	for _, interval := range []string{"D", "60"} {
		key, input := dKey, page
		key.IntervalCode = interval
		step := 24 * time.Hour
		if interval == "60" {
			key.Environment, input.Environment, input.Endpoint = "mainnet", "mainnet", "https://api.bybit.com/v5/market/kline"
			step = time.Hour
		}
		row := page.Candles[0]
		row.IntervalCode = interval
		row.OpenTime = row.OpenTime.In(time.FixedZone("offset", 3*3600))
		input.ServerTime = row.OpenTime.Add(step)
		input.Candles = make([]model.Candle, 2)
		if interval == "60" {
			input.Candles = make([]model.Candle, 200)
			row.OpenTime = row.OpenTime.Add(200 * step)
			input.ServerTime = row.OpenTime
			row.Confirmed = false
		}
		for i := range input.Candles {
			input.Candles[i] = row
			row.OpenTime, row.Confirmed = row.OpenTime.Add(-step), true
		}
		tx := candleWriterTx(t, db)
		migrationResult(t, AppendCandlePage(ctx, tx, key, input), nil)
		start, end := row.OpenTime.UTC(), input.Candles[0].OpenTime.Add(step).UTC()
		rows, err := ReadCandleRange(ctx, db, key, start, end, 200)
		if err != nil || len(rows) != 0 {
			t.Fatalf("append committed early: %v, rows=%d", err, len(rows))
		}
		migrationResult(t, tx.Commit(), nil)
		rows, err = ReadCandleRange(ctx, db, key, start, end, 200)
		if err != nil || len(rows) != len(input.Candles) {
			t.Fatalf("read after append: %v rows=%d", err, len(rows))
		}
		for i, got := range rows {
			want := input.Candles[len(rows)-i-1]
			if !got.OpenTime.Equal(want.OpenTime) || got.OpenTime.Location() != time.UTC || got.Confirmed != want.Confirmed {
				t.Fatal("wrong row identity or confirmation")
			}
			for j, field := range [...]model.Decimal{got.Open, got.High, got.Low, got.Close, got.Volume} {
				cmp, err := field.Cmp([...]model.Decimal{want.Open, want.High, want.Low, want.Close, want.Volume}[j])
				if err != nil || cmp != 0 {
					t.Fatalf("field %d lost precision", j)
				}
			}
		}
		var xmin string
		migrationResult(t, db.QueryRow("SELECT max(xmin::text) FROM momentum.candles WHERE interval_code=$1", interval).Scan(&xmin), nil)
		tx = candleWriterTx(t, db)
		migrationResult(t, AppendCandlePage(ctx, tx, key, input), nil)
		migrationResult(t, tx.Commit(), nil)
		var count, rewritten int
		migrationResult(t, db.QueryRow("SELECT count(*),count(*) FILTER (WHERE xmin::text<>$2) FROM momentum.candles WHERE interval_code=$1", interval, xmin).Scan(&count, &rewritten), nil)
		if count != len(rows) || rewritten != 0 || input.Candles[0].OpenTime.Location() == time.UTC {
			t.Fatal("retry rewrote/duplicated rows or changed input")
		}
	}
}

// TestAppendCandlePageConflict проверяет все persisted поля и rollback чужих изменений.
func TestAppendCandlePageConflict(t *testing.T) {
	db, key, page := candleWriterFixture(t)
	ctx := context.Background()
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendCandlePage(ctx, tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	const identitySQL = "SELECT row(open,high,low,close,volume,confirmed,xmin::text)::text FROM momentum.candles"
	original := migrationValue(t, db, identitySQL)
	for _, field := range []string{"open", "high", "low", "close", "volume", "confirmed"} {
		t.Run(field, func(t *testing.T) {
			input := page
			newer, existing := page.Candles[0], page.Candles[0]
			input.ServerTime = page.ServerTime.Add(24 * time.Hour)
			newer.OpenTime = newer.OpenTime.Add(24 * time.Hour)
			if field == "confirmed" {
				input.ServerTime = existing.OpenTime
				newer.Confirmed, existing.Confirmed = false, false
			} else {
				d := map[string]*model.Decimal{"open": &existing.Open, "high": &existing.High,
					"low": &existing.Low, "close": &existing.Close, "volume": &existing.Volume}[field]
				delta, err := model.ParseQty("0.0000001")
				migrationResult(t, err, nil)
				*d, err = d.Add(delta)
				migrationResult(t, err, nil)
			}
			input.Candles = []model.Candle{newer, existing}
			tx := candleWriterTx(t, db)
			_, err := tx.Exec("INSERT INTO momentum.datasets VALUES ('testnet','prior-marker','bybit_v5','linear',now())")
			migrationResult(t, err, nil)
			requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, key, input), ErrCandleConflict)
			if migrationValue(t, db, "SELECT count(*) FROM momentum.candles") != "1" || migrationValue(t, db, identitySQL) != original ||
				migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE dataset_id='prior-marker'") != "0" {
				t.Fatal("partial row or previous transaction changes survived")
			}
		})
	}
}

// TestAppendCandlePageRejections проверяет вход, уровень транзакции, SQL и context.
func TestAppendCandlePageRejections(t *testing.T) {
	db, key, page := candleWriterFixture(t)
	ctx := context.Background()
	for _, change := range []func(*CandleStream, *model.CandlePage){
		func(k *CandleStream, _ *model.CandlePage) { k.DatasetID = " invalid" },
		func(_ *CandleStream, p *model.CandlePage) { p.Endpoint = "https://api.bybit.com/v5/market/kline" },
		func(_ *CandleStream, p *model.CandlePage) { p.Environment = "mainnet" },
		func(_ *CandleStream, p *model.CandlePage) { p.Source = "wrong" },
		func(_ *CandleStream, p *model.CandlePage) { p.Category = "spot" },
		func(_ *CandleStream, p *model.CandlePage) { p.ServerTime = time.Time{} },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles = nil },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles = make([]model.Candle, 201) },
		func(_ *CandleStream, p *model.CandlePage) {
			p.Candles[0].OpenTime = p.Candles[0].OpenTime.Add(time.Nanosecond)
		},
		func(_ *CandleStream, p *model.CandlePage) {
			p.Candles[0].OpenTime = p.Candles[0].OpenTime.Add(time.Hour)
		},
		func(_ *CandleStream, p *model.CandlePage) { p.Candles[0].IntervalCode = "60" },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles[0].Symbol = "ETHUSDT" },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles[0].Confirmed = false },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles[0].Open = model.Zero() },
		func(_ *CandleStream, p *model.CandlePage) {
			one, _ := model.ParsePrice("1")
			three, _ := model.ParsePrice("3")
			p.Candles[0].Low, _ = one.Quo(three)
		},
		func(_ *CandleStream, p *model.CandlePage) { p.Candles[0].High = p.Candles[0].Low },
		func(_ *CandleStream, p *model.CandlePage) { p.Candles = append(p.Candles, p.Candles[0]) },
	} {
		k, p := key, page
		p.Candles = append([]model.Candle(nil), p.Candles...)
		change(&k, &p)
		tx := candleWriterTx(t, db)
		requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, k, p), ErrCandleArgument)
	}
	invalid, err := page.Candles[0].Volume.Sub(page.Candles[0].Close)
	migrationResult(t, err, nil)
	input := page
	input.Candles = append([]model.Candle(nil), page.Candles...)
	input.Candles[0].Volume = invalid
	tx := candleWriterTx(t, db)
	requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, key, input), ErrCandleArgument)
	for _, opt := range []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelReadCommitted, ReadOnly: true}} {
		tx := candleWriterTx(t, db, opt)
		requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, key, page), ErrCandleTx)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	tx = candleWriterTx(t, db)
	requireCandleWriteFailure(t, tx, AppendCandlePage(canceled, tx, key, page), context.Canceled)
	requireCandleWriteFailure(t, nil, AppendCandlePage(ctx, nil, key, page), ErrCandleTx)
	missing := key
	missing.DatasetID = "absent"
	tx = candleWriterTx(t, db)
	requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, missing, page), ErrCandleDataset)
	_, err = db.Exec(`CREATE FUNCTION momentum.fail_candle() RETURNS trigger LANGUAGE plpgsql AS
	 $$ BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END; $$;
	 CREATE TRIGGER stop_candle BEFORE INSERT ON momentum.candles FOR EACH ROW EXECUTE FUNCTION momentum.fail_candle()`)
	migrationResult(t, err, nil)
	tx = candleWriterTx(t, db)
	requireCandleWriteFailure(t, tx, AppendCandlePage(ctx, tx, key, page), ErrCandles)
	if migrationValue(t, db, "SELECT count(*) FROM momentum.candles") != "0" {
		t.Fatal("failed write changed journal")
	}
}

// TestAppendCandlePageConcurrent проверяет заблокированного конкурента на независимом pool.
func TestAppendCandlePageConcurrent(t *testing.T) {
	db, key, page := candleWriterFixture(t)
	ctx := context.Background()
	dsn, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	migrationResult(t, err, nil)
	dsn.Path, dsn.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	other, err := OpenMomentumPostgres(ctx, dsn.String())
	migrationResult(t, err, nil)
	defer other.Close()
	for _, variant := range []string{"exact", "divergent", "deadline"} {
		t.Run(variant, func(t *testing.T) {
			winner := candleWriterTx(t, db)
			input := page
			input.Candles = append([]model.Candle(nil), page.Candles...)
			for i := range input.Candles {
				input.Candles[i].Symbol = strings.ToUpper(variant) + "USDT"
			}
			stream := key
			stream.Symbol = input.Candles[0].Symbol
			migrationResult(t, AppendCandlePage(ctx, winner, stream, input), nil)
			contender := candleWriterTx(t, other)
			var pid int
			migrationResult(t, contender.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			if variant == "divergent" {
				input.Candles[0].Volume = input.Candles[0].Open
			}
			call, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- AppendCandlePage(call, contender, stream, input) }()
			for {
				var waiting bool
				err := db.QueryRowContext(call, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting)
				migrationResult(t, err, nil)
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if variant != "deadline" {
				migrationResult(t, winner.Commit(), nil)
			}
			got := <-result
			switch variant {
			case "exact":
				migrationResult(t, got, nil)
				migrationResult(t, contender.Commit(), nil)
			case "divergent":
				requireCandleWriteFailure(t, contender, got, ErrCandleConflict)
			case "deadline":
				requireCandleWriteFailure(t, contender, got, context.DeadlineExceeded)
				migrationResult(t, winner.Rollback(), nil)
			}
		})
	}
	if migrationValue(t, db, "SELECT count(*) FROM momentum.candles") != "2" {
		t.Fatal("concurrent winner or rollback incorrect")
	}
}
