# Issue #138 — HM-009

Contract: live Issue #138, `hermes-momentum:HM-009:v1`,
`codex-per-issue-handoff:138:v1`, `codex-hm009-principal-binding:v2`.
Checkpoint: `issue138-c1` — WAIT budget; draft, not GREEN or READY.

- Implementation chat: `01a1af17-8f67-7a80-813b-75fcdc653c09`.
- Developer owner: `/root/issue138_developer` (developer-middle); retained.
- Coordinator: root chat `01a1a83c-8caa-7e91-96c6-a7eee06d19e2`.
- Worktree: `/Users/alekseenkodenis/.codex/worktrees/a7e2/bybit_bot_liquidity`.
- Branch: `codex/138-controlapi-principal`.
- Base: `dfab37fb52f52e5cff19465950e5c599fbf904cf`.
- DEV grant: queue revision 20; no ACTIVE_SINGLE_SLOT grant.

Exact approved paths:

- `internal/controlapi/auth/principal.go` — untracked formatted draft, 262
  physical lines; frozen after production ceiling 250 exceeded by 12.
- `internal/controlapi/auth/principal_test.go` — not created.
- `docs/crypto-momentum/issues/HM-009.md` — not created.
- `.ai/handoffs/issue-138.md` — this interruption record, explicitly authorized.

Draft SHA-256:
`a7a7002b0e0dd8497f186d1a95cff99a775b428bea5db97d6cada9d1344afb8f`.
Byte-exact source saved at `/private/tmp/issue138-principal-draft.go`; `cmp`
confirmed equality. Patch: `/private/tmp/issue138-principal-draft.patch`, SHA-256
`d52da6b02772f04382122d8a4dc07dd5412467e9b9909421be654bc6083ce82c`.
External checkpoint: `/private/tmp/issue138-blocker.json`; approved binding:
`/private/tmp/issue138-approved-binding.md`. Temporary files are local evidence,
not committed artifacts or an archive of published history.

Checks: gofmt applied to draft. Targeted/full tests, race, vet, build and
govulncheck NOT RUN: implementation stopped before tests on factual budget
overflow. Tracked diff was empty; prior diff-check does not validate untracked
draft. No final review, GREEN, staging, commit, push or PR. CI unknown.

Blocker: 262 production lines exceed hard 250; test ceiling remains 150 and all
required cases remain mandatory. No minification, omitted cases or budget
expansion authorized. Production/tests frozen. Existing root designer
`/root/parallel_workstreams` owns targeted budget remediation; no second designer.
Next: await that decision and explicit continuation grant, reuse this chat,
owner, worktree, branch and draft. Final base update/review/publication additionally
require separate ACTIVE_SINGLE_SLOT grant.

Quiescence: developer STOP confirmed; no running or scheduled publication.
Foreign untracked `.serena/` remains unread and untouched. Global handoff and
legacy archives unchanged. Service pause must remain denied; human runtime
issuer/capability unavailable. No runtime/trading/deployment authority.

## Checkpoint issue138-c2 — residual Authorize STOP

Contract: live Issue #138, `codex-hm009-authorize-residual:v3`; prerequisite #226 merged as PR #228 at `9133e29390df701fc5a852b07caa3879b654f88e`, verified by coordinator. Same owner/chat/worktree/branch; no final base merge, final review, commit, push or PR. Current branch HEAD/base remain `dfab37fb52f52e5cff19465950e5c599fbf904cf` pending separate ACTIVE_SINGLE_SLOT grant.

- Developer owner: `/root/issue138_developer`; implementation chat `01a1af17-8f67-7a80-813b-75fcdc653c09`.
- Current coordinator at grant: `01a1af5e-f61f-70f0-9837-46262ce33f95`.
- Exact paths remain `internal/controlapi/auth/principal.go`, `internal/controlapi/auth/principal_test.go`, `docs/crypto-momentum/issues/HM-009.md`, `.ai/handoffs/issue-138.md`; docs not yet created.
- `principal.go` now contains only residual `Authorize`, `ErrForbidden`, static operation policy and private required-selector helpers consuming merged #226 API; 93 physical lines, SHA256 `5e638a540583f5163a0dc99bcc8bb88a9d7065a3d1e324e08292e8a6bebcdc3c`.
- `principal_test.go` preserves the full required residual matrix at 162 physical lines, SHA256 `b64b29e1b6b5b8558a7c21a67a37147b6d76f74a048e2c456dec83ffd5037e10`; exceeds hard 150 by 12. Do not shrink or omit cases without targeted budget decision.
- Full byte copies and evidence manifest: `/private/tmp/issue138-residual-budget-source-before/`; manifest records hashes, test matrix and blocker.
- `gofmt` applied. Targeted `go test ./internal/controlapi/auth` blocked before compilation: `go.mod requires go >= 1.24.13 (running go 1.23.1; GOTOOLCHAIN=local)`. Full tests/race/vet/build/govulncheck/diff-check not run; no PASS claims.
- Test coverage draft includes enabled read mappings and required selectors; risk's two grants; role noninheritance; exact full tuple and nil mismatch; authorized-scope copy isolation; service pause denial; test-only simulated human pause success and missing-operate denial; disabled 11–17 and run-selector precedence; unknown aliases; zero principal and error precedence.
- Known draft test defect, not verified: the disabled-operations loop clears `RunID` for operations 15–17 but still expects `ErrForbidden`; v3 requires missing `RunID` to return `ErrInvalidArgument` before disabled denial. Correct it after budget decision while preserving both outcomes.

