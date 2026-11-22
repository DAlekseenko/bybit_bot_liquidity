# Issue #437 — canonical Observation codec, класс A
Ветка: `feat/437-paper-observation-codec`; worktree: `/private/tmp/bybit-issue-437`; base: `219f01f4085adbd1218556c1cfde412d35c00d06`.
Контракт: live body #437 `codex-paper-observation-codec:v1`; foundation #426 присутствует в base; #427/#432 не изменялись.
Сделано: bare six-field canonical JSON, fullRat/UTC, полная provenance/OHLCV, nil/[]; копии request lists сортируются, raw rows сохраняются; market validity не утверждается.
Защита: exact byte/token/depth visitor по fixed DTO; lengths до clone/sort; global preflight + strict known-path streaming page/row counts до typed unmarshal; canonical re-encode.
Пути: `internal/momentum/paperwire/observation.go`, `observation_values.go`, `observation_limits.go`, `observation_test.go`, `observation_limits_test.go`; этот handoff. Scope: 594 production / 605 test lines, 6 paths.
PASS: gofmt, targeted paperwire tests, go test ./..., go test -race ./..., go vet ./..., go build ./..., golangci-lint v2.10.1 (0 issues), offline govulncheck (no vulnerabilities), git diff --check.
Coverage PASS: total 74.6%, paperwire 94.0%; Go toolchain 1.26.11, код совместим с Go 1.24. Логи: `/private/tmp/issue437-logs/`; sandbox bind failures перепроверены успешно с loopback-разрешением.
Не запускались opt-in PostgreSQL/Testnet contract tests: переменные отключены; SQL/API/бот не запускались; security использовал локальный snapshot базы.
Статус: LOCAL READY, без stage/commit/push/PR; blocker отсутствует; следующий шаг — independent review и публикация orchestrator по лёгкому процессу A.
