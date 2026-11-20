package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// artifactHash возвращает SHA-256 точных тестовых байтов.
func artifactHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// artifactTx создаёт транзакцию; тест отвечает за Commit при необходимости.
func artifactTx(t *testing.T, db *sql.DB, opts *sql.TxOptions) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), opts)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// artifactFailure проверяет типизированную ошибку без частичного результата и утечки SQL.
func artifactFailure(t *testing.T, got StoredDatasetArtifact, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || got.SHA256 != "" || got.Payload != nil || !got.CreatedAt.IsZero() {
		t.Fatalf("nonempty result or wrong category: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive_sql_marker") {
		t.Fatal("SQL diagnostic leaked")
	}
}

// TestDatasetArtifactProjection проверяет validation и копирование без PostgreSQL.
func TestDatasetArtifactProjection(t *testing.T) {
	payload := []byte{0, 255, '\n'}
	hash := artifactHash(payload)
	key, err := datasetArtifactKey(hash)
	migrationResult(t, err, nil)
	for _, bad := range []string{"", "zz", strings.ToUpper(hash), hash[1:], hash + "00"} {
		_, err = datasetArtifactKey(bad)
		migrationResult(t, err, ErrDatasetArtifactArgument)
	}
	stamp := sql.NullTime{Time: time.Unix(0, 123000).In(time.FixedZone("offset", 19800)), Valid: true}
	got, err := checkedDatasetArtifact(key, key, payload, stamp)
	migrationResult(t, err, nil)
	if got.SHA256 != hash || !bytes.Equal(got.Payload, payload) || got.CreatedAt.Location() != time.UTC {
		t.Fatal("projection changed bytes, digest or timezone")
	}
	got.Payload[0] = 1
	if payload[0] != 0 {
		t.Fatal("projection shares mutable bytes")
	}
	for _, tc := range []struct {
		key, stored, payload []byte
		stamp                sql.NullTime
	}{
		{nil, key, payload, stamp}, {key, nil, payload, stamp}, {key, key, nil, stamp},
		{key, key, []byte("wrong"), stamp}, {key, key, payload, sql.NullTime{}},
		{key, key, payload, sql.NullTime{Time: time.Unix(-1, 0), Valid: true}},
	} {
		got, err = checkedDatasetArtifact(tc.key, tc.stored, tc.payload, tc.stamp)
		artifactFailure(t, got, err, ErrDatasetArtifactCorrupt)
	}
	expired, finish := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer finish()
	got, err = ReadDatasetArtifactTx(expired, &sql.Tx{}, hash)
	artifactFailure(t, got, err, context.DeadlineExceeded)
	got, err = WriteDatasetArtifactTx(expired, &sql.Tx{}, hash, payload)
	artifactFailure(t, got, err, context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx     context.Context
		tx      *sql.Tx
		hash    string
		payload []byte
		want    error
	}{
		{nil, &sql.Tx{}, hash, payload, ErrDatasetArtifactArgument},
		{ctx, nil, hash, payload, ErrDatasetArtifactArgument},
		{ctx, &sql.Tx{}, "wrong", payload, ErrDatasetArtifactArgument},
		{ctx, &sql.Tx{}, hash, nil, ErrDatasetArtifactArgument},
		{ctx, &sql.Tx{}, hash, []byte("wrong"), ErrDatasetArtifactArgument},
		{ctx, &sql.Tx{}, hash, payload, context.Canceled},
	} {
		got, err = WriteDatasetArtifactTx(tc.ctx, tc.tx, tc.hash, tc.payload)
		artifactFailure(t, got, err, tc.want)
	}
	for _, tc := range []struct {
		ctx  context.Context
		tx   *sql.Tx
		hash string
		want error
	}{
		{nil, &sql.Tx{}, hash, ErrDatasetArtifactArgument}, {ctx, nil, hash, ErrDatasetArtifactArgument},
		{ctx, &sql.Tx{}, "wrong", ErrDatasetArtifactArgument}, {ctx, &sql.Tx{}, hash, context.Canceled},
	} {
		got, err = ReadDatasetArtifactTx(tc.ctx, tc.tx, tc.hash)
		artifactFailure(t, got, err, tc.want)
	}
}

// TestDatasetArtifactRepository проверяет idempotence и caller-owned atomicity.
func TestDatasetArtifactRepository(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	payload := bytes.Repeat([]byte{0, 255, '\n'}, 350000)
	hash := artifactHash(payload)
	tx := artifactTx(t, db, nil)
	got, err := WriteDatasetArtifactTx(ctx, tx, hash, payload)
	migrationResult(t, err, nil)
	if got.SHA256 != hash || !bytes.Equal(got.Payload, payload) || got.CreatedAt.Location() != time.UTC {
		t.Fatal("write readback differed")
	}
	var xmin string
	migrationResult(t, tx.QueryRowContext(ctx, "SELECT xmin::text FROM momentum.dataset_artifacts").Scan(&xmin), nil)
	again, err := WriteDatasetArtifactTx(ctx, tx, hash, payload)
	migrationResult(t, err, nil)
	if !again.CreatedAt.Equal(got.CreatedAt) || !bytes.Equal(again.Payload, payload) {
		t.Fatal("duplicate replaced artifact")
	}
	var secondXmin string
	migrationResult(t, tx.QueryRowContext(ctx, "SELECT xmin::text FROM momentum.dataset_artifacts").Scan(&secondXmin), nil)
	if xmin != secondXmin {
		t.Fatal("duplicate issued UPDATE")
	}
	outside := artifactTx(t, db, nil)
	absent, err := ReadDatasetArtifactTx(ctx, outside, hash)
	artifactFailure(t, absent, err, ErrDatasetArtifactAbsent)
	migrationResult(t, outside.Rollback(), nil)
	migrationResult(t, tx.Commit(), nil)
	ro := artifactTx(t, db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	exact, err := ReadDatasetArtifactTx(ctx, ro, hash)
	migrationResult(t, err, nil)
	if !bytes.Equal(exact.Payload, payload) || !exact.CreatedAt.Equal(got.CreatedAt) {
		t.Fatal("committed artifact changed")
	}
	migrationResult(t, ro.Commit(), nil)
	group := artifactTx(t, db, nil)
	for _, p := range [][]byte{[]byte("content"), []byte("manifest")} {
		_, err = WriteDatasetArtifactTx(ctx, group, artifactHash(p), p)
		migrationResult(t, err, nil)
	}
	migrationResult(t, group.Rollback(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.dataset_artifacts", "1")
	committed := artifactTx(t, db, nil)
	for _, p := range [][]byte{[]byte("content"), []byte("manifest")} {
		_, err = WriteDatasetArtifactTx(ctx, committed, artifactHash(p), p)
		migrationResult(t, err, nil)
	}
	migrationResult(t, committed.Commit(), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.dataset_artifacts", "3")
}

// TestDatasetArtifactRepositoryFailures проверяет SQL failure, corruption и lifecycle caller.
func TestDatasetArtifactRepositoryFailures(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	payload := []byte("sensitive_sql_marker")
	hash := artifactHash(payload)
	missing := artifactTx(t, db, nil)
	got, err := ReadDatasetArtifactTx(ctx, missing, hash)
	artifactFailure(t, got, err, ErrDatasetArtifactUnavailable)
	migrationResult(t, missing.Rollback(), nil)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	for _, readonly := range []bool{true, false} {
		tx := artifactTx(t, db, &sql.TxOptions{ReadOnly: readonly})
		if !readonly {
			migrationResult(t, tx.Rollback(), nil)
		}
		got, err = WriteDatasetArtifactTx(ctx, tx, hash, payload)
		artifactFailure(t, got, err, ErrDatasetArtifactUnavailable)
		got, err = ReadDatasetArtifactTx(ctx, tx, hash)
		artifactFailure(t, got, err, ErrDatasetArtifactUnavailable)
	}
	tx := artifactTx(t, db, nil)
	got, err = WriteDatasetArtifactTx(ctx, tx, hash, nil)
	artifactFailure(t, got, err, ErrDatasetArtifactArgument)
	_, err = WriteDatasetArtifactTx(ctx, tx, hash, payload)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	marketStatement(t, db, `ALTER TABLE momentum.dataset_artifacts DISABLE TRIGGER dataset_artifacts_immutable;
 ALTER TABLE momentum.dataset_artifacts DROP CONSTRAINT dataset_artifacts_created_at_check;
 UPDATE momentum.dataset_artifacts SET created_at='infinity'`, true)
	invalid := artifactTx(t, db, nil)
	got, err = ReadDatasetArtifactTx(ctx, invalid, hash)
	artifactFailure(t, got, err, ErrDatasetArtifactCorrupt)
	got, err = WriteDatasetArtifactTx(ctx, invalid, hash, payload)
	artifactFailure(t, got, err, ErrDatasetArtifactCorrupt)
	migrationResult(t, invalid.Rollback(), nil)
	marketStatement(t, db, `UPDATE momentum.dataset_artifacts SET created_at='epoch';
 ALTER TABLE momentum.dataset_artifacts DROP CONSTRAINT dataset_artifacts_check;
 UPDATE momentum.dataset_artifacts SET payload='different'`, true)
	invalid = artifactTx(t, db, nil)
	got, err = ReadDatasetArtifactTx(ctx, invalid, hash)
	artifactFailure(t, got, err, ErrDatasetArtifactCorrupt)
	migrationResult(t, invalid.Rollback(), nil)
	marketStatement(t, db, `CREATE FUNCTION momentum.artifact_error_proof() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END $$;
 CREATE TRIGGER artifact_error_proof BEFORE INSERT ON momentum.dataset_artifacts FOR EACH STATEMENT EXECUTE FUNCTION momentum.artifact_error_proof()`, true)
	blocked := artifactTx(t, db, nil)
	got, err = WriteDatasetArtifactTx(ctx, blocked, hash, payload)
	artifactFailure(t, got, err, ErrDatasetArtifactUnavailable)
}

// artifactWaitLock наблюдает настоящее ожидание INSERT вместо предположения по sleep.
func artifactWaitLock(t *testing.T, db *sql.DB, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		migrationResult(t, db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')", pid).Scan(&waiting), nil)
		if waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("concurrent INSERT never waited on winner")
}

// TestDatasetArtifactRepositoryConcurrent проверяет commit/rollback/cancel и старый RR snapshot.
func TestDatasetArtifactRepositoryConcurrent(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	dsn, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	migrationResult(t, err, nil)
	dsn.Path, dsn.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	other, err := OpenMomentumPostgres(ctx, dsn.String())
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = other.Close() })
	for _, mode := range []string{"commit", "rollback", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte(mode)
			hash := artifactHash(payload)
			winner := artifactTx(t, db, nil)
			first, err := WriteDatasetArtifactTx(ctx, winner, hash, payload)
			migrationResult(t, err, nil)
			waiter := artifactTx(t, other, nil)
			var pid int
			migrationResult(t, waiter.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
			call, cancel := context.WithCancel(ctx)
			defer cancel()
			type result struct {
				got StoredDatasetArtifact
				err error
			}
			done := make(chan result, 1)
			go func() { got, err := WriteDatasetArtifactTx(call, waiter, hash, payload); done <- result{got, err} }()
			artifactWaitLock(t, db, pid)
			switch mode {
			case "cancel":
				cancel()
			case "rollback":
				migrationResult(t, winner.Rollback(), nil)
			default:
				migrationResult(t, winner.Commit(), nil)
			}
			var next result
			select {
			case next = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("waiter never finished")
			}
			if mode == "cancel" {
				artifactFailure(t, next.got, next.err, context.Canceled)
				migrationResult(t, winner.Rollback(), nil)
				return
			}
			migrationResult(t, next.err, nil)
			if !bytes.Equal(next.got.Payload, payload) || mode == "commit" && !next.got.CreatedAt.Equal(first.CreatedAt) {
				t.Fatal("concurrent duplicate changed original")
			}
			migrationResult(t, waiter.Commit(), nil)
		})
	}
	old := artifactTx(t, db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	var count int
	migrationResult(t, old.QueryRow("SELECT count(*) FROM momentum.dataset_artifacts").Scan(&count), nil)
	payload := []byte("snapshot")
	tx := artifactTx(t, db, nil)
	_, err = WriteDatasetArtifactTx(ctx, tx, artifactHash(payload), payload)
	migrationResult(t, err, nil)
	migrationResult(t, tx.Commit(), nil)
	got, err := WriteDatasetArtifactTx(ctx, old, artifactHash(payload), payload)
	artifactFailure(t, got, err, ErrDatasetArtifactUnavailable)
}
