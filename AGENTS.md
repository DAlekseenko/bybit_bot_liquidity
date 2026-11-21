# Инструкции участникам разработки

Источники истины: код, тесты, `go.mod`, Docker-файлы и `configs/*.yaml` — что
реализовано; [DOMAIN](docs/DOMAIN.md) и [DECISIONS](docs/DECISIONS.md) — что решено.

## О проекте

Go-система автоматической торговли на Bybit V5 по заранее утверждённому
детерминированному алгоритму. Цель ([PROJECT_GOAL.md](PROJECT_GOAL.md)) —
надёжное исполнение: свежие проверенные данные, неизменяемый `TradePlan`,
идемпотентные заявки, сверка с биржей, восстановление после рестарта и
аварийная остановка. Поиск и оптимизация стратегий в задачи системы не входят.

В репозитории два изолированных контура; код, схемы БД, runtime и Compose
между ними не смешиваются:

| Контур | Код и хранилище | Что делает |
| --- | --- | --- |
| Liquidity (legacy) | `cmd/bot`, `internal/{strategy,risk,papertrade,execution,storage,...}`, SQLite | Каждые 30 с читает 1m свечи, open interest и ликвидации одного символа, считает `LONG`/`SHORT`/`NONE`, ведёт paper-trade с net PnL; signed read-only проверки аккаунта |
| Crypto Momentum | `cmd/momentum`, `internal/momentum/*`, PostgreSQL | Портфель из трёх USDT perpetual (пример: BTC, ETH, HYPE): дневной ROC30, доля `equity/3` на символ, недельная ребалансировка, LONG или USDT; точная арифметика `big.Rat`, ledger |

Спецификация Momentum — [docs/crypto-momentum/SPEC.md](docs/crypto-momentum/SPEC.md),
задачи — GitHub Issues (эпик #55, CM-001…CM-062).

## Ограничения

- Только Testnet и read-only. Не подключайте отправку и закрытие ордеров, live
  broker calls и order/close transport без отдельной Issue с явным одобрением
  владельца. `dry_run: false` не должен включать торговлю.
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
| Бизнес-правила и формулы | [docs/DOMAIN.md](docs/DOMAIN.md) |
| Компоненты и зависимости | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Go и внешние контракты | [docs/API.md](docs/API.md) |
| Команды и конфигурация | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) |
| Принятые ограничения и TODO | [docs/DECISIONS.md](docs/DECISIONS.md) |
| Спецификация Momentum | [docs/crypto-momentum/SPEC.md](docs/crypto-momentum/SPEC.md) |

Контекст проекта: `~/Library/Mobile Documents/iCloud~md~obsidian/Documents/Knowledge-Hub/01 Projects/Momentum trading bot`

## Границы кода

- Совместимость с Go 1.24 из `go.mod`, `gofmt`, стандартная библиотека; новую
  dependency обоснуйте, а если она влияет на архитектуру — обновите
  `docs/DECISIONS.md`.
- `context.Context` первым у внешних/долгих операций, ошибки с контекстом на
  границах, без глобального изменяемого состояния. Секреты только в env, не в Git
  и логах. Не редактируйте IDE-файлы и чужие изменения.
- Краткие русские комментарии к каждой функции/методу, включая private и test
  helpers; комментарий экспортируемого идентификатора начинается с его имени.
- Constructor injection; интерфейсы принадлежат потребителю и минимальны.
- `strategy` и `risk` без I/O, биржевой адаптер без торговых правил, SQL только
  в `storage`, координация — в `execution` или composition root.
- Sentinel errors для ожидаемых ветвей, структурированный `slog`, время в UTC.
- Доменные типы и единицы сохраняйте либо явно документируйте изменение в DOMAIN
  и API.
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
Запуск бинарников и конфигурация — в [DEVELOPMENT](docs/DEVELOPMENT.md).

## Процесс Issue

Одна Issue — одна ветка, один PR и один чат; approved body Issue — единственный
контракт. Роли: system-designer → один developer owner → независимый reviewer;
publish после GREEN делает developer owner, merge и закрытие — только
orchestrator. Review укладывается в 15 минут: не более 6 файлов, 250 production
и 300 test lines, иначе контракт делится. Полная процедура — handoff, роли и
модели, GREEN, публикация, merge gate, очередь интеграции — в
[.ai/WORKFLOW.md](.ai/WORKFLOW.md); она обязательна для orchestrator и всех ролей
перед работой над Issue.
