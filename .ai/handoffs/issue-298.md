# Issue #298 — DEV checkpoint

<!-- codex-cm012-candle-warmup:v1 -->

- Task/chat: `01a1f205-9414-7a60-94ea-fd0b6ecea9c0`; owner `/root/issue298_developer`, native `developer-middle`.
- Worktree `/private/tmp/bybit-issue-298`; branch `codex/298-candle-warmup`; fresh base `origin/master = 35642a0f4d84e22ff034dc0675cfcbb284014d0f`. The branch was fast-forwarded by ordinary merge from its prior base `f1a4e2b5ee4308580ef537bdffd583f73c455a09`.
- Approved paths: `internal/momentum/history/candle_warmup.go`, `internal/momentum/history/candle_warmup_test.go`, `.ai/handoffs/issue-298.md`.
- Implementation: one bounded `ReadCandleRange` call for `[cutoff-31d, cutoff)`; PostgreSQL validates the registered namespace, exact bounds, sorted precise values and isolation. Existing daily validator classifies ready, unavailable, insufficient, gap and unconfirmed states; only ready returns 31 exact closes. Errors return a zero result with reader/context sentinels preserved.
- Tests: exact range boundaries including lower bound, cutoff and out-of-range rows; ready, empty registered HYPE, 30-day, internal-gap, missing-tail, unconfirmed, namespace isolation, validation/context/read failures and exact decimal ordering.
- Checks on final files, Go 1.26.11: targeted `go test -race -count=1 -v ./internal/momentum/history -run '^TestReadCandleWarmup'` PASS against disposable PostgreSQL 17.11, all targeted tests ran and passed without skips; full `go test ./...`, full `go test -race ./...`, `go vet ./...`, `go build ./...`, gofmt and `git diff --check` PASS with `MOMENTUM_TEST_POSTGRES_DSN` unset. CI coverage: 2735/3860 statements = 70.854922% (70% minimum). golangci-lint v2.10.1: 0 issues. govulncheck v1.1.4 PASS: no reachable vulnerabilities; one module advisory is present but code does not appear to call it.
- Limits: implementation 96 physical lines; affected test file 192 physical lines after gofmt. Exact final file SHA256 and check logs are recorded in `/private/tmp/issue298-final-ready.json`; staging index is empty. PostgreSQL container `codex-298-final-pg17-20261119` was removed after the targeted run and verified absent.
- Next: independent reviewer under the active #298 integration slot. Developer stops at READY pending exact GREEN; no commit, push or PR before that review.
