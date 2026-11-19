# Issue #271 handoff — issue-271-c1

<!-- codex-cm027-run-identity:v1 -->

## Контракт

- Issue: [#271 — неизменяемая идентичность запусков ребалансировки Momentum](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/271), `codex-cm027-run-identity:v1`.
- Checkpoint: `issue-271-c1`.
- Task/chat: `01a1e5ed-2ffc-7d42-88f3-78b2f0d91d73`; owner `/root/issue271_developer`, native developer-middle.
- Coordinator grant: `ACTIVE_SINGLE_SLOT rev217`, authorized for final base integration and independent review preparation only.
- База: `1b1058b7b9a490001a3a2af6dfc16abd84046092`; ветка `codex/271-paper-run-identity`.
- Worktree: `/private/tmp/issue-271-paper-run-identity`, отдельный локальный clone с собственным Git index. `origin` указывает на `git@github.com:DAlekseenko/bybit_bot_liquidity.git`.
- `git fetch origin master` завершился свежим `FETCH_HEAD=1b1058b7b9a490001a3a2af6dfc16abd84046092`, совпавшим с branch HEAD. Интеграция — подтверждённый no-op, merge/fast-forward не требовался.
- PR #234 / Issue #65 и PR #269 / Issue #266 подтверждены на текущей базе и достижимы по истории.
- Реализуется только migration v7 `momentum.rebalance_runs` и её контрактные проверки. SQL v1–v6 сохранён.

## Изменённые файлы

1. `internal/momentum/storage/rebalance_run_schema.go` — схема таблицы, ключи, CHECK, внешний ключ и триггер неизменяемости.
2. `internal/momentum/storage/migrations.go` — применение только нового суффикса v7.
3. `internal/momentum/storage/migrations_test.go` — реальные PostgreSQL проверки миграции, ключей, ограничений, SQLSTATE и неизменяемости.
4. `internal/momentum/storage/market_schema_test.go` — ожидаемый размер каталога обновлён до семи версий.
5. `internal/momentum/storage/market_candles_schema_test.go` — ожидаемый размер каталога обновлён до семи версий.
6. `.ai/handoffs/issue-271.md` — передача реализации и доказательств.

## Проверки и блокеры

- Подтверждено, что #65 и #266 достижимы от базового SHA. Очередь G7 назначила v7.
- Go 1.26.11: форматирование `cmd`/`internal`, `git diff --check`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...` прошли.
- Coverage: `go test -coverprofile=... ./...` прошёл, суммарно 76.2% при пороге 70%.
- golangci-lint v2.10.1: 0 issues. govulncheck v1.1.4: 0 вызываемых/импортируемых уязвимостей; найдена одна module-only advisory, код проекта её не вызывает.
- PostgreSQL 17.11, `server_version_num=170011`, image digest из CI, loopback, одноразовый tmpfs 1 GiB и synthetic DSN. Целевой race-пакет: 9/9 PASS без skips на финальном содержимом. Весь Momentum PostgreSQL race workflow прошёл с `-p 1` без skips. Direct pq connector smoke подтвердил `SELECT 1`, версию и закрытие pool. Все контейнеры удалены, cleanup проверен.
- Финальные логи проверок: целевой PostgreSQL `/private/tmp/issue271-targeted.Cn8KnJV6`, полный Momentum PostgreSQL и pq smoke `/private/tmp/issue271-momentum-pg.DJJ62iW6`, общий gate `/private/tmp/issue271-final-gates.cbJnoQCl`.
- Исходный `READY` manifest сохранён в `/private/tmp/issue271-ready.json`. Финальный набор hashes/blob IDs и review input будут в `/private/tmp/issue271-review-input.json`.
- Выполнены интеграционная проверка базы, повторные тесты и финальная проверка blobs. Следующий шаг — fresh independent read-only review ровно шести файлов по review input. Stage/commit/push/PR не выполнялись.
