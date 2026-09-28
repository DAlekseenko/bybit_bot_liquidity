package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bybit_bot_liquidity/internal/strategy"

	_ "modernc.org/sqlite"
)

const DriverName = "sqlite"

var (
	ErrInvalidConfig        = errors.New("invalid storage config")
	ErrIntegrityCheckFailed = errors.New("storage integrity check failed")
	ErrVirtualSignalNotOpen = errors.New("virtual signal is not open")
)

// SignalRecord объединяет сигнал и необязательный снимок виртуальной сделки.
type SignalRecord struct {
	Signal      strategy.Signal
	SignalPrice *float64
	Indicators  *strategy.Indicators
	Virtual     *VirtualTrade
	ExitReason  string
}

// VirtualTrade хранит расчётные параметры позиции на момент сигнала.
type VirtualTrade struct {
	SignalCandleAt    time.Time
	HypotheticalEntry float64
	StopLoss          float64
	TakeProfit        float64
	EstimatedFees     float64
	EstimatedSlippage float64
	Qty               float64
}

// VirtualOutcome закрывает ранее сохранённую виртуальную сделку.
type VirtualOutcome struct {
	ExitPrice  float64
	ExitReason string
	PnL        float64
	ClosedAt   time.Time
}

// OpenVirtualSignal содержит данные одной незакрытой виртуальной позиции.
type OpenVirtualSignal struct {
	SignalID int64
	Plan     VirtualTrade
	Side     string
	Symbol   string
}

// Trade описывает завершенную или открытую сделку для таблицы trades.
type Trade struct {
	// ID - первичный ключ в SQLite.
	ID int64
	// Symbol - торговый инструмент.
	Symbol string
	// Side - направление сделки: LONG или SHORT.
	Side string
	// EntryPrice и ExitPrice - цены входа и выхода.
	EntryPrice float64
	ExitPrice  float64
	// Qty - размер позиции.
	Qty float64
	// PnL - финансовый результат сделки.
	PnL float64
	// OpenedAt и ClosedAt - время открытия и закрытия сделки.
	OpenedAt time.Time
	ClosedAt *time.Time
}

// Stats содержит агрегированную торговую статистику по закрытым сделкам.
type Stats struct {
	// TotalTrades - количество закрытых сделок.
	TotalTrades int
	// WinRate - доля прибыльных сделок в процентах.
	WinRate float64
	// ProfitFactor - отношение валовой прибыли к валовому убытку.
	ProfitFactor float64
	// TotalPnL - суммарный результат всех закрытых сделок.
	TotalPnL float64
	// AverageTrade - средний результат одной сделки.
	AverageTrade float64
	// MaxDrawdown - максимальная просадка по накопленной кривой PnL.
	MaxDrawdown float64
}

// VirtualStats содержит аналитику только по завершённым виртуальным сигналам.
type VirtualStats struct {
	ClosedTrades, OpenTrades, SkippedSignals                   int
	WinRate, ProfitFactor, TotalPnL, AverageTrade, MaxDrawdown float64
}

// Store инкапсулирует доступ к SQLite через database/sql.
type Store struct {
	db *sql.DB
}

// Open открывает и проверяет соединение с хранилищем.
func Open(driverName, dsn string) (*Store, error) {
	if driverName == "" {
		return nil, fmt.Errorf("%w: driver is required", ErrInvalidConfig)
	}
	if dsn == "" {
		return nil, fmt.Errorf("%w: dsn is required", ErrInvalidConfig)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping storage: %w", err)
	}

	return &Store{db: db}, nil
}

// New создаёт хранилище на основе готового соединения.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Close закрывает соединение хранилища.
func (s *Store) Close() error {
	return s.db.Close()
}

// Check выполняет read-only SQLite quick_check для runtime healthcheck.
func (s *Store) Check(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("check storage integrity: %w", err)
	}
	defer rows.Close()

	checked := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("check storage integrity: %w", err)
		}
		checked = true
		if result != "ok" {
			return fmt.Errorf("%w: %s", ErrIntegrityCheckFailed, result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check storage integrity: %w", err)
	}
	if !checked {
		return fmt.Errorf("%w: empty result", ErrIntegrityCheckFailed)
	}
	return nil
}

