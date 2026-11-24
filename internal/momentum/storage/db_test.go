package storage

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// TestOpenPostgresFailures проверяет config/context precedence, handshake и закрытый порт.
func TestOpenPostgresFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dsn := "postgres://u:secret-marker@" + listener.Addr().String() + "/db?sslmode=disable"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct{ debug, uri string }{{"1", dsn}, {"0", dsn + "&options=forbidden"}} {
		t.Setenv("PQGO_DEBUG", tc.debug)
		if db, err := OpenMomentumPostgres(canceled, tc.uri); db != nil || err != ErrPostgresConfig {
			t.Fatalf("config must precede context: pool=%v err=%v", db, err)
		}
	}
	t.Setenv("PQGO_DEBUG", "0")
	for _, tc := range []struct {
		ctx  context.Context
		want error
	}{{canceled, context.Canceled}, {expired, context.DeadlineExceeded}} {
		if db, err := OpenMomentumPostgres(tc.ctx, dsn); db != nil || !errors.Is(err, ErrPostgresConnect) || !errors.Is(err, tc.want) {
			t.Fatalf("context failure lost: pool=%v err=%v", db, err)
		}
	}
	// Принятый сокет удерживается молча до завершения отменённого Open.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	start := time.Now()
	// Горутина владеет результатом Open; ошибочно возвращённый pool обязательно закрывается.
	go func() {
		db, err := OpenMomentumPostgres(ctx, dsn)
		if db != nil {
			_ = db.Close()
			err = errors.New("unexpected nonnil pool")
		}
		result <- err
	}()
	if err := listener.(*net.TCPListener).SetDeadline(start.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, ErrPostgresConnect) || !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second+250*time.Millisecond || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("unsafe/slow handshake result after %s: %v", time.Since(start), err)
		}
	case <-time.After(time.Until(start.Add(5*time.Second + 250*time.Millisecond))):
		t.Fatal("handshake exceeded configured startup bound")
	}
	_ = listener.Close()
	start = time.Now()
	if db, err := OpenMomentumPostgres(context.Background(), dsn); db != nil || !errors.Is(err, ErrPostgresConnect) || time.Since(start) > 5*time.Second || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("unsafe or unbounded failure: pool=%v err=%v", db, err)
	}
}

// openTestPostgres создаёт новую случайную базу только на проверенном disposable сервере.
func openTestPostgres(t *testing.T) *sql.DB {
	t.Helper()
	return pgtest.Open(t, OpenMomentumPostgres)
}

// TestDisposableOptInFailures проверяет отказ до вызова opener и до любого SQL.
func TestDisposableOptInFailures(t *testing.T) {
	const valid = "postgres://u:p@127.0.0.1:5432/momentum_test_source?sslmode=disable"
	for _, tc := range []struct{ name, dsn string }{
		{"malformed", "postgres://%"},
		{"remote", strings.Replace(valid, "127.0.0.1", "192.0.2.1", 1)},
		{"hostname", strings.Replace(valid, "127.0.0.1", "localhost", 1)},
		{"password", strings.Replace(valid, ":p@", "@", 1)},
		{"user", strings.Replace(valid, "u:p@", ":p@", 1)},
		{"database", strings.Replace(valid, "momentum_test_source", "postgres", 1)},
		{"escaped slash", strings.Replace(valid, "_source", "_source%2Fother", 1)},
		{"parameter", valid + "&options=forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PQGO_DEBUG", "0")
			db, err := pgtest.Create(t.Context(), tc.dsn, func(context.Context, string) (*sql.DB, error) {
				t.Fatal("opener вызван при недопустимом opt-in")
				return nil, nil
			})
			if db != nil || err == nil {
				t.Fatal("не получен отказ для недопустимого opt-in")
			}
		})
	}
}

// TestDisposablePostgres проверяет реальные session defaults, env isolation и независимые pools.
func TestDisposablePostgres(t *testing.T) {
	for _, key := range []string{"PGHOST", "PGDATABASE", "PGPASSWORD", "PGOPTIONS", "PGSERVICE", "PGPASSFILE"} {
		t.Setenv(key, "poison.invalid")
	}
	var first *sql.DB
	var firstName string
	second := openTestPostgres(t)
	if !t.Run("cleanup", func(t *testing.T) {
		first = openTestPostgres(t)
		if err := first.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&firstName); err != nil {
			t.Fatal(err)
		}
		for query, want := range map[string]string{
			"SELECT 1":         "1",
			"SHOW search_path": "pg_catalog,momentum",
			"SHOW TimeZone":    "UTC",
			"SELECT count(*) FROM pg_namespace WHERE nspname='momentum'": "0",
		} {
			var got string
			if err := first.QueryRowContext(context.Background(), query).Scan(&got); err != nil || got != want {
				t.Fatalf("query=%s got=%q err=%v", query, got, err)
			}
		}
	}) {
		return
	}
	var secondName string
	if err := second.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&secondName); err != nil {
		t.Fatal(err)
	}
	if firstName == secondName {
		t.Fatal("создана одна и та же база")
	}
	var one int
	if first.QueryRowContext(context.Background(), "SELECT 1").Scan(&one) == nil {
		t.Fatal("closed pool still worked")
	}
	if err := second.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("independent pool failed: value=%d err=%v", one, err)
	}
	if first.Stats().MaxOpenConnections != 4 {
		t.Fatalf("max pool size: %d", first.Stats().MaxOpenConnections)
	}
}
