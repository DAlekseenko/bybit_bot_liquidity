package config

import (
	"strings"
	"testing"
)

// validYAML возвращает минимальную корректную paper-конфигурацию.
func validYAML() string {
	return `schema_version: 1
strategy_id: momentum-v1
profile_version: v1
model_id: roc30
start_policy: now
symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
data_source:
  kind: bybit_testnet_public
  environment: testnet
`
}

// TestParseDefaults проверяет defaults, копии полей и безопасный snapshot.
func TestParseDefaults(t *testing.T) {
	if _, err := Load("../../../configs/momentum.paper.yaml"); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(validYAML()))
	if err != nil {
		t.Fatal(err)
	}
	v := c.Values()
	if v.Profile != "baseline_delta_1x" || v.Mode != "paper" || v.InitialEquity != "10000" || strings.Join(v.Symbols, ",") != "BTCUSDT,ETHUSDT,HYPEUSDT" {
		t.Fatalf("unexpected config: %+v", v)
	}
	v.Symbols[0] = "SECRET"
	a, ah, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	b, bh, err := c.Snapshot()
	if err != nil || ah != bh || string(a) != string(b) || strings.Contains(string(a), "SECRET") {
		t.Fatalf("unsafe or unstable snapshot: %v", err)
	}
}

// TestProfilesAndLeverage проверяет разрешённые плечи обоих профилей.
func TestProfilesAndLeverage(t *testing.T) {
	for _, lev := range []string{"1", "2", "3", "5"} {
		y := strings.Replace(validYAML(), "profile_version: v1", "profile: isolated_weekly_reset\nprofile_version: v1", 1)
		y = strings.Replace(y, "leverage: 1", "leverage: "+lev, 1)
		if _, err := Parse([]byte(y)); err != nil {
			t.Fatalf("reset leverage %s: %v", lev, err)
		}
	}
}

// TestRejectGuards проверяет обязательные поля, режимы и fail-closed guards.
func TestRejectGuards(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(validYAML(), "schema_version: 1\n", "", 1), strings.Replace(validYAML(), "schema_version: 1", "schema_version: 2", 1), strings.Replace(validYAML(), "strategy_id: momentum-v1", "strategy_id: ../../secret", 1),
		strings.Replace(validYAML(), "start_policy: now\n", "", 1), strings.Replace(validYAML(), "start_policy: now", "start_policy: later", 1),
		strings.Replace(validYAML(), "leverage: 1", "leverage: 2", 1), strings.Replace(validYAML(), "profile_version: v1", "profile: unknown\nprofile_version: v1", 1),
		strings.Replace(strings.Replace(validYAML(), "profile_version: v1", "profile: isolated_weekly_reset\nprofile_version: v1", 1), "leverage: 1", "leverage: 10", 1),
		strings.Replace(validYAML(), "margin_mode: ISOLATED_MARGIN", "margin_mode: CROSS_MARGIN", 1), strings.Replace(validYAML(), "auto_add_margin: false", "auto_add_margin: true", 1), strings.Replace(validYAML(), "position_mode: one_way", "position_mode: hedge", 1),
		strings.Replace(validYAML(), "symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]", "symbols: [BTCUSDT, BTCUSDT, ETHUSDT]", 1), strings.Replace(validYAML(), "symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]", "symbols: []", 1), strings.Replace(validYAML(), "symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]", "symbols: [BTCUSDT, ETHUSDT, XRPUSDT]", 1),
		strings.Replace(validYAML(), "kind: bybit_testnet_public", "kind: frozen_dataset", 1), strings.Replace(validYAML(), "environment: testnet", "environment: mainnet", 1), strings.Replace(validYAML(), "environment: testnet", "environment: testnet\n  dataset_id: ''", 1),
		strings.Replace(validYAML(), "schema_version: 1", "schema_version: 1\napi_secret: super-secret-value", 1), strings.Replace(validYAML(), "position_mode: one_way", "position_mode: one_way\ninitial_equity_usdt: '-1'", 1), strings.Replace(validYAML(), "position_mode: one_way", "position_mode: one_way\ninitial_equity_usdt: ''", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil || strings.Contains(err.Error(), "super-secret-value") {
			t.Fatalf("accepted or leaked invalid YAML: %v", err)
		}
	}
	for _, mode := range []string{"testnet", "live", "unknown"} {
		y := strings.Replace(validYAML(), "start_policy: now", "mode: "+mode+"\nstart_policy: now", 1)
		if _, err := Parse([]byte(y)); err == nil {
			t.Fatalf("accepted mode %s", mode)
		}
	}
}

