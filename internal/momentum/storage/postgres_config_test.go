package storage

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/lib/pq"
)

// TestLocalPostgresConfig проверяет URI, явные значения, ошибки и отсутствие зависимости от PG env.
func TestLocalPostgresConfig(t *testing.T) {
	valid := []struct {
		dsn, host, db, user, password string
		port                          uint16
		addr                          string
	}{
		{"postgres://u:p@127.0.0.1:5432/db?sslmode=disable", "127.0.0.1", "db", "u", "p", 5432, "127.0.0.1"},
		{"postgresql://u%40x:p%2Fq@[::1]:6432/a%20b?sslmode=disable", "::1", "a b", "u@x", "p/q", 6432, "::1"},
	}
	for _, tc := range valid {
		cfg, err := localPostgresConfig(tc.dsn, "0")
		if err != nil {
			t.Fatalf("valid URI: %v", err)
		}
		if cfg.Host != tc.host || cfg.Hostaddr != netip.MustParseAddr(tc.addr) || cfg.Port != tc.port || cfg.Database != tc.db || cfg.User != tc.user || cfg.Password != tc.password {
			t.Fatalf("URI fields mismatch: %+v", cfg)
		}
		if cfg.SSLMode != pq.SSLModeDisable || cfg.MinProtocolVersion != "3.0" || cfg.MaxProtocolVersion != "3.0" || cfg.ClientEncoding != "UTF8" || cfg.Datestyle != "ISO, MDY" || cfg.TZ != "UTC" || cfg.ApplicationName != "momentum" || cfg.ConnectTimeout != 5*time.Second || cfg.Options != "-c search_path=pg_catalog,momentum -c timezone=UTC" {
			t.Fatalf("static fields mismatch: %+v", cfg)
		}
		if cfg.Passfile != "" || cfg.Service != "" || cfg.ServiceFile != "" || cfg.SSLCert != "" || cfg.SSLKey != "" || cfg.SSLRootCert != "" {
			t.Fatalf("credential paths populated: %+v", cfg)
		}
	}
	bad := []struct{ name, dsn string }{
		{"empty", ""},
		{"keyword DSN", "user=u password=p"},
		{"invalid scheme", "https://u:p@127.0.0.1:5432/db?sslmode=disable"},
		{"malformed scheme", "postgres:/db"},
		{"opaque URI", "postgres:opaque"},
		{"empty host", "postgres:///db?sslmode=disable"},
		{"absent host", "postgres://u:p@/db?sslmode=disable"},
		{"missing userinfo", "postgres://127.0.0.1:5432/db?sslmode=disable"},
		{"fragment", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable#x"},
		{"empty fragment", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable#"},
		{"missing sslmode", "postgres://u:p@127.0.0.1:5432/db"},
		{"unknown sslmode", "postgres://u:p@127.0.0.1:5432/db?sslmode=require"},
		{"verify-full sslmode", "postgres://u:p@127.0.0.1:5432/db?sslmode=verify-full"},
		{"unknown query key", "postgres://u:p@127.0.0.1:5432/db?x=y&sslmode=disable"},
		{"duplicate sslmode", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&sslmode=disable"},
		{"encoded sslmode", "postgres://u:p@127.0.0.1:5432/db?sslmode=%64isable"},
		{"malformed query escape", "postgres://u:p@127.0.0.1:5432/db?sslmode=%zz"},
		{"forbidden option", "postgres://u:p@127.0.0.1:5432/db?service=x&sslmode=disable"},
		{"semicolon query", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable;foo=bar"},
		{"empty query item", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&"},
		{"DNS localhost", "postgres://u:p@localhost:5432/db?sslmode=disable"},
		{"DNS with TLS", "postgres://u:p@localhost:5432/db?sslmode=verify-full"},
		{"DNS remote", "postgres://u:p@example.com:5432/db?sslmode=disable"},
		{"DNS remote with TLS", "postgres://u:p@example.com:5432/db?sslmode=require"},
		{"remote IPv4", "postgres://u:p@192.0.2.1:5432/db?sslmode=disable"},
		{"remote IPv4 with TLS", "postgres://u:p@192.0.2.1:5432/db?sslmode=verify-full"},
		{"bracketed IPv4", "postgres://u:p@[127.0.0.1]:5432/db?sslmode=disable"},
		{"missing port", "postgres://u:p@127.0.0.1/db?sslmode=disable"},
		{"empty port", "postgres://u:p@127.0.0.1:/db?sslmode=disable"},
		{"nonnumeric port", "postgres://u:p@127.0.0.1:x/db?sslmode=disable"},
		{"zero port", "postgres://u:p@127.0.0.1:0/db?sslmode=disable"},
		{"port overflow", "postgres://u:p@127.0.0.1:65536/db?sslmode=disable"},
		{"empty database", "postgres://u:p@127.0.0.1:5432/?sslmode=disable"},
		{"extra path segment", "postgres://u:p@127.0.0.1:5432/db/extra?sslmode=disable"},
		{"encoded path separator", "postgres://u:p@127.0.0.1:5432/a%2Fb?sslmode=disable"},
		{"malformed database escape", "postgres://u:p@127.0.0.1:5432/%zz?sslmode=disable"},
		{"empty username", "postgres://:p@127.0.0.1:5432/db?sslmode=disable"},
		{"empty password", "postgres://u:@127.0.0.1:5432/db?sslmode=disable"},
		{"missing password", "postgres://u@127.0.0.1:5432/db?sslmode=disable"},
		{"malformed credential escape", "postgres://u%zz:p@127.0.0.1:5432/db?sslmode=disable"},
		{"malformed query component", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&x=%zz"},
		{"multi-host", "postgres://u:p@127.0.0.1,127.0.0.1:5432/db?sslmode=disable"},
		{"unix socket", "postgres://u:p@/var/run/postgresql/db?sslmode=disable"},
		{"unspecified IPv6", "postgres://u:p@[::]:5432/db?sslmode=disable"},
		{"remote IPv6", "postgres://u:p@[2001:db8::1]:5432/db?sslmode=disable"},
		{"remote IPv6 with TLS", "postgres://u:p@[2001:db8::1]:5432/db?sslmode=require"},
		{"IPv6 missing port", "postgres://u:p@[::1]/db?sslmode=disable"},
		{"IPv6 empty port", "postgres://u:p@[::1]:/db?sslmode=disable"},
	}
	for _, tc := range bad {
		cfg, err := localPostgresConfig(tc.dsn, "0")
		if !errors.Is(err, ErrPostgresConfig) || !reflect.DeepEqual(cfg, pq.Config{}) {
			t.Fatalf("rejected %s URI leaked result: %q, cfg=%+v err=%v", tc.name, tc.dsn, cfg, err)
		}
		if err.Error() != ErrPostgresConfig.Error() {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	for _, debug := range []string{"1"} {
		cfg, err := localPostgresConfig(valid[0].dsn, debug)
		if !errors.Is(err, ErrPostgresConfig) || !reflect.DeepEqual(cfg, pq.Config{}) {
			t.Fatalf("debug accepted: cfg=%+v err=%v", cfg, err)
		}
	}
	baseline, err := localPostgresConfig(valid[0].dsn, "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"PGHOST", "PGDATABASE", "PGPASSWORD", "PGOPTIONS", "PGSERVICE", "PGPASSFILE"} {
		t.Setenv(key, "poison")
	}
	first, err := localPostgresConfig(valid[0].dsn, "0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := localPostgresConfig(valid[0].dsn, "0")
	if err != nil || !reflect.DeepEqual(first, baseline) || !reflect.DeepEqual(first, second) {
		t.Fatalf("explicit result changed: %+v %+v %v", first, second, err)
	}
	first.Options = "mutated"
	if second.Options != baseline.Options {
		t.Fatalf("configs share mutable state: %+v", second)
	}
}
