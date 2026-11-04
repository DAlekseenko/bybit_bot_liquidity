# Контекст проекта

Общие правила и workflow — в [AGENTS.md](../AGENTS.md); начальный маршрут и
документы по конкретной необходимости указаны там. Этот файл содержит только
краткую архитектуру и подтверждённые решения.

## Архитектура и статус

- Go module `bybit_bot_liquidity`, исходная совместимость Go 1.24.
- Legacy Liquidity — единственный runtime: `cmd/bot`, `configs/config.yaml`,
  SQLite, Compose. Momentum config guards/sample есть в #59/PR #129; отдельные
  `cmd/momentum`, storage/runtime/Compose запланированы в #65/#86/#109/#112.
- Bybit V5 Testnet public REST/WebSocket дают свечи, OI и окно ликвидаций;
  read-only loop рассчитывает и сохраняет сигнал. Стратегия использует закрытые
  свечи; sizing резервирует комиссию и проскальзывание.
- SQLite хранит снимок индикаторов, условий и paper-trade расчёта. Paper-trade —
  виртуальная аналитика, не биржевое исполнение.
- Signed reads account/balance/one-way position/current leverage, instrument
  limits, account/leverage/readiness preflight реализованы. Startup readiness
  выполняется раз при наличии credentials и не останавливает public loop при
  ошибке; account values и секреты не логируются.
- Private order/close transport не реализован; executor не подключён к `cmd/bot`.
  `dry_run: false` не включает торговлю. Testnet/read-only ограничения обязательны.
- Сохраняйте все настроенные accounts и `primary_execution`; менять их можно
  только в отдельно одобренном scope.

## Подтверждённые границы

Liquidity и Crypto Momentum изолированы; код, схемы, runtime и Compose не
смешивать. Momentum граница: planner → immutable `TradePlan` → отдельная
confirmation/authorization policy → execution → подтверждённые fills → ledger.
Это целевая граница, не реализованное поведение. Разделение не требует ручного
подтверждения каждого плана и не даёт live-полномочий. #126 — документированный
этап #124; родитель открыт до runtime-доказательств.

Перед будущим подключением торговли остаются TODO: private order/close wire
format, выбор/установка целевого leverage, восстановление состояния после
перезапуска, безопасная интеграционная проверка Testnet. Новые Issue должны
явно определить контракт; не выводить решения из целевой архитектуры.
