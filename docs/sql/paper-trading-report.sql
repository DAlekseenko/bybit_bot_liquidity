-- Paper-trade report. It never reads the trades table because that table is
-- reserved for a future exchange-execution lifecycle.
SELECT
  id,
  symbol,
  signal_type,
  signal_candle_at,
  hypothetical_entry,
  stop_loss,
  take_profit,
  virtual_qty,
  estimated_fees,
  estimated_slippage,
  virtual_exit_price,
  exit_reason,
  virtual_pnl,
  virtual_closed_at
FROM signals
WHERE hypothetical_entry IS NOT NULL
ORDER BY signal_candle_at ASC, id ASC;

-- Aggregate only completed paper trades. Open rows are intentionally excluded.
SELECT
  COUNT(*) AS closed_trades,
  SUM(CASE WHEN virtual_pnl > 0 THEN 1 ELSE 0 END) * 100.0 / COUNT(*) AS win_rate_percent,
  SUM(virtual_pnl) AS total_virtual_pnl,
  AVG(virtual_pnl) AS average_virtual_pnl
FROM signals
WHERE virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL;
