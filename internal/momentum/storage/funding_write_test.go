package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// fundingWriterFixture создаёт два namespace и нерегулярные signed события.
func fundingWriterFixture(t *testing.T) (*sql.DB, FundingStream, model.FundingPage) {
	t.Helper()
	ctx := context.Background()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	key := FundingStream{"testnet", "quoted' funding", "BTCUSDT"}
	for _, env := range []string{"testnet", "mainnet"} {
		migrationResult(t, RegisterDataset(ctx, db, env, key.DatasetID), nil)
	}
	page := model.FundingPage{Environment: key.Environment, Source: "bybit_v5", Category: "linear",
		Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Symbol: key.Symbol,
		StartTime: time.UnixMilli(0).UTC(), EndTime: time.UnixMilli(113).UTC(), ServerTime: time.UnixMilli(115).UTC()}
	for i, literal := range []string{"9007199254740993.123456789012345678", "-0.0000000000000000001", "0"} {
		rate, err := model.ParseRate(literal)
		migrationResult(t, err, nil)
		stamp := time.UnixMilli([]int64{113, 11, 0}[i]).In(time.FixedZone("offset", -3*3600))
		page.Events = append(page.Events, model.FundingEvent{FundingTime: stamp, Rate: rate})
	}
	return db, key, page
}

// requireFundingWriteFailure проверяет тип, закрытый tx и безопасный текст ошибки.
func requireFundingWriteFailure(t *testing.T, tx *sql.Tx, err, want error) {
	t.Helper()
	migrationResult(t, err, want)
	if !errors.Is(err, ErrFunding) {
		t.Fatalf("unexpected funding error: %v", err)
	}
	for _, forbidden := range []string{"sensitive_sql_marker", "secret-marker", "postgres://", "pq:"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("raw database diagnostic leaked")
		}
	}
	if tx != nil {
		migrationResult(t, tx.Commit(), sql.ErrTxDone)
	}
}

// nilFundingContext возвращает отсутствующий context для fail-closed проверки.
func nilFundingContext() context.Context { return nil }

// TestAppendFundingPageDurable проверяет точность, повтор без UPDATE и границы namespace.
func TestAppendFundingPageDurable(t *testing.T) {
	db, key, page := fundingWriterFixture(t)
	ctx := context.Background()
	original := page
	original.Events = append([]model.FundingEvent(nil), page.Events...)
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(ctx, tx, key, page), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding", "0")
	migrationResult(t, tx.Rollback(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding", "0")
	tx = candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(ctx, tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, "SELECT string_agg(((extract(epoch FROM funding_time)*1000)::bigint)::text,',' ORDER BY funding_time) FROM momentum.funding", "0,11,113")
	migrationValue(t, db, "SELECT string_agg(rate::text,',' ORDER BY funding_time) FROM momentum.funding", "0,-0.0000000000000000001,9007199254740993.123456789012345678")
	marketStatement(t, db, "UPDATE momentum.funding SET rate='0.00' WHERE rate=0", true)
	const xminSQL = "SELECT string_agg(xmin::text,',' ORDER BY funding_time) FROM momentum.funding"
	xmin := migrationValue(t, db, xminSQL)
	tx = candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(ctx, tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	if got := migrationValue(t, db, xminSQL); got != xmin || !reflect.DeepEqual(page, original) {
		t.Fatalf("repeat rewrote rows or input: %q versus %q", got, xmin)
	}
	migrationValue(t, db, "SELECT rate::text FROM momentum.funding WHERE rate=0", "0.00")
	key.Environment, page.Environment, page.Endpoint = "mainnet", "mainnet", "https://api.bybit.com/v5/market/funding/history"
	tx = candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(ctx, tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE environment='mainnet'", "3")
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE environment='testnet'", "3")
	otherDataset, otherSymbol := key, key
	otherDataset.DatasetID, otherSymbol.Symbol = "second dataset", "ETHUSDT"
	migrationResult(t, RegisterDataset(ctx, db, otherDataset.Environment, otherDataset.DatasetID), nil)
	for _, other := range []FundingStream{otherDataset, otherSymbol} {
		page.Symbol = other.Symbol
		tx := candleWriterTx(t, db)
		migrationResult(t, AppendFundingPage(ctx, tx, other, page), nil)
		migrationResult(t, tx.Commit(), nil)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE dataset_id='second dataset'", "3")
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE symbol='ETHUSDT'", "3")
}

// TestAppendFundingPageLimit проверяет максимальный вход без зависимости от частоты funding.
func TestAppendFundingPageLimit(t *testing.T) {
	db, key, page := fundingWriterFixture(t)
	page.EndTime, page.ServerTime = time.UnixMilli(1000).UTC(), time.UnixMilli(1001).UTC()
	page.Events = make([]model.FundingEvent, 200)
	for i := range page.Events {
		page.Events[i] = model.FundingEvent{FundingTime: time.UnixMilli(int64(199-i) * 5).UTC(), Rate: model.Zero()}
	}
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(context.Background(), tx, key, page), nil)
	migrationResult(t, tx.Commit(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE rate=0", "200")
}

// TestAppendFundingPageRollback проверяет откат всей операции после несовпадения и SQL ошибки.
func TestAppendFundingPageRollback(t *testing.T) {
	db, key, page := fundingWriterFixture(t)
	ctx := context.Background()
	first := page
	first.Events = page.Events[1:]
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendFundingPage(ctx, tx, key, first), nil)
	migrationResult(t, tx.Commit(), nil)
	xmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.funding WHERE rate<0")
	input := page
	input.Events = append([]model.FundingEvent(nil), page.Events...)
	input.Events[1].Rate = model.Zero()
	tx = candleWriterTx(t, db)
	_, err := tx.Exec("INSERT INTO momentum.datasets (environment,dataset_id,source,category) VALUES('testnet','rollback marker','bybit_v5','linear')")
	migrationResult(t, err, nil)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, input), ErrFundingConflict)
	migrationValue(t, db, "SELECT (SELECT count(*) FROM momentum.datasets WHERE dataset_id='rollback marker')::text||':'||(SELECT count(*) FROM momentum.funding)", "0:2")
	if migrationValue(t, db, "SELECT xmin::text FROM momentum.funding WHERE rate<0") != xmin {
		t.Fatal("conflict rewrote the winning row")
	}
	marketStatement(t, db, `CREATE FUNCTION momentum.funding_deny() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF NEW.rate=0 THEN RAISE EXCEPTION 'sensitive_sql_marker'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER funding_deny BEFORE INSERT ON momentum.funding
	 FOR EACH ROW EXECUTE FUNCTION momentum.funding_deny()`, true)
	tx = candleWriterTx(t, db)
	_, err = tx.Exec("INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES('testnet','sql marker','bybit_v5','linear')")
	migrationResult(t, err, nil)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, page), ErrFunding)
	migrationValue(t, db, "SELECT (SELECT count(*) FROM momentum.datasets WHERE dataset_id='sql marker')::text||':'||(SELECT count(*) FROM momentum.funding)", "0:2")
	marketStatement(t, db, `CREATE OR REPLACE FUNCTION momentum.funding_deny() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN RETURN NULL; END $$`, true)
	tx = candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, page), ErrFunding)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding", "2")
}

