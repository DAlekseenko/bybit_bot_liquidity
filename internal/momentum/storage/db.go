package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/lib/pq"
)

// ErrPostgresConnect сообщает о невозможности установить соединение PostgreSQL.
var ErrPostgresConnect = errors.New("cannot connect to local PostgreSQL")

// OpenMomentumPostgres создаёт и проверяет независимый локальный пул PostgreSQL.
func OpenMomentumPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	cfg, err := localPostgresConfig(dsn, os.Getenv("PQGO_DEBUG"))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(ErrPostgresConnect, err)
	}
	connector, err := pq.NewConnectorConfig(cfg)
	if err != nil {
		return nil, ErrPostgresConnect
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pingErr := db.PingContext(ctx)
	ctxErr := ctx.Err()
	if pingErr != nil || ctxErr != nil {
		_ = db.Close()
		if ctxErr != nil {
			return nil, errors.Join(ErrPostgresConnect, ctxErr)
		}
		return nil, ErrPostgresConnect
	}
	return db, nil
}
