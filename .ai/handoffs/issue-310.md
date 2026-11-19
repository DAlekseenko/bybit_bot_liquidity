# Issue #310 READY — trade candle content digest

<!-- codex-cm015-trade-candle-content:v1 -->

- Contract: [Issue #310](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/310); exact base `55c157919fe889448a0e61478d2e1191061e96a3`.
- Task/chat: `01a1f2a8-7508-7ca3-97cd-32681f5df8f7`, owner `/root/issue310_developer` (`developer-middle`). Branch `codex/310-trade-candle-content`, copy `/private/tmp/bybit-issue-310`.
- Only approved paths: `internal/momentum/data/trade_candle_content.go`, `internal/momentum/data/trade_candle_content_test.go`, and this handoff.
- Implementation is a pure deterministic encoder over caller-selected actual stored OHLCV fields. It validates exact namespace/endpoints, UTC grids and ranges, confirmed candles, exact decimal values and OHLC bounds; sorts a copied row set, rejects duplicate timestamps, emits compact ordered JSON, and returns SHA-256. `Turnover` is absent. Empty content is not completeness evidence.
- Acceptance: exact 502-byte golden and SHA-256 `0ac58d8bafec8caf8cee4086c8a72a707bf8dd324a077c3b904ae914f339df8b`; ordering, zone and decimal-scale invariance, input/output independence, identity/window/field digest binding, empty rows, exact precision, range/invalid cases pass.
- Physical sizes after gofmt: production 159 lines, tests 244 lines. Test helpers have Russian comments. No dependencies added; production imports only standard library and `internal/momentum/model`.
- Verification: targeted race test, `go test ./...`, `go test -race ./...`, ordinary coverage with `MOMENTUM_TEST_POSTGRES_DSN` unset (71.2% total; 98.0% `internal/momentum/data`), `go vet ./...`, `go build ./...`, gofmt/diff check, golangci-lint v2.10.1 (0 issues), govulncheck v1.1.4 (0 reachable vulnerabilities; 1 module-only finding). Logs and coverage artifact under `/private/tmp/issue310-*.log` and `/private/tmp/issue310-coverage.out`.
- Local initial test attempts were blocked only because the sandbox forbade an existing test's temporary localhost listener; rerunning the same full test, race and coverage commands with permitted local networking passed.
- READY is a pre-review candidate only. Index remains empty; no commit, push, PR, review, merge, or Issue update. Wait for coordinator ACTIVE before final-base update/review/publication.

## Checkpoint issue-310-c2 — READY на refreshed master

<!-- codex-cm015-trade-candle-content:v1 -->

- Contract: [Issue #310](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/310). Native task/chat `01a1f2a8-7508-7ca3-97cd-32681f5df8f7`; owner `/root/issue310_developer`, `developer-middle`.
- Та же копия `/private/tmp/bybit-issue-310` и branch `codex/310-trade-candle-content`. Branch fast-forward обновлён без конфликтов с прежней базы до current master `173a61d0ca650de3883e956852565dad456dabbb`; HEAD совпадает с base. Исходный checkpoint выше сохранён побайтно и отдельно заархивирован в `/private/tmp/issue310-pre-active-20261120`.
- Только три approved paths: `internal/momentum/data/trade_candle_content.go`, `internal/momentum/data/trade_candle_content_test.go`, `.ai/handoffs/issue-310.md`. Реализация и контракт не менялись при обновлении базы. Размеры после gofmt: 159 production и 244 test lines.
- На новой базе успешно выполнены targeted race, `go test ./...`, `go test -race ./...`, ordinary coverage (DSN unset: 2946/4157 = 70.9%; package `data` 98.0%), `go vet ./...`, `go build ./...`, gofmt, whitespace scan и `git diff --check`, golangci-lint v2.10.1 (0 issues), govulncheck v1.1.4 (0 reachable vulnerabilities; 1 module-only advisory).
- Полные логи и coverage profile: `/private/tmp/issue310-c2-*.log`, `/private/tmp/issue310-c2-coverage.out`. PostgreSQL integration не запускался: `MOMENTUM_TEST_POSTGRES_DSN` unset. Golden остаётся 502 bytes с digest `0ac58d8bafec8caf8cee4086c8a72a707bf8dd324a077c3b904ae914f339df8b`.
- Следующий шаг: независимый reviewer проверяет exact final paths/blobs на этой базе. Индекс пуст; не было stage/commit/push/PR. Изменения после GREEN требуют нового ревью точных финальных blobs.