// Migrate создаёт таблицы и недостающие колонки схемы.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema migrations: %w", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema migrations: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("migrate storage: database schema version %d is newer than supported version 1", version)
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS trades (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	symbol TEXT NOT NULL,
	side TEXT NOT NULL,
	entry_price REAL NOT NULL,
	exit_price REAL,
	qty REAL NOT NULL,
	pnl REAL,
	opened_at TEXT NOT NULL,
	closed_at TEXT
);

CREATE TABLE IF NOT EXISTS signals (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	symbol TEXT NOT NULL,
	signal_type TEXT NOT NULL,
	reason TEXT NOT NULL,
	created_at TEXT NOT NULL,
	signal_price REAL,
	indicators_complete INTEGER,
	confirmed_candles INTEGER,
	required_candles INTEGER,
	previous_open_interest REAL,
	current_open_interest REAL,
	previous_oi_at TEXT,
	current_oi_at TEXT,
	oi_growth_percent REAL,
	oi_growth_threshold_percent REAL,
	oi_condition_met INTEGER,
	current_volume REAL,
	volume_sma REAL,
	volume_ratio REAL,
	volume_threshold REAL,
	volume_multiplier REAL,
	volume_condition_met INTEGER,
	candle_open REAL,
	candle_close REAL,
	candle_direction TEXT,
	short_liquidations_usd REAL,
	long_liquidations_usd REAL,
	liquidation_threshold_usd REAL,
	short_liquidation_condition_met INTEGER,
	long_liquidation_condition_met INTEGER,
	long_setup_met INTEGER,
	short_setup_met INTEGER,
	evaluated_at TEXT,
	liquidation_window_start TEXT,
	hypothetical_entry REAL,
	stop_loss REAL,
	take_profit REAL,
	estimated_fees REAL,
	estimated_slippage REAL,
	virtual_qty REAL,
	signal_candle_at TEXT,
	virtual_exit_price REAL,
	exit_reason TEXT,
	virtual_pnl REAL,
	virtual_closed_at TEXT
);`)
	if err != nil {
		return fmt.Errorf("migrate storage: %w", err)
	}

	if err := s.ensureSignalAnalysisColumns(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record schema migration: %w", err)
	}
	return nil
}

// OpenVirtualSignals читает все незакрытые виртуальные позиции по ID.
func (s *Store) OpenVirtualSignals(ctx context.Context) ([]OpenVirtualSignal, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, symbol, signal_type, signal_candle_at, hypothetical_entry, stop_loss, take_profit,
       estimated_fees, estimated_slippage, virtual_qty
FROM signals
WHERE virtual_closed_at IS NULL AND (hypothetical_entry IS NOT NULL OR stop_loss IS NOT NULL OR
      take_profit IS NOT NULL OR estimated_fees IS NOT NULL OR estimated_slippage IS NOT NULL OR virtual_qty IS NOT NULL)
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list open virtual signals: %w", err)
	}
	defer rows.Close()
	var result []OpenVirtualSignal
	for rows.Next() {
		var item OpenVirtualSignal
		var candleAt string
		if err := rows.Scan(&item.SignalID, &item.Symbol, &item.Side, &candleAt, &item.Plan.HypotheticalEntry, &item.Plan.StopLoss, &item.Plan.TakeProfit, &item.Plan.EstimatedFees, &item.Plan.EstimatedSlippage, &item.Plan.Qty); err != nil {
			return nil, fmt.Errorf("scan open virtual signal: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, candleAt)
		if err != nil {
			return nil, fmt.Errorf("parse open virtual signal candle time: %w", err)
		}
		item.Plan.SignalCandleAt = parsed.UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open virtual signals: %w", err)
	}
	if len(result) > 1 {
		return nil, fmt.Errorf("ambiguous open virtual signals: found %d", len(result))
	}
	return result, nil
}

// SaveSignal сохраняет сигнал без дополнительных снимков.
func (s *Store) SaveSignal(ctx context.Context, signal strategy.Signal) error {
	_, err := s.SaveSignalRecord(ctx, SignalRecord{Signal: signal})
	return err
}

// SaveSignalRecord сохраняет сигнал, индикаторы и виртуальный план.
func (s *Store) SaveSignalRecord(ctx context.Context, record SignalRecord) (int64, error) {
	var signalPrice any
	if record.SignalPrice != nil {
		signalPrice = *record.SignalPrice
	}
	indicatorValues := signalIndicatorValues(record.Indicators)

	var hypotheticalEntry any
	var stopLoss any
	var takeProfit any
	var estimatedFees any
	var estimatedSlippage any
	var virtualQty any
	var signalCandleAt any
	if record.Virtual != nil {
		hypotheticalEntry = record.Virtual.HypotheticalEntry
		stopLoss = record.Virtual.StopLoss
		takeProfit = record.Virtual.TakeProfit
		estimatedFees = record.Virtual.EstimatedFees
		estimatedSlippage = record.Virtual.EstimatedSlippage
		virtualQty = record.Virtual.Qty
		signalCandleAt = record.Virtual.SignalCandleAt.UTC().Format(time.RFC3339Nano)
	} else if record.Indicators != nil && !record.Indicators.SignalCandleAt.IsZero() {
		signalCandleAt = record.Indicators.SignalCandleAt.UTC().Format(time.RFC3339Nano)
	}

	var exitReason any
	if record.ExitReason != "" {
		exitReason = record.ExitReason
	}

	result, err := s.db.ExecContext(ctx, `
