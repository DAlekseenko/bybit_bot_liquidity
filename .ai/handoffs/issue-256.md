# Issue #256 — handoff

<!-- codex-momentum-signal-journal-read:v1 -->

Checkpoint `issue-256-c1` — implementation complete, awaiting independent GREEN.

- Chat: `01a1deca-1b5e-7e31-87fd-75c036a198d6`, host `local`.
- Owner: `developer-middle`.
- Worktree: `/private/tmp/bybit-issue-256`.
- Branch: `codex/issue-256-signal-batch-read`.
- Base: `e400a4dbb2f95c6204004b5fee24d8f0e2d9c128`.
- Contract: [Issue #256](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/256), marker `codex-momentum-signal-journal-read:v1`. Contract base `2f4b5a4b5934ab9c6abbc3099fb78fe09983a896` is an ancestor of the requested base; the only intervening commit is docs-only #258.

## Slot grant and refreshed base

Coordinator queue comment [revision 174](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55#issuecomment-6668604794) grants `ACTIVE_SINGLE_SLOT` for #256. From this isolated worktree, `origin/master`, `FETCH_HEAD`, and branch HEAD all read `e400a4dbb2f95c6204004b5fee24d8f0e2d9c128`; the commit range to refreshed master is empty and the merge base is identical. This is a verified no-op base refresh. No merge or publication was performed.

Raw fetch/readback evidence: `/private/tmp/issue256-slot-refresh-fetch.log` and `/private/tmp/issue256-slot-refresh-readback.log`. Exact untracked file diff against `/dev/null`: `/private/tmp/issue256-final-diff.patch`.

## Implementation

`ReadSignalBatch` reads one parameterized exact-key row, rejects noncanonical lowercase SHA-256 input, and verifies stored key, finite post-epoch timestamp, canonical payload, replayed ROC30 signals, and resulting hash. It checks context immediately after payload decoding and before classifying a decode/hash failure, preserving `ErrSignalBatchUnavailable` and the context cause if cancellation occurs during replay. All failures return a zero value and safe sentinel categories. The method performs no writes, migration, custom retry, explicit locking, or runtime integration.

Changed only the approved three paths:

- `internal/momentum/storage/signal_batch_read.go` — 73 lines.
- `internal/momentum/storage/signal_batch_read_test.go` — 147 lines, within the 150-line test budget.
- `.ai/handoffs/issue-256.md`.

## Verification

Go `1.26.11`; PostgreSQL image `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`. Actual `SHOW server_version_num` returned `170011`; `SHOW server_version` returned `17.11 (Debian 17.11-1.pgdg13+2)`. Each disposable server used 1 GiB tmpfs, loopback-only ephemeral port, synthetic credentials, and no host mounts. Stage-specific redacted synthetic DSNs, SHOW output, runtime metadata, and explicit post-removal `docker inspect` readbacks are saved with logs in `/private/tmp/issue256-evidence/`.

- Targeted PostgreSQL tests and targeted race on the corrected final code — PASS without skips (`final-targeted-test.log`, `final-targeted-race.log`).
- `go test -count=1 ./...` and `go test -race -count=1 ./...` on the corrected final code — PASS without skips (`final-full-test.log`, `final-full-race.log`).
- CI coverage suite — PASS on the corrected final code, 83.3% overall against 70% minimum (`final-coverage-test.log`, `final-coverage-summary.log`, `final-coverage.out`).
- `go vet ./...`, `go build ./...`, Go formatting check, and `git diff --check` — PASS (`vet.log`, `build.log`, `format-status.log`).
- `golangci-lint v2.10.1` — PASS, 0 issues (`golangci.log`).
- `govulncheck v1.1.4` — PASS, 0 reachable findings; it reported one advisory in a required module that this code does not call (`govulncheck.log`).

## Status and next step

No implementation blocker. Worktree remains uncommitted and unpublished pending independent review of the exact three paths and hashes. After GREEN, the same owner may publish only those reviewed blobs. Testnet/read-only scope remains unchanged; no broker calls, order transport, deployment, credentials, risk changes, migrations, or Momentum readiness claim were added.
