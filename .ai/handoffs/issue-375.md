# Issue #375 — локальный checkpoint
- Контракт: `codex-cm021b-complete-replay-journal:v1`, класс A; полный offline-журнал уже вычисленного `BaselineReplayResult`.
- Ветка `codex/375-offline-complete-replay-journal`, checkout `/private/tmp/bybit-issue-375`, base `0973d16aafbf7e46f3a54f18d9f5553e61403745`.
- Изменены только `internal/momentum/backtest/journal.go`, `internal/momentum/backtest/journal_test.go` и этот handoff.
- Реализованы канонический wire, точные суммы, IDs/plan payload, counts, проверки ссылок/топологии и строгий zero-result отказ; fixed-three codec-вызова нет.
- Целевые, `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, gofmt, diff-check, lint, offline govulncheck и обычный coverage завершились успешно.
- PostgreSQL integration tests пропущены: `MOMENTUM_TEST_POSTGRES_DSN` не задан; обычный coverage составил 27.7% storage, профиль сохранён вне checkout.
- Проверки выполнялись Go 1.26.11, `GOTOOLCHAIN=local`; полные unit/race/coverage потребовали разрешённого loopback fake-server теста, реальные API не вызывались.
- Следующий шаг: независимый review финальных blobs; до GREEN ничего не публиковать.
