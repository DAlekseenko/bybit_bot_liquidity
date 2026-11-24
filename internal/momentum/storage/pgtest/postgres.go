// Package pgtest создаёт пустые базы на одноразовом локальном PostgreSQL для тестов.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Opener открывает проверенный пул с настройками сессии Momentum.
type Opener func(context.Context, string) (*sql.DB, error)

// Open пропускает тест без opt-in и закрывает созданный пул при завершении теста.
func Open(t *testing.T, open Opener) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL opt-in отсутствует")
	}
	db, err := Create(t.Context(), dsn, open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("закрытие тестового пула: %v", err)
		}
	})
	return db
}

// Create создаёт случайную пустую базу и передаёт вызывающему владение её пулом.
func Create(ctx context.Context, dsn string, open Opener) (*sql.DB, error) {
	u, err := url.Parse(dsn)
	if err != nil || !validOptIn(u) || strings.Contains(dsn, "#") || os.Getenv("PQGO_DEBUG") == "1" {
		return nil, errors.New("opt-in must be literal local momentum_test_ URI")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	name := "momentum_test_" + hex.EncodeToString(suffix[:])
	u.Path, u.RawPath = "/postgres", ""
	admin, err := open(ctx, u.String())
	if err != nil {
		return nil, err
	}
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	closeErr := admin.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	u.Path = "/" + name
	return open(ctx, u.String())
}

// validOptIn проверяет исходный URI до подмены имени базы и обращения к серверу.
func validOptIn(u *url.URL) bool {
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" ||
		u.RawQuery != "sslmode=disable" || !strings.HasPrefix(u.Path, "/momentum_test_") || strings.Contains(u.Path[1:], "/") {
		return false
	}
	password, ok := u.User.Password()
	host, port, err := net.SplitHostPort(u.Host)
	if !ok || password == "" || err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	n, portErr := strconv.ParseUint(port, 10, 16)
	return err == nil && addr.IsLoopback() && addr.Zone() == "" &&
		(!strings.HasPrefix(u.Host, "[") || addr.Is6()) && portErr == nil && n > 0
}
