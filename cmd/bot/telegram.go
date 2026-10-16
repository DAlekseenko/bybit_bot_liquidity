package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/config"
	"bybit_bot_liquidity/internal/storage"
	"bybit_bot_liquidity/internal/telegram"
)

const (
	telegramTokenEnvironment = "TELEGRAM_BOT_TOKEN"
	telegramOwnerEnvironment = "TELEGRAM_OWNER_USER_ID"
)

// telegramConfigFromEnv загружает полную пару настроек только из environment.
func telegramConfigFromEnv(lookup func(string) (string, bool)) (telegram.Config, bool, error) {
	token, _ := lookup(telegramTokenEnvironment)
	owner, _ := lookup(telegramOwnerEnvironment)
	if token == "" && owner == "" {
		return telegram.Config{}, false, nil
	}
	if strings.TrimSpace(token) != token || token == "" || strings.TrimSpace(owner) != owner || owner == "" {
		return telegram.Config{}, false, telegram.ErrInvalidConfig
	}
	ownerID, err := strconv.ParseInt(owner, 10, 64)
	if err != nil || ownerID <= 0 {
		return telegram.Config{}, false, telegram.ErrInvalidConfig
	}
	return telegram.Config{Token: token, OwnerID: ownerID}, true, nil
}

type telegramStatus struct {
	cfg   config.Config
	store *storage.Store
}

// Status сообщает среду и режим без утверждения о свежести или готовности рынка.
func (s telegramStatus) Status() string {
	return fmt.Sprintf("Процесс работает. Среда: Bybit Testnet: %t. Режим: read-only market data + paper trade. Символ: %s. Биржевые ордера не подключены.", s.cfg.Exchange.Testnet, s.cfg.Symbol)
}

// Settings показывает только whitelist публичных strategy/risk настроек.
func (s telegramStatus) Settings() string {
	return fmt.Sprintf("Символ: %s\nOI: %d мин, порог %.2f%%\nОбъём: SMA %d, множитель %.2f\nЛиквидации: %d мин, порог %.2f USD\nPaper риск: %.2f%%, SL %.2f%%, TP %.2f%%\nКонфигурация меняется через YAML и перезапуск.", s.cfg.Symbol, s.cfg.Strategy.OIWindowMinutes, s.cfg.Strategy.OIGrowthPercent, s.cfg.Strategy.VolumeSMAPeriod, s.cfg.Strategy.VolumeMultiplier, s.cfg.Strategy.LiquidationWindowMinutes, s.cfg.Strategy.LiquidationThresholdUSD, s.cfg.Risk.RiskPerTradePercent, s.cfg.Risk.StopLossPercent, s.cfg.Risk.TakeProfitPercent)
}

// Signal возвращает последний сохранённый сигнал символа как исторический снимок.
func (s telegramStatus) Signal(ctx context.Context) (string, error) {
	item, err := s.store.LatestSignalBySymbol(ctx, s.cfg.Symbol)
	if err != nil {
		return "", err
	}
	if item == nil {
		return fmt.Sprintf("Сохранённый сигнал для %s отсутствует.", s.cfg.Symbol), nil
	}
	result := fmt.Sprintf("Режим: Testnet/read-only/paper\nПоследний сохранённый сигнал (исторический снимок, актуальность не проверяется)\nСимвол: %s\nТип: %s\nПричина: %s\nСоздан: %s", item.Symbol, item.Type, item.Reason, item.CreatedAt.UTC().Format(time.RFC3339Nano))
	if item.CurrentOIAt != nil {
		result += "\nCurrent OI: " + item.CurrentOIAt.UTC().Format(time.RFC3339Nano)
	}
	if item.SignalCandleAt != nil {
		result += "\nСигнальная свеча: " + item.SignalCandleAt.UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}

// startTelegramControl запускает отдельный worker после восстановления paper state.
func startTelegramControl(ctx context.Context, log *slog.Logger, cfg telegram.Config, appConfig config.Config, store *storage.Store, done func()) error {
	bot, err := telegram.New(cfg, telegramStatus{cfg: appConfig, store: store}, nil)
	if err != nil {
		return err
	}
	go func() {
		defer done()
		if err := bot.Run(ctx, func(message string, err error) { log.Warn(message, "error", err) }); err != nil && ctx.Err() == nil {
			log.Warn("Telegram control stopped", "error", err)
		}
	}()
	return nil
}

// telegramEnvironment читает env-переменную без её вывода в логи.
func telegramEnvironment(name string) (string, bool) { return os.LookupEnv(name) }
