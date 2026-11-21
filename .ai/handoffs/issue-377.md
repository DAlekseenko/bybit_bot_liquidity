# Issue #377 handoff

- Contract: `codex-cm022h-canonical-model-input:v1`; GitHub Issue body is the sole contract.
- Task/chat: `01a1f914-58e1-7fc1-a071-0b4373c5eb58`; developer owner: `/root/issue377_developer` (`developer-middle`, medium).
- Checkout: `/private/tmp/bybit-issue-377`; branch: `codex/377-offline-canonical-backtest-model-input`; base: `54581fad2e3c67c750ce2d455fbdbcdedc831efc` (tree `907a3da117a197d6d86be40422d77fde60bfcaf3`).
- Approved paths: `cmd/momentum/backtest_model.go`, `cmd/momentum/backtest_model_test.go`, `.ai/handoffs/issue-377.md`.
- Implementation: bounded strict JSON decode and capped file read rebuild the existing immutable `SimulationModel`; canonical bytes must match exactly. No command/runtime, broker, network, database, environment, order, schema, or financial-rule changes.
- Budget: 110 production lines and 135 test lines; 3 exact paths. Limits: 250 production / 300 test lines.
- Checks: targeted `go test -count=1 ./cmd/momentum`, full `go test -count=1 ./...`, full race `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...`, gofmt, whitespace/scope, golangci-lint 2.10.1, offline govulncheck 1.1.4, and ordinary coverage all pass. Coverage is 73.6% (CI threshold 70%). Logs: `/private/tmp/issue377-targeted.log`, `/private/tmp/issue377-full-unit.log`, `/private/tmp/issue377-race.log`, `/private/tmp/issue377-vet.log`, `/private/tmp/issue377-build.log`, `/private/tmp/issue377-gofmt.log`, `/private/tmp/issue377-diff-check.log`, `/private/tmp/issue377-scope.log`, `/private/tmp/issue377-lint.log`, `/private/tmp/issue377-govulncheck.log`, `/private/tmp/issue377-coverage.log`; profile `/private/tmp/issue377-coverage.out`. Toolchain Go 1.26.11. Offline vulnerability database: `file:///private/tmp/g10-public-vulndb.Q6sE6qf4/db`.
- Sandbox note: existing storage unit test opens a localhost listener. The required test/race/coverage runs passed with sandbox loopback approval; no external database was used.
- Status: local DEV complete; final independent review and publication have not started. No staging, commit, push, PR, or merge was performed.
- Next action: STOP and await coordinator's explicit integration-slot grant; then the same developer owner proceeds under the exact Issue paths and current base evidence.

## Checkpoint after base refresh (2026-11-22)

- Issue/contract: #377 `codex-cm022h-canonical-model-input:v1`; task/chat `01a1f914-58e1-7fc1-a071-0b4373c5eb58`; owner `/root/issue377_developer` (`developer-middle`, medium).
- Checkout `/private/tmp/bybit-issue-377`, branch `codex/377-offline-canonical-backtest-model-input`; fast-forwarded from the recorded base to verified master `768f0178ed009900be2cec3c851f9ad0316a061f` (tree `187f7fbb4ec5c6cddbc52bbc0ca6475f68d02e8f`). GitHub compare confirms `master` identical, ahead/behind 0, files []; local `git fetch` was unavailable due DNS.
- Exact paths remain `cmd/momentum/backtest_model.go`, `cmd/momentum/backtest_model_test.go`, `.ai/handoffs/issue-377.md`; implementation and tests unchanged. Final budget: 110 production / 135 test lines; hard limits 250 / 300.
- On refreshed base: targeted, full unit, full race, vet, build, gofmt, lint 2.10.1, offline govulncheck 1.1.4, diff/scope checks and ordinary coverage pass; coverage 73.7% (CI floor 70%). Sequential `-p=1` logs: `/private/tmp/issue377-targeted-post379.log`, `/private/tmp/issue377-full-unit-post379.log`, `/private/tmp/issue377-race-post379.log`, `/private/tmp/issue377-vet-post379.log`, `/private/tmp/issue377-build-post379.log`, `/private/tmp/issue377-lint-post379.log`, `/private/tmp/issue377-govulncheck-post379.log`, `/private/tmp/issue377-coverage-post379.log`; profile `/private/tmp/issue377-coverage-post379.out`.
- PostgreSQL DSN was unset; verbose storage tests explicitly skipped opt-in PostgreSQL cases (`/private/tmp/issue377-postgres-skip-post379.log`). The existing failure-handling unit test only opened localhost; loopback was approved. No external database was used.
- G11 reports green master runs `137005726248` and `137005725838`; GitHub compare confirmed the exact master commit. This developer could not read workflow jobs (API 404) and combined-status readback was empty; remote CI remains coordinator evidence.
- No staging, source commit, push, PR, merge beyond the base fast-forward, trade, broker call, deploy, or Stage5 action. Next: root starts fresh independent review; developer awaits GREEN and separate local-commit grant.

## G11 publication ownership clarification (r498)

- Source owner and local committer: `/root/issue377_developer`. Local commit still requires a separate grant after GREEN.
- Wire push and PR owner: G11 coordinator by direct human exception. This records routing ownership only; it is not a current publication grant to either owner.
- G11 may wire-push/open the PR only after fresh independent GREEN covers the exact final three blobs and the required post-publication CI evidence is available at the exact head. G11 retains the merge gate.
- Next: root/reviewer resolves the current exact handoff and source hashes; then follow the granted Stage4 sequence. No staging, commit, push, PR, or merge was performed here.

## Corrected publication sequence (r498 follow-up)

- This addendum corrects the preceding publication-ownership sentence; all earlier handoff bytes remain preserved. The direct-owner exception for G11 normal push/PR is already granted and is bounded through Stage4; it does not bypass the sequence or grant an action now.
- Sequence: local gates pass and a fresh independent GREEN covers the exact final three blobs; the original developer receives the separate local-commit grant and commits; G11 performs normal push/PR under the existing exception; GitHub review and all configured CI must pass on the exact PR head before G11's merge gate.
- Current status remains WAIT for fresh GREEN and the separate local-commit grant. G11 retains the merge gate. No source staging, commit, push, PR, or merge was performed.
