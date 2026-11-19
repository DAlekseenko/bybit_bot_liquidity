<!-- codex-cm015-dataset-artifact-schema:v1 -->

# Checkpoint issue-324-c2 — READY for fresh review

- Issue #324 CM-015B; task/chat `01a1f5c7-480f-7da3-88b1-7b88f71f306b`; owner `/root/issue324_developer`, `developer-middle`.
- Worktree `/private/tmp/bybit-issue-324`, branch `codex/324-dataset-artifact-schema`, base/HEAD `f76340ca44a97a176b54f3fee24a5f6df1b1e627`. Index empty; no implementation commit.
- Exactly six paths are reserved: `internal/momentum/storage/dataset_artifact_schema.go`, `internal/momentum/storage/migrations.go`, `internal/momentum/storage/migrations_test.go`, `internal/momentum/storage/market_schema_test.go`, `internal/momentum/storage/market_candles_schema_test.go`, `.ai/handoffs/issue-324.md`. Budget: 19 production lines, 196 physical test lines. Market test files only change migration-count expectations.
- Only explicit migration v10 was added. SQL sources/catalog for v1–9 are byte-identical across the refresh from `605be511b55700e6bb749f0db29a4ab366eca573`; source blob comparison is in `/private/tmp/issue324-gates-postrefresh/prefix-source-compare.log`.
- Reviewer returned RED/P2: the funding content regression did not reread source rows after UPDATE. Fixed in `migrations_test.go`: re-read range, call real `EncodeFundingContent`, require the post-UPDATE bytes and digest to differ from initial and post-INSERT content, then retain pinned-artifact assertions. No production files changed for this correction. New independent review is pending.
- Fresh Go 1.26.11 gates pass: full test/race; coverage 3015/4261 = 70.758038019244%; vet, build, lint (0 issues), gofmt, diff-check and govulncheck. Fresh PostgreSQL 17.11 storage race: 104 PASS, zero skips. Temporary 4 GiB-capped container removed; exact-name readback empty. Raw evidence is under `/private/tmp/issue324-p2fix-gates/`; hashes are in the READY manifest.
- Security residual: GO-2026-5024 in `golang.org/x/sys@v0.36.0`, Windows-only, fixed in v0.44.0; govulncheck reports zero reachable vulnerabilities.
- Previous READY and RED candidate evidence remain archived at `/private/tmp/issue324-pre-refresh-20261119T231033Z` and `/private/tmp/issue324-red-review-20261119T232543Z`.
- Next: root launches a fresh independent review against the exact six frozen blobs. Wait for GREEN before staging or publication; no PR or repository mutation has occurred.