STOP on measured test-budget overflow. Next step: wait for the existing targeted designer/coordinator budget decision and any renewed continuation grant. No READY, GREEN or publication. Service pause remains denied; human runtime issuer and pause execution remain unavailable. Foreign `.serena/` remains untouched. This checkpoint is appended after c1; c1 bytes are preserved unchanged.

## Checkpoint issue138-c3 — residual matrix ready for integration

Contract remains live Issue #138 `codex-hm009-authorize-residual:v3`; designer decision `/private/tmp/issue138-residual-budget-design.md` approved the complete table-driven matrix within existing budget. c1 and c2 bytes remain an unchanged prefix. Same owner/chat/worktree/branch; no final base merge or ACTIVE_SINGLE_SLOT grant. Branch HEAD remains `dfab37fb52f52e5cff19465950e5c599fbf904cf`; merged #226 source is `9133e29390df701fc5a852b07caa3879b654f88e` (PR #228), injected only through a temporary Go overlay for preliminary verification.

- Exact paths: `internal/controlapi/auth/principal.go` (97 lines; SHA256 `92bb6b55b125a470785f0bbfa1b52ad36ac243452a8f97c7b7ee0ef68969d8db`), `internal/controlapi/auth/principal_test.go` (149 lines; SHA256 `d7a30dbdfb693963cd77f807a0495fce72ebcee119c5477665d31572b342f96b`), `docs/crypto-momentum/issues/HM-009.md` (30 lines; SHA256 `270d40269472fe5061e0c4ccdba3599073db8fa1d741a97dcc58f493b4e99978`), and this handoff. Production/test budgets are 97/250 and 149/150.
- Tests cover exact role/type/principal Cartesian mappings, all enabled read selector requirements, service/human pause behavior, disabled 11–17 and missing-run precedence, unknown aliases, full four-field tuple mismatches in both directions, risk grant combinations/wrong roles, nil nullable values, authorized result copy isolation, and error precedence/zero Scope. Child #226 tests are included only in temporary overlay and remain unchanged.
- Toolchain: pinned Go 1.26.10 at `/private/tmp/issue63-go126/bin/go`. `gofmt` applied. Targeted auth tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` passed with overlay mapping the exact merged #226 identity source/tests into the unchanged branch. New-file whitespace checks passed for all four paths; tracked `git diff --check` had no output. This is preliminary overlay evidence; the Issue branch itself still lacks #226 until a later authorized base update, so final-base checks remain pending.
- `govulncheck` is not installed in the searched PATH or pinned toolchain bin; exact blocker recorded. No bot was run. No final review/GREEN, commit, push, PR, CI or publication claim.
- External readiness evidence: `/private/tmp/issue138-ready.json`, containing owner/chat/worktree/branch/base, all four hashes, budgets, checks, pending dependency-base note and blockers.

Next: coordinator obtains separate ACTIVE_SINGLE_SLOT grant before any final base update, independent review or publication. Root owns post-publication gates and merge/closure/archive. Human runtime issuer and pause execution remain unavailable; Testnet/read-only behavior is unchanged. `.serena/` remains foreign and untouched.

## Checkpoint issue138-c4 — final-base review candidate

ACTIVE_SINGLE_SLOT #138 received from current coordinator at queue revision 46. Refreshed `origin/master` fetched at `9133e29390df701fc5a852b07caa3879b654f88e`; branch `codex/138-controlapi-principal` advanced from `dfab37fb52f52e5cff19465950e5c599fbf904cf` to that exact commit by ordinary fast-forward merge. No conflicts. Upstream merge brought the approved #226 identity files plus #225 files; those remain base content and are not part of this Issue's four-file set. c1/c2/c3 history remains byte-for-byte prefix; c4 appended.

- Exact Issue paths and budgets: `internal/controlapi/auth/principal.go` 97/250 lines, SHA256 `92bb6b55b125a470785f0bbfa1b52ad36ac243452a8f97c7b7ee0ef68969d8db`; `internal/controlapi/auth/principal_test.go` 150/150 lines, SHA256 `b6aad3cfb598af36d96a41a5a3044830ca5bb811aefb229230bb86a6ea063a64`; `docs/crypto-momentum/issues/HM-009.md` 30 lines, SHA256 `270d40269472fe5061e0c4ccdba3599073db8fa1d741a97dcc58f493b4e99978`; this per-Issue handoff.
- Final-base checks on Go 1.26.10: gofmt clean; targeted auth tests PASS; `go test ./...` PASS; `go test -race ./...` PASS; `go vet ./...` PASS; `go build ./...` PASS; golangci-lint 2.10.1 PASS (0 issues); total coverage 81.8% (auth package 98.9%); new-file whitespace checks and tracked `git diff --check` clean.
- govulncheck built from cached `golang.org/x/vuln@v1.1.4` source with Go 1.26.10 and run against official DB: zero vulnerabilities in imported packages/symbols. One module-only finding is `GO-2026-5024`, `golang.org/x/sys@v0.36.0` Windows-only integer overflow, fixed in v0.44.0; scanner found no calls in this code. This is disclosed as module exposure, not an all-clear claim; no dependency change was authorized.
- External evidence `/private/tmp/issue138-ready.json` updated with refreshed base/head, exact four hashes, budgets, checks, vulnerability classification and no-publication state.

This is a review candidate only. No commit, push, PR, merge, closure or archive has occurred. Freeze all four blobs after independent GREEN; any required edit needs a fresh independent review. Same owner publishes only after GREEN; root owns exact-head CI and all post-publication/merge gates. Testnet/read-only remains unchanged; no live broker transport, human issuer, or pause execution was added. Foreign `.serena/` remains untouched.
