# Issue #330 — DEV/READY handoff

## Контракт

Issue [#330](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/330), `codex-cm015-mark-rr-reader:v1`. База `4897b35a62f28c74e69d897ed20a013bd59571c7`; ветка `codex/330-mark-rr-reader`. Выполнена только caller-owned mark read часть общего снимка.

## Изменения

- `ReadMarkPriceRangeTx` принимает только caller-owned `REPEATABLE READ READ ONLY`, проверяет режим через существующий `readSnapshotMode` и оставляет lifecycle транзакции caller.
- Существующая mark projection вынесена в `readMarkPriceRange` и используется обоими public readers. Два SQL literals, provenance до records, ограничения окна, exact NUMERIC и fail-closed overflow сохранены.
- `mark_read_test.go` и `read_snapshot.go` побайтно совпадают с базой. Иные файлы не менялись.

## Проверки

Все проверки выполнены Go `go1.26.11 darwin/arm64`.

- Целевой PostgreSQL race: 11 групп, PostgreSQL `17.11 (Debian 17.11-1.pgdg13+2)`, без skip. Проверены exact NUMERIC, invalid rows, confirmed, D/60, два поля registry и чужой source, пустой результат, предел 200/201, UTC +03/off-grid в `Pacific/Honolulu`, late insert/стабильность общего snapshot и видимость из свежего reader, pre-expired context и SQL-blocked deadline с наблюдаемым `pg_blocking_pids`. Лог: `/private/tmp/issue330-targeted-pg-race.log`.
- `go test -count=1 ./...`: PASS; лог `/private/tmp/issue330-unit.log`.
- `go test -race -count=1 ./...`: PASS; лог `/private/tmp/issue330-race.log`.
- Полный coverage suite с `MOMENTUM_TEST_POSTGRES_DSN` unset: PASS, `2976/4231 = 70.337981564642%` (порог CI 70%); лог `/private/tmp/issue330-coverage.log`, profile `/private/tmp/issue330-coverage.out`.
- `go vet ./...`, `go build ./...`, `gofmt -l cmd internal`, `git diff --check`: PASS.
- `govulncheck@v1.1.4`: PASS, 0 вызываемых/imported vulnerabilities; 1 module-only advisory. Лог `/private/tmp/issue330-security.log`.
- `golangci-lint v2.10.1`: PASS, 0 issues. Лог `/private/tmp/issue330-lint.log`.

Целевой PostgreSQL работал во временном loopback-only контейнере `codex-issue330-pg` (ID `5da00e95d9f8`). После тестов контейнер остановлен; точная проверка `docker ps -a --filter name=^/codex-issue330-pg$` не вернула контейнер. Cleanup log: `/private/tmp/issue330-cleanup.log`, SHA256 `756efcc6112192118cb44003ef1c895652439972e8f449e21dd30c8fdde6b847`. Общие run logs и финальные хеши перечислены в `/private/tmp/issue330-ready.json`.

## Размер и состояние

`mark_read.go`: 148 физических строк; `mark_read_tx_test.go`: 272 строки; в пределах контракта. Индекс Git пустой. Только три пути Issue разрешены к рассмотрению: `internal/momentum/storage/mark_read.go`, `internal/momentum/storage/mark_read_tx_test.go`, `.ai/handoffs/issue-330.md`.

## Следующий шаг

Остановиться на READY. Независимое ревью и последующие публикационные действия требуют отдельного ACTIVE и принадлежат последующим этапам координатора.


## ACTIVE refresh → READY_FOR_INDEPENDENT_REVIEW — 2026-11-20

- G10 authorized the refreshed DEV/ACTIVE checkpoint after canonical queue #55 comment `6668604794`, revision `389`, identified #330 as the sole `ACTIVE_SINGLE_SLOT` and HELD these three paths. Exact Issue #330 contract remains `codex-cm015-mark-rr-reader:v1`. Native implementation task/chat `01a1f601-a5d3-7cc1-8a1e-ef97b5b877d8`; coordinator chat `01a1ee9e-f0d5-71c2-ba96-ceadad8f8f5f`; owner `/root/issue330_developer`, `developer-middle` at medium effort. Branch `codex/330-mark-rr-reader`, worktree `/private/tmp/bybit-issue-330`.
- Refreshed the same branch by fetching only local mirror ref `refs/remotes/origin/master` from `/private/tmp/g10-master-git.yRD9rk` and running ordinary `git merge --ff-only`. Current base/HEAD `d0d1e69a607eccd0a5c8f63e0e88a5ba18e6adef`, tree `70389d4454b3e670a072957381d053c300aea8a7`; reflog says `merge refs/remotes/origin/master: Fast-forward`, and prior base `4897b35a62f28c74e69d897ed20a013bd59571c7` is its ancestor. Evidence: `/private/tmp/issue330-active-base-refresh.log`.
- The implementation and test Go files retain their exact prior READY SHA256 values. The full previous handoff is byte-for-byte the prefix of this file (prefix SHA256 `f6c9467b755fd21e0daa9da0077e2ab7b96674f525ef6fa3d24d84e40022550b`). Protected `mark_read_test.go` and `read_snapshot.go` remain unchanged.
- Fresh verification on refreshed HEAD using Go `go1.26.11 darwin/arm64`: 11/11 targeted PostgreSQL 17.11 race groups PASS, no skips; full ordinary suite PASS; full race suite PASS; full coverage with `MOMENTUM_TEST_POSTGRES_DSN` unset PASS, `3414/4794 = 71.214017521902%`; `go vet ./...`, `go build ./...`, gofmt, diff check, `golangci-lint v2.10.1` PASS (0 issues).
- Offline security scan: build of the cached `golang.org/x/vuln@v1.1.4` module and scan against `file:///private/tmp/g10-public-vulndb.Q6sE6qf4/db` with `GOPROXY=off` and `GOSUMDB=off`; 0 code-reachable vulnerabilities and 1 module-only advisory. Log: `/private/tmp/issue330-active-security.log`.
- Disposable loopback-only PostgreSQL container `codex-issue330-active-pg` (ID `022faef03291`) was stopped and exact-name absence verified. Cleanup log `/private/tmp/issue330-active-cleanup.log`, SHA256 `70dcebaf268d5bd151d4b17bf9673af4f6aaa560d7a5b0a35e76f9b84024247b`. Fresh raw check logs and hashes are recorded in `/private/tmp/issue330-active-ready.json`.
- Scope remains exactly three paths: `internal/momentum/storage/mark_read.go` (148 lines), `internal/momentum/storage/mark_read_tx_test.go` (272 lines), and this handoff. Index is empty.

STOP at READY_FOR_INDEPENDENT_REVIEW. Reviewer admission/publication requires the next separately authorized step; no commit, push, PR, Issue, or queue mutation was performed.
