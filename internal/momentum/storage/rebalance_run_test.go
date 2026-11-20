package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
)

// runRepositoryConfig создаёт проверенный исходный конфиг без изменения его defaults.
func runRepositoryConfig(t *testing.T, now bool) config.Config {
	t.Helper()
	b, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	migrationResult(t, err, nil)
	if now {
		b = bytes.Replace(b, []byte("start_policy: next_monday"), []byte("start_policy: now"), 1)
	}
	c, err := config.Parse(b)
	migrationResult(t, err, nil)
	return c
}

// runRepositoryFixture создаёт disposable схему и точного кандидата.
func runRepositoryFixture(t *testing.T) (*sql.DB, RebalanceRunInput) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	c := runRepositoryConfig(t, false)
	return db, RebalanceRunInput{RunID: " Run/α ", AccountID: " Account/β ", StrategyID: c.Values().StrategyID, ScheduledAt: time.Unix(1700000000, 123456000).UTC(), Config: c}
}

// requireRunRepositoryFailure проверяет sentinel, нулевой результат и безопасную диагностику.
func requireRunRepositoryFailure(t *testing.T, ctx context.Context, db *sql.DB, in RebalanceRunInput, want error) {
	t.Helper()
	got, err := CreateOrReadRebalanceRun(ctx, db, in)
	if !reflect.ValueOf(got).IsZero() || !errors.Is(err, ErrRebalanceRun) || !errors.Is(err, want) {
		t.Fatalf("unexpected result/error: %#v %v", got, err)
	}
	for _, marker := range []string{"pq:", "postgres://", "secret-marker"} {
		if strings.Contains(err.Error(), marker) {
			t.Fatal("unsafe diagnostic")
		}
	}
}

// TestRebalanceRunRepository проверяет исходную identity/config и scoped repeat.
func TestRebalanceRunRepository(t *testing.T) {
	db, in := runRepositoryFixture(t)
	first, err := CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	payload, hash, _ := in.Config.Snapshot()
	if first.RunID != in.RunID || first.AccountID != in.AccountID || !first.ScheduledAt.Equal(in.ScheduledAt) || first.ConfigHash != hash || !bytes.Equal(first.ConfigPayload, payload) || first.CreatedAt.Location() != time.UTC || first.ScheduledAt.Location() != time.UTC || first.CreatedAt.IsZero() {
		t.Fatal("initial run changed")
	}
	migrationValue(t, db, "SELECT bool_and(isfinite(created_at) AND created_at >= 'epoch'::timestamptz) FROM momentum.rebalance_runs", "true")
	original := migrationValue(t, db, "SELECT xmin::text FROM momentum.rebalance_runs")
	in.RunID, in.Config = "new-candidate", runRepositoryConfig(t, true)
	in.ScheduledAt = in.ScheduledAt.In(time.FixedZone("east", 10800))
	again, err := CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	if !reflect.DeepEqual(first, again) {
		t.Fatal("weekly retry replaced original identity/config")
	}
	migrationValue(t, db, "SELECT xmin::text FROM momentum.rebalance_runs", original)
	again.ConfigPayload[0] = 0
	again, err = CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	if !bytes.Equal(again.ConfigPayload, payload) {
		t.Fatal("output shared bytes")
	}
	in.RunID, in.OnboardingID = "onboard", " Onboard/α "
	onboard, err := CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	in.RunID, in.ScheduledAt, in.Config = "retry-now", in.ScheduledAt.Add(time.Hour), runRepositoryConfig(t, false)
	resumed, err := CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	if !reflect.DeepEqual(onboard, resumed) {
		t.Fatal("onboarding retry replaced original instant/config")
	}
	in.ScheduledAt = first.ScheduledAt
	in.RunID, in.OnboardingID, in.AccountID = "other-scope", "", "different-account"
	_, err = CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	in.RunID, in.AccountID = "other-strategy", first.AccountID
	variant, _, _ := in.Config.Snapshot()
	variant = bytes.Replace(variant, []byte("momentum-v1"), []byte("momentum-v2"), 1)
	in.Config, err = config.Parse(variant)
	migrationResult(t, err, nil)
	in.StrategyID = in.Config.Values().StrategyID
	_, err = CreateOrReadRebalanceRun(context.Background(), db, in)
	migrationResult(t, err, nil)
	in.Config, in.StrategyID = first.Config, first.StrategyID
	in.RunID, in.AccountID = first.RunID, "collision-account"
	requireRunRepositoryFailure(t, context.Background(), db, in, ErrRebalanceRunCollision)
	in.AccountID, in.ScheduledAt = first.AccountID, first.ScheduledAt
	in.RunID = "other-scope"
	requireRunRepositoryFailure(t, context.Background(), db, in, ErrRebalanceRunCollision)
	migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "4")
}

