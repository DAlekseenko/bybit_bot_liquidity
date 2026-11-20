# Issue #309 — manifest envelope

- Contract: `codex-cm015-manifest-envelope:v1`, Issue https://github.com/DAlekseenko/bybit_bot_liquidity/issues/309.
- Phase: DEV complete; waiting for coordinator ACTIVE grant before final base refresh, independent review, or publication.
- Task/chat: `01a1f29c-1c05-74d3-a513-710f5c83bcac` (host `local`); owner `/root/issue309_developer` (`developer-middle`).
- Branch: `codex/309-dataset-manifest-envelope`; isolated copy `/private/tmp/bybit-issue-309`; base `55c157919fe889448a0e61478d2e1191061e96a3`.
- Approved paths: `internal/momentum/data/manifest.go`, `internal/momentum/data/manifest_test.go`, `.ai/handoffs/issue-309.md`.
- Result: pure canonical v1 JSON envelope and lowercase SHA-256. It checks namespace, opaque UTF-8 registry key, exact millisecond windows, supported endpoints/kinds/intervals, links, sorting, and duplicate keys. Integration test uses actual `EncodeFundingContent` (`momentum/funding-content/v1`) output hash, explicit unknown coverage/margin and unapproved assumptions evidence; changing evidence changes envelope hash. This does not establish artifact presence/content, freeze membership, or replay admission.
- Checks: Go 1.26.11; gofmt/diff-check, targeted race, full ordinary/race tests, vet, build, govulncheck v1.1.4, golangci-lint v2.10.1 PASS. Ordinary coverage with `MOMENTUM_TEST_POSTGRES_DSN` unset: 71.2% (CI threshold 70%). Loopback permission was required for an existing test that binds an ephemeral `127.0.0.1` port; no PostgreSQL DSN/server used. Govulncheck reports no reachable package findings; module-only `GO-2026-5024` in `golang.org/x/sys@v0.36.0` affects Windows and is fixed in v0.44.0.
- Scope: 192 production Go lines, 253 physical test lines after gofmt. No dependencies, schema, network code, or trade execution changed.
- Current blocker/next action: await coordinator ACTIVE grant; then refresh default base and relevant diff, resolve only approved-scope changes, rerun gates, prepare exact-blob independent review. No integration slot/PR yet.

## issue-309-c2 — refreshed base and final validation

- Refreshed base: `0b37dad67c52368e8f770e69a3fd2d9f694194b5` (`05435c5f8ee6db3c753b1ae3a0249d7f8ef42799`); branch fast-forwarded from the fetched mirror. `funding_content.go`, its golden test, module files, and CI workflow are unchanged from c1; current AGENTS/CI reviewed.
- The c1 source and test blobs remain byte-identical. This append preserves the prior 11-line checkpoint.
- Final validation on refreshed base `0b37dad67c52368e8f770e69a3fd2d9f694194b5`: gofmt/diff-check, targeted race, full tests, full race, coverage, govulncheck, golangci-lint, vet, and build all PASS. Coverage 71.0% with PostgreSQL DSN unset; security scan has zero reachable findings and module-only Windows `GO-2026-5024` in `golang.org/x/sys@v0.36.0`. Existing PostgreSQL storage unit test uses a temporary loopback listener; no DSN or PostgreSQL server was used. Logs are under `/private/tmp/issue309-final-*`.
- Next: independent reviewer checks exact final blobs listed in `/private/tmp/issue309-final.json`. No commit or publication before GREEN.
