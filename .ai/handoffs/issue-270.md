# Issue #270 handoff

<!-- codex-cm012-page-checkpoint:v1 -->

- Checkpoint: `issue-270-c1`.
- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/270.
- Contract: `codex-cm012-page-checkpoint:v1`.
- Task/chat: `01a1e5ed-20a7-7bd3-8fe8-a78dfba93dae`; developer owner: `/root/issue270_developer` (`developer-middle`).
- Base and branch HEAD: `c7481aae83740fd81fccef5036782ee934cfd120`.
- Branch: `codex/270-candle-import-checkpoint`.
- Worktree: `/private/tmp/issue270-candle-import-checkpoint`.
- Git common directory: `/private/tmp/issue270-candle-repo/.git`; isolated service clone uses the local source checkout as its read-only origin. The source checkout's `.git` rejected branch-ref writes, so the clone was created with `--no-local --no-hardlinks`; the requested commit was fetched by SHA. The source dirty checkout and index remain untouched.
- Approved files: `internal/momentum/storage/candle_import_advance.go`; `internal/momentum/storage/candle_import_advance_test.go`; `.ai/handoffs/issue-270.md`. No dependency, migration, schema, API, runtime, broker, deployment, or risk changes.
- Production/test source sizes: 117 / 284 physical lines.

## Implementation

`AdvanceCandleImportPage` validates UTC bucket-aligned window and checkpoint values, verifies the caller's READ COMMITTED writable transaction, locks the exact existing progress row, and compares revision, cursor, and state after lock acquisition. It accepts only `pending` and `progress` checkpoints with available revision space. It checks that the page is a confirmed contiguous prefix, appends through the existing immutable writer, and advances the row in the same transaction. Failures return a zero checkpoint, preserve root/context sentinels, and roll back the caller transaction. The page is not mutated.

Tests cover D/60 short-page commit and caller rollback, a pinned independent observer connection and durable resume, exact source time at a distinct +137 ms instant and offset timestamps, stale revision/cursor/state, absent namespace, invalid transaction modes, blocked and max-revision rows, invalid keys and independently aligned cursor bounds, contiguous lower-bound and below-lower rows, gap and late reorder after valid prefix, confirmation, future/source cutoff, size, duplicate rows, immutable exact-payload `xmin`, divergent writer rollback, forced UPDATE rollback, and zero checkpoint on every error including lock contention and deadline. The target and complete Momentum PostgreSQL race suites completed without skips.

## Verification

- Go toolchain: Go 1.26.11.
- PostgreSQL: pinned `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`; SQL `server_version_num=170011`; loopback-only port `63681`; fresh disposable 1 GiB tmpfs; synthetic test credentials. Container was removed and absence verified.
- Target: `go test -race -count=1 -v -p 1 ./internal/momentum/storage -run '^TestAdvanceCandleImportPage'` — PASS, no skips; log `/private/tmp/issue270-targeted-v2.log`.
- Momentum PostgreSQL matrix: `go test -race -count=1 -v -p 1 ./cmd/momentum ./internal/momentum/...` — PASS, no skips; log `/private/tmp/issue270-momentum-pg-v2.log`.
- CI unit tests: `go test ./...` — PASS; log `/private/tmp/issue270-unit-v2.log`.
- Coverage: `go test -coverprofile=/private/tmp/issue270-coverage-v2.out ./...` — PASS, 75.0% total against the 70% CI threshold; logs `/private/tmp/issue270-coverage-run-v2.log` and `/private/tmp/issue270-coverage-summary-v2.log`.
- Full Go race: `go test -race ./...` — PASS; log `/private/tmp/issue270-race-v2.log`.
- `go vet ./...`, `go build ./...`, Go formatting check, and whitespace check — PASS; logs `/private/tmp/issue270-vet-v2.log` and `/private/tmp/issue270-build-v2.log` (vet/build clean).
- golangci-lint v2.10.1 — PASS, 0 issues; log `/private/tmp/issue270-lint-v2.log`.
- govulncheck v1.1.4 — PASS; 0 reachable vulnerabilities. It found one required-module advisory that is not called by project code; log `/private/tmp/issue270-vuln-v2.log`.

## State and next action

The branch is based on the G7-approved `ACTIVE_SINGLE_SLOT` base. The three approved #270 files are the only working-tree changes. The original READY record remains immutable at `/private/tmp/issue270-ready.json` (SHA256 `dbdbcf872340eaadce38467e8e5ad86859debfa2a97aea4e9a288fac2e23f5f6`). A prior 327-line test draft is preserved separately at `/private/tmp/issue270-test-draft-327.go` (SHA256 `02ebb933c8498c05928e466ed4c71d5e1ba6bfcbbed696dea6a2736ba53b7025`). Nothing is staged, committed, pushed, or published. Next is fresh independent review of the exact v2 blobs. No Testnet requests, trades, deployment, or risk changes were performed.
