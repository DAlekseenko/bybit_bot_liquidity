# Issue 287 handoff

<!-- codex-cm014-mark-write:v1 -->

- Checkpoint: issue-287-c1
- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/287
- Task/chat: 01a1e6db-829e-7201-ad37-0c7d196d049f; developer owner: /root/issue287_developer (developer-middle).
- Worktree: /private/tmp/bybit-issue-287
- Branch/base: codex/287-mark-history-write from cb476b9d7f0973b6b7813d70774eea4ae3e5bb2f.
- Approved paths: internal/momentum/storage/mark_write.go, internal/momentum/storage/mark_write_test.go, .ai/handoffs/issue-287.md.
- Implementation: added atomic append for validated, confirmed mark-price pages using caller-owned READ COMMITTED transactions, exact dataset provenance and endpoint checks, existing key/time validation, exact Decimal/NUMERIC writes, immutable conflict verification and safe context/sentinel errors. PostgreSQL timestamp bounds are checked before SQL writes. Tests cover D/60 and both environments, boundary/timezone cases, 200 rows and a retained gap, precise OHLC and equal-scale replay, each conflict field and legacy unconfirmed rows, invalid input/provenance, transaction mode and rollback, SQL failure redaction, and concurrent winner commit/rollback/deadline behavior. A pure unit test also verifies exact decimal projection for a value beyond binary64 precision.
- Budget: 171 production Go lines and 300 touched test lines after gofmt, at the approved 250/300 limits. No dependency changes.
- Verification: Go 1.26.11; gofmt and `git diff --check`; fresh default `go test -count=1 ./...` and `go test -race -count=1 ./...` with `MOMENTUM_TEST_POSTGRES_DSN` unset; coverage 70.1%, above the 70% CI threshold; `go vet ./...`; `go build ./...`; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities). Targeted Issue #287 tests passed with race on PostgreSQL 17.11, no skips, and verified cleanup. An earlier full PostgreSQL-backed repository run also passed on the same production blobs. Logs and coverage are under `/private/tmp/issue287-v2-*`.
- State: READY at c1. Superseded by the G10 c2 checkpoint below. No staging, commit, push, PR, or independent review performed by developer.

## Checkpoint issue-287-c2 G10 integration

- Task/chat and owner remain 01a1e6db-829e-7201-ad37-0c7d196d049f and /root/issue287_developer. Worktree/branch remain /private/tmp/bybit-issue-287 and codex/287-mark-history-write.
- Base: fast-forwarded from c1 base cb476b9d7f0973b6b7813d70774eea4ae3e5bb2f to exact refreshed master 197b4aabd830bc05941b053a58466393887d4dd7. Ancestry verified and all three approved paths were absent from the base delta. READY-v2 remains preserved as historical evidence.
- Final budget: 171 production Go lines, 300 test lines, and three approved paths. The source and test blobs match READY-v2; this checkpoint updates only this handoff.
- Final-base verification: Go 1.26.11; `gofmt`, `git diff --check`; default `go test -count=1 ./...`, coverage with `MOMENTUM_TEST_POSTGRES_DSN` unset at 70.1%, and `go test -race -count=1 ./...` with the DSN unset; `go vet ./...`; `go build ./...`; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities).
- PostgreSQL verification: all Issue #287 targeted tests, including `TestMarkPriceWriteValues`, passed with race and no skips on pinned PostgreSQL 17.11. Disposable container cleanup verified. Version/image/cleanup evidence is `/private/tmp/issue287-g10-pg-evidence.txt`; test output is `/private/tmp/issue287-g10-targeted-pg-race.log`; default gate logs and coverage are `/private/tmp/issue287-g10-*`.
- State: READY at c2 before review. Superseded by the P2 correction and c3 checkpoint below. No staging, commit, push, or PR performed by developer.

## Checkpoint issue-287-c3 P2 correction

- Contract and base remain Issue #287 `codex-cm014-mark-write:v1` and exact G10 master `197b4aabd830bc05941b053a58466393887d4dd7`. The independent c2 RED is preserved at `/private/tmp/issue287-review-g10.json`, SHA256 `50dcc5730712aeb9ed58d43e4c6f2c54b4f0410b22ba676317bb62e8c693d8e4`.
- Correction: `markPriceMillis` now validates positive exact Unix-ms representability for page metadata without imposing a PostgreSQL ceiling. Stored row timestamps still require the existing UTC-grid `candleTime`, which rejects years beyond PostgreSQL's supported 294276 maximum. No runtime or contract scope changed.
- Regression coverage: the existing append fixture accepts year 3000 for D/60 pages; the argument cases separately reject metadata beyond int64 Unix-ms and a grid-aligned 294277 row whose start/end/server metadata remain valid. Existing acceptance cases and SQL trigger coverage remain present.
- Negative control: a `/private/tmp` Go overlay reinstated the old 2262 ceiling and caused the year-3000 `TestAppendMarkPricePage` case to fail with `ErrMarkPriceArgument`, as expected. Evidence: `/private/tmp/issue287-negative-control/negative-control.log`; overlay source is external to the worktree.
- Budget: 170 production Go lines, 300 test lines after gofmt, and the same three approved paths.
- Final verification on G10 base: Go 1.26.11; gofmt and `git diff --check`; full unit and race tests with PostgreSQL DSN unset; fresh default coverage 70.1%; `go vet ./...`; `go build ./...`; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities). All Issue #287 tests passed with race, no skips, on pinned PostgreSQL 17.11; cleanup verified. Logs are `/private/tmp/issue287-fix-*`.
- PostgreSQL detail record: `/private/tmp/issue287-fix-pg-evidence.txt`, SHA256 `05104c7362fb7317c1d64d2e314549c096f13efdac31bc3b0b2d2e20208e253b`; captured target output is `/private/tmp/issue287-fix-targeted-pg-race.log`.
- State: READY for a fresh independent exact-blob review. No staging, commit, push, PR, or independent review performed by developer.
