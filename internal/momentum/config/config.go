// Package config загружает изолированную декларативную конфигурацию Momentum.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"

	"bybit_bot_liquidity/internal/momentum/model"
	"gopkg.in/yaml.v3"
)

const maxBytes = 64 << 10

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Config хранит валидированные параметры без изменяемого публичного доступа.
type Config struct {
	values Values
}

// Values содержит копируемое представление декларативных параметров Momentum.
type Values struct {
	SchemaVersion  int        `json:"schema_version"`
	StrategyID     string     `json:"strategy_id"`
	Profile        string     `json:"profile"`
	ProfileVersion string     `json:"profile_version"`
	ModelID        string     `json:"model_id"`
	Mode           string     `json:"mode"`
	StartPolicy    string     `json:"start_policy"`
	Symbols        []string   `json:"symbols"`
	Leverage       int        `json:"leverage"`
	MarginMode     string     `json:"margin_mode"`
	AutoAddMargin  bool       `json:"auto_add_margin"`
	PositionMode   string     `json:"position_mode"`
	InitialEquity  string     `json:"initial_equity_usdt"`
	DataSource     DataSource `json:"data_source"`
}

// DataSource описывает происхождение данных без чтения или сетевого доступа.
type DataSource struct {
	Kind           string `json:"kind"`
	Environment    string `json:"environment"`
	DatasetID      string `json:"dataset_id,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

type rawConfig struct {
	SchemaVersion  *int      `yaml:"schema_version"`
	StrategyID     string    `yaml:"strategy_id"`
	Profile        *string   `yaml:"profile"`
	ProfileVersion string    `yaml:"profile_version"`
	ModelID        string    `yaml:"model_id"`
	Mode           *string   `yaml:"mode"`
	StartPolicy    string    `yaml:"start_policy"`
	Symbols        []string  `yaml:"symbols"`
	Leverage       *int      `yaml:"leverage"`
	MarginMode     string    `yaml:"margin_mode"`
	AutoAddMargin  *bool     `yaml:"auto_add_margin"`
	PositionMode   string    `yaml:"position_mode"`
	InitialEquity  *string   `yaml:"initial_equity_usdt"`
	DataSource     rawSource `yaml:"data_source"`
}
type rawSource struct {
	Kind           string `yaml:"kind"`
	Environment    string `yaml:"environment"`
	DatasetID      string `yaml:"dataset_id"`
	ManifestSHA256 string `yaml:"manifest_sha256"`
}

// Load читает файл ограниченного размера и передаёт его строгому парсеру.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot load momentum config")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(b) > maxBytes {
		return Config{}, errors.New("invalid momentum config")
	}
	return Parse(b)
}

// Parse проверяет YAML и все guards, возвращая только безопасную конфигурацию.
func Parse(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > maxBytes {
		return Config{}, errors.New("invalid momentum config")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if d.Decode(&doc) != nil || d.Decode(&extra) != io.EOF || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || !safeNode(doc.Content[0]) {
		return Config{}, errors.New("invalid momentum config")
	}
	var r rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if dec.Decode(&r) != nil {
		return Config{}, errors.New("invalid momentum config")
	}
	equity := "10000"
	if r.InitialEquity != nil {
		equity = *r.InitialEquity
	}
	profile, mode := "baseline_delta_1x", "paper"
	if r.Profile != nil {
		profile = *r.Profile
	}
	if r.Mode != nil {
		mode = *r.Mode
	}
	if r.SchemaVersion == nil || *r.SchemaVersion != 1 || !validID(r.StrategyID) || !validID(r.ProfileVersion) || !validID(r.ModelID) || r.StartPolicy != "now" && r.StartPolicy != "next_monday" || r.Leverage == nil || r.AutoAddMargin == nil {
		return Config{}, errors.New("invalid momentum config")
	}
	if profile != "baseline_delta_1x" && profile != "isolated_weekly_reset" {
		return Config{}, errors.New("unsupported profile")
	}
	if mode == "testnet" {
		return Config{}, errors.New("testnet mode is disabled")
	}
	if mode == "live" {
		return Config{}, errors.New("live mode is disabled")
	}
	if mode != "paper" && mode != "backtest" {
		return Config{}, errors.New("unsupported mode")
	}
	if profile == "baseline_delta_1x" && *r.Leverage != 1 || profile == "isolated_weekly_reset" && !oneOf(*r.Leverage, 1, 2, 3, 5) {
		return Config{}, errors.New("invalid leverage")
	}
	equity, valid := canonicalEquity(equity)
	if !valid || r.MarginMode != "ISOLATED_MARGIN" || *r.AutoAddMargin || r.PositionMode != "one_way" {
		return Config{}, errors.New("invalid momentum guards")
	}
	if len(r.Symbols) != 3 {
		return Config{}, errors.New("invalid symbols")
	}
	want := map[string]bool{"BTCUSDT": true, "ETHUSDT": true, "HYPEUSDT": true}
	for _, s := range r.Symbols {
		if !want[s] {
			return Config{}, errors.New("invalid symbols")
		}
		delete(want, s)
	}
	if len(want) != 0 {
		return Config{}, errors.New("invalid symbols")
	}
	s := r.DataSource
	if s.Kind == "bybit_testnet_public" && s.Environment == "testnet" && mode == "paper" && s.DatasetID == "" && s.ManifestSHA256 == "" && !hasDatasetFields(doc.Content[0]) {
	} else if s.Kind == "frozen_dataset" && (s.Environment == "testnet" || s.Environment == "mainnet") && mode == "backtest" && validID(s.DatasetID) && digest.MatchString(s.ManifestSHA256) {
	} else {
		return Config{}, errors.New("invalid data source")
	}
	symbols := append([]string(nil), r.Symbols...)
	sort.Strings(symbols)
	return Config{Values{1, r.StrategyID, profile, r.ProfileVersion, r.ModelID, mode, r.StartPolicy, symbols, *r.Leverage, r.MarginMode, *r.AutoAddMargin, r.PositionMode, equity, DataSource(s)}}, nil
}

// Values возвращает снимок полей с копией списка символов.
func (c Config) Values() Values {
	v := c.values
	v.Symbols = append([]string(nil), v.Symbols...)
	return v
}

// Snapshot сериализует безопасный снимок и возвращает SHA-256 его байтов.
func (c Config) Snapshot() ([]byte, string, error) {
	b, err := json.Marshal(c.values)
	if err != nil {
		return nil, "", errors.New("cannot encode momentum snapshot")
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

// safeNode запрещает YAML aliases, anchors, merge и повторяющиеся ключи.
func safeNode(n *yaml.Node) bool {
	if n.Anchor != "" || n.Alias != nil || n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return false
	}
	seen := map[string]bool{}
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if !safeNode(c) {
			return false
		}
	}
	return true
}

// validID проверяет безопасный ASCII идентификатор.
func validID(s string) bool { return identifier.MatchString(s) }

// canonicalEquity проверяет положительный equity и канонизирует его сумму.
func canonicalEquity(s string) (string, bool) {
	d, err := model.ParseAmount(s)
	if err != nil {
		return "", false
	}
	cmp, err := d.Cmp(model.Zero())
	if err != nil || cmp <= 0 {
		return "", false
	}
	wire, err := d.WireDecimal()
	return wire, err == nil
}

// oneOf проверяет принадлежность целого значения разрешённому набору.
func oneOf(v int, values ...int) bool {
	for _, n := range values {
		if v == n {
			return true
		}
	}
	return false
}

// hasDatasetFields проверяет, что paper YAML не задаёт поля датасета.
func hasDatasetFields(n *yaml.Node) bool {
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value == "data_source" {
				for j := 0; j < len(n.Content[i+1].Content); j += 2 {
					k := n.Content[i+1].Content[j].Value
					if k == "dataset_id" || k == "manifest_sha256" {
						return true
					}
				}
			}
		}
	}
	return false
}
