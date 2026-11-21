package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// migrationValue читает scalar и при указанном ожидании сразу сверяет его.
func migrationValue(t *testing.T, db *sql.DB, query string, want ...string) string {
	t.Helper()
	var got string
	if err := db.QueryRowContext(context.Background(), query).Scan(&got); err != nil || len(want) != 0 && got != want[0] {
		t.Fatalf("query=%s got=%q want=%v err=%v", query, got, want, err)
	}
	return got
}

// migrationState сохраняет всю историю либо отсутствие новой схемы.
func migrationState(t *testing.T, db *sql.DB) string {
	t.Helper()
	if migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'") == "0" {
		return ""
	}
	return migrationValue(t, db, `SELECT coalesce(json_agg(r ORDER BY version)::text, '[]')
 FROM (SELECT version, encode(checksum,'hex') AS checksum, applied_at FROM momentum.schema_migrations) r`)
}

// migrationResult проверяет ожидаемую категорию и отсутствие сырых диагностик.
func migrationResult(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || regexp.MustCompile(`secret-marker|sensitive_sql_marker|postgres://|pq:`).MatchString(fmt.Sprint(err)) {
		t.Fatalf("got=%v want=%v", err, want)
	}
}

// TestMigrationTransactions проверяет реальную блокировку, два pool, bootstrap и повтор.
func TestMigrationTransactions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	migrationResult(t, MigrateMomentumPostgres(context.Background(), nil), ErrMigration)
	migrationResult(t, MigrateMomentumPostgres(ctx, nil), context.Canceled)
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), context.Canceled)
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'", "0")
	before := migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'")
	holder, err := db.BeginTx(context.Background(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(context.Background(), "SELECT pg_catalog.pg_advisory_xact_lock(651010)")
	migrationResult(t, err, nil)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = MigrateMomentumPostgres(ctx, db)
	migrationResult(t, err, ErrMigration)
	migrationResult(t, err, context.DeadlineExceeded)
	if time.Since(start) > 2*time.Second {
		t.Fatal("lock cancellation exceeded bound")
	}
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'", "0")
	migrationResult(t, holder.Rollback(), nil)
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")) // Исходный opt-in уже проверен helper.
	u.Path, u.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	db.SetMaxOpenConns(1) // Явный retry обязан получить обратно единственное соединение.
	second, err := OpenMomentumPostgres(context.Background(), u.String())
	migrationResult(t, err, nil)
	defer second.Close()
	retry, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	barrier, results := make(chan struct{}), make(chan error, 2)
	for _, pool := range []*sql.DB{db, second} {
		// Оба worker ожидают один барьер и используют независимые pools той же БД.
		go func(pool *sql.DB) {
			<-barrier
			results <- MigrateMomentumPostgres(retry, pool)
		}(pool)
	}
	close(barrier)
	for range 2 {
		migrationResult(t, <-results, nil)
	}
	checksum := sha256.Sum256([]byte(momentumBootstrap))
	migrationValue(t, db, `SELECT count(*)::text || ':' || min(version)::text || ':' || min(encode(checksum,'hex')) FILTER (WHERE version=1) || ':' ||
 bool_and(applied_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00' AND isfinite(applied_at))::text FROM momentum.schema_migrations`, fmt.Sprintf("%d:1:%x:true", 11, checksum))
	initial := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != initial || migrationState(t, second) != initial {
		t.Fatal("repeat or independent pool changed exact history")
	}
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'", before)
	migrationResult(t, db.Close(), nil)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), ErrMigration)
	unavailable, err := sql.Open("postgres", "postgres://u:secret-marker@127.0.0.1:1/momentum_test_unavailable?sslmode=disable&connect_timeout=1")
	migrationResult(t, err, nil)
	defer unavailable.Close()
	bounded, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	migrationResult(t, MigrateMomentumPostgres(bounded, unavailable), ErrMigration)
}

