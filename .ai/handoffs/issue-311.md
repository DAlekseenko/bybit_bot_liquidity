# Issue #311 — READY for independent review

- Контракт: `codex-cm014-mark-read:v1`; Issue #311 и exact three-path reservation подтверждены; parent/schema/row prerequisites выполнены, зависимости #287/#309 для чтения не нужны.
- Owner: `/root/issue311_developer`; task copy `/private/tmp/bybit-issue-311`; branch `codex/311-mark-range-read`; current master base `66c43e0da9fee3f772382bc278d2e40a4d48cce1` (fast-forward from prior base `55c157919fe889448a0e61478d2e1191061e96a3`).
- Точные изменённые пути: `internal/momentum/storage/mark_read.go`, `internal/momentum/storage/mark_read_test.go`, этот handoff. Production/test budgets: 116/274 lines.
- `ReadMarkPriceRange` читает `momentum.mark_prices` в одном REPEATABLE READ READ ONLY snapshot; проверяет exact dataset provenance и stream namespace, использует миллисекундный полуоткрытый UTC диапазон, сортирует ASC, ограничивает limit+1, сохраняет `Confirmed` и gaps, выполняет `trim_scale` плюс точный decimal parse и отказывает без частичного результата. Writer/fallback/runtime/risk/trading не затронуты.
- Все полные проверки выполнены на base `66c43e0`: test, race, обычное coverage с unset DSN (70.5%, порог 70%), vet, build, golangci-lint v2.10.1, govulncheck v1.1.4 и gofmt/whitespace/diff. Security scan сообщает 0 reachable findings и 1 module-only advisory.
- PostgreSQL 17.11 targeted race suite выполнен без пропусков: D/60, off-grid границы, namespace/provenance/redaction, exact NUMERIC, gaps/Confirmed, no-fallback, 200/201, invalid/cancel/deadline, наблюдаемая блокировка и repeatable-read snapshot. Контейнер остановлен/удалён; version, databases и absence подтверждены.
- Финальные hashes, команды и durable logs: `/private/tmp/issue311-ready.json`; предыдущий READY комплект сохранён в `/private/tmp/issue311-ready-history.iPftsp`.
- Index пуст; commit/push/PR отсутствуют. Состояние READY; ждать результата независимого ревью. После GREEN любые blob edits требуют нового review.
