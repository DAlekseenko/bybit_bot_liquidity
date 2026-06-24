package storage

import (
	"context"
	"database/sql"
	"time"

	"bybit_bot_liquidity/internal/strategy"
)

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

// Store инкапсулирует доступ к SQLite через database/sql.
type Store struct {
	db *sql.DB
}

func Open(driverName, dsn string) (*Store, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
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
	created_at TEXT NOT NULL
);`)
	return err
}

func (s *Store) SaveSignal(ctx context.Context, signal strategy.Signal) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO signals (symbol, signal_type, reason, created_at) VALUES (?, ?, ?, ?)`,
		signal.Symbol,
		string(signal.Type),
		signal.Reason,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

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
	return err
}

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
