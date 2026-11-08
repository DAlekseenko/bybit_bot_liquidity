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
)

// datasetLookup проверяет явную категорию ошибки и нулевой результат при отказе.
func datasetLookup(t *testing.T, db *sql.DB, environment, id string, want error) Dataset {
	t.Helper()
	got, err := ReadDataset(context.Background(), db, environment, id)
	migrationResult(t, err, want)
	if want != nil && (got != (Dataset{}) || !errors.Is(err, ErrDataset) || (want == ErrDataset && errors.Is(err, ErrDatasetNotFound))) {
		t.Fatalf("error returned partial dataset: %+v", got)
	}
	return got
}

// TestDatasetRegistry проверяет реальные SQL, provenance, namespace и неизменность.
func TestDatasetRegistry(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, RegisterDataset(ctx, db, "testnet", "before-schema"), ErrDataset)
	datasetLookup(t, db, "testnet", "before-schema", ErrDataset)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	db.SetMaxOpenConns(1)
	_, err := db.ExecContext(ctx, "SET TimeZone='Pacific/Honolulu'")
	migrationResult(t, err, nil)
	datasetLookup(t, db, "testnet", "shared", ErrDatasetNotFound)
	migrationResult(t, RegisterDataset(ctx, db, "testnet", "shared"), nil)
	datasetLookup(t, db, "mainnet", "shared", ErrDatasetNotFound)
	for _, key := range [][2]string{{"testnet", "shared"}, {"mainnet", "shared"},
		{"testnet", strings.Repeat("я", 128)}, {"testnet", "quoted' secret-marker"}, {"testnet", "\tid\t"}} {
		migrationResult(t, RegisterDataset(ctx, db, key[0], key[1]), nil)
		row := datasetLookup(t, db, key[0], key[1], nil)
		if row.Environment != key[0] || row.ID != key[1] || row.Source != "bybit_v5" || row.Category != "linear" ||
			row.CreatedAt.IsZero() || row.CreatedAt.Before(time.Unix(0, 0)) || row.CreatedAt.Location() != time.UTC {
			t.Fatalf("dataset=%+v", row)
		}
		before := migrationValue(t, db, `SELECT json_agg(r ORDER BY environment,dataset_id)::text
 FROM (SELECT *,xmin::text AS version,ctid::text AS locator FROM momentum.datasets) r`)
		migrationResult(t, RegisterDataset(ctx, db, key[0], key[1]), nil)
		if after := datasetLookup(t, db, key[0], key[1], nil); after != row || before != migrationValue(t, db, `SELECT json_agg(r ORDER BY environment,dataset_id)::text
 FROM (SELECT *,xmin::text AS version,ctid::text AS locator FROM momentum.datasets) r`) {
			t.Fatal("repeat updated immutable dataset row")
		}
	}
	datasetLookup(t, db, "testnet", "missing", ErrDatasetNotFound)
	migrationValue(t, db, "SELECT count(*) FROM momentum.datasets", "5")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "0")
	migrationResult(t, db.Close(), nil)
	migrationResult(t, RegisterDataset(ctx, db, "testnet", "closed"), ErrDataset)
	datasetLookup(t, db, "testnet", "closed", ErrDataset)
}

// TestDatasetArguments проверяет точные границы ключа без нормализации и context.
func TestDatasetArguments(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	for _, key := range [][2]string{{"", "id"}, {"TESTNET", "id"}, {" testnet", "id"}, {"live", "id"},
		{"testnet", ""}, {"testnet", " id"}, {"testnet", "id "}, {"testnet", " "},
		{"testnet", strings.Repeat("я", 129)}, {"testnet", "nul\x00id"}, {"testnet", "bad\xffid"}} {
		migrationResult(t, RegisterDataset(context.Background(), db, key[0], key[1]), ErrDatasetArgument)
		datasetLookup(t, db, key[0], key[1], ErrDatasetArgument)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.datasets", "0")
	migrationResult(t, RegisterDataset(context.Background(), nil, "testnet", "id"), ErrDatasetArgument)
	datasetLookup(t, nil, "testnet", "id", ErrDatasetArgument)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	migrationResult(t, RegisterDataset(ctx, nil, "testnet", "id"), context.Canceled)
	if row, err := ReadDataset(ctx, nil, "testnet", "id"); !errors.Is(err, context.Canceled) || row != (Dataset{}) {
		t.Fatalf("canceled row=%+v err=%v", row, err)
	}
}

// TestDatasetConcurrency проверяет два pool и реальную отмену заблокированных SQL.
func TestDatasetConcurrency(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")) // Opt-in уже проверен helper.
	u.Path, u.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	other, err := OpenMomentumPostgres(ctx, u.String())
	migrationResult(t, err, nil)
	defer other.Close()
	concurrent, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	barrier, results := make(chan struct{}), make(chan error, 2)
	for _, pool := range []*sql.DB{db, other} {
		// datasetWorker синхронизирует регистрацию через общий барьер.
		go func(pool *sql.DB) {
			<-barrier
			results <- RegisterDataset(concurrent, pool, "testnet", "concurrent")
		}(pool)
	}
	close(barrier)
	for range 2 {
		migrationResult(t, <-results, nil)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.datasets", "1")
	datasetLookup(t, other, "testnet", "concurrent", nil)
	holder, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(ctx, "LOCK TABLE momentum.datasets IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	for _, operation := range []func(context.Context) error{
		// registerBlocked ждёт отменяемую блокировку INSERT.
		func(ctx context.Context) error {
			return RegisterDataset(ctx, other, "testnet", "blocked")
		},
		// readBlocked ждёт отменяемую блокировку SELECT.
		func(ctx context.Context) error {
			_, err := ReadDataset(ctx, other, "testnet", "concurrent")
			return err
		},
	} {
		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		start := time.Now()
		err = operation(bounded)
		cancel()
		migrationResult(t, err, context.DeadlineExceeded)
		if !errors.Is(err, ErrDataset) || time.Since(start) > 2*time.Second {
			t.Fatalf("blocked SQL exceeded bound: %v", err)
		}
	}
	migrationResult(t, holder.Rollback(), nil)
	migrationResult(t, RegisterDataset(ctx, other, "testnet", "blocked"), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.datasets", "2")
}
