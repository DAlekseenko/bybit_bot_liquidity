# Issue #244 — c1

- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/244
- Contract: bounded read-only OHLCV range for registered Bybit linear datasets.
- Owner: `/root/issue244_developer` (`developer-middle`, Luna/medium).
- Chat: `01a1bc68-21e0-7a63-8374-8734731594f9`.
- Worktree/branch/base: `/Users/alekseenkodenis/.codex/worktrees/issue244-candle-read/bybit_bot_liquidity`, `codex/244-candle-read`, `f00608461065ccbd862e77d7ba88254d46486a25`.
- State: DEV implementation complete; awaiting orchestrator admission to independent exact-blob review. No commit, push, or PR.

## Scope and behavior

Approved paths only: `internal/momentum/storage/candle_read.go`, `internal/momentum/storage/candle_read_test.go`, and this handoff. The reader validates exact stream keys and UTC-aligned half-open day/hour bounds, reads known dataset provenance and OHLCV rows in one read-only REPEATABLE READ transaction, orders oldest-first and queries `limit+1`, parses NUMERIC text through exact Decimal parsers, returns nil on every error, and exposes no Turnover field. Existing schema/catalog/model/registry/HTTP and global handoff files were not edited.

Measured content is 134 production lines and 148 physical test lines, including imports, comments, blank lines, and test helpers. The complete acceptance specimen was retained; no cases were removed or compressed. `gofmt` and `git diff --check` pass.

## Verification

- Targeted `TestReadCandle*`: PASS, 4 tests, no skips, on a fresh PostgreSQL 17.11 / `server_version_num=170011` container. Runtime inspect confirmed the pinned image digest, 1 GiB tmpfs and loopback port; stdout/stderr were saved separately. Transcript: `/private/tmp/issue244-targeted-1g.log`, SHA256 `15067a3939bd1fe8e0febc004a12290ed5436b3821b4384c6cd7b2f8da7330aa`.
- `go test -count=1 -v ./...`: PASS on a separate fresh pinned PostgreSQL 17.11 container with 1 GiB tmpfs, no skips; transcript `/private/tmp/issue244-full-test-1g.log`, SHA256 `bb9a2aff966f3f029a1b861649eb0dba51b50b9784571aa787425d0ee03e2f8d`.
- `go test -race -count=1 -v ./...`: PASS on its own 1 GiB container, no skips; transcript `/private/tmp/issue244-full-race-1g.log`, SHA256 `d41d0a1401d9b3a618341d26a01ad4362afa0d98df016a12c6a101a548a57b3a`.
- `go test -race -count=1 -v ./internal/momentum/...`: PASS on its own 1 GiB container, no skips; transcript `/private/tmp/issue244-momentum-race-1g.log`, SHA256 `8281f8654dc4ae76ba4ebfcde749f2259a9b5ddaef552a865d67bc5ba856ede5`.
- `go test -count=1 -coverprofile=/private/tmp/issue244-coverage.out ./...`: PASS on its own 1 GiB container, no skips. Total coverage is 82.8%; storage package is 94.4%. Transcript `/private/tmp/issue244-coverage-1g.log`, SHA256 `77e48a32e08c61e05afb0080e00c376acd67bfdd156fee1b936708637d68fed6`.
- `go vet ./...`, `go build ./...`, and golangci-lint v2.10.1 pass with Go 1.26.10; lint reports 0 issues. Logs: `/private/tmp/issue244-vet.log`, `/private/tmp/issue244-build.log`, `/private/tmp/issue244-lint-go126.log` (SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`).
- Source-verified `govulncheck -show verbose ./...`: PASS, EXIT 0, 18 root packages and 15 modules; no called or imported vulnerabilities. It reports one required-module-only Windows advisory `GO-2026-5024` in `golang.org/x/sys@v0.36.0`, fixed in v0.44.0. Transcript `/private/tmp/issue244-security-root-sourceverified.log`, SHA256 `8c519d1e10932929cf073c6f76f69257a52cfb78070bd3f683858f30dfc98c13`. Current code was unchanged after this scan; current `candle_read.go` and `candle_read_test.go` hashes are recorded in `/private/tmp/issue244-ready.json`. `go.mod` and `go.sum` match base.

All successful PostgreSQL stages used fresh owned containers, the pinned `postgres:17` digest, 1 GiB tmpfs, loopback-only published ports, synthetic credentials, direct SQL version queries, sanitized actual Go DSNs, and cleanup absence verification. Two earlier 512 MiB full-suite attempts failed: `/private/tmp/issue244-full-test.log` SHA256 `6e4867dbebdcd7c515dd1791893b168dcd2f32c190b03eefdc8e77ef57375aff` ended with PostgreSQL recovery-mode connection errors; `/private/tmp/issue244-full-test-retry.log` SHA256 `7c875d251766ebf6b58b790779e92766b7f97fadd715157a80552bfdd454279b` includes actual 512 MiB runtime inspect and server logs confirming PostgreSQL WAL `No space left on device`. Both are retained as failures. After a separate ROOT grant for a 1 GiB tmpfs, the full test, full race, configured Momentum race, and coverage stages passed. The initial 512 MiB targeted PASS transcript predates complete runtime-inspect evidence; the later 1 GiB targeted transcript above contains it.

The first lint/security attempts used system Go 1.23 and were rerun successfully with Go 1.26.10 for lint. An initial govulncheck network attempt failed DNS. An escalated attempt was auto-review rejected on outbound module-metadata exposure; this rejection was not bypassed. Root's later source-verified scan resolved the security gate. The original attempts and exact rejection are preserved in `/private/tmp/issue244-security-approval-blocker.json`.

## Next step

Root owns queue state and the next independent review grant. This handoff and the approved blobs remain unreviewed and unfrozen. Do not update the base, review, commit, push, create a PR, merge, or close the Issue until the separate ACTIVE_SINGLE_SLOT and final-GREEN instructions are read back.
