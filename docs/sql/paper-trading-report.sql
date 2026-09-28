BEGIN DEFERRED TRANSACTION; SELECT COUNT(CASE WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL THEN 1 END) AS completed_count,
       COUNT(CASE WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NULL THEN 1 END) AS open_count,
       COUNT(CASE WHEN hypothetical_entry IS NULL AND exit_reason LIKE 'SKIPPED\_%' ESCAPE '\' THEN 1 END) AS skipped_count
FROM signals;
SELECT id, symbol, signal_type, created_at, hypothetical_entry, virtual_closed_at, virtual_pnl, exit_reason,
       CASE WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL THEN 'completed'
            WHEN hypothetical_entry IS NOT NULL AND virtual_closed_at IS NULL THEN 'open'
            WHEN hypothetical_entry IS NULL AND exit_reason LIKE 'SKIPPED\_%' ESCAPE '\' THEN 'skipped' ELSE 'other' END AS virtual_status
FROM signals
ORDER BY virtual_closed_at IS NULL, virtual_closed_at, id;
WITH completed AS (
  SELECT id, virtual_closed_at, virtual_pnl FROM signals
  WHERE hypothetical_entry IS NOT NULL AND virtual_closed_at IS NOT NULL AND virtual_pnl IS NOT NULL
), running AS (
  SELECT id, virtual_closed_at, virtual_pnl,
         SUM(virtual_pnl) OVER (ORDER BY virtual_closed_at, id ROWS UNBOUNDED PRECEDING) AS equity FROM completed
), drawdowns AS (
  SELECT MAX(CASE WHEN equity > 0 THEN equity ELSE 0 END)
         OVER (ORDER BY virtual_closed_at, id ROWS UNBOUNDED PRECEDING) - equity AS drawdown FROM running
), aggregate_stats AS (
  SELECT COUNT(*) AS completed_count, COALESCE(SUM(virtual_pnl), 0) AS total_pnl,
         COALESCE(AVG(virtual_pnl), 0) AS average_trade,
         CASE WHEN COUNT(*) = 0 THEN 0 ELSE 100.0 * SUM(CASE WHEN virtual_pnl > 0 THEN 1 ELSE 0 END) / COUNT(*) END AS win_rate_percent,
         COALESCE(SUM(CASE WHEN virtual_pnl > 0 THEN virtual_pnl ELSE 0 END), 0) AS gross_profit,
         COALESCE(-SUM(CASE WHEN virtual_pnl < 0 THEN virtual_pnl ELSE 0 END), 0) AS gross_loss FROM completed
)
SELECT completed_count, total_pnl, average_trade, win_rate_percent,
       CASE WHEN gross_loss > 0 THEN gross_profit / gross_loss ELSE 0 END AS profit_factor,
       (SELECT COALESCE(MAX(drawdown), 0) FROM drawdowns) AS max_drawdown FROM aggregate_stats; COMMIT;
