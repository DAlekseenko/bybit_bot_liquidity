Issue #376, marker `codex-cm023b-bind-completed-replay-bundle:v1`, класс A.
Ветка `codex/376-offline-sealed-replay-bundle`, worktree `/private/tmp/bybit-issue-376`, base `ddbf14bfc30fc1fc143ae7391792b6a1cf739780`.
Изменены только `internal/momentum/backtest/run_artifacts.go`, `internal/momentum/backtest/run_artifacts_test.go` и этот handoff.
Добавлен чистый сборщик шести канонических файлов из свежих снимков и `EncodeReplayJournal`; writer и inventory #371 переиспользуются.
Проверены связи идентичности и исходной preview-ссылки, approval reference, exact wire/checksum, детерминизм, строгие отказы и ненулевой slippage без циклов.
Проверки: gofmt, targeted tests, `go test ./...`, race, vet, build, golangci-lint, offline govulncheck и coverage — успешно; логи в `/private/tmp/issue376-gates`.
Локальный fake listener потребовал разрешённый loopback retry; PostgreSQL integration tests пропущены без `MOMENTUM_TEST_POSTGRES_DSN`.
Риск: сериализатор не выполняет I/O, replay, торговые вызовы или изменение финансовых правил; следующий шаг — независимый review точных файлов и handoff.
