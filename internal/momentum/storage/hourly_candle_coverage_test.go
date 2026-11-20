package storage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

// hourlyCoverageRows создаёт только проверяемую проекцию timestamp/confirmed.
func hourlyCoverageRows(start time.Time, count int) []StoredCandle {
	rows := make([]StoredCandle, count)
	for i := range rows {
		rows[i].OpenTime, rows[i].Confirmed = start.Add(time.Duration(i)*time.Hour), true
	}
	return rows
}

// TestHourlyCandleCoverage проверяет полное окно 1080h и все отдельные состояния.
func TestHourlyCandleCoverage(t *testing.T) {
	start := time.Unix(86400, 0).UTC()
	end := start.Add(1080 * time.Hour)
	key := CandleStream{"mainnet", "materialized", "HYPEUSDT", "60"}
	for _, tc := range []struct {
		name   string
		want   HourlyCandleCoverageStatus
		change func([]StoredCandle) []StoredCandle
	}{
		{"covered", HourlyCandleCovered, func(rows []StoredCandle) []StoredCandle { return rows }},
		{"empty", HourlyCandleEmpty, func(rows []StoredCandle) []StoredCandle { return nil }},
		{"short-prefix", HourlyCandleShort, func(rows []StoredCandle) []StoredCandle { return rows[1:] }},
		{"internal-gap", HourlyCandleGap, func(rows []StoredCandle) []StoredCandle { return slices.Delete(rows, 500, 501) }},
		{"missing-tail", HourlyCandleGap, func(rows []StoredCandle) []StoredCandle { return rows[:len(rows)-1] }},
		{"unconfirmed", HourlyCandleUnconfirmed, func(rows []StoredCandle) []StoredCandle {
			rows[500].Confirmed = false
			return rows
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := tc.change(hourlyCoverageRows(start, 1080))
			before := slices.Clone(rows)
			zone := time.FixedZone("east", 3*3600)
			got, err := CheckHourlyCandleCoverage(key, start.In(zone), end.In(zone), rows)
			if err != nil || got != tc.want || !reflect.DeepEqual(rows, before) {
				t.Fatalf("status=%q want=%q error=%v", got, tc.want, err)
			}
		})
	}
	key.Environment = "testnet"
	if got, err := CheckHourlyCandleCoverage(key, start, start.Add(time.Hour), hourlyCoverageRows(start, 1)); err != nil || got != HourlyCandleCovered {
		t.Fatalf("single hour=%q %v", got, err)
	}
}

// TestHourlyCandleCoverageRejects проверяет сетку, порядок, рамки и отсутствие частичного статуса.
func TestHourlyCandleCoverageRejects(t *testing.T) {
	start := time.Unix(86400, 0).UTC()
	end := start.Add(3 * time.Hour)
	key := CandleStream{"testnet", "materialized", "BTCUSDT", "60"}
	for _, change := range []func([]StoredCandle){
		func(rows []StoredCandle) { rows[1].OpenTime = rows[0].OpenTime },
		func(rows []StoredCandle) { slices.Reverse(rows) },
		func(rows []StoredCandle) { rows[0].OpenTime = start.Add(-time.Hour) },
		func(rows []StoredCandle) { rows[2].OpenTime = end },
		func(rows []StoredCandle) { rows[1].OpenTime = rows[1].OpenTime.Add(time.Nanosecond) },
		func(rows []StoredCandle) {
			rows[0].Confirmed = false
			rows[2].OpenTime = end
		},
	} {
		rows := hourlyCoverageRows(start, 3)
		change(rows)
		status, err := CheckHourlyCandleCoverage(key, start, end, rows)
		if status != "" || !errors.Is(err, ErrCandles) || !errors.Is(err, ErrCandleArgument) {
			t.Fatalf("status=%q error=%v", status, err)
		}
	}
	for _, tc := range []struct {
		start, end time.Time
		rows       []StoredCandle
	}{
		{start, start, nil}, {end, start, nil}, {time.UnixMilli(-1), end, nil},
		{start.Add(time.Minute), end, nil}, {start, end.Add(time.Nanosecond), nil},
		{start, end, hourlyCoverageRows(start, 4)}, {start, time.Unix(1<<63-1, 0), nil},
	} {
		status, err := CheckHourlyCandleCoverage(key, tc.start, tc.end, tc.rows)
		if status != "" || !errors.Is(err, ErrCandleArgument) {
			t.Fatalf("status=%q error=%v", status, err)
		}
	}
	for _, bad := range []CandleStream{
		{"paper", "materialized", "BTCUSDT", "60"}, {"testnet", "", "BTCUSDT", "60"},
		{"testnet", "materialized", "btcusdt", "60"}, {"testnet", "materialized", "BTCUSDT", "D"},
	} {
		status, err := CheckHourlyCandleCoverage(bad, start, end, nil)
		if status != "" || !errors.Is(err, ErrCandleArgument) {
			t.Fatalf("status=%q error=%v", status, err)
		}
	}
	upper := time.Date(294276, 12, 31, 23, 0, 0, 0, time.UTC)
	if status, err := CheckHourlyCandleCoverage(key, time.Unix(0, 0), upper, nil); err != nil || status != HourlyCandleEmpty {
		t.Fatalf("large finite range=%q error=%v", status, err)
	}
}

