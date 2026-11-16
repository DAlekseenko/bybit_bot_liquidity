# Issue #263 handoff

<!-- codex-momentum-public-batch:v1 -->

- Checkpoint: `issue-263-c1`
- Issue: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/263
- Task/chat: `01a1e28c-b9ca-7452-a4de-391dbac5a225`
- Owner: `/root/issue263_developer`, developer-middle
- Coordinator grant: `ACTIVE_SINGLE_SLOT rev190`, final review preparation only
- Base: `cf2b05bed47a86176e0f713865e0a58842b2b12e`; refreshed `origin/master` and verified it is the same commit.
- Branch: `codex/263-testnet-batch`
- Worktree: `/Users/alekseenkodenis/.codex/worktrees/issue-263-testnet-batch/bybit_bot_liquidity`
- Parent #260 worktree and dirty primary checkout were not changed.

## Approved files and limits

- `internal/momentum/collect/source.go` — 86 lines, SHA-256 `c501d95277954e9c05fd9037bc5b896a85bb2a8f48311d173f59f1376dc0ad85`, git blob `468cb0841eb9b3161adb6ff99b48327979541414`.
- `internal/momentum/collect/source_test.go` — 192 lines, SHA-256 `39e6525632046f08d0a113d1c38b971a09dc7f49b6ce17ad91e9f339b7ff1ac0`, git blob `22577cb67cbb46a116a10f2609d39d18c19aa474`.
- `.ai/handoffs/issue-263.md` — this Issue handoff.
- The approved scope is exactly these three paths, within the 110-line implementation and 220-line test caps. `.serena/` is incidental untracked state, excluded from the approved scope and left untouched.

## Behavior and tests

`collect.ReadBatch` validates the supported paper/ROC30/public-Testnet config before HTTP and returns no partial result on error. It uses the fixed canonical Testnet origin, reads the public server clock, derives a UTC midnight cutoff, requests one daily candle page per configured symbol, sorts a fresh `DailyClose` projection, preserves source provenance and delegates canonical window and signal validation to `signals.NewBatch`.

Fake transport tests verify unsigned GET requests at the exact TLS Testnet origin, endpoint/query/window bounds, newest-first candle pages, one clock plus three candle reads, canonical batch SHA and exact payload replay. Failure cases cover empty, partial, gapped, rollover and unconfirmed windows, bad/too-early/out-of-range clock, redacted vendor errors, unsupported config, pre-cancel, wrapped cancel/deadline and in-flight timeout, request counts and zero partial result.

## Verification

Go 1.26.11 at `/private/tmp/g6-go12611.vbIcdd/go/bin/go`.

- `gofmt` and `git diff --check`: PASS.
- Targeted `go test -count=1 -v ./internal/momentum/collect` and targeted `go test -race -count=1 -v ./internal/momentum/collect`: PASS.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`: PASS.
- CI coverage command: PASS, 78.1% total against 70% threshold.
- golangci-lint v2.10.1: PASS, 0 issues.
- govulncheck v1.1.4: PASS, no vulnerabilities found in code paths. It reported one required module advisory that does not appear to be called.
- Full test, race and coverage suites include a PostgreSQL test that binds a localhost socket, so these commands ran with local loopback access enabled. The implementation tests make no broker/API requests.
- Detailed commands and logs: `/private/tmp/issue263-ready.json` and the evidence paths recorded there.

## Next step

Ready for fresh independent read-only review of these three exact blobs. No review, staging, commit, push, PR, or GitHub/queue writes have been performed. Await the coordinator's review result and any separate publication grant.
