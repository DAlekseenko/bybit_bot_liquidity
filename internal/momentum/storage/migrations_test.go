package storage

import (
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
 bool_and(applied_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00' AND isfinite(applied_at))::text FROM momentum.schema_migrations`, fmt.Sprintf("8:1:%x:true", checksum))
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
