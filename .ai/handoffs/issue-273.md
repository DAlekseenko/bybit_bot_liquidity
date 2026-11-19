# Issue 273 handoff

<!-- codex-cm013-funding-page:v1 -->

- Checkpoint: issue-273-c1
- Issue/contract: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/273; `codex-cm013-funding-page:v1`.
- Dependencies: #66 and #240 are closed. Coordinator live queue readback: revision 233, sole integration slot #273.
- Task/chat: 01a1e5fe-8732-75b3-870a-e33b28338602 on local; developer owner: /root/issue273_developer (developer-middle).
- Worktree: /private/tmp/bybit-issue-273
- Branch/base: codex/273-momentum-funding-page from 7736790d2cfa57a2f6348783dab493afe21c22c5. Branch fast-forwarded from the READY base after verifying ancestry and no overlap between master changes and the six reserved paths. Original READY file bytes and blob ids were verified unchanged before rebasing the evidence on this final base.
- Approved paths: internal/momentum/model/funding_page.go; internal/exchange/bybit/momentum_funding.go; internal/exchange/bybit/momentum_funding_test.go; internal/exchange/bybit/momentum_candle_page.go; docs/API.md; .ai/handoffs/issue-273.md.
- Implementation: added neutral funding page/event types and one unsigned historical funding GET. Validates origin, symbol, inclusive millisecond bounds, response metadata, exact timestamps and signed rates; rejects malformed/duplicate rows and returns validated events in descending timestamp order without pagination. Shared candle-page HTTP origin policy extraction preserves existing request semantics. API documentation added. No runtime, storage, trading, risk or deployment changes. A page is one bounded observation and does not assert range completeness.
- Verification on final base with Go 1.26.11: gofmt, whitespace checks, and git diff --check passed; `go test -count=1 ./internal/exchange/bybit ./internal/momentum/model` passed; `go test -race -count=1 ./internal/exchange/bybit ./internal/momentum/model` passed; `go test -count=1 ./internal/exchange/bybit -run '^TestMomentumFundingPage'` passed after adding the malformed timestamp/rate, empty-list server-time, real HTTP timeout, and unique over-limit cases. An out-of-tree Go overlay removed only the page-size guard and the over-limit test failed on the resulting valid 201-event page, confirming the test detects the 200-row contract. `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` passed. golangci-lint v2.10.1 passed. CI coverage command passed at 75.2% (threshold 70%). `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` exited 0 after fetching the official database from `https://vuln.go.dev/index/modules.json.gz`: zero reachable vulnerabilities and one module advisory not called by project code.
- Scope: 138 affected production lines and 233 affected test lines. Exact final SHA256/Git blob pairs and verification evidence are in /private/tmp/issue273-final-candidate.json.
- State: READY for independent review. No staging, commit, push, PR, final review, or queue mutation performed.
- Next: coordinator routes independent reviewer. Stop before publication pending reviewer GREEN and separate owner publication confirmation.