// TestMigrationRejections проверяет validator, оба rollback и реальное подключение metadata verifier.
func TestMigrationRejections(t *testing.T) {
	for _, tc := range []struct {
		name, change, pk string
		prefix, fail     bool
		want             error
	}{
		{"checksum", "UPDATE momentum.schema_migrations SET checksum=decode(repeat('00',32),'hex')", "1", true, false, ErrMigrationVersion},
		{"fresh", "", "0", false, true, ErrMigration},
		{"prefix", "", "1", true, true, ErrMigration},
		{"metadata", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_pkey", "0", true, false, ErrMigration},
	} {
		db := openTestPostgres(t)
		if tc.prefix {
			migrationResult(t, migrateMomentumPostgres(context.Background(), db, []migrationSpec{{1, momentumBootstrap}}), nil)
		}
		if tc.change != "" {
			_, err := db.ExecContext(context.Background(), tc.change)
			migrationResult(t, err, nil)
		}
		before := migrationState(t, db)
		specs := []migrationSpec{{1, momentumBootstrap}, {2, "CREATE TABLE momentum.pending_marker(id bigint)"}}
		valid := specs
		if tc.fail {
			specs = append(specs, migrationSpec{3, "sensitive_sql_marker INVALID SQL"})
		}
		migrationResult(t, migrateMomentumPostgres(context.Background(), db, specs), tc.want)
		if migrationState(t, db) != before {
			t.Fatalf("case=%s changed prefix or leaked bootstrap", tc.name)
		}
		migrationValue(t, db, "SELECT to_regclass('momentum.pending_marker') IS NULL", "true")
		migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_constraint WHERE conrelid=to_regclass('momentum.schema_migrations') AND contype='p'", tc.pk)
		if tc.fail {
			migrationResult(t, migrateMomentumPostgres(context.Background(), db, valid), nil)
			migrationValue(t, db, "SELECT count(*)=2 AND max(version)=2 AND to_regclass('momentum.pending_marker') IS NOT NULL FROM momentum.schema_migrations", "true")
			if tc.prefix && !strings.Contains(migrationState(t, db), strings.Trim(before, "[]")) {
				t.Fatal("retry changed prefix/checksum/timestamp")
			}
		}
	}
}

// TestConfigSnapshotMigration проверяет применение нового суффикса без потери ранее записанного.
func TestConfigSnapshotMigration(t *testing.T) {
	ctx := context.Background()
	prefix := []migrationSpec{
		{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema},
		{4, momentumImportProgressSchema}, {5, momentumSignalJournalSchema},
	}
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
	history := migrationState(t, db)
	marketStatement(t, db, "INSERT INTO momentum.signal_batches(batch_hash,payload) VALUES(sha256('kept'::bytea),'kept'::bytea)", true)
	saved := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.signal_batches r")
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=5) r`, history)
	checksum := sha256.Sum256([]byte(momentumConfigSnapshotSchema))
	migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=6", fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.signal_batches r", saved)
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "0")
	after := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	if migrationState(t, db) != after {
		t.Fatal("repeat changed the committed migration history")
	}

	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, collision, prefix), nil)
	before := migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.config_snapshots(owned_marker TEXT); INSERT INTO momentum.config_snapshots VALUES ('kept')", true)
	migrationResult(t, MigrateMomentumPostgres(ctx, collision), ErrMigration)
	if migrationState(t, collision) != before {
		t.Fatal("failed migration changed the committed prefix")
	}
	migrationValue(t, collision, "SELECT owned_marker FROM momentum.config_snapshots", "kept")
}

// TestConfigSnapshotSchema проверяет точное сохранение снимка и локальные ограничения целостности.
func TestConfigSnapshotSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	raw, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	migrationResult(t, err, nil)
	for _, document := range []string{string(raw), strings.Replace(string(raw), "next_monday", "now", 1)} {
		parsed, err := config.Parse([]byte(document))
		migrationResult(t, err, nil)
		payload, digest, err := parsed.Snapshot()
		migrationResult(t, err, nil)
		key, err := hex.DecodeString(digest)
		migrationResult(t, err, nil)
		_, err = db.ExecContext(ctx, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
		migrationResult(t, err, nil)
		var stored []byte
		err = db.QueryRowContext(ctx, "SELECT payload FROM momentum.config_snapshots WHERE config_hash=$1", key).Scan(&stored)
		migrationResult(t, err, nil)
		if string(stored) != string(payload) {
			t.Fatal("snapshot bytes changed")
		}
		reparsed, err := config.Parse(stored)
		migrationResult(t, err, nil)
		_, again, err := reparsed.Snapshot()
		migrationResult(t, err, nil)
		if again != digest {
			t.Fatal("snapshot hash changed on reparse")
		}
		_, err = db.ExecContext(ctx, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
		if err == nil {
			t.Fatal("duplicate config hash inserted")
		}
	}
	for _, values := range []string{
		"(NULL,'new'::bytea,DEFAULT)",
		"('x'::bytea,'new'::bytea,DEFAULT)",
		"(sha256('different'::bytea),'new'::bytea,DEFAULT)",
		"(sha256(''::bytea),''::bytea,DEFAULT)",
		"(sha256('new'::bytea),NULL,DEFAULT)",
		"(sha256('new'::bytea),'new'::bytea,NULL)",
		"(sha256('new'::bytea),'new'::bytea,'infinity')",
		"(sha256('new'::bytea),'new'::bytea,'-infinity')",
		"(sha256('new'::bytea),'new'::bytea,'1969-12-31 23:59:59+00')",
	} {
		marketStatement(t, db, "INSERT INTO momentum.config_snapshots VALUES "+values, false)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "2")
	migrationValue(t, db, "SELECT bool_and(isfinite(created_at)) FROM momentum.config_snapshots", "true")
}

// TestRebalanceRunMigration проверяет новый суффикс и сохранность префикса со снимком.
func TestRebalanceRunMigration(t *testing.T) {
	ctx := context.Background()
	prefix := []migrationSpec{
		{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema},
		{4, momentumImportProgressSchema}, {5, momentumSignalJournalSchema}, {6, momentumConfigSnapshotSchema},
	}
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
	oldHistory := migrationState(t, db)
	marketStatement(t, db, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256('kept'::bytea),'kept'::bytea)", true)
	snapshot := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.config_snapshots r")
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=6) r`, oldHistory)
	checksum := sha256.Sum256([]byte(momentumRebalanceRunSchema))
	migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=7", fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.config_snapshots r", snapshot)
	migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "0")
	after := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	if migrationState(t, db) != after {
		t.Fatal("repeat changed the committed migration history")
	}

	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, collision, prefix), nil)
	before := migrationState(t, collision)
	marketStatement(t, collision, "CREATE FUNCTION momentum.reject_rebalance_run_mutation() RETURNS int LANGUAGE SQL AS 'SELECT 5'", true)
	migrationResult(t, MigrateMomentumPostgres(ctx, collision), ErrMigration)
	if migrationState(t, collision) != before {
		t.Fatal("failed migration changed the committed prefix")
	}
	migrationValue(t, collision, "SELECT to_regclass('momentum.rebalance_runs') IS NULL", "true")
	migrationValue(t, collision, "SELECT momentum.reject_rebalance_run_mutation()", "5")
}

