package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
)

// MigrateMomentumPostgres применяет встроенный каталог в одной транзакции.
func MigrateMomentumPostgres(ctx context.Context, db *sql.DB) error {
	return migrateMomentumPostgres(ctx, db, []migrationSpec{
		{version: 1, sql: momentumBootstrap},
		{version: 2, sql: momentumMarketCandlesSchema},
		{version: 3, sql: momentumMarketSchema},
		{version: 4, sql: momentumImportProgressSchema},
		{version: 5, sql: momentumSignalJournalSchema},
		{version: 6, sql: momentumConfigSnapshotSchema},
		{version: 7, sql: momentumRebalanceRunSchema},
		{version: 8, sql: momentumPlanRevisionSchema},
	})
}

// migrateMomentumPostgres сериализует проверку истории и применение суффикса.
func migrateMomentumPostgres(ctx context.Context, db *sql.DB, specs []migrationSpec) error {
	if db == nil {
		return migrationError(ctx, ErrMigration)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return migrationError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(651010)"); err != nil {
		return migrationError(ctx, err)
	}
	applied, err := prepareMigrationState(ctx, tx, specs)
	if err != nil {
		return migrationError(ctx, err)
	}
	for _, spec := range specs[applied:] {
		if _, err = tx.ExecContext(ctx, spec.sql); err != nil {
			return migrationError(ctx, err)
		}
		checksum := sha256.Sum256([]byte(spec.sql))
		if _, err = tx.ExecContext(ctx, "INSERT INTO momentum.schema_migrations (version, checksum) VALUES ($1, $2)", spec.version, checksum[:]); err != nil {
			return migrationError(ctx, err)
		}
	}
	return migrationError(ctx, tx.Commit())
}
