# Issue #260 handoff

- Workspace: `/Users/alekseenkodenis/.codex/worktrees/issue-260-momentum-collect/bybit_bot_liquidity`
- Branch: `codex/260-momentum-collect`
- Base at c1 start: `4b00257ec8701bdc04de74ed721b9a3c63cca66b` (historical)
- Owner: implementation chat `01a1e0ad-1ce8-7c71-87fc-6640132a3bca` on local host, role `developer-middle`; coordinator chat `01a1d174-d67c-7c61-951e-862aa3881bcb`.
- Contract: GitHub Issue [#260](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/260). Dependencies #59, #240, #247, #254 and #256 were closed at start. GitHub master matched the base at start.
- Historical c1 scope was the six reserved paths. The command uses public Testnet time and candles, validates a ROC30 batch, writes to the explicitly configured loopback PostgreSQL pool, and reports success only after exact-key canonical readback. It sends no broker or private requests.

## Historical c1 verification

- Go 1.26.11: `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, targeted command and Bybit tests, and targeted Bybit race passed. `gofmt`, `golangci-lint` and `git diff --check` passed.
- Disposable PostgreSQL end-to-end/race run passed with `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, server version 170011, loopback networking, 1 GiB tmpfs, zero skipped tests and verified cleanup. The fake transport exercised the actual command runner, canonical Testnet host and exact time/kline requests, append, exact readback, replay and one-row idempotency. Synthetic BTC/ETH/HYPE rows checked positive, zero and negative ROC30 flags, provenance and ascending candle order. Final evidence is `/private/tmp/issue260-pg-race.2Jk8w1po/output.log` and `tests.log`.
- Deterministic callback tests exercise one insert and exact-key read after `ErrSignalBatchInsertUnknown` and `ErrSignalBatchNotInserted`; absent, corrupt, mismatched and canceled readback fail closed. No production fault injection was added.
- Coverage is 77.6% against the 70% CI minimum. `govulncheck@v1.1.4` found no called vulnerabilities and one module-only advisory in a required dependency. Full gate logs are under `/private/tmp/issue260-checks/`.
- No real public Testnet collection or trade was run. The simulated end-to-end test proves the command/database flow, not current live source availability. The post-merge public-source run remains with the coordinator.

## Historical c1 next action (superseded)

- The six-path review plan above was superseded by c2 RED and the approved c3 split.

## Revision chronology

- `issue-260-c1` recorded the original six-path implementation and its checks. Its historical report remains above; later review required more proof than the approved test-line cap allowed.
- `issue-260-c2` was returned RED. The full test draft measured 366 raw lines against the 300-line cap, so that draft was not ready (412 projected gofmt lines) and no c2 PostgreSQL result is claimed as final evidence.
- `issue-260-c3` resumes under the revised four-path contract at queue revision 193. #261 (public Testnet clock) and #263 (validated public batch collection) are complete and merged. The unchanged approved body is SHA-256 `1d4ca4e455d66c17c7e6f6b5098816938ba29813e45d09bef557e67739e58feb`. Final review-only slot revision 194 was confirmed by the coordinator. GitHub master/base is `bcd234b3d4a4c62c69a50427c94007fc687f9d24`; the Issue branch was fast-forwarded normally to that exact commit after verifying ancestry.

## C3 scope and validation

- Contract marker: `<!-- codex-momentum-first-collect:v1 -->`; approved body SHA-256 `1d4ca4e455d66c17c7e6f6b5098816938ba29813e45d09bef557e67739e58feb`.
- Exact paths: `cmd/momentum/main.go`, `cmd/momentum/main_test.go`, `.github/workflows/momentum-postgres.yml`, `.ai/handoffs/issue-260.md`.
- The command delegates public clock, validated windows, and batch construction to `collect.ReadBatch`; it opens the explicitly configured local journal only after successful collection, then migrates, inserts once, verifies exact-key payload replay, and prints one JSON success line. It performs no trading or private broker calls.
- Validation ran first in `/private/tmp/issue260-validated-base.fY1mJGJB` and then in the final Issue worktree at base/HEAD `bcd234b3d4a4c62c69a50427c94007fc687f9d24`. The merge was `git merge --ff-only`; ancestry and the exact resulting HEAD were verified. Only the four reserved paths contain candidate modifications; unrelated `.serena/.gitignore` and `.serena/project.yml` remain untouched.
- Budget: 127 production additions (126 Go + 1 workflow line) and 299 formatted test additions, within 250/300 limits.
- Go 1.26.11 checks passed: targeted command/collector/Bybit/Momentum tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; gofmt; per-file `git diff --no-index --check` (no whitespace reports). Archive logs are `/private/tmp/issue260-c3-checks/{go-test.log,race.log,vet.log,build.log,gofmt.log,diffcheck.log}`; actual final-worktree reruns are `/private/tmp/issue260-c3-checks/finalfix-{targeted,all,race,vet,build}.log`, plus `finalfix-gofmt.log` and `finalfix-diffcheck.log`. The full `go test -race ./...` passed (the opt-in PostgreSQL E2E skips without its DSN). Separately, the coordinator's fresh PostgreSQL race command `go test -race -count=1 -v ./cmd/momentum ./internal/momentum/...` passed with zero skips.
- `golangci-lint` 2.10.1 returned 0 issues using the complete Go 1.26.11 GOROOT after the test-only correction. Lint log `/private/tmp/issue260-c3-checks/finalfix-lint.log`. `govulncheck@v1.1.4` found no called vulnerabilities and one module-only advisory in a required dependency; log `/private/tmp/issue260-c3-checks/govulncheck.log`. CI coverage is 77.9% against the 70% minimum, rerun after the test-only correction; `/private/tmp/issue260-c3-checks/finalfix-coverage.log` and `finalfix-coverage.out`.
- Fresh final-worktree PostgreSQL proof: `/private/tmp/issue260-c3-final-pg-race.oLhbJ6xn/output.log` and `tests.log`; pinned PostgreSQL 17.11 (170011), loopback, 1 GiB tmpfs, zero skips and verified disposable cleanup. The log records final HEAD `bcd234b3d4a4c62c69a50427c94007fc687f9d24` and exact `main.go`, `main_test.go`, and workflow hashes below. The test proves public fake transport, no auth/private requests, insert/replay, raw canonical hash, provenance, 31 ascending confirmed candles per symbol, ROC flags, output failure and checked database cleanup.
- Final hashes: `main.go` `ad0711be056149d07e0a9f001f642a414d9688a6c8d9497c7404ddd48c280e31`; `main_test.go` `05f03a409316729f5b15476b79fe34b62409a212350901e9341c4bea626f6d03`; workflow `9c0269aa9f267073a1d4e821d99672b5227d666018731b2ab8ce42e2b00aef9b`. The exact four path hashes, including this handoff, are in `/private/tmp/issue260-ready.json`. No live public Testnet collection or trade was performed. The first c3 review found a 50 ms timeout shared by the empty-source and timeout cases; this was a test-only flake, corrected by giving the empty-source case 5 s while preserving the 50 ms deadline case and all assertions. The test file remains 299 lines. `go test -race -count=50 -run '^TestCollectFailures$' ./cmd/momentum` passed in `/private/tmp/issue260-c3-checks/finalfix-stress.log`. The corrected test hash is `05f03a409316729f5b15476b79fe34b62409a212350901e9341c4bea626f6d03`; the fresh pinned PostgreSQL gate above used that exact hash. Independent re-review is now pending coordinator launch; no review or publication was performed by this developer.