// TestRebalanceRunSchema проверяет ключи, целостность и запрет мутации записи запуска.
func TestRebalanceRunSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	marketStatement(t, db, `INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES
 (sha256('profile-a'::bytea),'profile-a'::bytea), (sha256('profile-b'::bytea),'profile-b'::bytea)`, true)
	const insert = "INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,onboarding_id,config_hash) VALUES "
	const insertCreated = "INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,onboarding_id,config_hash,created_at) VALUES "
	const at = "'2026-11-16 00:00:00.123456+00'"
	const a = "sha256('profile-a'::bytea)"
	const b = "sha256('profile-b'::bytea)"
	marketStatement(t, db, insert+"('weekly','account','strategy',"+at+",NULL,"+a+")", true)
	runSchemaSQLState(t, db, "23505", insert+"('different-id','account','strategy','2026-11-16 03:00:00.123456+03',NULL,"+b+")")
	for _, tc := range []struct{ state, values string }{
		{"23505", "('weekly','other','strategy'," + at + ",NULL," + a + ")"},
		{"23503", "('missing-snapshot','account','strategy'," + at + ",'missing',sha256('absent'::bytea))"},
		{"23502", "('null-hash','account','strategy'," + at + ",'bad-hash',NULL)"},
		{"23503", "('bad-digest','account','strategy'," + at + ",'bad-digest','x'::bytea)"},
		{"23514", "('','account','strategy'," + at + ",'bad-run'," + a + ")"},
		{"23502", "(NULL,'account','strategy'," + at + ",NULL," + a + ")"},
		{"23514", "('blank-account',E'\t','strategy'," + at + ",'bad-account'," + a + ")"},
		{"23514", "('blank-strategy','account',' ', " + at + ",'bad-strategy'," + a + ")"},
		{"23514", "('blank-onboarding','account','strategy'," + at + ",E'\n'," + a + ")"},
		{"23502", "('null-account',NULL,'strategy'," + at + ",'null-account'," + a + ")"},
		{"23502", "('null-strategy','account',NULL," + at + ",'null-strategy'," + a + ")"},
		{"23502", "('null-time','account','strategy',NULL,'null-time'," + a + ")"},
		{"23514", "('infinity','account','strategy','infinity','infinity'," + a + ")"},
		{"23514", "('before','account','strategy','1969-12-31 23:59:59+00','before'," + a + ")"},
	} {
		runSchemaSQLState(t, db, tc.state, insert+tc.values)
	}
	for _, tc := range []struct{ state, values string }{
		{"23502", "('null-created','account','strategy'," + at + ",NULL," + a + ",NULL)"},
		{"23514", "('infinite-created','account','strategy'," + at + ",NULL," + a + ",'infinity')"},
		{"23514", "('early-created','account','strategy'," + at + ",NULL," + a + ",'1969-12-31 23:59:59+00')"},
	} {
		runSchemaSQLState(t, db, tc.state, insertCreated+tc.values)
	}
	marketStatement(t, db, insert+"('other-account','other','strategy',"+at+",NULL,"+a+")", true)
	marketStatement(t, db, insert+"('other-strategy','account','other',"+at+",NULL,"+a+")", true)
	marketStatement(t, db, insert+"('now','account','strategy',"+at+",'onboard',"+a+")", true)
	runSchemaSQLState(t, db, "23505", insert+"('now-retry','account','strategy','2026-11-17 01:00:00+00','onboard',"+b+")")
	marketStatement(t, db, insert+"('other-onboard','other','strategy',"+at+",'onboard',"+a+")", true)
	marketStatement(t, db, insert+"('  Arbitrary ID  ',' Account ','Strategy/β',"+at+",'  Onboard ID  ',"+a+")", true)
	migrationValue(t, db, "SELECT run_id='  Arbitrary ID  ' AND account_id=' Account ' AND strategy_id='Strategy/β' AND onboarding_id='  Onboard ID  ' FROM momentum.rebalance_runs WHERE run_id='  Arbitrary ID  '", "true")
	original := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.rebalance_runs r WHERE run_id='now'")
	for _, statement := range []string{
		"UPDATE momentum.rebalance_runs SET config_hash=" + b + " WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET scheduled_at=scheduled_at + INTERVAL '1 day' WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET onboarding_id=NULL WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET run_id=run_id WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET account_id=account_id WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET strategy_id=strategy_id WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET scheduled_at=scheduled_at WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET onboarding_id=onboarding_id WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET config_hash=config_hash WHERE run_id='now'",
		"UPDATE momentum.rebalance_runs SET created_at=created_at WHERE run_id='now'",
		"DELETE FROM momentum.rebalance_runs WHERE run_id='now'",
	} {
		runSchemaSQLState(t, db, "55000", statement)
	}
	runSchemaSQLState(t, db, "23503", "DELETE FROM momentum.config_snapshots WHERE config_hash="+a)
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.rebalance_runs r WHERE run_id='now'", original)
	migrationValue(t, db, "SELECT scheduled_at="+at+"::timestamptz AND config_hash="+a+" AND onboarding_id='onboard' AND isfinite(created_at) FROM momentum.rebalance_runs WHERE run_id='now'", "true")
	migrationValue(t, db, "SELECT count(*) FROM momentum.rebalance_runs", "6")
}

// runSchemaSQLState проверяет конкретное серверное ограничение для корректного SQL.
func runSchemaSQLState(t *testing.T, db *sql.DB, want, statement string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), statement)
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) || string(pgErr.Code) != want {
		t.Fatalf("expected SQLSTATE %s, got %v", want, err)
	}
}

