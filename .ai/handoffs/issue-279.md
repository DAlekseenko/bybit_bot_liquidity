# Issue #279 — checkpoint issue-279-c1

<!-- codex-cm013-funding-write:v1 -->

- Contract: [CM-013B](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/279), marker `codex-cm013-funding-write:v1`. Developer owner `/root/issue279_developer`, native role `developer-middle`.
- Identity: dedicated implementation chat `01a1e661-d517-76d0-831b-1544d445e82a`; G8 coordinator/grant chat `01a1e65b-3e93-7c41-aa64-08b8c47b2148`. The original c1 note mislabeled the coordinator chat as the implementation thread; c3 below corrects the record.
- Initial base/branch/worktree: `2aec12adf23fdfe088e524c89651b07e0b5ead2d`, `codex/279-funding-write`, `/private/tmp/bybit-issue-279`.
- Implementation: typed `FundingStream` and transactional `AppendFundingPage` validate key/provenance/page bounds, exact signed rates, order and uniqueness; require read-write READ COMMITTED; lock existing provenance `FOR SHARE`; perform bound immutable inserts, accept equal numeric repeats, and reject divergent rates. Any error rolls back the caller transaction; success leaves Commit to the caller. No schema, transport, importer, cursor/coverage, broker, risk, or live-execution changes.
- Initial exact paths: `internal/momentum/storage/funding_write.go`, `internal/momentum/storage/funding_write_test.go`, `.ai/handoffs/issue-279.md`; 148 production and 287 test lines. Original DEV-ready manifest `/private/tmp/issue279-ready.json` remains unchanged, SHA256 `f7f9ce0aa1dc3f04aa27dccafcf6dbd626fade37d5bc60bfa0b613935c6ffc4d`.
- Initial checks: targeted AppendFundingPage race tests passed without skips on pinned PostgreSQL 17.11/1 GiB tmpfs; full Go tests, race tests, vet, build, coverage (73.9%), govulncheck, golangci-lint and formatting passed. The full serial Momentum PostgreSQL suite passed on 4 GiB tmpfs.

## Checkpoint issue-279-c2 — ACTIVE base refresh

- Same owner, task, native role and dedicated implementation chat recorded above; coordinator chat `01a1e65b-3e93-7c41-aa64-08b8c47b2148` separately supplied the grant. G8 `ACTIVE_SINGLE_SLOT` and canonical owner/slot were read back at queue revision 251.
- Fresh HTTPS fetch and GitHub API agreed on master `f5f1a7b5e49ce82fc4e2e72ece3553e462e93824`, exactly two commits above c1 base. Their seven changed paths are disjoint from all three Issue #279 paths. Branch fast-forwarded with `git merge --ff-only`; candidate paths remained intact. The c1 manifest SHA remains unchanged.
- Candidate checks on this base passed: targeted no-skip PostgreSQL 17.11 race suite; full serial Momentum PostgreSQL race suite on 4 GiB tmpfs; full Go tests/race/vet/build, coverage 71.1%, govulncheck, golangci-lint, gofmt and whitespace checks. Evidence is in `/private/tmp/issue279-active-evidence/`; both disposable servers were removed and cleanup verified.

## Checkpoint issue-279-c3 — HOLD correction and revalidation

- Review HOLD artifact: `/private/tmp/issue279-review-hold.json`, SHA256 `e2bf4f9ea2364a8af4fd554df9ad1d81d8e9e0794bacd06e7fa5f8082287cac0`. The HOLD identified the c1/c2 chat identity conflation and two missing acceptance assertions. This checkpoint supersedes the c2 candidate; both earlier manifests remain unchanged.
- Corrected ownership record: same owner `/root/issue279_developer`, native role `developer-middle`, implementation chat `01a1e661-d517-76d0-831b-1544d445e82a`, and distinct coordinator chat `01a1e65b-3e93-7c41-aa64-08b8c47b2148`.
- Added concurrent verification of exact final event timestamp/rate pairs and count after each interleaving. In the divergent case, the contender inserts a new timestamp before blocking on the conflicting event; the final assertion confirms no prefix row escaped rollback. Added a negative finite Decimal built by exact arithmetic: `WireDecimal` succeeds, `ParseRate` rejects it, and the writer rejects the page. Existing test cases remain; test file is 299 physical lines (limit 300). No production file changed.
- Final c3 checks: targeted PostgreSQL 17.11 race tests without skips; full serial Momentum PostgreSQL race suite on 4 GiB tmpfs; full Go/race/vet/build, coverage, lint/security, gofmt and whitespace checks. Exact commands, logs, base, hashes and blob IDs are in `/private/tmp/issue279-active-ready-r1.json`; temporary PostgreSQL containers were removed and cleanup verified.
- State: READY for fresh independent review of the exact r1 blobs. Index remains empty; no commit, push, PR or GitHub mutation. Preserve candidate files until review; any later edits require another exact-blob review.
