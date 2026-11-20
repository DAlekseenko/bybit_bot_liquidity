# Issue #360 — DEV READY

- Контракт: [Issue #360](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/360), marker `codex-cm020c-market-boundaries:v1`; dependency #357 is merged in the selected base.
- Native task/chat: `01a1f7f5-4dad-7530-8504-2c269005cf04`. Sole developer owner: `/root/issue360_developer`, `developer-middle` / medium.
- Branch `codex/360-offline-market-boundaries`; isolated checkout `/private/tmp/bybit-issue-360`. Base `5810b2cae80ca40e3620efab543e8284936b1096`, tree `1137c8454c0c870c0379e9b5ca7ecadffb4cc006`.
- Scope: pure projection of selected mark/60 and trade/60 streams from a bound `FrozenDataset`. Emits each boundary's prior-hour mark close and its observation time, plus the current trade open reference for nonterminal groups. The terminal boundary retains its final closed-hour marks and has no trade opens. The API comment says an open reference does not prove execution. No funding, weekly schedule, fee/slippage, quantity, fill, or runtime behavior was added.
- Exact files: this handoff, `internal/momentum/backtest/market_tape.go` (112 lines), and `internal/momentum/backtest/market_tape_test.go` (200 physical lines). Both budgets are within #360's 220/250 caps. Synthetic fixtures use the existing content encoders and have no absolute execution path.

## Acceptance and verification

Synthetic tests pass for three canonical symbols, UTC normalization, prior close/current open, terminal output without end opens, repeatability/order, output ownership, and no look-ahead when only the current mark bar's High/Low/Close change. Rejection cases return nil plus `ErrMarketTape` for a missing selected stream, symbol/content mismatch, mutated bound bytes, missing prior mark, missing trade-open hour, insufficient pre-start history, unaligned/zero/reversed/empty/out-of-range intervals, and invalid configured symbols.

The independent actual frozen v3/master artifact check used the exact manifest selector and bytes from the existing frozen bundle, with a temporary certificate removed after use. It produced 337 boundaries: 336 actionable, 1 terminal, 1,008 mark observations; repeated output was `DeepEqual`. Targeted certificate race also passed. No PostgreSQL DSN was set.

Final checks on the candidate: targeted backtest tests, `go test ./...`, `go test -race ./...`, coverage, `go vet ./...`, `go build ./...`, gofmt, `git diff --check`, CI golangci-lint v2.10.1, and offline govulncheck v1.1.4 all passed. Full ordinary coverage is 72.0%. govulncheck found 0 reachable vulnerabilities and 1 module-only advisory. The complete unit/race/coverage suite required the explicitly allowed local test listener for existing `TestOpenPostgresFailures`; the database DSN remained unset and no external database was contacted.

Logs and exit receipts are under `/private/tmp/issue360-logs/`: `targeted-final2`, `unit-final2`, `race-final2`, `coverage-final2`, `vet-final2`, `build-final2`, `gofmt-final2.exit`, `diffcheck-final2`, `lint-final4`, `security-final4`, `actual-final2`, and `actual-race-final2`. Successful raw log SHA-256 values and exit codes are recorded in `/private/tmp/issue360-v1-ready.json`.

DEV READY only. The index is empty. No reviewer, staging, commit, push, PR, queue update, or trading action was performed. Stop and wait for G10's next instruction.


## Issue #360 — READY revalidation on current master

- q418 grants the sole `#360 ACTIVE_SINGLE_SLOT`. Same owner `/root/issue360_developer`, role `developer-middle` / medium, native task/chat `01a1f7f5-4dad-7530-8504-2c269005cf04`, branch `codex/360-offline-market-boundaries`. Exact Issue marker remains `codex-cm020c-market-boundaries:v1`.
- Checkout was refreshed FF-only from prior base `5810b2cae80ca40e3620efab543e8284936b1096` to current master `6d59e077991608e22537569b65971822cb305c68`, tree `6eefd002f9086a4d424c2c97f619e877984bb3c6`. Ancestry was verified; upstream changed only the five disjoint #356 paths. Both Issue #360 Go blobs remain byte-identical to v1: production SHA256 `799972a4ca22e4f8cad331319dcbab5a3d6ce04f27fd0f54195fbe2cbbb7b0af`, tests SHA256 `68bff6fe397db0ab388e419c8f1dff7bc9ba37a099a9fbbcf629f304b39dda18`. The complete old handoff prefix was preserved exactly: 3335 bytes, SHA256 `c538e7ea6527bc88efeceb9aea3cf1eb5e254d77bc1018d6f79d2af6c257aebd`.
- On the refreshed base: targeted backtest, full `go test ./...`, `go test -race ./...`, full coverage, `go vet ./...`, `go build ./...`, gofmt, diff-check, golangci-lint v2.10.1 and offline govulncheck v1.1.4 all passed. Coverage is 72.2%. `MOMENTUM_TEST_POSTGRES_DSN` is unset. The existing storage unit test requires a local listener; the G10-authorized local bind exception was used, with no external database connection.
- The independent actual frozen v3/master certificate passed again: 337 boundaries (336 actionable, one terminal), 1008 mark observations, repeated output equal; targeted race also passed. This validates only the market projection, not replay admission or an actual strategy replay.
- Lint reported 0 issues. The offline govulncheck used the cached public database ZIP SHA256 `714ff02d9533ec6ca269e565b61b27cbd959aa0af89d51f318261322e875861c`, whose filesystem timestamp is `2026-11-20T05:01:52+0300` (about six hours before this scan). It found 0 reachable vulnerabilities and one module-only advisory; this records the cache age rather than claiming a live database refresh.
- Raw logs and exit receipts: `/private/tmp/issue360-v2-logs/`. Exact hashes for all three final files, logs, base/tree and empty-index status are in `/private/tmp/issue360-v2-ready.json`.

DEV READY revalidated; index remains empty. No review, staging, commit, push, PR, queue mutation, or trading action was performed. Stop and wait for G10.


## Issue #360 — READY correction after independent RED

- The reviewer identified P2 isolation defect in the rejection table: `d := base` shallow-copied `FrozenDataset`, leaving the manifest stream slice and artifact map shared across randomized subtests. A symbol-mismatch mutation or bound-byte replacement could therefore contaminate later cases and let them pass for the wrong reason.
- Corrected `TestBuildMarketBoundariesRejectsIncompleteInputs`: each subtest creates a new `tapeFixture(t, 0)` and verifies it produces a valid baseline before applying that case's mutation. All acceptance cases and Russian comments remain. The production blob is unchanged from the reviewed v2 candidate: SHA256 `799972a4ca22e4f8cad331319dcbab5a3d6ce04f27fd0f54195fbe2cbbb7b0af`.
- Base remains actual master `6d59e077991608e22537569b65971822cb305c68`, tree `6eefd002f9086a4d424c2c97f619e877984bb3c6`; branch `codex/360-offline-market-boundaries`. Existing native task/chat `01a1f7f5-4dad-7530-8504-2c269005cf04`, owner `/root/issue360_developer`, role `developer-middle` / medium. Issue marker unchanged `codex-cm020c-market-boundaries:v1`.
- The isolation matrix passed 25 consecutive runs. Final targeted, full unit/race/coverage, vet, build, gofmt, diff-check, golangci-lint v2.10.1 and offline govulncheck v1.1.4 all pass; coverage 72.2%. DSN unset; the G10-authorized local listener exception was used only for the existing storage unit test.
- Final frozen v3/master certificate: 337 boundaries (336 actionable, one terminal), 1008 observations, repeat equal; certificate race passed. This is a market projection check, not an actual replay.
- Offline vulnerability database snapshot is unchanged: ZIP SHA256 `714ff02d9533ec6ca269e565b61b27cbd959aa0af89d51f318261322e875861c`, timestamp `2026-11-20T05:01:52+0300`. The final scan found 0 reachable and 1 module-only advisory; age at this scan is recorded in the v3 receipt.
- Final log paths and SHA-256, all three candidate blob IDs, line counts, base/tree, and preserved v2 prefix proof are recorded in `/private/tmp/issue360-v3-ready.json`.

DEV READY after bounded correction; index empty. Stop and wait for G10 fresh review authorization. No reviewer was started from this developer task; no staging, commit, push, PR, queue mutation, or trading action occurred.
