package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// preflightSnapshot фиксирует bounded metadata и все строки без выдуманного pg_get_tabledef.
func preflightSnapshot(t *testing.T, tx *sql.Tx) string {
	t.Helper()
	const query = `SELECT json_build_array(c.relkind,
 (SELECT json_agg(json_build_array(a.attname,a.atttypid,a.attnotnull,a.attisdropped) ORDER BY a.attnum)
  FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0),
 (SELECT json_agg(json_build_array(k.conname,k.convalidated,pg_catalog.pg_get_constraintdef(k.oid,false)) ORDER BY k.conname)
  FROM pg_catalog.pg_constraint k WHERE k.conrelid=c.oid),
 (SELECT json_agg(json_build_array(d.adnum,pg_catalog.pg_get_expr(d.adbin,d.adrelid,false)) ORDER BY d.adnum)
  FROM pg_catalog.pg_attrdef d WHERE d.adrelid=c.oid),
 (SELECT json_agg(r) FROM momentum.schema_migrations r), to_regclass('momentum.pending_marker'))::text
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='momentum' AND c.relname='schema_migrations'`
	var got string
	if err := tx.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// preflightTx создаёт заимствованную READ COMMITTED transaction с тем же advisory lock.
func preflightTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	// Владелец fixture выполняет отложенный rollback тестовой транзакции.
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.ExecContext(context.Background(), "SELECT pg_catalog.pg_advisory_xact_lock(651010)"); err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestMigrationPreflight проверяет реальные signatures, историю и отсутствие ремонта или pending writes.
func TestMigrationPreflight(t *testing.T) {
	checksum := sha256.Sum256([]byte(momentumBootstrap))
	if fmt.Sprintf("%x", checksum) != "62af123c38c34cc6a0324a118a11648136338f9eb5c1a5a0b22ba7618f3a9459" {
		t.Fatal("bootstrap payload changed")
	}
	partitioned := "DROP TABLE momentum.schema_migrations;" + strings.TrimSuffix(momentumBootstrap, ")") + ") PARTITION BY RANGE(version)"
	standard := []migrationSpec{{1, momentumBootstrap}, {2, "CREATE TABLE momentum.pending_marker(id bigint)"}}
	insert := "INSERT INTO momentum.schema_migrations(version,checksum) VALUES (%d,decode('%s','hex'))"
	for _, tc := range []struct {
		name, fixture string
		applied       int
		want          error
		specs         []migrationSpec
	}{
		{"empty", "", 0, nil, standard},
		{"extra index", "CREATE INDEX extra_idx ON momentum.schema_migrations(checksum)", 0, nil, standard},
		{"version type", "ALTER TABLE momentum.schema_migrations ALTER COLUMN version TYPE integer", 0, ErrMigration, standard},
		{"checksum type", "ALTER TABLE momentum.schema_migrations ALTER COLUMN checksum TYPE text USING encode(checksum,'hex')", 0, ErrMigration, standard},
		{"applied type", "ALTER TABLE momentum.schema_migrations ALTER COLUMN applied_at TYPE timestamp", 0, ErrMigration, standard},
		{"missing column", "ALTER TABLE momentum.schema_migrations DROP COLUMN applied_at", 0, ErrMigration, standard},
		{"extra column", "ALTER TABLE momentum.schema_migrations ADD COLUMN extra text", 0, ErrMigration, standard},
		{"wrong name", "ALTER TABLE momentum.schema_migrations RENAME COLUMN version TO revision", 0, ErrMigration, standard},
		{"nullable checksum", "ALTER TABLE momentum.schema_migrations ALTER COLUMN checksum DROP NOT NULL", 0, ErrMigration, standard},
		{"nullable applied", "ALTER TABLE momentum.schema_migrations ALTER COLUMN applied_at DROP NOT NULL", 0, ErrMigration, standard},
		{"missing PK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_pkey", 0, ErrMigration, standard},
		{"wrong PK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_pkey, ADD PRIMARY KEY(checksum)", 0, ErrMigration, standard},
		{"wrong positive CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_version_check, ADD CHECK(version>=0)", 0, ErrMigration, standard},
		{"wrong length CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_checksum_check, ADD CHECK(pg_catalog.octet_length(checksum)=16)", 0, ErrMigration, standard},
		{"unvalidated CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_version_check, ADD CHECK(version>0) NOT VALID", 0, ErrMigration, standard},
		{"unvalidated checksum CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_checksum_check, ADD CHECK(pg_catalog.octet_length(checksum)=32) NOT VALID", 0, ErrMigration, standard},
		{"partitioned", partitioned, 0, ErrMigration, standard},
		{"positive CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_version_check", 0, ErrMigration, standard},
		{"length CHECK", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_checksum_check", 0, ErrMigration, standard},
		{"missing default", "ALTER TABLE momentum.schema_migrations ALTER COLUMN applied_at DROP DEFAULT", 0, ErrMigration, standard},
		{"wrong default", "ALTER TABLE momentum.schema_migrations ALTER COLUMN applied_at SET DEFAULT '2000-01-01'::timestamptz", 0, ErrMigration, standard},
		{"view", "DROP TABLE momentum.schema_migrations; CREATE VIEW momentum.schema_migrations AS SELECT 1::bigint version, ''::bytea checksum, CURRENT_TIMESTAMP applied_at", 0, ErrMigration, standard},
		{"future", fmt.Sprintf(insert, 9, fmt.Sprintf("%x", checksum)), 0, ErrMigrationVersion, standard},
		{"gap", fmt.Sprintf(insert, 2, fmt.Sprintf("%x", checksum)), 0, ErrMigrationVersion, standard},
		{"checksum", fmt.Sprintf(insert, 1, fmt.Sprintf("%064x", 0)), 0, ErrMigrationVersion, standard},
		{"prefix", fmt.Sprintf(insert, 1, fmt.Sprintf("%x", checksum)), 1, nil, standard},
		{"bootstrap bytes", "", 0, ErrMigrationVersion, append([]migrationSpec{{1, momentumBootstrap + " "}}, standard[1:]...)},
		{"nil catalog", "", 0, ErrMigrationVersion, nil},
		{"empty catalog", "", 0, ErrMigrationVersion, []migrationSpec{}},
		{"invalid catalog", "", 0, ErrMigrationVersion, []migrationSpec{{2, momentumBootstrap}}},
	} {
		db := openTestPostgres(t)
		if _, err := db.ExecContext(context.Background(), momentumBootstrap+";"+tc.fixture); err != nil {
			t.Fatalf("fixture=%s err=%v", tc.name, err)
		}
		tx := preflightTx(t, db)
		before := preflightSnapshot(t, tx)
		got, err := prepareMigrationState(context.Background(), tx, tc.specs)
		if got != tc.applied || err != tc.want || preflightSnapshot(t, tx) != before {
			t.Fatalf("case=%s applied=%d err=%v", tc.name, got, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigrationPreflightOwnership проверяет bootstrap и владение transaction только вызывающим кодом.
func TestMigrationPreflightOwnership(t *testing.T) {
	db := openTestPostgres(t)
	tx := preflightTx(t, db)
	specs := []migrationSpec{{1, momentumBootstrap}}
	if got, err := prepareMigrationState(context.Background(), nil, specs); got != 0 || err != ErrMigration {
		t.Fatalf("nil transaction applied=%d err=%v", got, err)
	}
	for range 2 {
		if got, err := prepareMigrationState(context.Background(), tx, specs); got != 0 || err != nil {
			t.Fatalf("fresh/repeat applied=%d err=%v", got, err)
		}
	}
	var outside int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'").Scan(&outside); err != nil || outside != 0 {
		t.Fatalf("preflight committed caller transaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'").Scan(&outside); err != nil || outside != 0 {
		t.Fatalf("caller rollback leaked bootstrap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := preflightTx(t, db)
	for _, transaction := range []*sql.Tx{nil, tx, live} {
		got, err := prepareMigrationState(ctx, transaction, specs)
		if got != 0 || !errors.Is(err, ErrMigration) || !errors.Is(err, context.Canceled) || err.Error() != errors.Join(ErrMigration, context.Canceled).Error() {
			t.Fatalf("unsafe or nonzero failure applied=%d err=%v", got, err)
		}
	}
}

// TestMigrationErrorPolicy проверяет точную безопасную классификацию, контекст и nil-precedence.
func TestMigrationErrorPolicy(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	raw := errors.New("postgres://u:secret-marker@localhost/db; sensitive_sql_marker; pq: detail")
	wrapped := fmt.Errorf("sensitive_sql_marker postgres://u:secret-marker@localhost/db: %w", ErrMigrationVersion)
	deadline := errors.Join(ErrMigration, context.DeadlineExceeded)
	for _, tc := range []struct {
		name        string
		ctx         context.Context
		input, want error
		exact       bool
	}{
		{"nil", context.Background(), nil, nil, true},
		{"nil with expired context", expired, nil, nil, true},
		{"raw diagnostics", context.Background(), raw, ErrMigration, true},
		{"version", context.Background(), ErrMigrationVersion, ErrMigrationVersion, true},
		{"wrapped version diagnostics", context.Background(), wrapped, ErrMigrationVersion, true},
		{"deadline over raw", expired, raw, deadline, false},
		{"deadline over version", expired, ErrMigrationVersion, deadline, false},
		{"deadline over wrapped diagnostics", expired, wrapped, deadline, false},
	} {
		got := migrationError(tc.ctx, tc.input)
		if tc.exact && got != tc.want || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("case=%s got=%v want=%v", tc.name, got, tc.want)
		}
		for _, category := range []error{ErrMigration, ErrMigrationVersion, context.Canceled, context.DeadlineExceeded} {
			if errors.Is(got, category) != errors.Is(tc.want, category) {
				t.Fatalf("case=%s category=%v got=%v", tc.name, category, got)
			}
		}
	}
}