INSERT INTO signals (
	symbol,
	signal_type,
	reason,
	created_at,
	signal_price,
	indicators_complete,
	confirmed_candles,
	required_candles,
	previous_open_interest,
	current_open_interest,
	previous_oi_at,
	current_oi_at,
	oi_growth_percent,
	oi_growth_threshold_percent,
	oi_condition_met,
	current_volume,
	volume_sma,
	volume_ratio,
	volume_threshold,
	volume_multiplier,
	volume_condition_met,
	candle_open,
	candle_close,
	candle_direction,
	short_liquidations_usd,
	long_liquidations_usd,
	liquidation_threshold_usd,
	short_liquidation_condition_met,
	long_liquidation_condition_met,
	long_setup_met,
	short_setup_met,
	evaluated_at,
	liquidation_window_start,
	hypothetical_entry,
	stop_loss,
	take_profit,
	estimated_fees,
	estimated_slippage,
	virtual_qty,
	signal_candle_at,
	exit_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Signal.Symbol,
		string(record.Signal.Type),
		record.Signal.Reason,
		time.Now().UTC().Format(time.RFC3339Nano),
		signalPrice,
		indicatorValues[0],
		indicatorValues[1],
		indicatorValues[2],
		indicatorValues[3],
		indicatorValues[4],
		indicatorValues[5],
		indicatorValues[6],
		indicatorValues[7],
		indicatorValues[8],
		indicatorValues[9],
		indicatorValues[10],
		indicatorValues[11],
		indicatorValues[12],
		indicatorValues[13],
		indicatorValues[14],
		indicatorValues[15],
		indicatorValues[16],
		indicatorValues[17],
		indicatorValues[18],
		indicatorValues[19],
		indicatorValues[20],
		indicatorValues[21],
		indicatorValues[22],
		indicatorValues[23],
		indicatorValues[24],
		indicatorValues[25],
		indicatorValues[26],
		indicatorValues[27],
		hypotheticalEntry,
		stopLoss,
		takeProfit,
		estimatedFees,
		estimatedSlippage,
		virtualQty,
		signalCandleAt,
		exitReason,
	)
	if err != nil {
		return 0, fmt.Errorf("save signal record: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("save signal record: get id: %w", err)
	}
	return id, nil
}

// signalIndicatorValues преобразует снимок индикаторов в значения SQL.
func signalIndicatorValues(indicators *strategy.Indicators) [28]any {
	var values [28]any
	if indicators == nil {
		return values
	}

	values[0] = indicators.Complete
	values[1] = indicators.ConfirmedCandles
	values[2] = indicators.RequiredCandles
	values[3] = indicators.PreviousOpenInterest
	values[4] = indicators.CurrentOpenInterest
	values[5] = nullableTime(indicators.PreviousOpenInterestAt)
	values[6] = nullableTime(indicators.CurrentOpenInterestAt)
	values[7] = indicators.OIGrowthPercent
	values[8] = indicators.OIGrowthThresholdPercent
	values[9] = indicators.OIConditionMet
	if indicators.Complete {
		values[10] = indicators.CurrentVolume
		values[11] = indicators.VolumeSMA
		if indicators.VolumeRatioAvailable {
			values[12] = indicators.VolumeRatio
		}
		values[13] = indicators.VolumeThreshold
		values[14] = indicators.VolumeMultiplier
		values[15] = indicators.VolumeConditionMet
		values[16] = indicators.CandleOpen
		values[17] = indicators.CandleClose
		values[18] = string(indicators.CandleDirection)
	}
	values[19] = indicators.ShortLiquidationsUSD
	values[20] = indicators.LongLiquidationsUSD
	values[21] = indicators.LiquidationThresholdUSD
	values[22] = indicators.ShortLiquidationConditionMet
	values[23] = indicators.LongLiquidationConditionMet
	values[24] = indicators.LongSetupMet
	values[25] = indicators.ShortSetupMet
	values[26] = nullableTime(indicators.EvaluatedAt)
	values[27] = nullableTime(indicators.LiquidationWindowStart)
	return values
}

