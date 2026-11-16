package storage

import (
	"context"
	"database/sql"
	"math"
	"net/url"
	"os"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// blockImportObservation сохраняет точные аргументы исходного запроса.
func blockImportObservation(key CandleImportWindow, expected CandleImportCheckpoint, page model.CandlePage) *CandleImportPageObservation {
	return &CandleImportPageObservation{key.Stream.Symbol, key.Stream.IntervalCode, key.LowerOpen, expected.NextEnd, page}
}

// requireImportBlockFailure сверяет нулевой checkpoint и полный откат.
func requireImportBlockFailure(t *testing.T, ctx context.Context, tx *sql.Tx, key CandleImportWindow, expected CandleImportCheckpoint, reason CandleImportBlockReason, observation *CandleImportPageObservation, want error) {
	t.Helper()
	got, err := BlockCandleImport(ctx, tx, key, expected, reason, observation)
	importAdvanceFailure(t, tx, got, err, want)
}

// TestBlockCandleImportReasons проверяет все причины и отсутствие преждевременной фиксации.
func TestBlockCandleImportReasons(t *testing.T) {
	for _, reason := range []CandleImportBlockReason{CandleImportBlockEmpty, CandleImportBlockGap, CandleImportBlockUnconfirmed, CandleImportBlockSourceBoundary} {
		t.Run(string(reason), func(t *testing.T) {
			ctx := context.Background()
			interval, buckets := "D", 3
			if reason == CandleImportBlockEmpty {
				interval = "60"
			}
			if reason == CandleImportBlockSourceBoundary {
				buckets = 4
			}
			db, key, prior, page := importAdvanceFixture(t, interval, buckets)
			if reason == CandleImportBlockGap {
				marketStatement(t, db, "UPDATE momentum.candle_import_progress SET environment='mainnet'", true)
				key.Stream.Environment, page.Environment = "mainnet", "mainnet"
				page.Endpoint = "https://api.bybit.com/v5/market/kline"
			}
			obs := blockImportObservation(key, prior, page)
			source := page.ServerTime
			switch reason {
			case CandleImportBlockEmpty:
				obs.Page = importAdvanceOpens(page)
			case CandleImportBlockGap:
				obs.Page = importAdvanceOpens(page, page.Candles[0].OpenTime, key.LowerOpen)
			case CandleImportBlockUnconfirmed:
				source = key.UpperOpen.Add(-137 * time.Millisecond)
				obs.Page.ServerTime, obs.Page.Candles = source, make([]model.Candle, 0)
			case CandleImportBlockSourceBoundary:
				obs, source = nil, time.Time{}
			}
			want := CandleImportCheckpoint{1, prior.NextEnd, "blocked"}
			for _, commit := range []bool{false, true} {
				tx := candleWriterTx(t, db)
				got, err := BlockCandleImport(ctx, tx, key, prior, reason, obs)
				migrationResult(t, err, nil)
				if got != want {
					t.Fatalf("blocked checkpoint: %+v", got)
				}
				importAdvanceDurable(t, db, prior, 0, time.Time{})
				if commit {
					migrationResult(t, tx.Commit(), nil)
				} else {
					migrationResult(t, tx.Rollback(), nil)
					importAdvanceDurable(t, db, prior, 0, time.Time{})
				}
			}
			importAdvanceDurable(t, db, want, 0, source)
			migrationValue(t, db, "SELECT reason FROM momentum.candle_import_progress", string(reason))
			stored, err := ReadCandleImport(ctx, db, key)
			migrationResult(t, err, nil)
			if stored.Checkpoint != want || stored.Reason != string(reason) ||
				(source.IsZero() != (stored.LastServerTime == nil)) ||
				(stored.LastServerTime != nil && !stored.LastServerTime.Equal(source)) {
				t.Fatalf("reader returned a different blocked state: %+v", stored)
			}
		})
	}
}

// TestBlockCandleImportProgress проверяет остановку после реальной страницы без изменения курсора.
func TestBlockCandleImportProgress(t *testing.T) {
	db, key, initial, page := importAdvanceFixture(t, "D", 3)
	ctx := context.Background()
	tx := candleWriterTx(t, db)
	prior, err := AdvanceCandleImportPage(ctx, tx, key, initial, page)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	xmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.candles")
	tx = candleWriterTx(t, db)
	zone := time.FixedZone("offset", 3*3600)
	prior.NextEnd = prior.NextEnd.In(zone)
	obs := blockImportObservation(key, prior, importAdvanceOpens(page, key.LowerOpen))
	obs.Start, obs.Page.ServerTime = obs.Start.In(zone), obs.Page.ServerTime.Add(time.Millisecond).In(zone)
	blocked, err := BlockCandleImport(ctx, tx, key, prior, CandleImportBlockGap, obs)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	want := CandleImportCheckpoint{prior.Revision + 1, prior.NextEnd.UTC(), "blocked"}
	if blocked != want || !obs.End.Equal(prior.NextEnd) || obs.Page.ServerTime.Location() != zone {
		t.Fatalf("blocked progress or input: %+v", blocked)
	}
	importAdvanceDurable(t, db, want, 1, obs.Page.ServerTime)
	migrationValue(t, db, "SELECT xmin::text FROM momentum.candles", xmin)
	rowXmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.candle_import_progress")
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockGap, obs, ErrCandleImportStale)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, blocked, CandleImportBlockGap, obs, ErrCandleImportState)
	migrationValue(t, db, "SELECT xmin::text FROM momentum.candle_import_progress", rowXmin)
}