// TestStrictYAML проверяет структуру YAML и ограничение размера.
func TestStrictYAML(t *testing.T) {
	anchor := strings.Replace(validYAML(), "strategy_id: momentum-v1", "strategy_id: &id momentum-v1", 1)
	alias := strings.Replace(anchor, "model_id: roc30", "model_id: *id", 1)
	merge := strings.Replace(validYAML(), "data_source:\n  kind: bybit_testnet_public\n  environment: testnet", "data_source:\n  <<: {kind: bybit_testnet_public, environment: testnet}", 1)
	for _, bad := range []string{validYAML() + "---\nschema_version: 1\n", anchor, alias, merge, strings.Replace(validYAML(), "position_mode: one_way", "position_mode: one_way\ninitial_equity_usdt: null", 1)} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid YAML: %q", bad)
		}
	}
	dup := strings.Replace(validYAML(), "data_source:", "data_source:\n  kind: injected", 1)
	if _, err := Parse([]byte(dup)); err == nil {
		t.Fatal("accepted duplicate key")
	}
	if _, err := Parse(make([]byte, maxBytes+1)); err == nil {
		t.Fatal("accepted oversized input")
	}
}

// TestSnapshotCanonical проверяет стабильность, чувствительность hash и отсутствие env.
func TestSnapshotCanonical(t *testing.T) {
	t.Setenv("BYBIT_API_SECRET", "super-secret-value")
	y := strings.Replace(validYAML(), "schema_version: 1\nstrategy_id: momentum-v1", "strategy_id: momentum-v1\nschema_version: 1", 1)
	y = strings.Replace(y, "symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]", "symbols: [ETHUSDT, HYPEUSDT, BTCUSDT]", 1)
	c, err := Parse([]byte(y + "# comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, h, err := c.Snapshot()
	if err != nil || strings.Contains(string(b), "super-secret-value") {
		t.Fatalf("unsafe snapshot: %v", err)
	}
	canonical, err := Parse([]byte(validYAML()))
	if err != nil {
		t.Fatal(err)
	}
	_, want, _ := canonical.Snapshot()
	if h != want {
		t.Fatal("key/comment/symbol order changed hash")
	}
	different := strings.Replace(validYAML(), "schema_version: 1", "schema_version: 1\ninitial_equity_usdt: '10001'", 1)
	changed, err := Parse([]byte(different))
	if err != nil {
		t.Fatal(err)
	}
	_, changedHash, _ := changed.Snapshot()
	if h == changedHash {
		t.Fatal("significant value did not change hash")
	}
}

// TestBacktestSource проверяет frozen dataset и обязательные identity/hash поля.
func TestBacktestSource(t *testing.T) {
	y := strings.Replace(validYAML(), "schema_version: 1", "schema_version: 1\nmode: backtest", 1)
	y = strings.Replace(y, "kind: bybit_testnet_public", "kind: frozen_dataset", 1)
	y = strings.Replace(y, "environment: testnet", "environment: mainnet\n  dataset_id: sample-v1\n  manifest_sha256: "+strings.Repeat("a", 64), 1)
	if _, err := Parse([]byte(y)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(y, "  dataset_id: sample-v1\n", "", 1), strings.Replace(y, strings.Repeat("a", 64), "invalid", 1)} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("accepted incomplete dataset source")
		}
	}
}
