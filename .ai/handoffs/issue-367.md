# Issue #367 — DESIGN_BLOCKED checkpoint

- Контракт: [Issue #367](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/367), marker `codex-cm020e-prepare-baseline-cycle:v1`.
- Чат: `01a1f851-80a7-7b03-92fd-bc5073429c55`; owner `/root/issue367_developer`, `developer-middle`, medium.
- Worktree `/private/tmp/bybit-issue-367`; branch `codex/367-offline-prepare-baseline-cycle`; base `b2af76a2da66e4228adcc87e7f024788b2086cff`, tree `55f1a6dfa1bb78cfa3e8712dd5308123df4ea2fb`.
- Изменены только `internal/momentum/backtest/baseline_cycle.go` (140 строк), `internal/momentum/backtest/baseline_cycle_test.go` (298 строк), этот handoff. Лимиты соблюдены: 250/300.

## Реализация и покрытие

Реализована чистая композиция bound SimulationModel → D-07 ProjectAccountingStep → ReplayDailySignal → frozen portfolio snapshot → BaselineTargets/D-01 → immutable TradePlan → ProjectBaselineDeltas. Цикл проверяет исходный диапазон, onboarding/weekly policy, привязку модели к config/dataset и возвращает sentinel вместе с исходными делегированными ошибками без частичного результата. Identity использует зафиксированные JSON-рецепты SHA256.

Тесты проверяют полный трёхсимвольный расчёт с funding debit/credit, inactive target, reduction/increase, точные cutoff/equity/targets/deltas/metadata/hash; два независимых RunID/CycleID golden-вектора; детерминизм порядка map/funding и эквивалентных timezone; различия range/model; now и next_monday расписание, позднюю неделю и отрицательные расписания; ошибки модели/binding/dataset/range/kind/time/позиции/mark/funding/daily bytes/equity; атомарность, неизменность входов и владение планом.

Не дублируются полные числовые/keyset/codec матрицы уже существующих #356/#358/#364 тестов. Пробел: отдельные утверждения `Marks`/`Funding` map-slice ownership после вызова не исчерпывают каждый вложенный байтовый элемент Dataset, что передаётся независимому reviewer как остаточный риск. Нет фактического исторического запуска, fills, execution или admission; функция остаётся подготовкой плана по явным synthetic-test inputs. PostgreSQL не нужен: чистая композиция не обращается к storage/DB.

## Проверки

Проверки относятся к финальным Go-файлам; логи и SHA256 сохранены в `/private/tmp/issue367-v1-logs/`.

- `gofmt -w` и `test -z "$(gofmt -l cmd internal)"`: PASS; `git diff --check`: PASS.
- `go test ./internal/momentum/backtest`: PASS; targeted `go test -race ./internal/momentum/backtest`: PASS (`targeted-race-final.log`).
- `go test ./...`: PASS (`full-unit-final.log`); `go test -race ./...`: PASS (`full-race-final.log`). Первый запуск без разрешения локального bind выявил sandbox denial `listen tcp 127.0.0.1:0`; повтор выполнен с разрешённым `require_escalated` для test-only TCP listener, PostgreSQL DSN unset, торговые API не используются.
- `go vet ./...`, `go build ./...`: PASS (`vet-final.log`, `build-final.log`).
- CI coverage gate `go test -coverprofile=... ./...` и порог >=70%: PASS (`coverage-final.log`, `coverage-total-final.txt`).
- `golangci-lint v2.10.1 run ./...`: PASS (`lint-final.log`). Исправлена обнаруженная `ineffassign`; итоговый тестовый файл 298 строк.
- `govulncheck -db=file:///private/tmp/g10-public-vulndb.Q6sE6qf4/db ./...`: PASS (`security-final.log`).

## Статус и передача

DESIGN_BLOCKED: READY не объявляется, GREEN не получен. Проверено отсутствие явных assertions для increase delta quantity, confirmation-required, полного plan metadata (StrategyID/ProfileID/ModelID/FormulaID/ConfigHash/ScheduledAt). Дополнительные требования к finer precision, end-of-source/terminal guards и полному getter/result ownership audit также не подтверждены тестами. Полная матрица не установлена в текущем лимите 300 физических строк; требуется возврат контракта system-designer для пересмотра бюджета или декомпозиции. Юнит, race, vet, build, lint, offline security, coverage 73.1%, gofmt и diff-check относятся к текущему неполному кандидату. Не staged, не committed, не pushed; PR отсутствует. Не запускались reviewer, публикация, merge или реальный исторический replay. Следующий шаг — G10 возвращает Issue designer; до нового DEV статус остаётся заблокированным.

## Exact Go blobs

- `internal/momentum/backtest/baseline_cycle.go`: SHA256 `7b884925aad2c18d8a81bc24a0ef9434437baa15ed54c8fa85dcb419422b0bec`; Git blob `2b0e956a1b2ecd2aa6ef23661df6363a6b6d0ae9`.
- `internal/momentum/backtest/baseline_cycle_test.go`: SHA256 `e973ff291e34f832901f05127f4b62e787f8dc87d09d340a8b0118b508d48d30`; Git blob `2c0a2ed77e54020296d5aa4e8c6631b12512bc5f`.

## Checkpoint v2 — READY candidate on refreshed base

The prior DESIGN_BLOCKED checkpoint above records the v1 acceptance gaps and remains as history. G10 authorized a v2 continuation on refreshed base `809ac9361169008b785c2f1310f992e1267a64b8` (tree `6d8134df20ebe325cd9b03acafaeb756c3c96958`). Branch remains `codex/367-offline-prepare-baseline-cycle`; worktree `/private/tmp/bybit-issue-367`; owner/chat/role remain `/root/issue367_developer`, `01a1f851-80a7-7b03-92fd-bc5073429c55`, developer-middle/medium. Approved test-only adaptation retained every prior case and added exact ETH increase `19.85`, reduction confirmation requirement, full typed PlanMetadata comparison including config hash/scheduled cutoff, and typed cloning for mutable inputs; encoded-plan errors are checked. Final sizes are 140 production / 299 test lines, three paths total.

Final v2 source identities: production SHA256 `7b884925aad2c18d8a81bc24a0ef9434437baa15ed54c8fa85dcb419422b0bec`, blob `2b0e956a1b2ecd2aa6ef23661df6363a6b6d0ae9`; tests SHA256 `992342a4768cf226f8a1a6269f40609a0a85fc40204863348f6604a90dcddf95`, blob `dd9e90ab75cff8f70cb916ddd193e8f5f72a03f4`.

Final v2 commands all passed on these Go files: targeted unit/race, `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, golangci-lint v2.10.1, offline govulncheck with approved local DB, CI coverage gate (total is recorded in `/private/tmp/issue367-v2-logs/coverage-total.txt`), gofmt check and `git diff --check`. Logs and digests are in `/private/tmp/issue367-v2-logs/`. Full test/race/coverage required `require_escalated` because a test-only TCP listener bind was denied in the sandbox; approved retry passed. PostgreSQL DSN was unset; this pure composition needs no database.

Current v2 status is READY candidate, not independent GREEN. G10 may authorize the independent review stage after verifying `/private/tmp/issue367-v2-ready.json`. No reviewer, stage, commit, push, PR, queue mutation, merge or actual historical run occurred. Previous v1 DESIGN_BLOCKED evidence is retained above and superseded for current candidate only by this v2 checkpoint.