// TestBlockCandleImportEvidence отклоняет page, не подтверждающий одну из причин.
func TestBlockCandleImportEvidence(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 3)
	ctx := context.Background()
	first := page.Candles[0].OpenTime
	for name, change := range map[string]func(*CandleImportPageObservation){
		"symbol":          func(o *CandleImportPageObservation) { o.Symbol = "ETHUSDT" },
		"interval":        func(o *CandleImportPageObservation) { o.IntervalCode = "60" },
		"lower":           func(o *CandleImportPageObservation) { o.Start = o.Start.Add(-time.Millisecond) },
		"cursor":          func(o *CandleImportPageObservation) { o.End = o.End.Add(time.Millisecond) },
		"source":          func(o *CandleImportPageObservation) { o.Page.Source = "other" },
		"category":        func(o *CandleImportPageObservation) { o.Page.Category = "inverse" },
		"env":             func(o *CandleImportPageObservation) { o.Page.Environment = "mainnet" },
		"endpoint":        func(o *CandleImportPageObservation) { o.Page.Endpoint += "#q" },
		"epoch clock":     func(o *CandleImportPageObservation) { o.Page.ServerTime = time.UnixMilli(0) },
		"submillis clock": func(o *CandleImportPageObservation) { o.Page.ServerTime = o.Page.ServerTime.Add(time.Microsecond) },
		"nil list":        func(o *CandleImportPageObservation) { o.Page.Candles = nil },
		"oversize":        func(o *CandleImportPageObservation) { o.Page.Candles = make([]model.Candle, 201) },
		"short valid":     func(o *CandleImportPageObservation) { o.Page = importAdvanceOpens(o.Page, first) },
		"range":           func(o *CandleImportPageObservation) { o.Page.Candles[1].OpenTime = key.LowerOpen.Add(-24 * time.Hour) },
		"duplicate":       func(o *CandleImportPageObservation) { o.Page.Candles[1] = o.Page.Candles[0] },
		"symbol row":      func(o *CandleImportPageObservation) { o.Page.Candles[1].Symbol = "ETHUSDT" },
		"interval row":    func(o *CandleImportPageObservation) { o.Page.Candles[1].IntervalCode = "60" },
		"false flag":      func(o *CandleImportPageObservation) { o.Page.Candles[1].Confirmed = false },
		"price":           func(o *CandleImportPageObservation) { o.Page.Candles[1].Open = model.Zero() },
		"ascending": func(o *CandleImportPageObservation) {
			o.Page.Candles[0], o.Page.Candles[1] = o.Page.Candles[1], o.Page.Candles[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			obs := blockImportObservation(key, prior, importAdvanceOpens(page, first, key.LowerOpen))
			change(obs)
			requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockGap, obs, ErrCandleArgument)
			importAdvanceDurable(t, db, prior, 0, time.Time{})
		})
	}
	empty := blockImportObservation(key, prior, importAdvanceOpens(page))
	gap := blockImportObservation(key, prior, importAdvanceOpens(page, first, key.LowerOpen))
	for _, reason := range []CandleImportBlockReason{CandleImportBlockGap, CandleImportBlockUnconfirmed, "invented", CandleImportBlockSourceBoundary} {
		requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, reason, empty, ErrCandleArgument)
	}
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockEmpty, gap, ErrCandleArgument)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockEmpty, nil, ErrCandleArgument)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockSourceBoundary, nil, ErrCandleArgument)
	empty.Symbol = "ETHUSDT"
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockEmpty, empty, ErrCandleArgument)
}