// nullableTime преобразует нулевое время в SQL NULL.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// CloseVirtualSignal сохраняет результат ранее открытого виртуального сигнала.
func (s *Store) CloseVirtualSignal(ctx context.Context, signalID int64, outcome VirtualOutcome) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE signals
SET virtual_exit_price = ?, exit_reason = ?, virtual_pnl = ?, virtual_closed_at = ?
WHERE id = ? AND hypothetical_entry IS NOT NULL AND virtual_closed_at IS NULL`,
		outcome.ExitPrice,
		outcome.ExitReason,
		outcome.PnL,
		outcome.ClosedAt.UTC().Format(time.RFC3339Nano),
		signalID,
	)
	if err != nil {
		return fmt.Errorf("close virtual signal: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("close virtual signal: rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: signal id %d", ErrVirtualSignalNotOpen, signalID)
	}
	return nil
}

// ensureSignalAnalysisColumns добавляет отсутствующие колонки аналитики сигналов.
func (s *Store) ensureSignalAnalysisColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(signals)`)
	if err != nil {
		return fmt.Errorf("inspect signals schema: %w", err)
	}

	existing := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("inspect signals schema: %w", err)
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect signals schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect signals schema: %w", err)
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "signal_price", definition: "signal_price REAL"},
		{name: "indicators_complete", definition: "indicators_complete INTEGER"},
		{name: "confirmed_candles", definition: "confirmed_candles INTEGER"},
		{name: "required_candles", definition: "required_candles INTEGER"},
		{name: "previous_open_interest", definition: "previous_open_interest REAL"},
		{name: "current_open_interest", definition: "current_open_interest REAL"},
		{name: "previous_oi_at", definition: "previous_oi_at TEXT"},
		{name: "current_oi_at", definition: "current_oi_at TEXT"},
		{name: "oi_growth_percent", definition: "oi_growth_percent REAL"},
		{name: "oi_growth_threshold_percent", definition: "oi_growth_threshold_percent REAL"},
		{name: "oi_condition_met", definition: "oi_condition_met INTEGER"},
		{name: "current_volume", definition: "current_volume REAL"},
		{name: "volume_sma", definition: "volume_sma REAL"},
		{name: "volume_ratio", definition: "volume_ratio REAL"},
		{name: "volume_threshold", definition: "volume_threshold REAL"},
		{name: "volume_multiplier", definition: "volume_multiplier REAL"},
		{name: "volume_condition_met", definition: "volume_condition_met INTEGER"},
		{name: "candle_open", definition: "candle_open REAL"},
		{name: "candle_close", definition: "candle_close REAL"},
		{name: "candle_direction", definition: "candle_direction TEXT"},
		{name: "short_liquidations_usd", definition: "short_liquidations_usd REAL"},
		{name: "long_liquidations_usd", definition: "long_liquidations_usd REAL"},
		{name: "liquidation_threshold_usd", definition: "liquidation_threshold_usd REAL"},
		{name: "short_liquidation_condition_met", definition: "short_liquidation_condition_met INTEGER"},
		{name: "long_liquidation_condition_met", definition: "long_liquidation_condition_met INTEGER"},
		{name: "long_setup_met", definition: "long_setup_met INTEGER"},
		{name: "short_setup_met", definition: "short_setup_met INTEGER"},
		{name: "evaluated_at", definition: "evaluated_at TEXT"},
		{name: "liquidation_window_start", definition: "liquidation_window_start TEXT"},
		{name: "hypothetical_entry", definition: "hypothetical_entry REAL"},
		{name: "stop_loss", definition: "stop_loss REAL"},
		{name: "take_profit", definition: "take_profit REAL"},
		{name: "estimated_fees", definition: "estimated_fees REAL"},
		{name: "estimated_slippage", definition: "estimated_slippage REAL"},
		{name: "virtual_qty", definition: "virtual_qty REAL"},
		{name: "signal_candle_at", definition: "signal_candle_at TEXT"},
		{name: "virtual_exit_price", definition: "virtual_exit_price REAL"},
		{name: "exit_reason", definition: "exit_reason TEXT"},
		{name: "virtual_pnl", definition: "virtual_pnl REAL"},
		{name: "virtual_closed_at", definition: "virtual_closed_at TEXT"},
	}

	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE signals ADD COLUMN `+column.definition); err != nil {
			return fmt.Errorf("add signals.%s: %w", column.name, err)
		}
	}
	return nil
}

// SaveTrade сохраняет сделку в таблицу биржевых сделок.
func (s *Store) SaveTrade(ctx context.Context, trade Trade) error {
	var closedAt any
	if trade.ClosedAt != nil {
		closedAt = trade.ClosedAt.UTC().Format(time.RFC3339Nano)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO trades (symbol, side, entry_price, exit_price, qty, pnl, opened_at, closed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		trade.Symbol,
		trade.Side,
		trade.EntryPrice,
		trade.ExitPrice,
		trade.Qty,
		trade.PnL,
		trade.OpenedAt.UTC().Format(time.RFC3339Nano),
		closedAt,
	)
	if err != nil {
		return fmt.Errorf("save trade: %w", err)
	}
	return nil
}

// Stats считает статистику закрытых биржевых сделок.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pnl FROM trades WHERE closed_at IS NOT NULL ORDER BY closed_at ASC`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	var stats Stats
	var grossProfit float64
	var grossLoss float64
	var equity float64
	var peak float64
	var wins int

	for rows.Next() {
		var pnl float64
		if err := rows.Scan(&pnl); err != nil {
			return Stats{}, err
		}

		stats.TotalTrades++
		stats.TotalPnL += pnl
		if pnl > 0 {
			grossProfit += pnl
			wins++
		}
		if pnl < 0 {
			grossLoss += -pnl
		}

		equity += pnl
		if equity > peak {
			peak = equity
		}
		drawdown := peak - equity
		if drawdown > stats.MaxDrawdown {
			stats.MaxDrawdown = drawdown
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}

	if stats.TotalTrades == 0 {
		return stats, nil
	}

	stats.AverageTrade = stats.TotalPnL / float64(stats.TotalTrades)
	stats.WinRate = float64(wins) / float64(stats.TotalTrades) * 100
	if grossLoss > 0 {
		stats.ProfitFactor = grossProfit / grossLoss
	}

	return stats, nil
}