// TestPlanRevisionMigration сохраняет прежнюю историю и откатывает неудачный суффикс.
func TestPlanRevisionMigration(t *testing.T) {
	ctx := context.Background()
	prefix := []migrationSpec{
		{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema},
		{4, momentumImportProgressSchema}, {5, momentumSignalJournalSchema}, {6, momentumConfigSnapshotSchema},
		{7, momentumRebalanceRunSchema},
	}
	for _, fail := range []bool{false, true} {
		db := openTestPostgres(t)
		migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
		before := migrationState(t, db)
		planSchemaRuns(t, db)
		rows := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.rebalance_runs r WHERE run_id='run'")
		config := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.config_snapshots r")
		if fail {
			marketStatement(t, db, "CREATE FUNCTION momentum.reject_plan_revision_mutation() RETURNS int LANGUAGE SQL AS 'SELECT 17'", true)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), ErrMigration)
			migrationValue(t, db, "SELECT to_regclass('momentum.plan_revisions') IS NULL", "true")
			migrationValue(t, db, "SELECT momentum.reject_plan_revision_mutation()", "17")
			if migrationState(t, db) != before {
				t.Fatal("failed suffix changed history")
			}
		} else {
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			checksum := sha256.Sum256([]byte(momentumPlanRevisionSchema))
			migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=8", fmt.Sprintf("%x", checksum))
			migrationValue(t, db, "SELECT count(*) FROM momentum.plan_revisions", "0")
			migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=7) r`, before)
			after := migrationState(t, db)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			if migrationState(t, db) != after {
				t.Fatal("repeat changed committed history")
			}
		}
		migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.rebalance_runs r WHERE run_id='run'", rows)
		migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.config_snapshots r", config)
	}
}

// planSchemaRuns создаёт независимые запуски с действующим снимком конфигурации.
func planSchemaRuns(t *testing.T, db *sql.DB) {
	t.Helper()
	marketStatement(t, db, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256('cfg'::bytea),'cfg'::bytea)", true)
	marketStatement(t, db, `INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash) VALUES
 ('run','account','strategy','2026-01-01+00',sha256('cfg'::bytea)),
 ('other','other-account','strategy','2026-01-01+00',sha256('cfg'::bytea))`, true)
}

// TestPlanRevisionSchema проверяет цепочку, digest и неизменность каждой колонки.
func TestPlanRevisionSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	planSchemaRuns(t, db)
	const insert = "INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,previous_hash,payload,payload_sha256) VALUES "
	const h = "sha256('one'::bytea)"
	const p = "'payload'::bytea"
	const digest = "sha256('payload'::bytea)"
	marketStatement(t, db, insert+"('run',1,"+h+",NULL,"+p+","+digest+")", true)
	marketStatement(t, db, insert+"('run',2,sha256('two'::bytea),"+h+","+p+","+digest+")", true)
	marketStatement(t, db, insert+"('run',3,sha256('three'::bytea),sha256('two'::bytea),"+p+","+digest+")", true)
	migrationValue(t, db, "SELECT string_agg(revision::text,',' ORDER BY revision) FROM momentum.plan_revisions", "1,2,3")
	migrationValue(t, db, "SELECT bool_and(isfinite(created_at) AND created_at >= '1970-01-01+00') FROM momentum.plan_revisions", "true")
	for _, tc := range []struct{ code, values string }{
		{"23505", "('run',1," + h + ",NULL," + p + "," + digest + ")"},
		{"23503", "('missing',1," + h + ",NULL," + p + "," + digest + ")"},
		{"23503", "('other',2,sha256('two'::bytea)," + h + "," + p + "," + digest + ")"},
		{"23503", "('run',4,sha256('four'::bytea)," + h + "," + p + "," + digest + ")"},
		{"23503", "('run',5,sha256('five'::bytea),sha256('three'::bytea)," + p + "," + digest + ")"},
		{"23514", "('other',1," + h + "," + h + "," + p + "," + digest + ")"},
		{"23514", "('other',2," + h + ",NULL," + p + "," + digest + ")"},
		{"23514", "('other',2," + h + ",'bad'::bytea," + p + "," + digest + ")"},
		{"23514", "('other',1,'bad'::bytea,NULL," + p + "," + digest + ")"},
		{"23514", "('other',1," + h + ",NULL,''::bytea,sha256(''::bytea))"},
		{"23514", "('other',1," + h + ",NULL," + p + ",'bad'::bytea)"},
		{"23514", "('other',1," + h + ",NULL," + p + "," + h + ")"},
		{"23502", "(NULL,1," + h + ",NULL," + p + "," + digest + ")"},
		{"23502", "('other',NULL," + h + ",NULL," + p + "," + digest + ")"},
		{"23502", "('other',1,NULL,NULL," + p + "," + digest + ")"},
		{"23502", "('other',1," + h + ",NULL,NULL," + digest + ")"},
		{"23502", "('other',1," + h + ",NULL," + p + ",NULL)"},
	} {
		runSchemaSQLState(t, db, tc.code, insert+tc.values)
	}
	marketStatement(t, db, insert+"('other',1,"+h+",NULL,"+p+","+digest+")", true)
	marketStatement(t, db, insert+"('other',2,sha256('two'::bytea),"+h+","+p+","+digest+")", true)
	before := migrationValue(t, db, "SELECT json_agg(r ORDER BY run_id,revision)::text FROM (SELECT *,xmin::text FROM momentum.plan_revisions) r")
	for _, column := range []string{"run_id", "revision", "plan_hash", "previous_hash", "payload", "payload_sha256", "created_at"} {
		runSchemaSQLState(t, db, "55000", "UPDATE momentum.plan_revisions SET "+column+"="+column+" WHERE run_id='run' AND revision=1")
	}
	for _, set := range []string{"revision=4", "run_id='changed'", "plan_hash=sha256('changed'::bytea)", "payload='changed'::bytea",
		"payload_sha256=sha256('changed'::bytea)", "previous_hash=sha256('changed'::bytea)", "created_at='2030-01-01+00'"} {
		runSchemaSQLState(t, db, "55000", "UPDATE momentum.plan_revisions SET "+set+" WHERE run_id='run' AND revision=1")
	}
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.plan_revisions WHERE run_id='run'")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.rebalance_runs WHERE run_id='run'")
	migrationValue(t, db, "SELECT json_agg(r ORDER BY run_id,revision)::text FROM (SELECT *,xmin::text FROM momentum.plan_revisions) r", before)
}

// TestPlanRevisionBounds проверяет реальные CHECK на полном uint64 без цепочки из миллиардов строк.
func TestPlanRevisionBounds(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	db.SetMaxOpenConns(1)
	marketStatement(t, db, "CREATE TEMP TABLE revision_bounds (LIKE momentum.plan_revisions INCLUDING DEFAULTS INCLUDING GENERATED INCLUDING CONSTRAINTS)", true)
	const insert = "INSERT INTO revision_bounds(run_id,revision,plan_hash,previous_hash,payload,payload_sha256,created_at) VALUES "
	const rest = ",sha256('plan'::bytea),sha256('previous'::bytea),'p'::bytea,sha256('p'::bytea),"
	marketStatement(t, db, insert+"('run',18446744073709551615"+rest+"'1970-01-01+00')", true)
	migrationValue(t, db, "SELECT revision::text || ':' || predecessor_revision::text FROM revision_bounds", "18446744073709551615:18446744073709551614")
	for _, revision := range []string{"0", "-1", "1.1", "18446744073709551615.1", "18446744073709551616", "'NaN'", "'Infinity'", "'-Infinity'"} {
		runSchemaSQLState(t, db, "23514", insert+"('bad',"+revision+rest+"'1970-01-01+00')")
	}
	for _, timestamp := range []string{"'infinity'", "'-infinity'", "'1969-12-31 23:59:59.999999+00'"} {
		runSchemaSQLState(t, db, "23514", insert+"('bad',2"+rest+timestamp+")")
	}
	runSchemaSQLState(t, db, "23502", insert+"('bad',2"+rest+"NULL)")
	runSchemaSQLState(t, db, "428C9", "INSERT INTO revision_bounds(run_id,revision,predecessor_revision,plan_hash,payload,payload_sha256) VALUES('bad',1,99,sha256('h'::bytea),'p'::bytea,sha256('p'::bytea))")
}

// TestPlanRevisionCodec сохраняет точные канонические байты и три реальные ревизии.
func TestPlanRevisionCodec(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	planSchemaRuns(t, db)
	one, err := model.ParseAmount("1")
	migrationResult(t, err, nil)
	three, err := model.ParseAmount("3")
	migrationResult(t, err, nil)
	third, err := one.Quo(three)
	migrationResult(t, err, nil)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{
		Environment: "paper", AccountID: "account", ConfigVersion: "v1", CapturedAt: stamp,
		Equity: third, Prices: map[string]model.Decimal{"BTCUSDT": one}, Positions: map[string]model.Decimal{},
	})
	migrationResult(t, err, nil)
	previous := ""
	for revision := uint64(1); revision <= 3; revision++ {
		plan, err := model.NewTradePlan(model.PlanInput{Snapshot: snapshot,
			Metadata: model.PlanMetadata{StrategyID: "strategy", RunID: "run", ProfileID: "opaque\x00\xff", ModelID: "model", FormulaID: "formula",
				ConfigHash: fmt.Sprintf("%x", sha256.Sum256([]byte("cfg"))), ScheduledAt: stamp, EquityCutoffAt: stamp,
				Revision: revision, PreviousHash: previous}, Targets: []model.Target{{Symbol: "BTCUSDT", Quantity: third}}})
		migrationResult(t, err, nil)
		payload, err := model.EncodeTradePlan(plan)
		migrationResult(t, err, nil)
		hash, err := hex.DecodeString(plan.Hash())
		migrationResult(t, err, nil)
		var predecessor any
		if previous != "" {
			predecessor, err = hex.DecodeString(previous)
			migrationResult(t, err, nil)
		}
		digest := sha256.Sum256(payload)
		_, err = db.ExecContext(ctx, `INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,previous_hash,payload,payload_sha256)
 VALUES($1,$2,$3,$4,$5,$6)`, "run", revision, hash, predecessor, payload, digest[:])
		migrationResult(t, err, nil)
		var stored []byte
		err = db.QueryRowContext(ctx, "SELECT payload FROM momentum.plan_revisions WHERE run_id=$1 AND revision=$2", "run", revision).Scan(&stored)
		migrationResult(t, err, nil)
		decoded, err := model.DecodeTradePlan(stored)
		migrationResult(t, err, nil)
		comparison, compareErr := decoded.Targets()[0].Quantity.Cmp(third)
		if string(stored) != string(payload) || decoded.Hash() != plan.Hash() || decoded.Metadata() != plan.Metadata() ||
			decoded.Snapshot().Hash() != snapshot.Hash() || comparison != 0 || compareErr != nil {
			t.Fatal("durable plan lost exact metadata, snapshot or rational targets")
		}
		previous = plan.Hash()
	}
}

// TestFundingImportProgressMigration проверяет новый суффикс, сохранность данных и откат коллизии.
func TestFundingImportProgressMigration(t *testing.T) {
	ctx := context.Background()
	prefix := []migrationSpec{
		{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema},
		{4, momentumImportProgressSchema}, {5, momentumSignalJournalSchema}, {6, momentumConfigSnapshotSchema},
		{7, momentumRebalanceRunSchema}, {8, momentumPlanRevisionSchema},
	}
	for _, collision := range []bool{false, true} {
		db := openTestPostgres(t)
		migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
		before := migrationState(t, db)
		planSchemaRuns(t, db)
		migrationResult(t, RegisterDataset(ctx, db, "testnet", "preserved"), nil)
		marketStatement(t, db, `INSERT INTO momentum.funding VALUES('testnet','preserved','BTCUSDT','1970-01-01 00:00:00.123+00',0);
 INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES('run',1,sha256('plan'::bytea),'kept'::bytea,sha256('kept'::bytea))`, true)
		const data = `SELECT json_build_object('runs',(SELECT json_agg(r) FROM momentum.rebalance_runs r),
 'config',(SELECT json_agg(r) FROM momentum.config_snapshots r),
 'plans',(SELECT json_agg(r) FROM momentum.plan_revisions r),
 'funding',(SELECT json_agg(r) FROM momentum.funding r))::text`
		saved := migrationValue(t, db, data)
		if collision {
			marketStatement(t, db, "CREATE TABLE momentum.funding_import_progress(owned_marker TEXT)", true)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), ErrMigration)
			migrationValue(t, db, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='funding_import_progress' AND column_name='owned_marker'", "1")
			if migrationState(t, db) != before {
				t.Fatal("collision changed migration history")
			}
		} else {
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			migrationValue(t, db, "SELECT count(*) FROM momentum.funding_import_progress", "0")
			migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=8) r`, before)
			checksum := sha256.Sum256([]byte(momentumFundingImportProgressSchema))
			migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=9", fmt.Sprintf("%x", checksum))
			after := migrationState(t, db)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			if migrationState(t, db) != after {
				t.Fatal("repeat changed history")
			}
		}
		migrationValue(t, db, data, saved)
	}
}

