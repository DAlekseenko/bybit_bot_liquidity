# Issue #299 READY — final candidate до independent review

Checkpoint: `issue-299-c2`.

<!-- codex-cm027-plan-revisions-schema:v1 -->

- Контракт: [Issue #299](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/299), CM-027D. Parent #79; consumer #83. #266/#271/#272 — completed.
- Owner `/root/issue299_developer`, native `developer-middle`; implementation chat `01a1f215-88ec-7fb3-9490-36c3249ad384`, host `local`.
- Очередь: единственный координатор G10 `01a1ee9e-f0d5-71c2-ba96-ceadad8f8f5f`; canonical comment #55 `6668604794`, revision 329; для #299 ACTIVE_SINGLE_SLOT, migration v8 и шесть exact-path reservations HELD.
- Копия `/private/tmp/bybit-issue-299`, branch `codex/299-trade-plan-revisions`, HEAD/base `85685bbdd518fd4bb58baa74f2368ad4d76e96e9`. GitHub `refs/heads/master` независимо подтверждён на том же SHA. Fast-forward от READY-базы `f1a4e2b5ee4308580ef537bdffd583f73c455a09`; intervening commits не затрагивают шесть зарезервированных путей, каталог миграций всё ещё v1–7. Основная пользовательская копия не менялась.
- Пути: `internal/momentum/storage/plan_revision_schema.go`, `internal/momentum/storage/migrations.go`, `internal/momentum/storage/migrations_test.go`, `internal/momentum/storage/market_schema_test.go`, `internal/momentum/storage/market_candles_schema_test.go`, `.ai/handoffs/issue-299.md`.
- Изменение: добавлена только миграция v8 и неизменяемая `momentum.plan_revisions`; v1–7 не менялись. Таблица проверяет точный `NUMERIC` uint64 revision, SHA-256 payload, predecessor/hash chain, run FK, timestamps и непустой payload. BEFORE UPDATE OR DELETE выдаёт статическое сообщение с SQLSTATE 55000. Новые Go API, runtime и trade transport не добавлены.
- PostgreSQL 17.11: целевые migration/schema/bounds/codec race-тесты прошли (4/4, без skips). Весь `internal/momentum/storage` прошёл race за 44.894 s без skips. Проверены rollback suffix collision, сохранность history/checksum/applied_at v1–7 и старых run/config rows, immutable UPDATE/DELETE, FK/check/digest границы, max uint64 и predecessor, actual `model.EncodeTradePlan` chain 1→2→3 с точным 1/3 и наносекундами. Disposable server/container удалён.
- Go 1.26.11, `MOMENTUM_TEST_POSTGRES_DSN` unset: `go test ./...` и `go test -race ./...` прошли. CI coverage — 2821/4025 = 70.086956521739%, порог 70%. Также прошли `go vet ./...`, `go build ./...`, gofmt, `git diff --check`, golangci-lint v2.10.1 (0 issues), govulncheck v1.1.4 (0 reachable vulnerabilities). Одна module-only issue GO-2026-5024 в `golang.org/x/sys@v0.36.0`, Windows-only, fixed in v0.44.0.
- Diff budget: 32 production additions; 183 затронутые физические test lines; ровно 6 разрешённых путей. Полные логи и coverage profile: `/private/tmp/issue299-final-logs/`.
- Исходный pre-merge READY и его file hashes сохранены без изменений: `/private/tmp/issue299-ready.json` (SHA256 `286b06bd307454999c24bdf86a3b7ca6ba58bb9abb1a32775cf6cbbb64b404a0`). Новый manifest перечисляет финальные hashes и проверочные evidence: `/private/tmp/issue299-final-candidate.json`.
- Следующий шаг: независимый reviewer сверяет эти шесть финальных blobs на указанной базе. Index пуст; commit, push, PR и merge ещё не выполнялись.