// VirtualStats считает завершённые виртуальные сигналы отдельно от открытых и пропущенных.
func (s *Store) VirtualStats(ctx context.Context) (VirtualStats, error) {
	var stats VirtualStats
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats transaction: %w", err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL THEN 1 END),
COUNT(CASE WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NULL THEN 1 END),
COUNT(CASE WHEN hypothetical_entry IS NULL AND exit_reason LIKE 'SKIPPED\_%' ESCAPE '\' THEN 1 END) FROM signals`).Scan(&stats.ClosedTrades, &stats.OpenTrades, &stats.SkippedSignals)
	if err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats counts: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT virtual_pnl
FROM signals
WHERE hypothetical_entry IS NOT NULL AND virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL
ORDER BY virtual_closed_at ASC, id ASC`)
	if err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats rows: %w", err)
	}
	defer rows.Close()

	var wins int
	var grossProfit, grossLoss, equity, peak float64
	for rows.Next() {
		var pnl float64
		if err := rows.Scan(&pnl); err != nil {
			return VirtualStats{}, fmt.Errorf("virtual stats scan: %w", err)
		}
		stats.TotalPnL += pnl
		if pnl > 0 {
			wins++
			grossProfit += pnl
		} else if pnl < 0 {
			grossLoss -= pnl
		}
		equity += pnl
		if equity > peak {
			peak = equity
		}
		if drawdown := peak - equity; drawdown > stats.MaxDrawdown {
			stats.MaxDrawdown = drawdown
		}
	}
	if err := rows.Err(); err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats close rows: %w", err)
	}
	if stats.ClosedTrades > 0 {
		stats.AverageTrade = stats.TotalPnL / float64(stats.ClosedTrades)
		stats.WinRate = float64(wins) / float64(stats.ClosedTrades) * 100
		if grossLoss > 0 {
			stats.ProfitFactor = grossProfit / grossLoss
		}
	}
	if err := tx.Commit(); err != nil {
		return VirtualStats{}, fmt.Errorf("virtual stats commit: %w", err)
	}
	return stats, nil
}
