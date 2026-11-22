# Контекст проекта

Общие правила и workflow — в [AGENTS.md](../AGENTS.md). Этот файл содержит только
краткую архитектуру и подтверждённые решения. Текущий статус задач — в vault
(`Status.md`), а не здесь.

## Архитектура

- Go module `bybit_bot_liquidity`, совместимость Go 1.24.
- Единственная стратегия — Crypto Momentum: `cmd/momentum`, `internal/momentum/*`,
  PostgreSQL. Прежний бот Liquidity удалён (тег `legacy-liquidity-final`).
- Адаптер Bybit V5 — `internal/exchange/bybit`: public REST (дневные/часовые свечи,
  funding, mark price, instrument rules), WebSocket, signed read-only GET
  (account, balance, position, leverage). Private order/close transport не
  реализован.
- Данные: импорт истории в PostgreSQL с checkpoints, общий UTC-срез символов,
  write-once датасет с manifest и checksum; backtest читает только замороженный
  датасет.
- Расчёт: точный decimal (`big.Rat`), ROC30, baseline-доли `equity/3`, план
  дельт с биржевыми шагами; planner без I/O возвращает неизменяемый `TradePlan`.
- Учёт: ledger подтверждённых fills, комиссий, funding; equity = wallet +
  unrealized P&L по mark.
- `cmd/momentum collect-once` — только публичный сбор и запись signal batch;
  торговых вызовов нет.

## Подтверждённые границы

Движок общий, стратегии подключаемые
([ADR-001](../docs/adr/ADR-001-engine-and-strategies.md)): стратегия — чистый
planner → `TradePlan`, всё остальное — движок. Momentum — первая стратегия.

Граница: planner → immutable `TradePlan` → отдельная confirmation/authorization
policy → execution → подтверждённые fills → ledger. Это целевая граница, не
реализованное поведение; она не требует ручного подтверждения каждого плана и не
даёт live-полномочий. Backtest и paper получают только simulated execution
adapter. Testnet и Mainnet имеют разные явные guards; Mainnet нельзя включить
сменой одного поля конфигурации.

Перед исполнением на Testnet (этап M4, класс B с ограничениями Testnet; Mainnet —
класс C) остаются TODO: private
place/cancel transport и wire format, isolated balance/position read models,
preflight с маржой, order lifecycle и reconciliation, восстановление после
перезапуска, безопасная интеграционная проверка Testnet. Новые Issue должны явно
определить контракт; не выводить решения из целевой архитектуры.
