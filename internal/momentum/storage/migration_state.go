package storage

import (
	"context"
	"database/sql"
	"errors"
)

// ErrMigration обозначает безопасную ошибку состояния или транзакции миграций.
var ErrMigration = errors.New("cannot migrate Momentum PostgreSQL")

const momentumBootstrap = `CREATE SCHEMA IF NOT EXISTS momentum;
CREATE TABLE IF NOT EXISTS momentum.schema_migrations (
 version BIGINT PRIMARY KEY CHECK (version > 0),
 checksum BYTEA NOT NULL CHECK (pg_catalog.octet_length(checksum) = 32),
 applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

// migrationError сохраняет только безопасные категории ошибки и контекст.
func migrationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errors.Join(ErrMigration, ctx.Err())
	}
	if errors.Is(err, ErrMigrationVersion) {
		return ErrMigrationVersion
	}
	return ErrMigration
}

// prepareMigrationState проверяет bootstrap, сигнатуру и историю в заимствованной транзакции.
func prepareMigrationState(ctx context.Context, tx *sql.Tx, specs []migrationSpec) (int, error) {
	if tx == nil {
		return 0, migrationError(ctx, ErrMigration)
	}
	if _, err := tx.ExecContext(ctx, momentumBootstrap); err != nil {
		return 0, migrationError(ctx, err)
	}
	if err := validateMigrationTable(ctx, tx); err != nil {
		return 0, migrationError(ctx, err)
	}
	history, err := readMigrationHistory(ctx, tx)
	if err != nil {
		return 0, migrationError(ctx, err)
	}
	if err := validateMigrationHistory(specs, history); err != nil {
		return 0, migrationError(ctx, err)
	}
	if specs[0].sql != momentumBootstrap {
		return 0, ErrMigrationVersion
	}
	return len(history), nil
}

// readMigrationHistory читает упорядоченные записи до выполнения нового DDL.
func readMigrationHistory(ctx context.Context, tx *sql.Tx) ([]migrationRecord, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version, checksum FROM momentum.schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []migrationRecord
	for rows.Next() {
		var record migrationRecord
		if err = rows.Scan(&record.version, &record.checksum); err != nil {
			return nil, err
		}
		history = append(history, record)
	}
	return history, rows.Err()
}

// validateMigrationTable проверяет ограниченную сигнатуру существующей таблицы.
func validateMigrationTable(ctx context.Context, tx *sql.Tx) error {
	const query = `SELECT c.relkind = 'r'
 AND (SELECT count(*) = 3 AND bool_and(a.attnotnull AND
   ((a.attname = 'version' AND a.atttypid = 'pg_catalog.int8'::pg_catalog.regtype)
 OR (a.attname = 'checksum' AND a.atttypid = 'pg_catalog.bytea'::pg_catalog.regtype)
 OR (a.attname = 'applied_at' AND a.atttypid = 'pg_catalog.timestamptz'::pg_catalog.regtype)))
 FROM pg_catalog.pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped)
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k
 WHERE k.conrelid = c.oid AND k.contype = 'p' AND k.convalidated
 AND k.conkey = ARRAY[(SELECT attnum FROM pg_catalog.pg_attribute
 WHERE attrelid = c.oid AND attname = 'version')]::smallint[])
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k
 WHERE k.conrelid = c.oid AND k.contype = 'c' AND k.convalidated
 AND pg_catalog.pg_get_expr(k.conbin, k.conrelid) = '(version > 0)')
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k
 WHERE k.conrelid = c.oid AND k.contype = 'c' AND k.convalidated
 AND pg_catalog.pg_get_expr(k.conbin, k.conrelid) = '(octet_length(checksum) = 32)')
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_attrdef d
 JOIN pg_catalog.pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
 WHERE d.adrelid = c.oid AND a.attname = 'applied_at'
 AND pg_catalog.pg_get_expr(d.adbin, d.adrelid) = 'CURRENT_TIMESTAMP')
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'momentum' AND c.relname = 'schema_migrations'`
	var valid bool
	if err := tx.QueryRowContext(ctx, query).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrMigration
	}
	return nil
}
