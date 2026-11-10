# Issue #247 — checkpoint issue-247-c1

Контракт: [Issue #247](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/247), marker `codex-momentum-signal-batch:v1`. Этот файл отсутствовал в чистой базе `64790dcdb3417b5987fecce519db00098ba1ed01`; исторических байтов в базе нет. Создан новый handoff без копирования primary checkout.

- Владелец: `/root/issue247_developer`, `developer-middle`; чат `01a1bd07-121f-7100-b07c-b8858731cdbf`, host `local`.
- Worktree: `/Users/alekseenkodenis/.codex/worktrees/issue247-signal-batch/bybit_bot_liquidity`; branch `codex/247-signal-batch`; стартовый base `64790dcdb3417b5987fecce519db00098ba1ed01`.
- Пути: `internal/momentum/signals/batch.go`, `internal/momentum/signals/batch_test.go`, `.ai/handoffs/issue-247.md`; reservations подтверждены root по queue rev127.
- Реализация взята за основу из полного прототипа по Issue и остаётся в пределах исключения этой Issue: 181 production line, 241 физических строк тестов. Прототип до этого не исполнялся; его статус не считается проверкой.
- Выполнено: branch создан в изолированной managed worktree; gofmt; targeted `go test ./internal/momentum/signals` — PASS на восстановленном Go 1.26.10 с отдельным cache `/private/tmp/bybit-g5-go126-cache`.
- Toolchain: `/private/tmp/bybit-g5-go126-restored/go`, `GOTOOLCHAIN=local`; прежний неполный toolchain/cache не использовался и не изменялся.
- Ожидаются: проверка owning config/window/ROC пакетов; полный test/race/vet/build и все заданные coverage/PG17.11 stages с сохранением доказательств; итоговый diff review и актуализация handoff.
- Блокер: отсутствует. Финальная база, independent review и публикация ожидают отдельный root ACTIVE_SINGLE_SLOT grant после полного завершения #246.
- Следующий шаг: выполнить оставшиеся проверки в выделенной worktree; сообщить root результат DEV без final GREEN/publication.

Scope остаётся чистым value/codec/replay. Этот этап не добавляет journal, runtime, collector readiness, внешние вызовы, торговлю или E2E claims.

## Checkpoint — issue-247-c2 (DEV complete; waiting for root integration slot)

- Final source/test counts: 181 production lines and 241 physical test lines (the Issue #247-only allowance is <=250 test lines). The CI-pinned formatter simplified one composite literal from `time.Time{}` to `{}`; behavior and line count are unchanged. No acceptance case was removed.
- Final source hashes: `batch.go` SHA256 `c230f30a005a07ef356fe98ed363bb9b63557855cf7eda1b0c26493d89326238`; `batch_test.go` SHA256 `d8625e4cfbac5316873733756e0aa94bc0462b92f327ffb3eb5a59b5553b4b90`.
- PASS on the final code semantics: Go 1.26.10 targeted signals/config/data/planner tests; full `go test -count=1 -v ./...` with disposable PostgreSQL 17.11; full `go test -race -count=1 -v ./...`; Momentum `go test -race -count=1 -v ./internal/momentum/...`; aggregate coverage 83.1% (gate 70%); `go vet ./...`; `go build ./...`; repository gofmt check; golangci-lint v2.10.1 (0 issues); govulncheck v1.1.4 (no reachable vulnerabilities); whitespace checks.
- Each DB-backed stage used its own new container from `postgres@sha256:ff87138949ad9a46f32ff23bd6852b52ce024a13dffe9b4087be98292cdd3b4f`, runtime memory/tmpfs 1 GiB, loopback-only ephemeral port, synthetic local DSN, direct SQL `server_version_num=170011`, and cleanup proof. No skipped tests in full, full-race, Momentum-race, or coverage logs.
- Evidence logs: `/private/tmp/issue247-full-2.log`, `/private/tmp/issue247-full-2-evidence.txt`; `/private/tmp/issue247-fullrace.log`, `/private/tmp/issue247-fullrace-evidence.txt`; `/private/tmp/issue247-momentumrace.log`, `/private/tmp/issue247-momentumrace-evidence.txt`; `/private/tmp/issue247-coverage.log`, `/private/tmp/issue247-coverage-evidence.txt`, `/private/tmp/issue247-coverage.out`, `/private/tmp/issue247-coverage-summary.log`; `/private/tmp/issue247-vet.log`, `/private/tmp/issue247-build.log`, `/private/tmp/issue247-lint-final.log`, `/private/tmp/issue247-vuln-escalated.log`.
- A first full-suite wrapper attempt also showed test PASS output but did not capture its pipeline exit because zsh lacked `PIPESTATUS`; it is excluded from gating evidence. The second full run above is the recorded gate. The initial linter-only formatting finding was resolved with the linter's formatter and final lint passed.
- Local status: branch `codex/247-signal-batch`, base remains `64790dcdb3417b5987fecce519db00098ba1ed01`; only the three approved paths are untracked/changed. Setup evidence: `/private/tmp/issue247-setup.json`.
- No implementation blocker. This is DEV completion only. Do not update base, request final review, commit, push, or open a PR until root grants ACTIVE_SINGLE_SLOT after #246 completes. No independent GREEN or publication is claimed.

## Checkpoint — issue-247-c3 (refreshed base; review fixes ready)

Перед этим review-fix сохранён прежний полный c3 в
`/private/tmp/issue247-handoff-before-reviewfix-20261110.md`; receipt CHANGES_REQUIRED
сохранён в `/private/tmp/issue247-review-final-before-fix.json`. Исторические c1+c2
остаются неизменным байтовым префиксом.

- Contract: актуальный body Issue #247, marker `codex-momentum-signal-batch:v1`;
  exact3 paths и предел <=250 физических test lines без изменений. Branch
  `codex/247-signal-batch`, refreshed base/HEAD
  `5fd2ae683d7c7f2c681557be769f3f24f689ac3b`; #248 не пересекался с reservations.
- Review findings закрыты в test path. Общий edit сдвигает все 31 open time во всех
  трёх рядах на сутки, не меняя request bounds и server clocks; тест требует точный
  `ErrBatch`, сохраняя однорядный typed `ReasonDifferentEnd` test. Caller slice,
  переданный в `DecodeBatch`, мутируется после decode; сохранение decoded payload/hash
  проверяется вместе с прежней getter-copy mutation.
- Counts/hashes: `batch.go` 181 line,
  SHA256 `c230f30a005a07ef356fe98ed363bb9b63557855cf7eda1b0c26493d89326238`;
  `batch_test.go` 250 физических строк,
  SHA256 `4da305b82e2704c75b05bb3e5741de619d2a2d4382eb1361e10c61cccff065ed`.
- Fresh focused tests PASS: `go test -count=1 -v ./internal/momentum/signals
  ./internal/momentum/config ./internal/momentum/data ./internal/momentum/planner`.
  Targeted SignalBatch coverage 96.1%; profile
  `/private/tmp/issue247-reviewfix-signals.out` shows
  `batch.go:128.56,130.4 1 1`, proving latest-C rejection executes.
- Final Go1.26.10 formatter diff/gofmt and whitespace checks PASS; golangci-lint
  v2.10.1 PASS (0 issues). Logs:
  `/private/tmp/issue247-reviewfix-targeted.log`,
  `/private/tmp/issue247-reviewfix-coverage.log`,
  `/private/tmp/issue247-reviewfix-signals-summary.log`,
  `/private/tmp/issue247-reviewfix-lint.log`.
- Earlier final-base full/full-race/Momentum-race/coverage (83.1%) and PG17.11
  evidence remains valid for unchanged production and refreshed base; these two
  additions alter tests only. Their raw evidence and four separate DB stage records
  remain at `/private/tmp/issue247-final-*`. No broad DB stage was rerun for this
  test-only review delta.
- Ready for a new independent read-only review of all three current blobs. No reviewer
  launched here, no commit/push/PR performed; wait for parent review result and any
  separate publication grant.
