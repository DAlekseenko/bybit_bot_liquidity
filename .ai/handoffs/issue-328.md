# Issue #328 — CM-030B immutable TradePlan revision repository

- Contract: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/328, `codex-cm030-plan-revision-repository:v1`; checkpoint `issue-328-c1`, DEV → READY, 2026-11-20 (Europe/Moscow).
- Task/chat `01a1f5ee-a05c-7d51-ae37-c8dd6db5db19`; coordinator G10 `01a1ee9e-f0d5-71c2-ba96-ceadad8f8f5f`; canonical queue #55 comment `6668604794`, verified revision 352. #311 retains the sole ACTIVE slot.
- Sole developer `/root/issue328_developer`; explicit `agent_type=developer-senior` launch selects medium, activated for PostgreSQL concurrency/idempotency and immutable semantic integrity.
- Isolated clone `/private/tmp/bybit-issue-328`; branch `codex/328-plan-revision-repository`; exact base `66c43e0da9fee3f772382bc278d2e40a4d48cce1`. Read-only fallback source `/private/tmp/g10-master-git.yRD9rk` used; primary dirty checkout untouched.
- Exact reserved paths: `internal/momentum/storage/plan_revision.go`, `internal/momentum/storage/plan_revision_test.go`, `.ai/handoffs/issue-328.md`. No existing tracked file, schema, catalog, old test or dependency changed; index empty.

## Result and acceptance

- Store owns READ COMMITTED transaction, source run/canonical config checks, predecessor validation, insert-if-absent and separate exact readback; returns only after commit. Read owns REPEATABLE READ READ ONLY snapshot and checks key/hash/payload/source identity without claiming a whole-chain audit.
- Exact uint64 decimal NUMERIC, canonical bytes, SHA256, UTC timestamps and opaque config/formula identity are preserved. Safe sentinel reasons and cancellation/deadline checks return zero output on failure; SQL/commit error never proves absence of a commit.
- All prototype acceptance cases retained. Additional direct proofs cover missing predecessor, exact persisted UTC CreatedAt, Store cancellation, Store/Read deadline, schema-valid malformed original config and SQL-trigger abort/rollback. Repeated assertions and key tables factored within the physical budget: 221 production / 300 test lines.

## Fresh verification on final Go files

- Go 1.26.11: targeted pure decode/config tests, `go test -count=1 ./...`, `go test -race -count=1 ./...`, ordinary coverage with `MOMENTUM_TEST_POSTGRES_DSN` unset, `go vet ./...`, `go build ./...`, gofmt and diff/whitespace checks PASS. Ordinary coverage 2982/4253 = 70.11521279097109% (threshold 70%).
- golangci-lint v2.10.1: 0 issues. govulncheck v1.1.4: 0 reachable/imported vulnerabilities, 1 module-only advisory; no dependency changes in this Issue.
- Actual disposable PostgreSQL 17.11 (Debian 17.11-1.pgdg13+2), image `sha256:ff87138949ad9a46f32ff23bd6852b52ce024a13dffe9b4087be98292cdd3b4f`, loopback `127.0.0.1:60877`: `go test -race -count=1 -v ./internal/momentum/storage -run '^TestPlanRevision'` PASS, 9 top-level groups, 0 skips. Existing Migration/Schema/Bounds/Codec wrappers and five new Decode/Repository/Identity/Corrupt/Concurrent groups included.
- Two independent pools observe `pg_blocking_pids`, uncommitted row invisibility, exact duplicate result/count1; rational 1/3, nanos, chain 1→2→3, immutable xmin, conflicts and malformed persisted data checked. Container `d04e1a5c78d1b9a28f2888acf5f78456c421f9a498ae3ab413369eee91bc2798` removed, exact-ID absence read back.
- Raw logs and launch/run/hash/cleanup record: `/private/tmp/issue328-gates/`. Earlier lint iterations retained separately in `/private/tmp/issue328-interim-gates/` and `/private/tmp/issue328-prefinal-gates/`; final gates all PASS. No mandatory DEV verification skipped; independent review intentionally pending separate ACTIVE authorization.
- Blocker: none for READY. STOP; next action belongs to G10: separate ACTIVE grant, refresh base, independent exact-blob review before any publication. No reviewer, staging, commit, push, PR, GitHub/queue write, deploy or Testnet operation performed.

## Active checkpoint issue-328-c2 (2026-11-20 Europe/Moscow)

- G10 grant: canonical queue comment `6668604794` revision 369, sole ACTIVE #328; same owner/chat/worktree/branch and three exact HELD paths. Original c1 above preserved byte-for-byte; its READY/no-slot status is historical. No new copy or branch.
- Ordinary fast-forward refreshed base to `0f347861d6b3b2a9c7bc2f1ddf829fe6b5fca0b0`, exact tree `47209143c929efb1c4f03bd8f5dacf7d066a6ddd`; read-only safe ref fetched from `/private/tmp/g10-master-git.yRD9rk`. No reset/rebase/force or primary/mirror changes.
- Production and all 300 test lines/cases unchanged from c1: SHA256 `eada50ced9f69b3e4c3a7c416684248268da2737ddd76d631377674301081268` and `c22e2700a140b8179a6a6dc77103eff48f08a92232b5336deefa2ecc3573c8a9`. Only this checkpoint appended to the handoff.
- Fresh Go 1.26.11 targeted/full unit/full race/ordinary DSN-unset coverage/vet/build/lint/security PASS. Coverage 3164/4509 = 70.17076957196717%; lint 0 issues, security 0 reachable/imported and 1 module-only advisory. gofmt, exact-path/empty-index and whitespace/diff checks PASS.
- Fresh PostgreSQL 17.11 loopback race selected `^Test(PlanRevision|RebalanceRunRepository|DatasetArtifact)`: all 19 top-level groups PASS, 0 skips, covering the 9 plan-revision groups plus integrated run/artifact schemas. Unique container `issue328-active-4daa7cae77`, ID `0796a1dc995583871733e947f1fd2fe921af82b3af21dcb31c419a6b8fdd0970`, loopback `127.0.0.1:61137`; exact final Go hashes bound before/after run. Exact container removed and absent by ID readback.
- Fresh evidence `/private/tmp/issue328-active-gates/`, including launch/version/race/hash/cleanup record. Prior c1 evidence preserved. No mandatory implementation check skipped.
- ACTIVE_READY: no implementation blocker. Parent launches fresh independent reviewer of these exact three blobs. Staging/commit/push/PR wait for exact independent GREEN; published head/PR/CI remain unknown. Same owner publication, if authorized after GREEN, uses one ordinary commit, nonforce verified SSH push and one normal PR to master with `Refs #328`; G10 owns all subsequent checks and integration. No queue/GitHub write, deploy, Testnet or broker action performed.
