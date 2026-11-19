# Issue #304 — funding import progress schema

- Contract: `codex-cm013-funding-progress-schema:v1`, GitHub #304.
- Phase: `DEV complete`; ready for independent review. Staging and publication have not started.
- Owner: `/root/issue304_developer`; task chat `01a1f598-181b-7b80-9638-f53fb150c9e8`.
- Worktree: `/private/tmp/bybit-issue-304`; branch `codex/304-funding-progress-schema`; base `776f4bc1eb61dbca9a542de0cc8f6c4d95d396e9` (fast-forwarded from the prior base after #303; six approved paths had no overlap).
- Exact six paths: `internal/momentum/storage/funding_import_schema.go`, `internal/momentum/storage/migrations.go`, `internal/momentum/storage/migrations_test.go`, `internal/momentum/storage/market_schema_test.go`, `internal/momentum/storage/market_candles_schema_test.go`, `.ai/handoffs/issue-304.md`.
- Migration v9 appends the funding page-progress table and retains v1–v8 SQL and catalog entries; their exact SQL SHA256 values were compared with refreshed base and logged. PostgreSQL 17.11 tests cover upgrade/repeat, retained v1–v8 checksums and applied_at, config/run/plan/funding rows, collision rollback, all five actions, equivalent UTC offsets, namespaces, equal positive bounds, epoch/source boundary, max timestamp/revision, and invalid constraints including cursor ordering and oldest-after-server.
- Changed Go production: 51 lines. Physically touched Go test lines after gofmt: 187, below 300.
- PostgreSQL 17.11 targeted `-race` migration/state suite: PASS. Full storage `-race -v`: 89 top-level tests PASS, 0 SKIP. Disposable container used tmpfs and was removed; `docker inspect` confirmed it absent.
- With `MOMENTUM_TEST_POSTGRES_DSN` unset: `go test -count=1 ./...`, `go test -count=1 -race ./...`: PASS; ordinary statement coverage `2883/4087=70.540738928309%`. `go vet ./...`, `go build ./...`, gofmt check, `git diff --check`: PASS. golangci-lint v2.10.1: 0 issues. govulncheck v1.1.4: 0 reachable vulnerabilities; one module-only finding is not called by the code.
- Full logs and coverage profile: `/private/tmp/issue304-gates-776f4bc/`; prior READY evidence is preserved at `/private/tmp/issue304-ready-backup/`. Final manifest: `/private/tmp/issue304-active-ready.json`.
- Next: independent reviewer checks these exact final blobs. After GREEN, the same owner may publish only those reviewed blobs. No PR/head has been created; no commit, push, migration against runtime data, or runtime wiring was performed.