// TestRebalanceRunRepositoryArguments проверяет аргументы без зависимости от PostgreSQL.
func TestRebalanceRunRepositoryArguments(t *testing.T) {
	c := runRepositoryConfig(t, false)
	base := RebalanceRunInput{RunID: "run", AccountID: "account", StrategyID: c.Values().StrategyID, ScheduledAt: time.Unix(1700000000, 0), Config: c}
	db, err := sql.Open("postgres", "")
	migrationResult(t, err, nil)
	defer db.Close()
	for name, change := range map[string]func(*RebalanceRunInput){
		"empty-account": func(in *RebalanceRunInput) { in.AccountID = "" },
		"nul-onboard":   func(in *RebalanceRunInput) { in.OnboardingID = "on\x00board" },
		"blank-run":     func(in *RebalanceRunInput) { in.RunID = " \t\n\r\f\v" },
		"utf8-account":  func(in *RebalanceRunInput) { in.AccountID = string([]byte{255}) },
		"nul-run":       func(in *RebalanceRunInput) { in.RunID += "\x00" },
		"blank-onboard": func(in *RebalanceRunInput) { in.OnboardingID = "\t" },
		"nanosecond":    func(in *RebalanceRunInput) { in.ScheduledAt = in.ScheduledAt.Add(time.Nanosecond) },
		"pre-epoch":     func(in *RebalanceRunInput) { in.ScheduledAt = time.Unix(-1, 0) },
		"overflow-year": func(in *RebalanceRunInput) { in.ScheduledAt = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC) },
		"strategy":      func(in *RebalanceRunInput) { in.StrategyID = "other" },
		"zero-config":   func(in *RebalanceRunInput) { in.Config = config.Config{} },
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			change(&in)
			requireRunRepositoryFailure(t, context.Background(), db, in, ErrRebalanceRunArgument)
		})
	}
	requireRunRepositoryFailure(t, nil, db, base, ErrRebalanceRunArgument)
	requireRunRepositoryFailure(t, context.Background(), nil, base, ErrRebalanceRunArgument)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requireRunRepositoryFailure(t, ctx, db, base, context.Canceled)
	migrationResult(t, db.Close(), nil)
	requireRunRepositoryFailure(t, context.Background(), db, base, ErrRebalanceRun)
}

// TestRebalanceRunRepositoryTimeBoundary проверяет epoch и верхнюю supported микросекунду.
func TestRebalanceRunRepositoryTimeBoundary(t *testing.T) {
	db, base := runRepositoryFixture(t)
	for _, at := range []time.Time{time.Unix(0, 0), time.Date(294276, 12, 31, 23, 59, 59, 999999000, time.UTC)} {
		in := base
		in.RunID, in.ScheduledAt = at.String(), at
		got, err := CreateOrReadRebalanceRun(context.Background(), db, in)
		migrationResult(t, err, nil)
		if !got.ScheduledAt.Equal(at) {
			t.Fatal("boundary instant changed")
		}
	}
}

// TestRebalanceRunRepositoryCorrupt проверяет semantic config и canonical bytes сохранённого run.
func TestRebalanceRunRepositoryCorrupt(t *testing.T) {
	for _, malformed := range []string{"invalid-json", "unknown-field", "noncanonical", "wrong-strategy"} {
		db, in := runRepositoryFixture(t)
		payload, _, _ := in.Config.Snapshot()
		switch malformed {
		case "invalid-json":
			payload = []byte(`{`)
		case "unknown-field":
			payload = []byte(`{"secret-marker":1}`)
		case "noncanonical":
			payload = append(payload, '\n')
		case "wrong-strategy":
			payload = bytes.Replace(payload, []byte("momentum-v1"), []byte("different"), 1)
		}
		_, err := db.Exec("INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256($1::bytea),$1)", payload)
		migrationResult(t, err, nil)
		_, err = db.Exec("INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash) VALUES($1,$2,$3,$4,sha256($5::bytea))", in.RunID, in.AccountID, in.StrategyID, in.ScheduledAt, payload)
		migrationResult(t, err, nil)
		requireRunRepositoryFailure(t, context.Background(), db, in, ErrRebalanceRunCorrupt)
		migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
		migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "1")
		var unchanged []byte
		migrationResult(t, db.QueryRow("SELECT payload FROM momentum.config_snapshots").Scan(&unchanged), nil)
		if !bytes.Equal(unchanged, payload) {
			t.Fatal("corrupt original replaced")
		}
	}
}

