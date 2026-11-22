package config

import (
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// maxTestnetDuration ограничивает длительности testnet-параметров разумным сроком.
const maxTestnetDuration = 24 * time.Hour

// Testnet — параметры исполнения на Testnet. Умолчаний нет: каждое значение задаёт
// владелец (D-04/D-05/D-06). TradingEnabled=false оставляет отправку заявок закрытой.
// Ключи API сюда не входят: они читаются только из env.
type Testnet struct {
	AccountID           string        `json:"account_id"`
	TradingEnabled      bool          `json:"trading_enabled"`
	StopEnabled         bool          `json:"stop_enabled"`
	MaxDataAge          time.Duration `json:"max_data_age_ns"`
	PollInterval        time.Duration `json:"poll_interval_ns"`
	RESTDeadline        time.Duration `json:"rest_deadline_ns"`
	SlippageRate        string        `json:"slippage_rate"`
	MarginBufferRate    string        `json:"margin_buffer_rate"`
	EquityToleranceUSDT string        `json:"equity_tolerance_usdt"`
}

// rawTestnet — YAML-форма блока testnet; указатели отличают отсутствие от нуля.
type rawTestnet struct {
	AccountID           string  `yaml:"account_id"`
	TradingEnabled      *bool   `yaml:"trading_enabled"`
	StopEnabled         *bool   `yaml:"stop_enabled"`
	MaxDataAge          string  `yaml:"max_data_age"`
	PollInterval        string  `yaml:"poll_interval"`
	RESTDeadline        string  `yaml:"rest_deadline"`
	SlippageRate        *string `yaml:"slippage_rate"`
	MarginBufferRate    *string `yaml:"margin_buffer_rate"`
	EquityToleranceUSDT *string `yaml:"equity_tolerance_usdt"`
}

// errTestnet — общая ошибка блока testnet; значения в текст не попадают.
var errTestnet = errors.New("invalid testnet parameters")

// parseTestnet проверяет блок testnet: он обязателен ровно для mode=testnet и
// разрешён только для профиля baseline (post-close equity для reset не решён, D-02).
func parseTestnet(r *rawTestnet, mode, profile string) (*Testnet, error) {
	if mode != "testnet" {
		if r != nil {
			return nil, errTestnet
		}
		return nil, nil
	}
	if r == nil || !validID(r.AccountID) || r.TradingEnabled == nil || r.StopEnabled == nil {
		return nil, errTestnet
	}
	if *r.StopEnabled {
		return nil, errors.New("testnet stop is not decided (D-05)")
	}
	if profile != "baseline_delta_1x" {
		return nil, errors.New("testnet reset equity base is not decided (D-02)")
	}
	age, okAge := positiveDuration(r.MaxDataAge)
	poll, okPoll := positiveDuration(r.PollInterval)
	deadline, okDeadline := positiveDuration(r.RESTDeadline)
	slippage, okSlippage := boundedRate(r.SlippageRate)
	buffer, okBuffer := boundedRate(r.MarginBufferRate)
	tolerance, okTolerance := nonNegativeAmount(r.EquityToleranceUSDT)
	// Ответ REST, пришедший позже предельного возраста данных, был бы уже устаревшим.
	if !okAge || !okPoll || !okDeadline || !okSlippage || !okBuffer || !okTolerance || deadline > age {
		return nil, errTestnet
	}
	return &Testnet{r.AccountID, *r.TradingEnabled, false, age, poll, deadline, slippage, buffer, tolerance}, nil
}

// positiveDuration разбирает длительность Go в интервале (0, maxTestnetDuration].
func positiveDuration(s string) (time.Duration, bool) {
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0 && d <= maxTestnetDuration
}

// boundedRate проверяет долю в [0, 1) и возвращает её каноническую запись.
func boundedRate(s *string) (string, bool) {
	wire, ok := nonNegativeAmount(s)
	if !ok {
		return "", false
	}
	d, _ := model.ParseRate(wire)
	one, _ := model.ParseRate("1")
	cmp, err := d.Cmp(one)
	return wire, err == nil && cmp < 0
}

// nonNegativeAmount проверяет явное неотрицательное decimal-значение и канонизирует его.
func nonNegativeAmount(s *string) (string, bool) {
	if s == nil {
		return "", false
	}
	d, err := model.ParseAmount(*s)
	if err != nil {
		return "", false
	}
	cmp, err := d.Cmp(model.Zero())
	if err != nil || cmp < 0 {
		return "", false
	}
	wire, err := d.WireDecimal()
	return wire, err == nil
}
