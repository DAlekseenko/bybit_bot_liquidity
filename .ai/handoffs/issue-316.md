# Issue 316 handoff

<!-- codex-cm015-trade-rr-reader:v1 -->

- Checkpoint: `issue-316-ready-1`.
- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/316.
- Task/chat: `01a1f56f-1662-73b1-95f2-7f1ab0621fb7`; owner: `/root/issue316_developer` (`developer-middle`).
- Worktree: `/private/tmp/bybit-issue-316`; branch `codex/316-trade-rr-reader`; exact base `85685bbdd518fd4bb58baa74f2368ad4d76e96e9`.
- Approved paths: `internal/momentum/storage/candle_read.go`, `internal/momentum/storage/read_snapshot.go`, `internal/momentum/storage/candle_read_tx_test.go`, `.ai/handoffs/issue-316.md`.
- Implementation: added `ReadCandleRangeTx` for caller-owned RR READ ONLY snapshots; the legacy `*sql.DB` wrapper retains ownership and request ordering. Both methods share the SQL projection. The prior `candle_read_test.go` is byte-identical to base (SHA256 `44583702025d6ad29a5b8c2c0673d8c9618d55eb7448ea2d3f8da90cfe0482c0`).
- Budget: 51 added production Go lines; 185 physical lines in the new Go test file; 4 approved paths.
- Verification: Go 1.26.11; `gofmt`, `git diff --check`, `go test ./...`, ordinary `go test -count=1 -coverprofile=... ./...` with PostgreSQL DSN unset (2835/4045 statements, 70.0865265760%), `go test -race -count=1 ./...` DSN unset, `go vet ./...`, `go build ./...`, golangci-lint v2.10.1 (0 issues), govulncheck v1.1.4 (no reachable vulnerabilities; one required module vulnerability is reported unreachable). Issue tests: all eight `TestReadCandle*` groups passed under race on PostgreSQL 17.11 (`server_version_num=170011`), no skips. Temporary container removal verified.
- Logs: `/private/tmp/issue316-test.log`, `issue316-coverage.log`, `issue316-coverage.out`, `issue316-race.log`, `issue316-postgres-race.log`, `issue316-postgres-version.log`, `issue316-cleanup.log`, `issue316-vet.log`, `issue316-build.log`, `issue316-lint.log`, `issue316-security.log` under `/private/tmp`.
- Limitations: full source capture/materialization, funding/mark readers, dataset freeze and exchange access remain outside this Issue. Security scan requires external module database access; the successful scan reported the one module vulnerability as unreachable.
- State: READY for orchestrator readback; wait for separate ACTIVE before review, base update or publication. No staging, commit, push, PR or review performed.


## Checkpoint issue-316-c2 — ACTIVE base update

- ACTIVE_SINGLE_SLOT #316 was confirmed in canonical queue rev342 by coordinator G10. The same owner, branch and four whole-path reservations remain active.
- Fast-forwarded from the prior exact base `85685bbdd518fd4bb58baa74f2368ad4d76e96e9` to exact master `605be511b55700e6bb749f0db29a4ab366eca573`; ancestry was verified before the fast-forward. The four approved paths retained their READY bytes. All 19 Go test files in `internal/momentum/storage` match the new base byte-for-byte before re-running tests.
- Fresh full verification: Go 1.26.11; `go test ./...`; `go test -count=1 -coverprofile=... ./...` with PostgreSQL DSN unset (2897/4107 statements, 70.5381056732%); `go test -race -count=1 ./...` with DSN unset; `go vet ./...`; `go build ./...`; `gofmt`; `git diff --check`; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities; one required-module vulnerability remains unreachable).
- Targeted PostgreSQL proof: all eight top-level `TestReadCandle*` groups passed under race with no skips on pinned PostgreSQL 17.11 (`server_version_num=170011`). Temporary database container removal verified.
- Evidence logs in `/private/tmp`: `issue316-active-test.log`, `issue316-active-coverage.log`, `issue316-active-coverage.out`, `issue316-active-race.log`, `issue316-active-postgres-race.log`, `issue316-active-postgres-version.log`, `issue316-active-cleanup.log`, `issue316-active-vet.log`, `issue316-active-build.log`, `issue316-active-lint.log`, `issue316-active-security.log`. READY-v1 manifest and its original logs/handoff remain preserved separately under `/private/tmp/issue316-ready.archive.json` and `/private/tmp/issue316-ready-archive/`.
- State: ACTIVE gates complete; READY for fresh independent review. Developer must stop until reviewer GREEN. No staging, commit, push or PR has been done.


## Checkpoint issue-316-c3 — independent review correction

- Independent review of c2 returned RED (P2): the approved new API needed direct coverage for mismatched registry source/category, non-UTC boundaries/session timezone, and pre-expired plus SQL-blocked deadlines. Verdict preserved at `/private/tmp/issue316-review.json` (SHA256 `6332ebedf4d96e3c44120fa1f117982eb2158045e4f1cde325ef0b5dee598dc7`).
- Correction is limited to `internal/momentum/storage/candle_read_tx_test.go`: added real PostgreSQL coverage for wrong registry source/category (temporary fixture constraints removed only in its isolated test database), unchanged non-UTC input bounds and UTC results under `Pacific/Honolulu`, pre-expired deadline with transaction still queryable, and access-exclusive-lock blocked SQL deadline returning nil plus `context.DeadlineExceeded`. The blocked query's caller-owned Tx rollback is exercised; the PostgreSQL driver reports `driver.ErrBadConn` after canceling that blocked query. No production source or prior test file changed. Test file is 248 physical lines (<=300).
- Fresh gates on exact base `605be511b55700e6bb749f0db29a4ab366eca573`: `go test ./...`; DSN-unset full ordinary coverage 2897/4107 (70.5381056732%); full race; vet; build; gofmt; diff check; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities, one required-module finding unreachable). All ten targeted `TestReadCandle*` groups pass under race on PostgreSQL 17.11, no skips, with disposable container deletion verified.
- Logs in `/private/tmp`: `issue316-corrected-test.log`, `issue316-corrected-coverage.log`, `issue316-corrected-coverage.out`, `issue316-corrected-race.log`, `issue316-corrected-postgres-race.log`, `issue316-corrected-postgres-version.log`, `issue316-corrected-cleanup.log`, `issue316-corrected-vet.log`, `issue316-corrected-build.log`, `issue316-corrected-lint.log`, `issue316-corrected-security.log`. Original c1/c2 handoffs, manifests and logs remain separately preserved under `/private/tmp/issue316-ready-archive/` and `/private/tmp/issue316-active-archive/`.
- State: corrected blobs READY for a fresh exact-blob independent review. No staging, commit, push or PR performed.