// TestAppendFundingPageRejections проверяет границы, provenance и варианты транзакции.
func TestAppendFundingPageRejections(t *testing.T) {
	db, key, page := fundingWriterFixture(t)
	ctx := context.Background()
	one, err := model.ParseRate("1")
	migrationResult(t, err, nil)
	three, err := model.ParseRate("3")
	migrationResult(t, err, nil)
	infiniteExpansion, err := one.Quo(three)
	migrationResult(t, err, nil)
	overlongRate, err := model.ParseRate(strings.Repeat("9", 127))
	overlongRate, mulErr := overlongRate.Mul(overlongRate)
	overlongRate, negErr := model.Zero().Sub(overlongRate)
	wire, wireErr := overlongRate.WireDecimal()
	migrationResult(t, errors.Join(err, mulErr, negErr, wireErr), nil)
	if _, parseErr := model.ParseRate(wire); parseErr == nil {
		t.Fatal("over-limit signed rate was accepted")
	}
	for name, change := range map[string]func(*model.FundingPage){
		"empty":                  func(p *model.FundingPage) { p.Events = []model.FundingEvent{} },
		"oversize":               func(p *model.FundingPage) { p.Events = make([]model.FundingEvent, 201) },
		"source":                 func(p *model.FundingPage) { p.Source = "foreign" },
		"category":               func(p *model.FundingPage) { p.Category = "inverse" },
		"endpoint":               func(p *model.FundingPage) { p.Endpoint += "?extra=1" },
		"env":                    func(p *model.FundingPage) { p.Environment = "mainnet" },
		"symbol":                 func(p *model.FundingPage) { p.Symbol = "ETHUSDT" },
		"range":                  func(p *model.FundingPage) { p.StartTime = p.EndTime.Add(time.Millisecond) },
		"zero-end":               func(p *model.FundingPage) { p.EndTime = p.StartTime },
		"zero-clock":             func(p *model.FundingPage) { p.ServerTime = p.StartTime },
		"start-epoch":            func(p *model.FundingPage) { p.StartTime = time.UnixMilli(-1) },
		"start-precision":        func(p *model.FundingPage) { p.StartTime = p.StartTime.Add(time.Nanosecond) },
		"end-precision":          func(p *model.FundingPage) { p.EndTime = p.EndTime.Add(time.Nanosecond) },
		"clock-precision":        func(p *model.FundingPage) { p.ServerTime = p.ServerTime.Add(time.Nanosecond) },
		"future":                 func(p *model.FundingPage) { p.ServerTime = p.EndTime.Add(-time.Millisecond) },
		"outside":                func(p *model.FundingPage) { p.EndTime = p.EndTime.Add(-time.Millisecond) },
		"precision":              func(p *model.FundingPage) { p.Events[1].FundingTime = p.Events[1].FundingTime.Add(time.Microsecond) },
		"duplicate":              func(p *model.FundingPage) { p.Events[1] = p.Events[0] },
		"unsorted":               func(p *model.FundingPage) { p.Events[1], p.Events[2] = p.Events[2], p.Events[1] },
		"rate":                   func(p *model.FundingPage) { p.Events[1].Rate = model.Decimal{} },
		"fraction":               func(p *model.FundingPage) { p.Events[1].Rate = infiniteExpansion },
		"over-limit signed rate": func(p *model.FundingPage) { p.Events[1].Rate = overlongRate },
	} {
		t.Run(name, func(t *testing.T) {
			input := page
			input.Events = append([]model.FundingEvent(nil), page.Events...)
			change(&input)
			tx := candleWriterTx(t, db)
			requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, input), ErrFundingArgument)
		})
	}
	missing := key
	missing.DatasetID = "unregistered"
	tx := candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, missing, page), ErrFundingDataset)
	missing.DatasetID = " not canonical"
	tx = candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, missing, page), ErrFundingArgument)
	for _, invalid := range []FundingStream{{"paper", "fixture", "BTCUSDT"}, {"testnet", "", "BTCUSDT"},
		{"testnet", "fixture ", "BTCUSDT"}, {"testnet", strings.Repeat("я", 129), "BTCUSDT"},
		{"testnet", "bad\x00id", "BTCUSDT"}, {"testnet", "bad\xffid", "BTCUSDT"},
		{"testnet", "fixture", "btcusdt"}, {"testnet", "fixture", "BTCUSDC"}} {
		tx := candleWriterTx(t, db)
		requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, invalid, page), ErrFundingArgument)
	}
	for _, opts := range []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelReadCommitted, ReadOnly: true}} {
		tx := candleWriterTx(t, db, opts)
		requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, page), ErrFundingTx)
	}
	requireFundingWriteFailure(t, nil, AppendFundingPage(ctx, nil, key, page), ErrFundingTx)
	tx = candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(nilFundingContext(), tx, key, page), ErrFundingArgument)
	call, cancel := context.WithCancel(ctx)
	cancel()
	tx = candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(call, tx, key, page), context.Canceled)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check", true)
	marketStatement(t, db, "UPDATE momentum.datasets SET source='other' WHERE environment='testnet'", true)
	tx = candleWriterTx(t, db)
	requireFundingWriteFailure(t, tx, AppendFundingPage(ctx, tx, key, page), ErrFundingDataset)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding", "0")
}

