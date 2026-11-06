# Issue 202 — CM-010C migration history validation

- Contract: `codex-cm010-split:C:v1`; handoff: `codex-per-issue-handoff:202:v1`.
- Checkpoint: `issue-202-c1` (implementation complete; awaiting integration slot and independent review).
- Owner: `/root/issue202_developer`; chat: `chat01a1afa2-8cf9-77f3-91de-79eb8509b5fa`.
- Worktree: `/Users/alekseenkodenis/.codex/worktrees/cd5c/bybit_bot_liquidity`; branch: `codex/202-migration-history`; starting base: `bd066c3c0def04a8bc8a8ca53dbd538411e53e73`.
- Approved paths: `internal/momentum/storage/migration_history.go`, `internal/momentum/storage/migration_history_test.go`, `.ai/handoffs/issue-202.md`. No archive; no prior own canonical history existed.
- Scope: implement the pure C:v1 catalog and ordered history-prefix validator and its C1–C5 tests. Preserve read-only/Testnet boundaries.
- Checks passed: `gofmt`, focused validator tests (final assertion refinement rerun), `go test ./...`, `go test -race ./internal/momentum/storage -run TestValidateMigrationHistory -count=1`, `go test -cover ./...` (storage 97.3%), `go vet ./...`, `go build ./...`, golangci-lint (0 issues), govulncheck (0 reachable findings; existing module-only Windows GO-2026-5024 in x/sys v0.36.0 is not called by this code), `git diff --check`.
- Budget: 51 production lines / 82 physical test lines; within 90 / 150.
- Skipped/pending: real disposable PostgreSQL regression without skip and its separate race run are deferred until ACTIVE_SINGLE_SLOT; no DB test was claimed by the focused race. Final refreshed-base checks, independent GREEN, exact-head CI, publication, and PR readback are also pending the slot.
- Blocker/next step: wait for explicit ACTIVE_SINGLE_SLOT grant/readback, then same owner performs required disposable PostgreSQL checks, final gates and review handoff; no commit/push/publication is in flight or planned before that grant.

## issue-202-c2 — identity metadata correction

This checkpoint corrects the chat ID typo in the historical `issue-202-c1` text above. The exact implementation chat ID is `01a1afa2-8cf9-77f3-91de-79eb8509b5fa`; the prior `chat01a1...` value was erroneous. Current developer owner remains `/root/issue202_developer`.

Approved paths remain exactly: `internal/momentum/storage/migration_history.go`, `internal/momentum/storage/migration_history_test.go`, `.ai/handoffs/issue-202.md`. Development blobs, measured budgets (51 production / 82 test lines), and passed checks are unchanged. Pending gates remain the real disposable PostgreSQL no-skip regression, its separate race run, and ACTIVE_SINGLE_SLOT refreshed-base/review/CI/publication work. No commit, push, or publication is underway or armed before grant.

## issue-202-c3 — final integration checks

- Owner/chat: `/root/issue202_developer`; `01a1afa2-8cf9-77f3-91de-79eb8509b5fa`. Branch: `codex/202-migration-history`.
- ACTIVE_SINGLE_SLOT refresh: fetched `origin/master`; HEAD and refreshed `origin/master` both `bd066c3c0def04a8bc8a8ca53dbd538411e53e73`; `git merge-base --is-ancestor origin/master HEAD` passed, so base update was a proven no-op.
- Final approved paths remain the two migration-history Go files and this handoff. Budgets: 51 production lines / 82 test lines (limits 90 / 150).
- Final gates passed on these code/test blobs: gofmt; validator-targeted tests; `go test ./...`; full `go test -race ./...`; `go vet ./...`; `go build ./...`; golangci-lint 2.10.1 (0 issues); govulncheck 1.1.4 (0 reachable findings; existing module-only Windows GO-2026-5024 in x/sys v0.36.0 is not called by these sources); coverage profile total 82.0%; `git diff --check` and whitespace check for every approved path. No mandatory test was skipped.
- PostgreSQL evidence: explicit `TestDisposablePostgres` passed ordinarily and separately under `-race`, both with no skip on fresh disposable PostgreSQL 17.11. Docker image was `postgres:17`, inspect reported image/repo digest `sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`; server bound only to `127.0.0.1` with random host ports, synthetic credentials and `/var/lib/postgresql/data` tmpfs. Logs: `/private/tmp/issue202-gates/`. Container stop succeeded and `docker ps -a --filter name=issue202-pg17-final` returned no container.
- C1–C5 audit: valid empty/prefix/complete histories and repeated calls; malformed catalogs including empty, nonpositive, start-2, duplicate, gap, reversed, blank SQL and invalid unapplied tail; malformed history start/duplicate/reverse/gap/future/extra, checksum mismatch, 0/31/33-byte sums and exact SQL newline difference; preserved spec/history/checksum inputs on success/failure and stable input-free sentinel; all required Go/security/coverage gates passed.
- Next step: independent read-only review of these exact final blobs. No commit, push, PR, merge, closure or archive action has occurred or is armed.

## issue-202-c4 — review fix for empty SQL case

- Review fix: added a distinct catalog fixture `{version: 1, sql: ""}` to `TestValidateMigrationHistoryRejectsCatalog`, alongside the existing whitespace-only SQL cases. No production, database, or other test helper code changed. Checkpoint history c1–c3 above is retained.
- Final sizes: 51 production lines / 82 readable test lines (limits 90 / 150); exactly the existing three approved paths.
- Final-code checks passed: gofmt; targeted validator tests and targeted race; full `go test ./...`; full `go test -race ./...`; `go vet ./...`; `go build ./...`; golangci-lint 2.10.1 (0 issues); govulncheck 1.1.4 (0 reachable findings, known module-only Windows GO-2026-5024 not called); coverage 82.0%; `git diff --check` and approved-path whitespace checks. Logs for this fix are `/private/tmp/issue202-gates/c4-*.log`.
- In the c4 full test/race/coverage runs the opt-in `TestDisposablePostgres` was skipped because `MOMENTUM_TEST_POSTGRES_DSN` was unset. The explicit no-skip ordinary and separate race PostgreSQL 17.11 runs and cleanup readback in c3 remain valid: c4 changed only a catalog fixture, while production and database test helpers were unchanged. Those logs remain in `/private/tmp/issue202-gates/postgres-ordinary.log`, `postgres-race.log`, `postgres-version.log`, `postgres-container-inspect.log`, and `postgres-cleanup.log`.
- One initial coverage attempt was blocked by the sandbox localhost bind restriction in existing `TestOpenPostgresFailures`; rerunning with local networking enabled passed at 82.0%. The failure note and successful logs are in `/private/tmp/issue202-gates/`.
- Next step: independent read-only re-review of final exact blobs. No commit, push, PR, merge, closure or archive action has occurred or is armed.
