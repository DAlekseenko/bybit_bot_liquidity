# Handoff: Issue #226

<!-- codex-per-issue-handoff:v1 -->

## Checkpoint — issue-226-c1

- Contract: [Issue #226](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/226), marker `codex-hm009-identity:v1`; task/chat `01a1af2c-4e3b-71e2-bbef-820e82c2d402`; owner `/root/issue226_developer` (developer-middle).
- Worktree `/Users/alekseenkodenis/.codex/worktrees/99f5/bybit_bot_liquidity`; branch `codex/226-principal-identity`; starting base `dfab37fb52f52e5cff19465950e5c599fbf904cf`; final integration base `72c148ee84e45f001f8a553de65ce32c1b3d092a` merged from refreshed `origin/master` during ACTIVE_SINGLE_SLOT.
- Approved and reserved paths: `internal/controlapi/auth/principal_identity.go`, `internal/controlapi/auth/principal_identity_test.go`, `.ai/handoffs/issue-226.md`; no archive.
- Contract boundary: immutable validated values only; no `Authorize`, `ErrForbidden`, human proof issuer/verification, runtime human route, trading or execution. `HumanIdentity` has private fields; tests simulate its proof state without verifying proof.
- Implementation: service and human constructors validate fail-closed, closed enums, UTF-8 identifiers (1–128 Unicode characters), grant limit and stated precedence; shared `validateScope` checks identifiers before environment, and snapshots deep-copy grant slices and nullable selectors; principal JSON decoding always rejects.
- Verification on final base (Go 1.26.10): targeted/full `go test`, `go test -race`, `go vet`, `go build`, `gofmt` and `git diff --check` pass; total coverage 81.5% (auth 98.1%); golangci-lint v2.10.1 pass; final-base govulncheck v1.1.4 pass (0 called vulnerabilities; 1 required module vulnerability not called). Latest rerun evidence is `/private/tmp/issue226-finalfix-gates.log`; scan is `/private/tmp/issue226-finalfix-vuln.log`. Budgets: production 236/250 physical lines; readable tests 150/150; review <=6 files and 15 minutes.
- Review fixes: tests reject unknown/empty `ScopeType` with a zero principal; `equalScope` covers every field, equal independent allocations and nullable differences; direct `validateScope` tests reject empty/unknown/lowercase environments and preserve identifier-before-environment precedence. Environment validation now lives only in `validateScope`, after identifier checks. Next: root runs fresh independent review of the final hashes; after GREEN freeze all three blobs and publish through the active slot. Parent #138/#127 remain open; successful foundation does not imply runtime authorization or human-pause capability.
