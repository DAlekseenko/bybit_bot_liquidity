package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/config"
	"bybit_bot_liquidity/internal/storage"
	"bybit_bot_liquidity/internal/strategy"
	"bybit_bot_liquidity/internal/telegram"
)

// TestTelegramConfigFromEnv проверяет выключенный режим, полную env-пару и fail-closed ошибки.
func TestTelegramConfigFromEnv(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  map[string]string
		enabled bool
		wantErr bool
	}{
		{name: "disabled", values: map[string]string{}},
		{name: "enabled", values: map[string]string{telegramTokenEnvironment: "123:abc-DEF_9", telegramOwnerEnvironment: "42"}, enabled: true},
		{name: "token only", values: map[string]string{telegramTokenEnvironment: "secret"}, wantErr: true},
		{name: "owner only", values: map[string]string{telegramOwnerEnvironment: "42"}, wantErr: true},
		{name: "invalid owner", values: map[string]string{telegramTokenEnvironment: "123:abc", telegramOwnerEnvironment: "0"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, enabled, err := telegramConfigFromEnv(func(name string) (string, bool) { value, ok := test.values[name]; return value, ok })
			if (err != nil) != test.wantErr || enabled != test.enabled {
				t.Fatalf("config result enabled=%t err=%v", enabled, err)
			}
			if enabled && (got.Token != test.values[telegramTokenEnvironment] || got.OwnerID != 42) {
				t.Fatalf("config = %#v", got)
			}
			if err != nil && !errors.Is(err, telegram.ErrInvalidConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestTelegramStatusSignalShowsOnlySavedPublicFields проверяет историческую метку, NONE и доступные timestamps.
func TestTelegramStatusSignalShowsOnlySavedPublicFields(t *testing.T) {
	store, err := storage.Open(storage.DriverName, filepath.Join(t.TempDir(), "telegram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	oiAt := created.Add(-time.Minute)
	candleAt := created.Add(-2 * time.Minute)
	if _, err := store.SaveSignalRecord(ctx, storage.SignalRecord{Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalNone, Reason: "no setup"}, Indicators: &strategy.Indicators{CurrentOpenInterestAt: oiAt, SignalCandleAt: candleAt}}); err != nil {
		t.Fatal(err)
	}
	view := telegramStatus{cfg: config.Default(), store: store}
	text, err := view.Signal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Testnet/read-only/paper", "исторический снимок", "Тип: NONE", "Причина: no setup", oiAt.Format(time.RFC3339Nano), candleAt.Format(time.RFC3339Nano)} {
		if !strings.Contains(text, want) {
			t.Errorf("signal response missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "account") || strings.Contains(text, "balance") {
		t.Fatalf("signal response included account data: %s", text)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Signal(ctx); err == nil {
		t.Fatal("Signal() did not distinguish a database error from an absent row")
	}
}
