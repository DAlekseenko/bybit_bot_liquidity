# Issue #297 READY — pre-review checkpoint

<!-- codex-cm015-funding-content:v1 -->

- Task/chat: `01a1eea8-f8f9-76a3-acff-84c759f2fee4`; owner `/root/issue297_developer`, native `developer-middle`.
- Worktree `/private/tmp/bybit-issue-297`; branch `codex/297-funding-content`.
- Queue: canonical Issue #55 comment revision 300 assigns `#297 ACTIVE_SINGLE_SLOT`; owner and three exact-path reservations remain in force.
- Base no-op: `HEAD = origin/master = 35b7bc9081f652947229fab0f03ef378c08708fd`, matching the fresh GitHub master commit confirmation.
- Contract: [Issue #297](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/297), marker `codex-cm015-funding-content:v1`.
- Paths: `internal/momentum/data/funding_content.go`, `internal/momentum/data/funding_content_test.go`, `.ai/handoffs/issue-297.md` only.
- Implementation: pure canonical JSON encoder and SHA-256 with identity, endpoint, UTC-millisecond and decimal validation; sorted copy of events; duplicate rejection. Coverage and snapshot provenance remain outside encoded content.
- Acceptance: 411-byte golden and independent digest; ordering, equivalent time zone/decimal scale, byte independence, digest changes, zero/empty behavior, precision/bounds, and fail-closed invalid cases all pass.
- Checks on unchanged Go blobs: targeted race, full tests, full race, configured coverage 70.5% (70% minimum), package coverage 98.1%, vet, build, gofmt, lint (0 issues), and govulncheck. Logs: `/private/tmp/issue297-logs/`.
- Environment note: `MOMENTUM_TEST_POSTGRES_DSN` unset; PostgreSQL integration tests did not run. govulncheck reports module-only GO-2026-5024 in `golang.org/x/sys@v0.36.0` for Windows; no reachable vulnerable symbols or packages.
- Exact candidate SHA256/blob IDs, limits, base, and check evidence: `/private/tmp/issue297-prereview.json`.
- Index remains empty. Next step is independent review of the frozen candidate. No edits after review starts; any requested change needs review of the new exact blobs. No commit, push, PR, or queue update in this grant.
