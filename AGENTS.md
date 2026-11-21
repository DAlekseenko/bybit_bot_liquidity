# Инструкции участникам разработки

Источники истины: код, тесты, `go.mod` и `configs/*.yaml` — что реализовано;
[SPEC](docs/crypto-momentum/SPEC.md) и [DECISIONS](docs/crypto-momentum/DECISIONS.md)
Momentum — что решено.

## О проекте

Go-система автоматической торговли на Bybit V5 по заранее утверждённому
детерминированному алгоритму. Цель ([PROJECT_GOAL.md](PROJECT_GOAL.md)) —
надёжное исполнение: свежие проверенные данные, неизменяемый `TradePlan`,
идемпотентные заявки, сверка с биржей, восстановление после рестарта и
аварийная остановка. Поиск и оптимизация стратегий в задачи системы не входят.

| Часть | Код и хранилище | Что делает |
| --- | --- | --- |
| Crypto Momentum | `cmd/momentum`, `internal/momentum/*`, PostgreSQL | Портфель из трёх USDT perpetual (пример: BTC, ETH, HYPE): дневной ROC30, доля `equity/3` на символ, недельная ребалансировка, LONG или USDT; точная арифметика `big.Rat`, ledger |
| Адаптер биржи | `internal/exchange`, `internal/exchange/bybit` | Bybit V5: public/signed REST, WebSocket, правила инструментов; без торговых правил |
| Control API | `internal/controlapi` | Политика авторизации операторских команд |

**Движок и стратегии** ([ADR-001](docs/adr/ADR-001-engine-and-strategies.md)).
Один общий движок (данные, ledger, исполнение, риск, отчёты) и подключаемые
стратегии. Стратегия — чистый planner, который из рыночного среза и
`PortfolioSnapshot` строит `TradePlan`; движок не знает о конкретной стратегии.
Momentum — первая стратегия; пакеты пока в `internal/momentum/*`, роли пакетов —
в ADR.

Прежний бот Liquidity (`cmd/bot`, SQLite) удалён; код — по тегу
`legacy-liquidity-final`. Не восстанавливайте его без отдельной Issue.

Спецификация Momentum — [docs/crypto-momentum/SPEC.md](docs/crypto-momentum/SPEC.md),
задачи — GitHub Issues (эпик #55, CM-001…CM-062).

## Ограничения

- Только Testnet и read-only. Не подключайте отправку и закрытие ордеров, live
  broker calls и order/close transport без отдельной Issue с явным одобрением
  владельца. Смена полей конфигурации (`mode`, `environment`) сама по себе не
  должна включать торговлю.
- Paper-trade — виртуальная аналитика, не исполнение. Fill не выводится из
  отсутствующей заявки: доказательство — только исполненная операция биржи.
- Live trading, deploy, изменение risk limits и destructive migrations требуют
  явного одобрения владельца; workflow сам таких полномочий не выдаёт.
- Не меняйте настроенные accounts и `primary_execution` вне отдельно одобренного
  scope.
- Целевая архитектура Momentum ([CONTEXT](.ai/CONTEXT.md), SPEC) не даёт
  live-полномочий; политику подтверждения планов задаёт только актуальный контракт.

## Карта чтения

| Нужно | Источник |
| --- | --- |
| Краткая архитектура и подтверждённые решения | [.ai/CONTEXT.md](.ai/CONTEXT.md) |
| Процедура Issue, роли, handoff, публикация | [.ai/WORKFLOW.md](.ai/WORKFLOW.md) |
| Handoffs Issue | [`.ai/handoffs/`](.ai/handoffs/); `.ai/HANDOFF.md` — legacy |
| Правила, формулы, архитектура Momentum | [docs/crypto-momentum/SPEC.md](docs/crypto-momentum/SPEC.md) |
| Решения владельца и открытые вопросы | [docs/crypto-momentum/DECISIONS.md](docs/crypto-momentum/DECISIONS.md) |
| Архитектурные решения (ADR) | [docs/adr/](docs/adr/) |
| Запуск и структура | [README.md](README.md) |

Контекст проекта: `~/Library/Mobile Documents/iCloud~md~obsidian/Documents/Knowledge-Hub/01 Projects/Momentum trading bot`

## Границы кода

- Совместимость с Go 1.24 из `go.mod`, `gofmt`, стандартная библиотека; новую
  dependency обоснуйте, а если она влияет на архитектуру — обновите
  `docs/crypto-momentum/DECISIONS.md`.
- `context.Context` первым у внешних/долгих операций, ошибки с контекстом на
  границах, без глобального изменяемого состояния. Секреты только в env, не в Git
  и логах. Не редактируйте IDE-файлы и чужие изменения.
- Краткие русские комментарии к каждой функции/методу, включая private и test
  helpers; комментарий экспортируемого идентификатора начинается с его имени.
- Constructor injection; интерфейсы принадлежат потребителю и минимальны.
- Код движка не вводит допущений стратегии (число символов, недельный цикл,
  ROC30, `equity/3`): такие значения приходят от стратегии параметрами.
- `planner`, `model` и расчёты backtest без I/O; биржевой адаптер без торговых
  правил; SQL только в `internal/momentum/storage`; координация — в `cmd/*`.
- Деньги, цены и количества — точный decimal (`big.Rat`), не `float64`.
- Sentinel errors для ожидаемых ветвей, структурированный `slog`, время в UTC.
- Доменные типы и единицы сохраняйте либо явно документируйте изменение в SPEC.
- Табличные или точечные unit-тесты рядом с пакетом; реальные заявки в тестах
  запрещены. Не запускайте бота для теста.

## Команды

```bash
test -z "$(gofmt -l cmd internal)"
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

CI дополнительно запускает golangci-lint, govulncheck и coverage. Тесты PostgreSQL
Momentum без `MOMENTUM_TEST_POSTGRES_DSN` пропускаются (`t.Skip`), и зелёный
`go test ./...` их не проверяет. Для них нужен одноразовый локальный PostgreSQL с
базой `momentum_test_*`; в CI — [momentum-postgres.yml](.github/workflows/momentum-postgres.yml).
Запуск и конфигурация — в [README](README.md).

## Процесс Issue

Одна Issue — одна ветка и один PR; тело Issue — единственный контракт. Строгость
процесса задаёт класс риска Issue:

- **A — офлайн** (model, planner, backtest, отчёты, импорт публичных данных) и
  **B — состояние** (PostgreSQL, paper runtime): лёгкий процесс — один developer,
  независимый reviewer, handoff в описании PR, gate = approve + зелёный CI.
- **C — деньги** (private API, заявки, исполнение, reconciliation, risk limits):
  полный процесс с designer, GREEN по хэшам, очередью интеграции и одобрением
  владельца.

При сомнении выбирается более высокий класс. Классы, лимиты и обе процедуры — в
[.ai/WORKFLOW.md](.ai/WORKFLOW.md); он обязателен для orchestrator и всех ролей
перед работой над Issue.
