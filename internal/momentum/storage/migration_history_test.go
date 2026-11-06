package storage

import (
	"crypto/sha256"
	"errors"
	"testing"
)

// TestValidateMigrationHistory покрывает валидные каталоги и префиксы.
func TestValidateMigrationHistory(t *testing.T) {
	specs := []migrationSpec{{1, "SELECT 1"}, {2, "SELECT 2\n"}}
	for _, history := range [][]migrationRecord{
		nil,
		{{1, sum("SELECT 1")}},
		{{1, sum("SELECT 1")}, {2, sum("SELECT 2\n")}},
	} {
		for range 2 {
			if err := validateMigrationHistory(specs, history); err != nil {
				t.Fatalf("valid history rejected: %v", err)
			}
		}
	}
}

// sum возвращает SHA256 точных байтов SQL для теста.
func sum(sql string) []byte { checksum := sha256.Sum256([]byte(sql)); return checksum[:] }

// TestValidateMigrationHistoryRejectsCatalog проверяет весь каталог, включая хвост.
func TestValidateMigrationHistoryRejectsCatalog(t *testing.T) {
	for _, specs := range [][]migrationSpec{nil, {{0, "x"}}, {{-1, "x"}}, {{2, "x"}}, {{1, "x"}, {1, "y"}}, {{1, "x"}, {3, "y"}}, {{2, "x"}, {1, "y"}}, {{1, ""}}, {{1, " "}}, {{1, "x"}, {2, "\n\t"}}} {
		for _, history := range [][]migrationRecord{nil, {{1, sum("x")}}} {
			if err := validateMigrationHistory(specs, history); !errors.Is(err, ErrMigrationVersion) {
				t.Fatalf("catalog=%v history=%v err=%v", specs, history, err)
			}
		}
	}
}

// TestValidateMigrationHistoryRejectsHistory проверяет версии и размеры checksum.
func TestValidateMigrationHistoryRejectsHistory(t *testing.T) {
	specs := []migrationSpec{{1, "x"}, {2, "y"}}
	bad := [][]migrationRecord{{{0, sum("x")}}, {{-1, sum("x")}}, {{2, sum("x")}}, {{1, sum("x")}, {1, sum("y")}}, {{2, sum("y")}, {1, sum("x")}}, {{1, sum("x")}, {3, sum("y")}}, {{1, sum("x")}, {2, sum("y")}, {3, sum("z")}}, {{1, sum("changed")}}}
	for _, size := range []int{0, 31, 33} {
		bad = append(bad, []migrationRecord{{1, make([]byte, size)}})
	}
	for _, history := range bad {
		if err := validateMigrationHistory(specs, history); !errors.Is(err, ErrMigrationVersion) {
			t.Fatalf("history=%v err=%v", history, err)
		}
	}
	if err := validateMigrationHistory([]migrationSpec{{1, "x\n"}}, []migrationRecord{{1, sum("x")}}); !errors.Is(err, ErrMigrationVersion) {
		t.Fatal("SQL newline was normalized")
	}
}

// TestValidateMigrationHistoryPreservesInputs проверяет байты и стабильную ошибку.
func TestValidateMigrationHistoryPreservesInputs(t *testing.T) {
	sql := []byte("x")
	checksum := sum("x")
	specs := []migrationSpec{{1, string(sql)}}
	history := []migrationRecord{{1, checksum}}
	specsBefore := append([]migrationSpec(nil), specs...)
	originalChecksum := append([]byte(nil), checksum...)
	versionBefore := history[0].version
	if err := validateMigrationHistory(specs, history); err != nil {
		t.Fatal(err)
	}
	if specs[0] != specsBefore[0] || history[0].version != versionBefore || string(originalChecksum) != string(history[0].checksum) {
		t.Fatal("successful validation changed input slices or checksum bytes")
	}
	history[0].checksum[0] ^= 1
	before := append([]byte(nil), history[0].checksum...)
	failedVersion := history[0].version
	err1 := validateMigrationHistory(specs, history)
	err2 := validateMigrationHistory(specs, history)
	if !errors.Is(err1, ErrMigrationVersion) || err1.Error() != err2.Error() || err1.Error() != "invalid migration version or history" {
		t.Fatalf("unstable/sensitive error: %v / %v", err1, err2)
	}
	if string(sql) != "x" || specs[0] != specsBefore[0] || history[0].version != failedVersion || string(before) != string(history[0].checksum) {
		t.Fatal("validation changed input slices or backing bytes")
	}
}