// hourlyCoverageProjection читает coverage-поля одной явно заданной membership в caller-owned snapshot.
func hourlyCoverageProjection(t *testing.T, tx *sql.Tx, key CandleStream, start, end time.Time) []StoredCandle {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), `SELECT open_time,confirmed FROM momentum.candles
 WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
 AND open_time >= $5 AND open_time < $6 ORDER BY open_time`, key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, start, end)
	migrationResult(t, err, nil)
	defer rows.Close()
	result := make([]StoredCandle, 0)
	for rows.Next() {
		var row StoredCandle
		migrationResult(t, rows.Scan(&row.OpenTime, &row.Confirmed), nil)
		row.OpenTime = row.OpenTime.UTC()
		result = append(result, row)
	}
	migrationResult(t, rows.Err(), nil)
	return result
}

// TestHourlyCandleCoverageSnapshot доказывает 1080h из одного RR snapshot и отличие после поздней вставки.
func TestHourlyCandleCoverageSnapshot(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	db.SetMaxOpenConns(3)
	for _, environment := range []string{"mainnet", "testnet"} {
		migrationResult(t, RegisterDataset(t.Context(), db, environment, "materialized"), nil)
	}
	start := time.Unix(86400, 0).UTC()
	end := start.Add(1080 * time.Hour)
	key := CandleStream{"mainnet", "materialized", "HYPEUSDT", "60"}
	_, err := db.ExecContext(t.Context(), `INSERT INTO momentum.candles SELECT 'mainnet','materialized','HYPEUSDT','60',
 $1::timestamptz+n*INTERVAL '1 hour',1,1,1,1,0,true FROM generate_series(-1,1080) n WHERE n<>500`, start)
	migrationResult(t, err, nil)
	marketStatement(t, db, "INSERT INTO momentum.candles VALUES('testnet','materialized','HYPEUSDT','60','1970-01-02+00',1,1,1,1,0,false)", true)
	rows, err := ReadCandleRange(t.Context(), db, key, start, end, 200)
	if rows != nil || !errors.Is(err, ErrCandleLimit) {
		t.Fatalf("old bounded reader result=%v error=%v", rows, err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	migrationResult(t, err, nil)
	defer tx.Rollback()
	var source, category string
	migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT source,category FROM momentum.datasets WHERE environment=$1 AND dataset_id=$2", key.Environment, key.DatasetID).Scan(&source, &category), nil)
	if source != "bybit_v5" || category != "linear" {
		t.Fatal("unexpected provenance")
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO momentum.candles VALUES('mainnet','materialized','HYPEUSDT','60',$1,1,1,1,1,0,true)`, start.Add(500*time.Hour))
	migrationResult(t, err, nil)
	oldRows := hourlyCoverageProjection(t, tx, key, start, end)
	migrationResult(t, tx.Commit(), nil)
	status, err := CheckHourlyCandleCoverage(key, start, end, oldRows)
	if err != nil || status != HourlyCandleGap || len(oldRows) != 1079 {
		t.Fatalf("old snapshot count=%d status=%q error=%v", len(oldRows), status, err)
	}
	fresh, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	migrationResult(t, err, nil)
	defer fresh.Rollback()
	currentRows := hourlyCoverageProjection(t, fresh, key, start, end)
	migrationResult(t, fresh.Commit(), nil)
	status, err = CheckHourlyCandleCoverage(key, start, end, currentRows)
	if err != nil || status != HourlyCandleCovered || len(currentRows) != 1080 || currentRows[0].OpenTime != start || currentRows[1079].OpenTime != end.Add(-time.Hour) {
		t.Fatalf("fresh snapshot count=%d status=%q error=%v", len(currentRows), status, err)
	}
}
