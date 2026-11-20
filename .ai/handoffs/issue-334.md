# Issue #334 — issue-334-c1 — READY

- Contract: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/334,
  `codex-cm015-dataset-artifact-repository:v1`; parent #70, dependency #324 integrated.
- Implementation chat: `01a1f633-b24c-7532-ab6f-519608c05599`.
- Sole owner: `/root/issue334_developer`, custom `developer-senior`, medium;
  activated by G10 for caller-owned transactions, idempotency and cross-pool concurrency.
- Worktree: `/private/tmp/bybit-issue-334`.
- Branch: `codex/334-dataset-artifact-repository`.
- Base: `0b37dad67c52368e8f770e69a3fd2d9f694194b5`;
  tree `05435c5f8ee6db3c753b1ae3a0249d7f8ef42799`.
- G10 DEV grant and exact three HELD paths independently read at queue revision 361.
  DEV ends at READY; separate ACTIVE grant is required before final base update,
  independent review or publication. #309 held the sole integration slot at grant.

## Exact paths and behavior

- `internal/momentum/storage/dataset_artifact_repository.go` — 113 lines:
  strict lowercase SHA-256, nonempty copied bytes, digest and finite UTC timestamp
  validation; caller-owned Tx reads/writes, immutable insert-or-read idempotence,
  distinct argument/absent/corrupt/unavailable errors without SQL/payload leakage.
  Context cancellation never returns a successful object. Commit/rollback/isolation
  and whole-operation retry remain caller responsibilities.
- `internal/momentum/storage/dataset_artifact_repository_test.go` — 297 lines:
  projection/argument/copy checks, rollback and pre-commit invisibility, exact
  committed bytes/timestamp, safe SQL failures, invalid stored metadata/digest,
  two-pool lock-observed commit/rollback/cancel concurrency and stale RR rejection.
- `.ai/handoffs/issue-334.md` — this checkpoint.

Existing tracked files, schema/migrations, source interfaces and tests are unchanged.
The validated G10 starting files were reused; only a test if-chain was changed to
an equivalent tagged switch to satisfy staticcheck QF1003. No production dependency,
format interpretation, freeze/replay claim, broker call or runtime operation added.

## Verification

Pinned `/private/tmp/issue271-toolchain/go/bin/go`: Go 1.26.11 darwin/arm64;
Go 1.24 source compatibility preserved. All final checks passed:

- formatting, `git diff --check`, targeted real PostgreSQL race (8 top-level groups,
  0 SKIP/FAIL), full tests/race/vet/build;
- ordinary coverage: 3039/4309 statements = 70.52680436296124%; PostgreSQL opt-in
  was absent for this ordinary measurement, matching the repository coverage gate;
- golangci-lint v2.10.1: 0 issues;
- govulncheck v1.1.4: 0 reachable vulnerabilities (one module-only advisory);
- full mandatory Momentum PostgreSQL race suites: 197 top-level PASS, 0 SKIP/FAIL;
- direct pinned driver smoke: server_version_num=170011;
- exact temporary `issue334-pg` PostgreSQL 17.11 container removed; empty name-filtered
  container listing independently recorded. Only synthetic local test data used.

Raw logs and command/exit records: `/private/tmp/issue334-logs/`.
READY manifest: `/private/tmp/issue334-ready.json` (external, includes file/log hashes).
Initial restricted cache/DNS failures and initial direct-smoke synthetic-password
mismatch are retained; final successful reruns supersede them. Pre-lint-fix logs
are retained separately. No required final gate omitted; ordinary tests intentionally
skip opt-in DB cases, while both actual PostgreSQL runs contain no skips.

## State and next action

READY; unchanged base, exactly three untracked approved files and empty index.
No review, commit, push, PR or queue mutation performed. Same owner stops here.
Next: G10 verifies READY evidence, grants a separate sole ACTIVE when appropriate,
then the same artifacts receive refreshed-base checks and independent exact-blob review.

## issue-334-c2 - ACTIVE refresh, ready for independent review

- G10 exact queue revision 385 grants sole ACTIVE #334 and renews the same three
  HELD paths. Same chat `01a1f633-b24c-7532-ab6f-519608c05599`, task/sole owner
  `/root/issue334_developer`, custom developer-senior medium, worktree and branch.
- Ordinary fetch and merge --ff-only advanced the branch to actual master
  `bbee2f36d7c1b9f3e10c7b85fda991d45d5e62a0`, tree
  `8cf475b616fe161b874700c48441bc3d3172904d`, without reset/rebase/force.
- Both original Go blobs remain byte-identical (113 implementation/297 test lines).
  The entire issue-334-c1 handoff above remains an exact byte prefix, verified
  against the original READY manifest and an external before-append snapshot.
- Fresh pinned Go1.26.11 format/diff, full ordinary tests/race/vet/build,
  golangci-lint2.10.1 all pass. Ordinary coverage3392/4761=71.24553665196387%.
- Real own disposable PostgreSQL17.11: targeted artifact race8 top-level PASS,
  full Momentum race239 top-level PASS, both0SKIP/0FAIL; direct driver smoke170011.
  Exact active container removed and empty container listing recorded.
- Scanner rebuilt offline from cached exact golang.org/x/vuln@v1.1.4 source;
  scanned only file:///private/tmp/g10-public-vulndb.Q6sE6qf4/db with GOPROXY=off,
  GOSUMDB=off, GOVCS=*:off, GOTOOLCHAIN=local, telemetry off and sandbox network
  restrictions. Exit0, zero reachable/package vulnerabilities, one module-only
  Windows advisory. Source archive/binary/database provenance hashes retained.
- Initial restricted local-bind test/coverage/race failures and cached scanner
  version lookup failure are retained. Successful final reruns supersede them;
  no scope/code change was needed and no required final gate is omitted.
- Current evidence: /private/tmp/issue334-active-logs/ and external
  /private/tmp/issue334-active-final.json. Original READY manifest/logs preserved.
- Empty index, exactly three approved untracked paths; all base tracked files
  unchanged. No review/commit/push/PR or queue write.
- STOP READY_FOR_INDEPENDENT_REVIEW. Next: independent exact final-blob review;
  publication remains blocked until a separate GREEN authorization.