// fundingProgressDatabase создаёт отдельную базу и два пространства имён.
func fundingProgressDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	for _, environment := range []string{"testnet", "mainnet"} {
		for _, dataset := range []string{"funding", "other"} {
			migrationResult(t, RegisterDataset(context.Background(), db, environment, dataset), nil)
		}
	}
	return db
}

const fundingProgressPending = `INSERT INTO momentum.funding_import_progress
 (environment,dataset_id,symbol,lower_time,upper_time,next_end)
 VALUES('testnet','funding','HYPEUSDT','2026-01-01 00:00:00.123+00','2026-01-01 01:00:00.987+00','2026-01-01 01:00:00.987+00')`

const fundingProgressContinue = `revision=1,action='continue',
 last_request_end=upper_time,last_server_time=upper_time+INTERVAL '1 second',
 last_page_count=200,last_page_oldest=upper_time-INTERVAL '7 milliseconds',
 next_end=upper_time-INTERVAL '8 milliseconds'`

// TestFundingImportProgressStates проверяет все исходы страницы без предположения о cadence.
func TestFundingImportProgressStates(t *testing.T) {
	db := fundingProgressDatabase(t)
	marketStatement(t, db, fundingProgressPending, true)
	migrationValue(t, db, "SELECT action || ':' || revision::text FROM momentum.funding_import_progress", "pending:0")
	runSchemaSQLState(t, db, "23505", strings.ReplaceAll(fundingProgressPending, "2026-01-01 01:00:00.987+00", "2026-01-01 04:00:00.987+03"))
	for _, statement := range []string{
		strings.Replace(fundingProgressPending, "'testnet'", "'mainnet'", 1),
		strings.Replace(fundingProgressPending, "'funding'", "'other'", 1),
		strings.Replace(fundingProgressPending, "'HYPEUSDT'", "'BTCUSDT'", 1),
		strings.Replace(fundingProgressPending, "00:00:00.123", "00:00:00.124", 1),
	} {
		marketStatement(t, db, statement, true)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding_import_progress", "5")
	marketStatement(t, db, "DELETE FROM momentum.funding_import_progress WHERE environment<>'testnet' OR dataset_id<>'funding' OR symbol<>'HYPEUSDT' OR lower_time<>'2026-01-01 00:00:00.123+00'", true)
	marketStatement(t, db, "UPDATE momentum.funding_import_progress SET "+fundingProgressContinue, true)
	migrationValue(t, db, "SELECT next_end=last_page_oldest-INTERVAL '1 millisecond' AND last_page_count=200 FROM momentum.funding_import_progress", "true")
	marketStatement(t, db, `UPDATE momentum.funding_import_progress SET revision=2,action='lower_reached',
 last_request_end=next_end,next_end=NULL,last_page_count=1,last_page_oldest=lower_time`, true)
	migrationValue(t, db, "SELECT action || ':' || (next_end IS NULL)::text FROM momentum.funding_import_progress", "lower_reached:true")
	marketStatement(t, db, "DELETE FROM momentum.funding_import_progress", true)
	marketStatement(t, db, strings.ReplaceAll(fundingProgressPending, "2026-01-01 00:00:00.123+00", "2026-01-01 01:00:00.987+00"), true)
	marketStatement(t, db, `UPDATE momentum.funding_import_progress SET revision=1,action='empty',
 last_request_end=upper_time,last_server_time=upper_time-INTERVAL '1 second',last_page_count=0,next_end=NULL`, true)
	migrationValue(t, db, "SELECT last_server_time<last_request_end AND last_page_oldest IS NULL AND next_end IS NULL FROM momentum.funding_import_progress", "true")
	marketStatement(t, db, "DELETE FROM momentum.funding_import_progress", true)
	marketStatement(t, db, `INSERT INTO momentum.funding_import_progress
 (environment,dataset_id,symbol,lower_time,upper_time,next_end,revision,action,last_request_end,last_server_time,last_page_count,last_page_oldest)
 VALUES('testnet','funding','HYPEUSDT','epoch','1970-01-01 00:00:00.001+00',NULL,1,'source_boundary',
 '1970-01-01 00:00:00.001+00','1970-01-01 00:00:00.001+00',1,'1970-01-01 00:00:00.001+00')`, true)
	migrationValue(t, db, "SELECT action || ':' || (next_end IS NULL)::text FROM momentum.funding_import_progress", "source_boundary:true")
}

// TestFundingImportProgressInvalid проверяет явные NULL и все семейства ограничений.
func TestFundingImportProgressInvalid(t *testing.T) {
	db := fundingProgressDatabase(t)
	marketStatement(t, db, fundingProgressPending, true)
	for _, tc := range []struct{ code, assignment string }{
		{"23502", "environment=NULL"}, {"23503", "environment='paper'"},
		{"23502", "dataset_id=NULL"}, {"23503", "dataset_id='missing'"},
		{"23502", "symbol=NULL"}, {"23514", "symbol='hypeusdt'"},
		{"23502", "lower_time=NULL"}, {"23514", "lower_time='-infinity'"},
		{"23514", "lower_time='1969-12-31 23:59:59.999+00'"}, {"23514", "lower_time=lower_time+INTERVAL '1 microsecond'"},
		{"23502", "upper_time=NULL"}, {"23514", "upper_time='infinity'"},
		{"23514", "upper_time=lower_time-INTERVAL '1 millisecond'"},
		{"23514", "lower_time='epoch',upper_time='epoch',next_end='epoch'"},
		{"23514", "upper_time='294276-12-31 23:59:59.999999+00'"},
		{"23514", "upper_time=upper_time+INTERVAL '1 microsecond'"},
		{"23514", "next_end=NULL"}, {"23514", "next_end=lower_time-INTERVAL '1 millisecond'"},
		{"23514", "next_end=upper_time+INTERVAL '1 millisecond'"}, {"23514", "next_end=next_end+INTERVAL '1 microsecond'"},
		{"23514", "next_end='epoch'"}, {"23514", "next_end='infinity'"},
		{"23502", "revision=NULL"}, {"23514", "revision=-1"}, {"22003", "revision=9223372036854775808"},
		{"23514", "revision=1"}, {"23502", "action=NULL"}, {"23514", "action='complete'"},
		{"23514", "last_request_end=upper_time"}, {"23514", "last_server_time=upper_time"},
		{"23514", "last_page_count=0"}, {"23514", "last_page_oldest=lower_time"},
	} {
		runSchemaSQLState(t, db, tc.code, "UPDATE momentum.funding_import_progress SET "+tc.assignment)
	}
	marketStatement(t, db, "UPDATE momentum.funding_import_progress SET "+fundingProgressContinue, true)
	for _, assignment := range []string{
		"revision=0", "last_request_end=NULL", "last_server_time=NULL", "last_page_count=NULL", "last_page_oldest=NULL", "next_end=NULL",
		"last_page_count=-1", "last_page_count=201", "last_page_count=0",
		"last_request_end=lower_time-INTERVAL '1 millisecond'", "last_request_end=upper_time+INTERVAL '1 millisecond'",
		"last_request_end=last_request_end+INTERVAL '1 microsecond'", "last_request_end='infinity'", "last_request_end='epoch'",
		"last_server_time='epoch'", "last_server_time='infinity'", "last_server_time=last_server_time+INTERVAL '1 microsecond'",
		"last_server_time='294276-12-31 23:59:59.999999+00'", "last_server_time=lower_time",
		"last_page_oldest=lower_time-INTERVAL '1 millisecond'", "last_page_oldest=upper_time+INTERVAL '1 millisecond'",
		"last_page_oldest=last_page_oldest+INTERVAL '1 microsecond'", "last_page_oldest='infinity'",
		"last_server_time=upper_time-INTERVAL '3 milliseconds',last_page_oldest=upper_time-INTERVAL '2 milliseconds',next_end=upper_time-INTERVAL '3 milliseconds'",
		"next_end=last_page_oldest", "next_end=last_page_oldest-INTERVAL '2 milliseconds'",
		"action='lower_reached',next_end=NULL", "action='source_boundary',next_end=NULL",
		"action='empty',last_page_count=0,next_end=NULL",
	} {
		runSchemaSQLState(t, db, "23514", "UPDATE momentum.funding_import_progress SET "+assignment)
	}
	marketStatement(t, db, "UPDATE momentum.funding_import_progress SET revision=9223372036854775807", true)
	migrationValue(t, db, "SELECT revision::text FROM momentum.funding_import_progress", "9223372036854775807")
}

// TestFundingImportProgressRails проверяет точную верхнюю границу и остановки с NULL-курсорoм.
func TestFundingImportProgressRails(t *testing.T) {
	db := fundingProgressDatabase(t)
	const upper = "294276-12-31 23:59:59.999+00"
	statement := strings.ReplaceAll(strings.ReplaceAll(fundingProgressPending, "2026-01-01 00:00:00.123+00", upper), "2026-01-01 01:00:00.987+00", upper)
	marketStatement(t, db, statement, true)
	marketStatement(t, db, `UPDATE momentum.funding_import_progress SET revision=1,action='lower_reached',
 last_request_end=upper_time,last_server_time=upper_time,last_page_count=1,last_page_oldest=lower_time,next_end=NULL`, true)
	migrationValue(t, db, "SELECT extract(milliseconds FROM last_server_time)::text || ':' || (last_server_time=lower_time AND next_end IS NULL)::text FROM momentum.funding_import_progress", "59999.000:true")
	for _, assignment := range []string{"next_end=upper_time", "last_page_oldest=NULL", "last_page_count=0", "last_request_end=NULL", "last_server_time=NULL", "last_page_count=NULL"} {
		runSchemaSQLState(t, db, "23514", "UPDATE momentum.funding_import_progress SET "+assignment)
	}
	marketStatement(t, db, "DELETE FROM momentum.funding_import_progress", true)
	marketStatement(t, db, fundingProgressPending, true)
	marketStatement(t, db, `UPDATE momentum.funding_import_progress SET revision=1,action='empty',
 last_request_end=upper_time,last_server_time=upper_time,last_page_count=0,next_end=NULL`, true)
	for _, assignment := range []string{"next_end=upper_time", "last_page_oldest=lower_time", "last_page_count=1", "last_request_end=NULL", "last_server_time=NULL", "last_page_count=NULL"} {
		runSchemaSQLState(t, db, "23514", "UPDATE momentum.funding_import_progress SET "+assignment)
	}
	marketStatement(t, db, "DELETE FROM momentum.funding_import_progress", true)
	marketStatement(t, db, `INSERT INTO momentum.funding_import_progress
 (environment,dataset_id,symbol,lower_time,upper_time,next_end,revision,action,last_request_end,last_server_time,last_page_count,last_page_oldest)
 VALUES('testnet','funding','HYPEUSDT','epoch','1970-01-01 00:00:00.001+00',NULL,1,'source_boundary',
 '1970-01-01 00:00:00.001+00','1970-01-01 00:00:00.001+00',1,'1970-01-01 00:00:00.001+00')`, true)
	for _, assignment := range []string{"next_end=upper_time", "last_page_oldest=NULL", "last_page_count=0", "last_page_oldest='epoch'", "lower_time=upper_time", "last_request_end=NULL", "last_server_time=NULL", "last_page_count=NULL"} {
		runSchemaSQLState(t, db, "23514", "UPDATE momentum.funding_import_progress SET "+assignment)
	}
}

// artifactMigrationPrefix возвращает exact actual prefix без нового migration suffix.
func artifactMigrationPrefix() []migrationSpec {
	return []migrationSpec{
		{1, momentumBootstrap},
		{2, momentumMarketCandlesSchema},
		{3, momentumMarketSchema},
		{4, momentumImportProgressSchema},
		{5, momentumSignalJournalSchema},
		{6, momentumConfigSnapshotSchema},
		{7, momentumRebalanceRunSchema},
		{8, momentumPlanRevisionSchema},
		{9, momentumFundingImportProgressSchema},
	}
}

// TestDatasetArtifactSchema проверяет точные bytes и append-only SQL boundary.
func TestDatasetArtifactSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	payload := []byte{0, 255, 10, 123, 125}
	key := sha256.Sum256(payload)
	insert := `INSERT INTO momentum.dataset_artifacts(artifact_hash,payload) VALUES ($1,$2)`
	_, err := db.Exec(insert, key[:], payload)
	migrationResult(t, err, nil)
	var got []byte
	var stamp time.Time
	migrationResult(t, db.QueryRow(`SELECT payload,created_at FROM momentum.dataset_artifacts WHERE artifact_hash=$1`, key[:]).Scan(&got, &stamp), nil)
	if !bytes.Equal(payload, got) || stamp.Before(time.Unix(0, 0)) {
		t.Fatal("lost exact binary payload or timestamp")
	}
	xmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.dataset_artifacts")
	_, err = db.Exec(insert+" ON CONFLICT (artifact_hash) DO NOTHING", key[:], payload)
	migrationResult(t, err, nil)
	migrationValue(t, db, "SELECT xmin::text FROM momentum.dataset_artifacts", xmin)
	_, err = db.Exec(insert, key[:], payload)
	if err == nil {
		t.Fatal("duplicate was inserted")
	}
	for _, query := range []string{
		"UPDATE momentum.dataset_artifacts SET payload=payload",
		"INSERT INTO momentum.dataset_artifacts SELECT artifact_hash,payload,created_at FROM momentum.dataset_artifacts WHERE true ON CONFLICT(artifact_hash) DO UPDATE SET payload=EXCLUDED.payload",
		"UPDATE momentum.dataset_artifacts SET artifact_hash=artifact_hash WHERE false",
		"UPDATE momentum.dataset_artifacts SET created_at='epoch'",
		"DELETE FROM momentum.dataset_artifacts",
		"DELETE FROM momentum.dataset_artifacts WHERE false",
		"TRUNCATE momentum.dataset_artifacts",
		"TRUNCATE momentum.dataset_artifacts CASCADE",
	} {
		_, err := db.Exec(query)
		var failure *pq.Error
		if !errors.As(err, &failure) || failure.Code != "55000" {
			t.Fatalf("mutable artifact: %s %v", query, err)
		}
	}
	for _, values := range []string{
		"(NULL,decode('01','hex'),DEFAULT)",
		"(decode('01','hex'),decode('01','hex'),DEFAULT)",
		"(decode(repeat('00',32),'hex'),decode('01','hex'),DEFAULT)",
		"(sha256(''::bytea),''::bytea,DEFAULT)",
		"(sha256('x'::bytea),NULL,DEFAULT)",
		"(sha256('x'::bytea),'x'::bytea,NULL)",
		"(sha256('x'::bytea),'x'::bytea,'infinity')",
		"(sha256('x'::bytea),'x'::bytea,'-infinity')",
		"(sha256('x'::bytea),'x'::bytea,'1969-12-31')",
	} {
		marketStatement(t, db, "INSERT INTO momentum.dataset_artifacts VALUES "+values, false)
	}
	large := bytes.Repeat([]byte{0, 1, 255, 10}, 262144)
	largeKey := sha256.Sum256(large)
	_, err = db.Exec(insert, largeKey[:], large)
	migrationResult(t, err, nil)
	migrationResult(t, db.QueryRow("SELECT payload FROM momentum.dataset_artifacts WHERE artifact_hash=$1", largeKey[:]).Scan(&got), nil)
	if !bytes.Equal(got, large) {
		t.Fatal("large content changed")
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.dataset_artifacts", "2")
}

// TestDatasetArtifactLateSource отделяет сохранённый content от поздних исходных строк.
func TestDatasetArtifactLateSource(t *testing.T) {
	db, key := fundingReadFixture(t)
	input := data.FundingContentInput{Environment: key.Environment, DatasetID: key.DatasetID, Source: "bybit_v5", Category: "linear", Symbol: key.Symbol, Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Start: time.UnixMilli(0), End: time.UnixMilli(10000)}
	var err error
	input.Events, err = ReadFundingRange(context.Background(), db, key, input.Start, input.End.Add(-time.Millisecond), 200)
	migrationResult(t, err, nil)
	before, digest, err := data.EncodeFundingContent(input)
	migrationResult(t, err, nil)
	hash, _ := hex.DecodeString(digest)
	_, err = db.Exec("INSERT INTO momentum.dataset_artifacts(artifact_hash,payload) VALUES ($1,$2)", hash, before)
	migrationResult(t, err, nil)
	marketStatement(t, db, "INSERT INTO momentum.funding VALUES ('testnet','fixture','BTCUSDT','1970-01-01 00:00:00.005+00',0.001)", true)
	input.Events, err = ReadFundingRange(context.Background(), db, key, input.Start, input.End.Add(-time.Millisecond), 200)
	migrationResult(t, err, nil)
	after, changedDigest, err := data.EncodeFundingContent(input)
	migrationResult(t, err, nil)
	if changedDigest == digest || bytes.Equal(before, after) {
		t.Fatal("late insert was not visible in mutable source")
	}
	marketStatement(t, db, "UPDATE momentum.funding SET rate=0.002 WHERE funding_time='epoch'", true)
	input.Events, err = ReadFundingRange(context.Background(), db, key, input.Start, input.End.Add(-time.Millisecond), 200)
	migrationResult(t, err, nil)
	updated, updatedDigest, err := data.EncodeFundingContent(input)
	migrationResult(t, err, nil)
	if updatedDigest == digest || updatedDigest == changedDigest || bytes.Equal(updated, before) || bytes.Equal(updated, after) {
		t.Fatal("funding update was not visible in mutable source")
	}
	var stored []byte
	migrationResult(t, db.QueryRow("SELECT payload FROM momentum.dataset_artifacts WHERE artifact_hash=$1", hash).Scan(&stored), nil)
	actual := sha256.Sum256(stored)
	if !bytes.Equal(before, stored) || hex.EncodeToString(actual[:]) != digest {
		t.Fatal("late source rows changed pinned artifact")
	}
}

// TestDatasetArtifactRollback проверяет коллизию и транзакционный откат DDL.
func TestDatasetArtifactRollback(t *testing.T) {
	for _, collision := range []string{
		"CREATE TABLE momentum.dataset_artifacts(owned_marker TEXT); INSERT INTO momentum.dataset_artifacts VALUES('foreign')",
		"CREATE FUNCTION momentum.reject_dataset_artifact_mutation() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NULL; END'",
	} {
		db := openTestPostgres(t)
		migrationResult(t, migrateMomentumPostgres(context.Background(), db, artifactMigrationPrefix()), nil)
		marketStatement(t, db, "INSERT INTO momentum.datasets VALUES ('testnet','keep','bybit_v5','linear',DEFAULT)", true)
		prefix := migrationState(t, db)
		marketStatement(t, db, collision, true)
		definition := ""
		if strings.HasPrefix(collision, "CREATE FUNCTION") {
			definition = migrationValue(t, db, "SELECT pg_get_functiondef('momentum.reject_dataset_artifact_mutation()'::regprocedure)")
		}
		migrationResult(t, MigrateMomentumPostgres(context.Background(), db), ErrMigration)
		if migrationState(t, db) != prefix {
			t.Fatal("collision changed migration prefix")
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE dataset_id='keep'", "1")
		if strings.HasPrefix(collision, "CREATE FUNCTION") {
			migrationValue(t, db, "SELECT to_regclass('momentum.dataset_artifacts') IS NULL", "true")
			migrationValue(t, db, "SELECT pg_get_functiondef('momentum.reject_dataset_artifact_mutation()'::regprocedure)", definition)
		} else {
			migrationValue(t, db, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='dataset_artifacts' AND column_name='owned_marker'", "1")
			migrationValue(t, db, "SELECT owned_marker FROM momentum.dataset_artifacts", "foreign")
		}
	}
}

// TestDatasetArtifactMigration проверяет exact prefix/data, новый checksum и повтор без изменений.
func TestDatasetArtifactMigration(t *testing.T) {
	ctx := context.Background()
	db := openTestPostgres(t)
	prefix := artifactMigrationPrefix()
	if len(prefix)+1 != 10 {
		t.Fatal("version 10 must follow the actual prefix")
	}
	migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
	before := migrationState(t, db)
	planSchemaRuns(t, db)
	migrationResult(t, RegisterDataset(ctx, db, "testnet", "kept"), nil)
	marketStatement(t, db, `INSERT INTO momentum.candles VALUES('testnet','kept','BTCUSDT','D','epoch',1,1,1,1,0,true);
 INSERT INTO momentum.funding VALUES('testnet','kept','BTCUSDT','epoch',0);
 INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES('run',1,sha256('plan'::bytea),'kept'::bytea,sha256('kept'::bytea));
 INSERT INTO momentum.funding_import_progress(environment,dataset_id,symbol,lower_time,upper_time,next_end)
 VALUES('testnet','kept','BTCUSDT','epoch','epoch'::timestamptz+INTERVAL '100 ms','epoch'::timestamptz+INTERVAL '100 ms')`, true)
	const dataSQL = `SELECT json_build_object('datasets',(SELECT json_agg(r) FROM momentum.datasets r),'config',(SELECT json_agg(r) FROM momentum.config_snapshots r),
 'runs',(SELECT json_agg(r) FROM momentum.rebalance_runs r),'plans',(SELECT json_agg(r) FROM momentum.plan_revisions r),
 'candles',(SELECT json_agg(r) FROM momentum.candles r),'funding',(SELECT json_agg(r) FROM momentum.funding r),
 'progress',(SELECT json_agg(r) FROM momentum.funding_import_progress r))::text`
	saved := migrationValue(t, db, dataSQL)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	prefixSQL := fmt.Sprintf(`SELECT json_agg(r ORDER BY version)::text FROM
 (SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<%d) r`, 10)
	migrationValue(t, db, prefixSQL, before)
	migrationValue(t, db, dataSQL, saved)
	checksum := sha256.Sum256([]byte(momentumDatasetArtifactSchema))
	migrationValue(t, db, fmt.Sprintf("SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=%d", 10), fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", fmt.Sprint(11))
	migrationValue(t, db, "SELECT count(*) FROM momentum.dataset_artifacts", "0")
	after := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	if migrationState(t, db) != after {
		t.Fatal("repeat changed exact committed history")
	}
	migrationValue(t, db, dataSQL, saved)
}
