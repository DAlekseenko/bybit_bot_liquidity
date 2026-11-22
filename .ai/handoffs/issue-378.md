Issue #378; contract `codex-cm022i-offline-backtest-command:v1`; class A.
Branch `codex/378-offline-baseline-backtest-command`; base `26e04c1b719c5877b99f5e6c5bb5edb3e151de6b`.
Implemented offline replay and tests/docs; corrected main command comment after review; no trading or network path added.
Approved paths: `cmd/momentum/main.go`, `cmd/momentum/backtest.go`, `cmd/momentum/backtest_test.go`, `docs/crypto-momentum/BACKTEST.md`, `.ai/handoffs/issue-378.md`.
Budget: 208 production lines and 297 test lines; `git diff --check` clean.
Checks PASS: gofmt, targeted, `go test ./...`, race, vet, build, golangci-lint, offline govulncheck, coverage (74.6% total).
Initial sandbox-only loopback failures passed on authorized retry; no PostgreSQL DSN tests apply to this offline Issue.
Next: reviewer rechecks exact final blobs; stop before publication pending GREEN and owner grant.
