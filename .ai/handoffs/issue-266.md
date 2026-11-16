# Issue #266 handoff

<!-- codex-cm027-config-snapshots:v1 -->

- Checkpoint: `issue-266-c1`
- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/266
- Task/chat: `01a1e52f-d768-71e2-88a5-c213928e913e`
- Owner: `/root/issue266_developer`, native developer-middle
- Coordinator grant: `ACTIVE_SINGLE_SLOT rev208`; authorized for final base update and review preparation only.
- Issue contract: `codex-cm027-config-snapshots:v1`.
- Base and branch HEAD: `330a08f48a68c2117be409034e40f79cd02ba520`, fetched from `origin/master` and fast-forwarded with `git merge --ff-only FETCH_HEAD`.
- Branch: `codex/266-config-snapshots`
- Worktree: `/private/tmp/bybit-issue266`
- The fast-forward contained only #267 handoff and candle-write files. No migration/schema source changed upstream. Unstaged #266 files remain intact and the Git index is empty.

## Approved files and limits

- `internal/momentum/storage/config_snapshot_schema.go` — 9 lines.
- `internal/momentum/storage/migrations.go`.
- `internal/momentum/storage/migrations_test.go`.
- `internal/momentum/storage/market_schema_test.go`.
- `internal/momentum/storage/market_candles_schema_test.go`.
- `.ai/handoffs/issue-266.md` — this handoff.
- Exactly six approved paths. 10 production additions and 100 changed test lines (95 added, 5 removed), under the 250 production and 300 test line caps.

## Behavior

Migration v6 creates `momentum.config_snapshots` with a binary 32-byte SHA-256 primary key, nonempty exact payload bytes, finite server timestamp at or after Unix epoch, and a database digest check. Migration SQL v1–v5 is unchanged. `TestConfigSnapshotMigration` verifies that exact v1–v5 migration history and a real signal row survive application of v6, checks the v6 checksum and repeat behavior, and verifies that a colliding preexisting object and marker row survive a safe failure. Schema tests exercise the existing Parse→Snapshot→PostgreSQL→bytea read→Parse path for two valid configs and database rejection of invalid keys, payloads, digest, and timestamps.

## Verification on final base

Go 1.26.11 at `/private/tmp/g6-go12611.vbIcdd/go/bin/go`.

- Target PostgreSQL 17.11 race suite: 7/7 PASS, zero skips; cleanup verified.
- `go test -count=1 ./...`: PASS with disposable PostgreSQL 17.11 and loopback TCP.
- `go test -count=1 -coverprofile=... ./...`: PASS, 83.7% against the 70% CI threshold.
- `go test -race -count=1 -p 1 ./...`: PASS with PostgreSQL 17.11; package execution serialized to fit disposable storage; cleanup verified.
- `go vet ./...`, `go build ./...`, golangci-lint v2.10.1 (0 issues), full `gofmt -l cmd internal`, and `git diff --check`: PASS.
- `govulncheck@v1.1.4`: no reachable vulnerabilities. It reported one advisory in a required module that it did not find called.
- Consolidated PG evidence: `/private/tmp/issue266-final-pg.coTFQK0R/output.log`. This includes targeted tests, full test suite, CI coverage, full race suite, server version 170011, and cleanup verification.
- No broker/API requests, trades, deploys, or risk changes.

## Next step

Candidate is ready for fresh independent read-only review of the exact six blobs. No staging, commit, push, PR, or queue update has been performed.