// TestAppendFundingPageConcurrent наблюдает блокировку и проверяет конфликт после Commit.
func TestAppendFundingPageConcurrent(t *testing.T) {
	for _, mode := range []string{"exact", "divergent", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			db, key, page := fundingWriterFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dsn, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
			migrationResult(t, err, nil)
			dsn.Path, dsn.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
			other, err := OpenMomentumPostgres(ctx, dsn.String())
			migrationResult(t, err, nil)
			t.Cleanup(func() { _ = other.Close() })
			winner, contender := candleWriterTx(t, db), candleWriterTx(t, other)
			var pid int
			migrationResult(t, contender.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			migrationResult(t, AppendFundingPage(ctx, winner, key, page), nil)
			if mode == "divergent" {
				event := page.Events[0]
				event.FundingTime = time.UnixMilli(123)
				page.EndTime, page.ServerTime = time.UnixMilli(123), time.UnixMilli(125)
				page.Events = []model.FundingEvent{event, {FundingTime: event.FundingTime.Add(-10 * time.Millisecond), Rate: model.Zero()}}
			}
			var call context.Context
			var stop context.CancelFunc
			if mode == "deadline" {
				call, stop = context.WithTimeout(ctx, 3*time.Second)
			} else {
				call, stop = context.WithCancel(ctx)
			}
			defer stop()
			result := make(chan error, 1)
			go func() { result <- AppendFundingPage(call, contender, key, page) }()
			for {
				var waiting bool
				err := db.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting)
				migrationResult(t, err, nil)
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if mode == "canceled" {
				stop()
				requireFundingWriteFailure(t, contender, <-result, context.Canceled)
			}
			if mode == "deadline" {
				requireFundingWriteFailure(t, contender, <-result, context.DeadlineExceeded)
			}
			migrationResult(t, winner.Commit(), nil)
			switch mode {
			case "exact":
				migrationResult(t, <-result, nil)
				migrationResult(t, contender.Commit(), nil)
			case "divergent":
				requireFundingWriteFailure(t, contender, <-result, ErrFundingConflict)
			}
			if got := migrationValue(t, db, `SELECT count(*)::text||':'||string_agg((extract(epoch FROM funding_time)*1000)::bigint||'='||rate::text,',' ORDER BY funding_time) FROM momentum.funding`); got != "3:0=0,11=-0.0000000000000000001,113=9007199254740993.123456789012345678" {
				t.Fatalf("winner payload or contender prefix leaked: %s", got)
			}
		})
	}
}
