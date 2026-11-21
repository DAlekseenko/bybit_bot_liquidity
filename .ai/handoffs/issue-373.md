Issue #373, marker `codex-cm020g-pure-baseline-replay:v1`; класс риска A.
Ветка `codex/373-offline-baseline-replay-runner`, refreshed base `61e97a7453c0759864d53848c2fbb099a432fdd6`; merge #397 и оба post-merge CI подтверждены.
`ReplayBaseline` — узкий wrapper: bind/hash identity, один `BuildReplaySchedule`, fresh flat cash, no-funding Initial и прямой вызов `replayBaselineTimeline`.
Изменены только `internal/momentum/backtest/runner.go`, `internal/momentum/backtest/runner_test.go` и этот handoff; лимиты 71/280 строк соблюдены.
Targeted/full unit/race, gofmt, vet, build, lint, offline security и coverage прошли; stdout/stderr, exit codes и profile сохранены в `/private/tmp/issue373-gates-refresh61/`.
PostgreSQL integration пропущена без `MOMENTUM_TEST_POSTGRES_DSN`; replay не использует network/broker/order transport.
Следующий шаг: fresh independent review exact final blobs; до GREEN нет commit/push/PR/merge.
