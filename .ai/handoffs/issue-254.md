# Issue #254 — signal batch insert

<!-- codex-momentum-signal-journal-insert:v1 -->

Checkpoint: `issue-254-c1` (тот же этап DEV, remediation review findings).
Task/chat ID: `01a1d185-f73e-7331-b6d1-13b585389468` (host `local`).
Developer owner: `/root/issue254_developer` (`developer-middle`).
Worktree: `/Users/alekseenkodenis/.codex/worktrees/issue-254-signal-batch-insert/bybit_bot_liquidity`.
Branch: `codex/254-signal-batch-insert`.
Base: `33dd1cfa49bf4bde49cf39850d99986c762e25c2`; refreshed `origin/master` совпал, ветка не обновлялась.

Статус: DEV READY после review remediation. Изменения подготовлены для независимого повторного review, не staged и не committed.

## Основание и границы

Issue #254, approved body в GitHub — единственный контракт. База `33dd1cfa49bf4bde49cf39850d99986c762e25c2`; зависимости #247 и #249 закрыты при admission. Дизайн `/private/tmp/momentum-signal-journal-insert-g6.md`, SHA-256 `775f8e5caa4ed159c59f824012daf4ec0b9b2ea8657f5afc2d65f193a25f15c4`.

Добавлена одна попытка parameterized autocommit INSERT через выделенный `sql.Conn`. Сетевую потерю ACK инъецировать не удалось, ошибки вызова conservatively дают Unknown, автоматических повторов нет. Чтение обратно остаётся ответственностью вызывающего кода. Внешняя роль с raw SQL может менять таблицу. Новая торговля и runtime не подключались; Momentum остаётся `NOT_READY`.

## Пути и line budget

- `internal/momentum/storage/signal_batch_insert.go` — 62 lines.
- `internal/momentum/storage/signal_batch_insert_test.go` — 149 lines, включая комментарии и пустые строки.
- `.ai/handoffs/issue-254.md`.

Изменены ровно эти три новых пути. Существующие файлы не изменялись. В тесте есть точечный `nolint:staticcheck` для обязательного вызова с nil context, который проверяет argument guard.

## Проверки

Инструменты: Go `go1.26.11`, lib/pq `v1.12.3`, golangci-lint `v2.10.1`, govulncheck `v1.1.4`.

- `gofmt` для новых Go-файлов и `test -z "$(gofmt -l cmd internal)"` — PASS.
- `go test -count=1 -v ./internal/momentum/storage -run '^TestInsertSignalBatch'` — PASS без skips; remediation повтор: `/private/tmp/issue254-evidence/targeted-test-reviewfix.log`.
- `go test -race -count=1 -v ./internal/momentum/storage -run '^TestInsertSignalBatch'` — PASS без skips; remediation повтор: `/private/tmp/issue254-evidence/targeted-race-reviewfix.log`.
- `go test -race -count=1 -v ./internal/momentum/...` — PASS без skips.
- Direct pinned `pq.NewConnectorConfig` smoke against disposable PG confirmed `SELECT 1`, `server_version_num=170011` and closed-pool rejection — PASS.
- `go test -count=1 ./...` — PASS.
- `go test -race -count=1 ./...` — PASS.
- `go test -coverprofile=... -count=1 ./...` и CI threshold 70% — PASS, 78.5%.
- `go vet ./...`, `go build ./...`, `golangci-lint run ./...` — PASS, 0 lint issues.
- `govulncheck v1.1.4 ./...` — PASS; reported 0 vulnerabilities affecting code and 1 vulnerability in a required module that this code does not call.
- `git diff --check` — PASS.

## Disposable PostgreSQL evidence

Использован `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, каждый крупный запуск — отдельный одноразовый контейнер с `--tmpfs /var/lib/postgresql/data:size=1g`, publish только на `127.0.0.1` с назначенным ephemeral port и synthetic-only credentials. Фактический сервер `17.11 (Debian 17.11-1.pgdg13+2)`, `server_version_num=170011`. Имена пользователя, базы и credentials тестовые. DSN использовал только loopback; пароль в evidence не записан.

Исходные runtime metadata, SQL version, результаты и cleanup proof сохранены вне репозитория в `/private/tmp/issue254-evidence/`. Обе отдельные target проверки находятся в `targeted-test-final.log` и `targeted-race-final.log`; весь Momentum race — `momentum-race-final.log`; полный test/race — `full-test-final.log`, `full-race-final.log`. Direct driver smoke proof: `postgres-smoke-runtime.log`, `postgres-smoke.log`, `postgres-smoke-cleanup.log`. Контейнеры описаны в `targeted-test-runtime.log`, `targeted-race-runtime.log`, `full-test-runtime.log` и `full-race-runtime.log`; удаления подтверждают соответствующие `*-cleanup.log` и `final-no-container.log`. Go/lib/pq source review с исходными line numbers — `go-libpq-source-review.txt`. Остальные проверки: `coverage-final.log`, `vet-final.log`, `build-final.log`, `golangci.log`, `govulncheck.log`, `gofmt.log`. Для каждого контейнера `docker inspect` после `docker rm -f` завершился с ожидаемым отсутствием объекта.

Первый полный race запуск на сервере после уже пройденного полного набора исчерпал tmpfs 1 GiB. Он был удалён, а финальный полный набор повторён на чистом контейнере и прошёл. Это свидетельствует о накоплении временных test databases при повторном использовании одного контейнера, не о сбое проверки после очистки.

## Проверка поведения Go и lib/pq

В `/private/tmp/issue249-go12611/src/database/sql/sql.go` проверено, что `(*sql.Conn).ExecContext` вызывает `db.execDC` напрямую; `DB.retry` вызывается путями DB, но не этим путём выделенного Conn. В lib/pq `v1.12.3`, `conn_go18.go` `(*conn).ExecContext` ставит context watcher и вызывает `Exec`; переданные параметры идут через подготовленный statement. `readExecuteResponse` ждёт `ReadyForQuery`, сохраняет `ErrorResponse` до завершения ответа, а `parseComplete` получает affected-row count из `CommandComplete`. Реальный отложенный constraint-trigger тест подтвердил, что ошибка autocommit commit возвращается как `ErrSignalBatchInsertUnknown` и durable row отсутствует.

## Текущий blocker и следующий шаг

Первый reviewer нашёл пропуск метаданных canonical checkpoint и недостаточно строгие проверки pre-canceled/deadline context. Метаданные добавлены в этот же handoff, тесты сравнивают прямой возврат `context.Canceled` / `context.DeadlineExceeded` по точному значению. Review-fix проверки: `/private/tmp/issue254-evidence/full-test-reviewfix.log`, `full-race-reviewfix.log`, `vet-reviewfix.log`, `build-reviewfix.log`, `lint-reviewfix.log`, `vuln-reviewfix.log`, `coverage-reviewfix.log`; target runtime/cleanup — `targeted-runtime-reviewfix.log` и `targeted-cleanup-reviewfix.log`, full runtime/cleanup — `full-test-runtime-reviewfix.log`, `full-race-runtime-reviewfix.log`, `full-test-cleanup-reviewfix.log`, `full-race-cleanup-reviewfix.log`. Следующий шаг — независимый повторный review финальных blobs. Реализационных blockers нет.

## Ограничения

Сетевую потерю ACK напрямую не моделировали. Код не повторяет statement после вызова `ExecContext`. `NotInserted` подтверждает только число строк, затронутое данным INSERT, и не сверяет payload сохранённой строки. После `NotInserted` или `Unknown` нужен отдельный проверенный read-back с побайтовым сравнением.