// TestRebalanceRunRepositoryConcurrent проверяет отдельные pools и реальный конфликт до commit/rollback/cancel.
func TestRebalanceRunRepositoryConcurrent(t *testing.T) {
	for _, onboarding := range []bool{false, true} {
		for _, outcome := range []string{"commit", "rollback", "cancel"} {
			db, in := runRepositoryFixture(t)
			if onboarding {
				in.OnboardingID = "onboard"
			}
			payload, hash, _ := in.Config.Snapshot()
			key, _ := hex.DecodeString(hash)
			_, err := db.Exec("INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
			migrationResult(t, err, nil)
			holder, err := db.BeginTx(context.Background(), nil)
			migrationResult(t, err, nil)
			defer holder.Rollback()
			_, err = holder.Exec("INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,onboarding_id,config_hash) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)", in.RunID, in.AccountID, in.StrategyID, in.ScheduledAt, in.OnboardingID, key)
			migrationResult(t, err, nil)
			u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
			u.Path = "/" + migrationValue(t, db, "SELECT current_database()")
			second, err := OpenMomentumPostgres(context.Background(), u.String())
			migrationResult(t, err, nil)
			defer second.Close()
			candidate := in
			candidate.RunID, candidate.Config = "concurrent", runRepositoryConfig(t, true)
			if onboarding {
				candidate.ScheduledAt = candidate.ScheduledAt.Add(time.Hour)
			}
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			type result struct {
				stored StoredRebalanceRun
				err    error
			}
			results := make(chan result, 1)
			go func() {
				stored, err := CreateOrReadRebalanceRun(ctx, second, candidate)
				results <- result{stored, err}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				waiting := migrationValue(t, db, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND cardinality(pg_blocking_pids(a.pid))>0)")
				if waiting == "true" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("duplicate never reached real PostgreSQL lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch outcome {
			case "commit":
				migrationResult(t, holder.Commit(), nil)
			case "rollback":
				migrationResult(t, holder.Rollback(), nil)
			case "cancel":
				stop()
			}
			got := <-results
			if outcome == "cancel" {
				if !errors.Is(got.err, context.Canceled) || !reflect.ValueOf(got.stored).IsZero() {
					t.Fatal("cancel returned run")
				}
				migrationResult(t, holder.Rollback(), nil)
				migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "0")
				migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
				continue
			}
			migrationResult(t, got.err, nil)
			want := in.RunID
			if outcome == "rollback" {
				want = candidate.RunID
				_, hash, _ = candidate.Config.Snapshot()
			}
			if got.stored.RunID != want || got.stored.ConfigHash != hash {
				t.Fatal("concurrent run/config changed")
			}
			migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "1")
		}
	}
}

// TestRebalanceRunRepositoryParallel проверяет единый результат восьми одновременных API calls.
func TestRebalanceRunRepositoryParallel(t *testing.T) {
	db, base := runRepositoryFixture(t)
	type result struct {
		stored StoredRebalanceRun
		err    error
	}
	start, results := make(chan struct{}), make(chan result, 8)
	for i := 0; i < 8; i++ {
		in := base
		in.RunID = string(rune('a' + i))
		go func() {
			<-start
			stored, err := CreateOrReadRebalanceRun(context.Background(), db, in)
			results <- result{stored, err}
		}()
	}
	close(start)
	first := <-results
	migrationResult(t, first.err, nil)
	for i := 1; i < 8; i++ {
		got := <-results
		migrationResult(t, got.err, nil)
		if !reflect.DeepEqual(got.stored, first.stored) {
			t.Fatal("parallel duplicate returned another run/config")
		}
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "1")
}
