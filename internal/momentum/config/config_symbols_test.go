package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestCanonicalSymbols проверяет три разных символа, копирование и стабильный порядок.
func TestCanonicalSymbols(t *testing.T) {
	for _, input := range [][]string{
		{"HYPEUSDT", "BTCUSDT", "ETHUSDT"},
		{"SOLUSDT", "ADAUSDT", "XRPUSDT"},
		{"XRPUSDT", "1000PEPEUSDT", "SOLUSDT"},
	} {
		before := append([]string(nil), input...)
		got, err := CanonicalSymbols(input)
		if err != nil || len(got) != 3 || got[0] >= got[1] || got[1] >= got[2] {
			t.Fatalf("symbols=%v result=%v error=%v", input, got, err)
		}
		got[0] = "MUTATED"
		again, err := CanonicalSymbols(input)
		if err != nil || !reflect.DeepEqual(input, before) || again[0] == "MUTATED" {
			t.Fatal("symbols alias input or another result")
		}
	}
	for _, input := range [][]string{
		nil,
		{},
		{"BTCUSDT", "ETHUSDT"},
		{"BTCUSDT", "ETHUSDT", "HYPEUSDT", "XRPUSDT"},
		{"BTCUSDT", "BTCUSDT", "ETHUSDT"},
		{"", "ETHUSDT", "HYPEUSDT"},
		{"btcusdt", "ETHUSDT", "HYPEUSDT"},
		{" BTCUSDT", "ETHUSDT", "HYPEUSDT"},
		{"BTCUSDT ", "ETHUSDT", "HYPEUSDT"},
		{"BTC/USDT", "ETHUSDT", "HYPEUSDT"},
		{"ВTCUSDT", "ETHUSDT", "HYPEUSDT"},
		{"BTCUSD", "ETHUSDT", "HYPEUSDT"},
		{"USDT", "ETHUSDT", "HYPEUSDT"},
		{"BTC-USDT", "ETHUSDT", "HYPEUSDT"},
	} {
		got, err := CanonicalSymbols(input)
		if got != nil || err == nil {
			t.Fatalf("accepted symbols=%v result=%v error=%v", input, got, err)
		}
		quoted := make([]string, len(input))
		for i, symbol := range input {
			quoted[i] = strconv.Quote(symbol)
		}
		yaml := strings.Replace(validYAML(), "[HYPEUSDT, BTCUSDT, ETHUSDT]", "["+strings.Join(quoted, ",")+"]", 1)
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Fatal("Parse accepted invalid symbol universe", input)
		}
	}
}

// configuredSymbolsFixture разбирает только синтетический YAML без I/O.
func configuredSymbolsFixture(t *testing.T, symbols string) Config {
	t.Helper()
	yaml := strings.Replace(validYAML(), "[HYPEUSDT, BTCUSDT, ETHUSDT]", symbols, 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestConfiguredSymbolsSnapshot проверяет Parse, Values и идентичность universe.
func TestConfiguredSymbolsSnapshot(t *testing.T) {
	c := configuredSymbolsFixture(t, "[SOLUSDT, ADAUSDT, XRPUSDT]")
	permuted := configuredSymbolsFixture(t, "[XRPUSDT, SOLUSDT, ADAUSDT]")
	changed := configuredSymbolsFixture(t, "[SOLUSDT, ADAUSDT, DOGEUSDT]")
	values := c.Values()
	if strings.Join(values.Symbols, ",") != "ADAUSDT,SOLUSDT,XRPUSDT" {
		t.Fatal("unexpected canonical symbols", values.Symbols)
	}
	before, hash, err := c.Snapshot()
	same, sameHash, sameErr := permuted.Snapshot()
	_, changedHash, changedErr := changed.Snapshot()
	if err != nil || sameErr != nil || changedErr != nil || string(before) != string(same) || hash != sameHash || hash == changedHash {
		t.Fatalf("snapshot errors=%v/%v/%v hashes=%s/%s/%s", err, sameErr, changedErr, hash, sameHash, changedHash)
	}
	values.Symbols[0] = "MUTATED"
	after, afterHash, err := c.Snapshot()
	if err != nil || string(before) != string(after) || hash != afterHash || c.Values().Symbols[0] != "ADAUSDT" {
		t.Fatal("Values symbols changed stored config")
	}
}
