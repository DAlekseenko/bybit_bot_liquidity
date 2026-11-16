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

// importAdvanceFixture создаёт окно нужной ширины и точную страницу из одной свечи.
func importAdvanceFixture(t *testing.T, interval string, buckets int) (*sql.DB, CandleImportWindow, CandleImportCheckpoint, model.CandlePage) {
	t.Helper()
	db, stream, page := candleWriterFixture(t)
	stream.IntervalCode, page.Candles[0].IntervalCode = interval, interval
	step, err := candleKey(stream)
	migrationResult(t, err, nil)
	latest, width := page.Candles[0].OpenTime, time.Duration(step)*time.Millisecond
	key := CandleImportWindow{stream, latest.Add(-time.Duration(buckets-1) * width), latest.Add(width)}
	page.ServerTime = key.UpperOpen.Add(137 * time.Millisecond)
	initial := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}
	_, err = db.Exec(`INSERT INTO momentum.candle_import_progress
	 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		stream.Environment, stream.DatasetID, stream.Symbol, interval, key.LowerOpen, key.UpperOpen, initial.NextEnd)
	migrationResult(t, err, nil)
	return db, key, initial, page
}

// importAdvanceDurable проверяет сохранённые checkpoint, свечи и точный source clock.
func importAdvanceDurable(t *testing.T, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, want CandleImportCheckpoint, rows int, sourceTime time.Time) CandleImportCheckpoint {
	t.Helper()
	var got CandleImportCheckpoint
	var count int
	var observed sql.NullTime
	err := reader.QueryRowContext(context.Background(), `SELECT revision,next_end,state,last_server_time,(SELECT count(*) FROM momentum.candles)
	 FROM momentum.candle_import_progress`).Scan(&got.Revision, &got.NextEnd, &got.State, &observed, &count)
	migrationResult(t, err, nil)
	if got.Revision != want.Revision || got.State != want.State || !got.NextEnd.Equal(want.NextEnd) || count != rows ||
		observed.Valid != !sourceTime.IsZero() || (observed.Valid && !observed.Time.Equal(sourceTime)) {
		t.Fatalf("unexpected durable state: %+v, rows=%d, source=%+v", got, count, observed)
	}
	return got
}

// importAdvanceFailure проверяет и нулевой checkpoint, и откат транзакции.
func importAdvanceFailure(t *testing.T, tx *sql.Tx, got CandleImportCheckpoint, err, want error) {
	t.Helper()
	if got != (CandleImportCheckpoint{}) {
		t.Fatalf("failure returned checkpoint: %+v", got)
	}
	requireCandleWriteFailure(t, tx, err, want)
}

// importAdvanceMustFail выполняет запись с ожидаемым отказом.
func importAdvanceMustFail(t *testing.T, tx *sql.Tx, ctx context.Context, key CandleImportWindow, expected CandleImportCheckpoint, page model.CandlePage, want error) {
	t.Helper()
	got, err := AdvanceCandleImportPage(ctx, tx, key, expected, page)
	importAdvanceFailure(t, tx, got, err, want)
}

// importAdvanceOpens строит независимую страницу с нужными timestamps и прежним provenance.
func importAdvanceOpens(page model.CandlePage, opens ...time.Time) model.CandlePage {
	row := page.Candles[0]
	page.Candles = make([]model.Candle, 0, len(opens))
	for _, open := range opens {
		row.OpenTime = open
		page.Candles = append(page.Candles, row)
	}
	return page
}

// TestAdvanceCandleImportPageCommit проверяет durable resume на другом физическом соединении.
func TestAdvanceCandleImportPageCommit(t *testing.T) {
	for _, interval := range []string{"D", "60"} {
		t.Run(interval, func(t *testing.T) {
			db, key, pending, page := importAdvanceFixture(t, interval, 2)
			ctx := context.Background()
			observer, err := db.Conn(ctx)
			migrationResult(t, err, nil)
			defer observer.Close()
			zone := time.FixedZone("fixture", 3*60*60)
			key.LowerOpen, key.UpperOpen = key.LowerOpen.In(zone), key.UpperOpen.In(zone)
			pending.NextEnd = pending.NextEnd.In(zone)
			page.ServerTime = page.ServerTime.In(zone)
			page = importAdvanceOpens(page, page.Candles[0].OpenTime.In(zone))
			source, input := page.ServerTime, page.Candles[0]
			part := CandleImportCheckpoint{1, input.OpenTime.UTC().Add(-time.Millisecond), "progress"}
			for _, commit := range []bool{false, true} {
				tx := candleWriterTx(t, db)
				got, err := AdvanceCandleImportPage(ctx, tx, key, pending, page)
				migrationResult(t, err, nil)
				if got != part || page.ServerTime != source || page.Candles[0] != input {
					t.Fatalf("first checkpoint or input: %+v", got)
				}
				importAdvanceDurable(t, observer, pending, 0, time.Time{})
				if commit {
					migrationResult(t, tx.Commit(), nil)
				} else {
					migrationResult(t, tx.Rollback(), nil)
					importAdvanceDurable(t, observer, pending, 0, time.Time{})
				}
			}
			durable := importAdvanceDurable(t, observer, part, 1, source)
			tx := candleWriterTx(t, db)
			importAdvanceMustFail(t, tx, ctx, key, pending, page, ErrCandleImportStale)
			lower := importAdvanceOpens(page, key.LowerOpen)
			tx = candleWriterTx(t, db)
			migrationResult(t, AppendCandlePage(ctx, tx, key.Stream, lower), nil)
			migrationResult(t, tx.Commit(), nil)
			const lowerXmin = "SELECT xmin::text FROM momentum.candles WHERE open_time=(SELECT lower_open FROM momentum.candle_import_progress)"
			xmin := migrationValue(t, db, lowerXmin)
			tx = candleWriterTx(t, db)
			complete, err := AdvanceCandleImportPage(ctx, tx, key, durable, lower)
			migrationResult(t, err, nil)
			want := CandleImportCheckpoint{2, key.LowerOpen.UTC().Add(-time.Millisecond), "complete"}
			if complete != want {
				t.Fatalf("final checkpoint: %+v", complete)
			}
			migrationResult(t, tx.Commit(), nil)
			migrationValue(t, db, lowerXmin, xmin)
			importAdvanceDurable(t, observer, want, 2, source)
			importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, complete, lower, ErrCandleImportState)
		})
	}
}

// TestAdvanceCandleImportPageRollback проверяет плохие страницы и SQL сбой после вставки.
func TestAdvanceCandleImportPageRollback(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 3)
	ctx := context.Background()
	const day = 24 * time.Hour
	first, middle, lower := page.Candles[0].OpenTime, key.LowerOpen.Add(day), key.LowerOpen
	unconfirmed := importAdvanceOpens(page, first)
	unconfirmed.Candles[0].Confirmed = false
	early, oversized := page, page
	early.ServerTime, oversized.Candles = key.UpperOpen.Add(-time.Millisecond), make([]model.Candle, 201)
	for _, tc := range []struct {
		name  string
		input model.CandlePage
	}{
		{"empty", importAdvanceOpens(page)},
		{"gap", importAdvanceOpens(page, first, lower)},
		{"future", importAdvanceOpens(page, key.UpperOpen)},
		{"duplicate", importAdvanceOpens(page, first, first)},
		{"reordered", importAdvanceOpens(page, first, middle, lower, middle)},
		{"below lower", importAdvanceOpens(page, first, middle, lower, lower.Add(-day))},
		{"unconfirmed", unconfirmed},
		{"source clock before cutoff", early},
		{"oversize", oversized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, prior, tc.input, ErrCandleArgument)
			importAdvanceDurable(t, db, prior, 0, time.Time{})
		})
	}
	marketStatement(t, db, `CREATE FUNCTION momentum.import_reject() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END $$;
	 CREATE TRIGGER import_reject BEFORE UPDATE ON momentum.candle_import_progress
	 FOR EACH ROW EXECUTE FUNCTION momentum.import_reject()`, true)
	importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, prior, page, ErrCandles)
	importAdvanceDurable(t, db, prior, 0, time.Time{})
}

// TestAdvanceCandleImportPageCheckpoint проверяет CAS, терминальные состояния и режим транзакции.
func TestAdvanceCandleImportPageCheckpoint(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	for _, expected := range []CandleImportCheckpoint{
		{1, prior.NextEnd, prior.State}, {prior.Revision, prior.NextEnd.Add(-24 * time.Hour), prior.State}, {prior.Revision, prior.NextEnd, "progress"},
	} {
		importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, expected, page, ErrCandleImportStale)
	}
	cursor := key.LowerOpen.Add(24*time.Hour - time.Millisecond)
	for _, state := range []CandleImportCheckpoint{{math.MaxInt64, cursor, "progress"}, {1, cursor, "blocked"}} {
		_, err := db.Exec("UPDATE momentum.candle_import_progress SET revision=$1,state=$2,next_end=$3,reason=CASE WHEN $2='blocked' THEN 'gap' ELSE '' END,last_server_time=upper_open", state.Revision, state.State, state.NextEnd)
		migrationResult(t, err, nil)
		importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, state, page, ErrCandleImportState)
	}
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET revision=0,state='pending',next_end=upper_open-INTERVAL '1 millisecond',reason='',last_server_time=NULL", true)
	missing := key
	missing.Stream.Environment = "mainnet"
	importAdvanceMustFail(t, candleWriterTx(t, db), ctx, missing, prior, page, ErrCandleImportMissing)
	for _, opt := range []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelReadCommitted, ReadOnly: true}} {
		importAdvanceMustFail(t, candleWriterTx(t, db, opt), ctx, key, prior, page, ErrCandleTx)
	}
	call, cancel := context.WithCancel(ctx)
	cancel()
	importAdvanceMustFail(t, candleWriterTx(t, db), call, key, prior, page, context.Canceled)
	importAdvanceDurable(t, db, prior, 0, time.Time{})
}

// TestAdvanceCandleImportPageArguments проверяет неверные ключи и независимо неверные курcоры.
func TestAdvanceCandleImportPageArguments(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	for _, invalid := range []CandleImportWindow{
		{CandleStream{Environment: "bad"}, key.LowerOpen, key.UpperOpen},
		{key.Stream, time.Unix(-86400, 0), key.UpperOpen},
		{key.Stream, key.LowerOpen, key.LowerOpen},
		{key.Stream, key.LowerOpen, key.UpperOpen.Add(time.Millisecond)},
	} {
		importAdvanceMustFail(t, candleWriterTx(t, db), ctx, invalid, prior, page, ErrCandleArgument)
		importAdvanceDurable(t, db, prior, 0, time.Time{})
	}
	for _, invalid := range []CandleImportCheckpoint{
		{-1, prior.NextEnd, prior.State},
		{prior.Revision, prior.NextEnd.Add(time.Microsecond), prior.State},
		{prior.Revision, key.UpperOpen.Add(24*time.Hour - time.Millisecond), prior.State},
		{prior.Revision, key.LowerOpen.Add(-24*time.Hour - time.Millisecond), prior.State},
	} {
		importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, invalid, page, ErrCandleArgument)
		importAdvanceDurable(t, db, prior, 0, time.Time{})
	}
	importAdvanceMustFail(t, nil, ctx, key, prior, page, ErrCandleTx)
}

// TestAdvanceCandleImportPageWriterFailure проверяет частичную запись перед конфликтом.
func TestAdvanceCandleImportPageWriterFailure(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "D", 2)
	ctx := context.Background()
	lower := importAdvanceOpens(page, key.LowerOpen)
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendCandlePage(ctx, tx, key.Stream, lower), nil)
	migrationResult(t, tx.Commit(), nil)
	lower.Candles[0].Volume = lower.Candles[0].Open
	page.Candles = append(page.Candles, lower.Candles[0])
	importAdvanceMustFail(t, candleWriterTx(t, db), ctx, key, prior, page, ErrCandleConflict)
	migrationValue(t, db, "SELECT revision FROM momentum.candle_import_progress", "0")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles WHERE volume = 0", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "1")
}

// TestAdvanceCandleImportPageConcurrent наблюдает блокировку job и проверяет stale/deadline.
func TestAdvanceCandleImportPageConcurrent(t *testing.T) {
	for _, mode := range []string{"stale", "deadline"} {
		t.Run(mode, func(t *testing.T) {
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
			var pid int
			migrationResult(t, contender.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			next, err := AdvanceCandleImportPage(ctx, winner, key, prior, page)
			migrationResult(t, err, nil)
			call, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			type outcome struct {
				checkpoint CandleImportCheckpoint
				err        error
			}
			result := make(chan outcome, 1)
			go func() {
				checkpoint, err := AdvanceCandleImportPage(call, contender, key, prior, page)
				result <- outcome{checkpoint, err}
			}()
			for waiting := false; !waiting; time.Sleep(5 * time.Millisecond) {
				migrationResult(t, db.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting), nil)
			}
			if mode == "stale" {
				migrationResult(t, winner.Commit(), nil)
				got := <-result
				importAdvanceFailure(t, contender, got.checkpoint, got.err, ErrCandleImportStale)
				importAdvanceDurable(t, db, next, 1, page.ServerTime)
			} else {
				got := <-result
				importAdvanceFailure(t, contender, got.checkpoint, got.err, context.DeadlineExceeded)
				migrationResult(t, winner.Rollback(), nil)
				importAdvanceDurable(t, db, prior, 0, time.Time{})
			}
		})
	}
}