// TestBlockCandleImportState проверяет отказ для завершённого задания и переполнения revision.
func TestBlockCandleImportState(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	full := importAdvanceOpens(page, page.Candles[0].OpenTime, key.LowerOpen)
	tx := candleWriterTx(t, db)
	complete, err := AdvanceCandleImportPage(ctx, tx, key, prior, full)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, complete, CandleImportBlockEmpty, nil, ErrCandleImportState)
	importAdvanceDurable(t, db, complete, 2, page.ServerTime)
	overflow := CandleImportCheckpoint{math.MaxInt64, key.LowerOpen.Add(24*time.Hour - time.Millisecond), "progress"}
	_, err = db.Exec("UPDATE momentum.candle_import_progress SET state='progress',revision=$1,next_end=$2", overflow.Revision, overflow.NextEnd)
	migrationResult(t, err, nil)
	obs := blockImportObservation(key, overflow, importAdvanceOpens(page))
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, overflow, CandleImportBlockEmpty, obs, ErrCandleImportState)
	importAdvanceDurable(t, db, overflow, 2, page.ServerTime)
}

// TestBlockCandleImportFailures проверяет типизацию, отсутствие raw SQL и откат caller marker.
func TestBlockCandleImportFailures(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	obs := blockImportObservation(key, prior, importAdvanceOpens(page))
	requireImportBlockFailure(t, ctx, nil, key, prior, CandleImportBlockEmpty, obs, ErrCandleTx)
	for _, options := range []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelReadCommitted, ReadOnly: true}} {
		requireImportBlockFailure(t, ctx, candleWriterTx(t, db, options), key, prior, CandleImportBlockEmpty, obs, ErrCandleTx)
	}
	missing := key
	missing.Stream.Environment = "mainnet"
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), missing, prior, CandleImportBlockEmpty, obs, ErrCandleImportMissing)
	call, cancel := context.WithCancel(ctx)
	cancel()
	requireImportBlockFailure(t, call, candleWriterTx(t, db), key, prior, CandleImportBlockEmpty, obs, context.Canceled)
	marketStatement(t, db, `CREATE FUNCTION momentum.reject_block() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END $$;
	 CREATE TRIGGER reject_block BEFORE UPDATE ON momentum.candle_import_progress
	 FOR EACH ROW EXECUTE FUNCTION momentum.reject_block()`, true)
	tx := candleWriterTx(t, db)
	marketStatement(t, tx, "INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES('testnet','rollback-marker','bybit_v5','linear')", true)
	requireImportBlockFailure(t, ctx, tx, key, prior, CandleImportBlockEmpty, obs, ErrCandles)
	migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE dataset_id='rollback-marker'", "0")
	importAdvanceDurable(t, db, prior, 0, time.Time{})
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check", true)
	marketStatement(t, db, "UPDATE momentum.datasets SET source='other' WHERE environment='testnet'", true)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, prior, CandleImportBlockEmpty, obs, ErrCandleDataset)
}

