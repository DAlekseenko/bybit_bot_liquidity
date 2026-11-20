# Issue #326 — checkpoint `issue-326-c1`

- Contract: [Issue #326](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/326), `codex-cm015-funding-rr-reader:v1`; parent #70. DEV only through READY; #310 retains the sole integration slot.
- Owner/task: `/root/issue326_developer`, task `01a1f5db-bcd7-7051-b436-172b2420df47`, `developer-middle`.
- Worktree: `/private/tmp/bybit-issue-326`; branch `codex/326-funding-rr-reader`; base `173a61d0ca650de3883e956852565dad456dabbb`.
- Approved paths: `internal/momentum/storage/funding_read.go`, `internal/momentum/storage/funding_read_tx_test.go`, `.ai/handoffs/issue-326.md`.
- Change: factor the existing funding projection and add a reader for a caller-owned RR/RO snapshot. Existing API transaction lifecycle and SQL projection are preserved. Tests cover mode rejection, caller ownership, errors, limit and a two-stream shared snapshot.
- Protected files: existing `internal/momentum/storage/funding_read_test.go` and `internal/momentum/storage/read_snapshot.go` remain byte-identical to base.
- Verification (Go 1.26.11, DSN unset for ordinary gates): targeted tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, gofmt/diff and golangci-lint v2.10.1 passed. Ordinary coverage is 2912/4123 = 70.628183361630%. govulncheck v1.1.4 reports zero reachable/imported vulnerabilities and one required-module advisory not called by project code. Real PostgreSQL 17.11 (`server_version_num=170011`) passed all 14 funding test groups under `-race`, no skips; disposable loopback container was stopped and removed. Evidence logs and hashes are in `/private/tmp/issue326-ready.json`.
- Invariants: prior `funding_read_test.go` and `read_snapshot.go` compare byte-identical to base. Index is empty; only the three approved paths are changed.
- Blocker/next: implementation and developer checks are READY. Wait for separate ACTIVE before independent review/publication. No review or publication is authorized in this phase.

## Active continuation — checkpoint `issue-326-c2`

- Queue #55 comment `6668604794`, revision 387: G10 granted #326 the sole ACTIVE slot. This checkpoint supersedes the prior READY-only next step; no review or publication is implied.
- Same task/chat `01a1f5db-bcd7-7051-b436-172b2420df47`, owner `/root/issue326_developer`, `developer-middle` at medium effort; same isolated worktree and branch.
- Fast-forwarded only from the prior base to current master `bcc31de9ea44d096168ede9758fc8c12772676fc` (tree `e06e1f5e2c37450aa8b4d4627379e791856b7132`). The two Go file SHA256 values remain exactly as in the previous READY manifest.
- Reservations remain the same exact three paths; existing `funding_read_test.go` and `read_snapshot.go` remain byte-identical to current base. The previous READY manifest/logs are preserved; active-base verification and a new READY manifest are pending.
- Next: run fresh pinned Go 1.26.11 full and targeted checks, PostgreSQL 17.11 race groups without skips, and offline-only govulncheck; then STOP READY for independent review.

### Active-base verification — checkpoint `issue-326-c3`

- Base `bcc31de9ea44d096168ede9758fc8c12772676fc`, tree `e06e1f5e2c37450aa8b4d4627379e791856b7132`: full `go test -count=1 ./...`, full `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...`, gofmt, diff check, golangci-lint v2.10.1, and offline govulncheck v1.1.4 all PASS.
- Ordinary coverage (DSN unset): 3407/4777 = 71.320912706720%.
- All 14 funding groups passed under race on PostgreSQL 17.11 (`server_version_num=170011`), no skips. Disposable loopback container was removed and its absence verified.
- Active evidence and log hashes are in `/private/tmp/issue326-active-ready.json`. The prior READY manifest and evidence remain preserved.
- Status: READY_FOR_INDEPENDENT_REVIEW. STOP pending independent review; publication remains gated on GREEN and separate permission.
