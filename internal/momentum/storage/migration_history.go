package storage

import (
	"crypto/sha256"
	"errors"
	"strings"
)

// ErrMigrationVersion обозначает некорректный каталог или историю миграций.
var ErrMigrationVersion = errors.New("invalid migration version or history")

type migrationSpec struct {
	version int64
	sql     string
}

type migrationRecord struct {
	version  int64
	checksum []byte
}

// validateMigrationHistory проверяет каталог и точный префикс применённых миграций.
func validateMigrationHistory(specs []migrationSpec, history []migrationRecord) error {
	if len(specs) == 0 || len(history) > len(specs) {
		return ErrMigrationVersion
	}
	for i, spec := range specs {
		if spec.version != int64(i+1) || strings.TrimSpace(spec.sql) == "" {
			return ErrMigrationVersion
		}
	}
	for i, record := range history {
		if record.version != int64(i+1) || len(record.checksum) != sha256.Size {
			return ErrMigrationVersion
		}
		checksum := sha256.Sum256([]byte(specs[i].sql))
		if !equalChecksum(record.checksum, checksum[:]) {
			return ErrMigrationVersion
		}
	}
	return nil
}

// equalChecksum сравнивает контрольные суммы без изменения входных байтов.
func equalChecksum(got, want []byte) bool {
	var diff byte
	for i := range want {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}