// TestBlockCandleImportNamespaceEpoch проверяет независимые среды и транспорт у epoch.
func TestBlockCandleImportNamespaceEpoch(t *testing.T) {
	db, key, pending, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO momentum.candle_import_progress
	 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
	 SELECT 'mainnet',dataset_id,symbol,interval_code,lower_open,upper_open,next_end
	 FROM momentum.candle_import_progress WHERE environment='testnet'`)
	migrationResult(t, err, nil)
	mainnet := key
	mainnet.Stream.Environment = "mainnet"
	observation := blockImportObservation(mainnet, pending, page)
	observation.Page.Environment, observation.Page.Endpoint = "mainnet", "https://api.bybit.com/v5/market/kline"
	observation.Page.Candles = make([]model.Candle, 0)
	tx := candleWriterTx(t, db)
	got, err := BlockCandleImport(ctx, tx, mainnet, pending, CandleImportBlockEmpty, observation)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	if got.State != "blocked" {
		t.Fatalf("mainnet checkpoint: %+v", got)
	}
	migrationValue(t, db, "SELECT state FROM momentum.candle_import_progress WHERE environment='testnet'", "pending")

	key.LowerOpen = time.Unix(0, 0).UTC()
	_, err = db.Exec("UPDATE momentum.candle_import_progress SET lower_open=$1 WHERE environment='testnet'", key.LowerOpen)
	migrationResult(t, err, nil)
	epochObservation := blockImportObservation(key, pending, page)
	epochObservation.Page.Candles = make([]model.Candle, 0)
	requireImportBlockFailure(t, ctx, candleWriterTx(t, db), key, pending, CandleImportBlockEmpty, epochObservation, ErrCandleArgument)
	epoch, err := BlockCandleImport(ctx, candleWriterTx(t, db), key, pending, CandleImportBlockSourceBoundary, nil)
	migrationResult(t, err, nil)
	if epoch.State != "blocked" || !epoch.NextEnd.Equal(pending.NextEnd) {
		t.Fatalf("epoch checkpoint: %+v", epoch)
	}
}

// TestBlockCandleImportConcurrent наблюдает конкуренцию с успешным продвижением.
func TestBlockCandleImportConcurrent(t *testing.T) {
	for _, action := range []string{"advance commits", "block waits"} {
		t.Run(action, func(t *testing.T) {
			db, key, prior, page := importAdvanceFixture(t, "D", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dsn, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
			migrationResult(t, err, nil)
			dsn.Path, dsn.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
			other, err := OpenMomentumPostgres(ctx, dsn.String())
			migrationResult(t, err, nil)
			defer other.Close()
			winner, contender := candleWriterTx(t, db), candleWriterTx(t, other)
			next, err := AdvanceCandleImportPage(ctx, winner, key, prior, page)
			migrationResult(t, err, nil)
			var pid int
			migrationResult(t, contender.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			call, stop := context.WithCancel(ctx)
			defer stop()
			type outcome struct {
				checkpoint CandleImportCheckpoint
				err        error
			}
			obs := blockImportObservation(key, prior, importAdvanceOpens(page))
			done := make(chan outcome, 1)
			go func() {
				got, err := BlockCandleImport(call, contender, key, prior, CandleImportBlockEmpty, obs)
				done <- outcome{got, err}
			}()
			for waiting := false; !waiting; time.Sleep(5 * time.Millisecond) {
				migrationResult(t, db.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting), nil)
			}
			if action == "advance commits" {
				migrationResult(t, winner.Commit(), nil)
				got := <-done
				importAdvanceFailure(t, contender, got.checkpoint, got.err, ErrCandleImportStale)
				importAdvanceDurable(t, db, next, 1, page.ServerTime)
			} else {
				stop()
				got := <-done
				importAdvanceFailure(t, contender, got.checkpoint, got.err, context.Canceled)
				migrationResult(t, winner.Rollback(), nil)
				importAdvanceDurable(t, db, prior, 0, time.Time{})
			}
		})
	}
}
