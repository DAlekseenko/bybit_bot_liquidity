# Issue #308 — LONG mark valuation

Реализована чистая `ProjectLongValuation` для переданной LONG позиции и явного
наблюдения mark цены. Проверяются инвариант позиции, аргументы и явный
`maxAge`; времена нормализуются в UTC с сохранением наносекунд, monotonic
metadata удаляется. Notional и UPL рассчитываются точными операциями Decimal:
`Quantity × Mark` и `Notional − remaining CostBasis`. Flat позиция возвращает
точные нули только после валидации и проверки свежести. Ошибки статические,
результат при ошибке нулевой.

Комиссии, funding, transfers, wallet, margin, liquidation и runtime не менялись.

Изменены только три зарезервированных пути: production
`internal/momentum/model/long_valuation.go` (84 строки), тесты
`internal/momentum/model/long_valuation_test.go` (190 строк) и этот handoff.

Проверки на Go 1.26.11, `MOMENTUM_TEST_POSTGRES_DSN` не задан:

- `go test -race ./internal/momentum/model -run TestProjectLongValuation -count=1` — PASS;
- `go test ./...` — PASS;
- `go test -race ./...` — PASS;
- CI coverage `go test -coverprofile=... ./...` — 2761/3885 = 71.068211068% (порог 70%) — PASS;
- `go vet ./...`, `go build ./...`, `gofmt`, `git diff --check` — PASS;
- golangci-lint v2.10.1 `run ./...` — 0 issues;
- govulncheck v1.1.4 `./...` — 0 reachable/package vulnerabilities; module-only
  advisory GO-2026-5024 (`golang.org/x/sys@v0.36.0`, Windows
  `NewNTUnicodeString` integer overflow; fixed in v0.44.0), not called by code.

Основные логи и профиль: `/private/tmp/issue308-logs/`. Unit, race и coverage
выполнялись с DSN unset. В существующем тесте storage используется loopback
listener; для полного набора потребовалось разрешение на локальный bind.

Готово к независимому ревью после получения отдельного ACTIVE слота. До этого
ветка остаётся незакоммиченной; staging, публикация и PR не выполнялись.


## Возобновление после ACTIVE — 2026-11-20

Ветка `codex/308-long-mark-valuation` fast-forward обновлена с исходной READY
базы `55c157919fe889448a0e61478d2e1191061e96a3` на свежий master
`e410b45f83e8d558d9153c4f5fdba57c33d32803` (tree
`b91616594125e7b114ad68dc7b2d4ceb750f743b`). Выполнен только обычный
`git merge --ff-only` из локального master-клона. Два Go-файла сохранили
исходные READY SHA-256 и Git blob hashes. Native chat:
`01a1f29c-0be1-7f22-bf5a-d1cab0649bd1` (`local`); owner `/root/issue308_developer`,
`developer-middle`, medium.

На обновлённой базе, Go 1.26.11, `MOMENTUM_TEST_POSTGRES_DSN` unset:

- targeted race, `go test ./...` и `go test -race ./...` — PASS;
- обычное CI coverage — 3493/4873 = 71.680689514%, порог 70% — PASS;
- `go vet ./...`, `go build ./...`, gofmt, whitespace и `git diff --check` — PASS;
- golangci-lint v2.10.1 — 0 issues;
- govulncheck v1.1.4 с локальной базой `/private/tmp/g10-public-vulndb.Q6sE6qf4/db` — 0 reachable/package vulnerabilities; одна module-only advisory GO-2026-5024 в `golang.org/x/sys@v0.36.0` (Windows integer overflow в `NewNTUnicodeString`, исправлено в v0.44.0).

Финальные логи — `/private/tmp/issue308-logs/*-merged.*`; финальный READY-манифест
будет сохранён вне checkout. Изменены только три зарезервированных пути.
Остановиться в READY_FOR_INDEPENDENT_REVIEW: ничего не stage/commit/push, PR и
review не выполнять до отдельного разрешения координатора.


## Review fix — P3 comment language, 2026-11-20

Independent review returned RED with exactly three P3 comments requesting Russian
wording for test comments. Updated only those three comment lines in
`internal/momentum/model/long_valuation_test.go`, preserving the identifiers
`TestProjectLongValuationTimeNormalization`,
`TestProjectLongValuationInvalidInputs` and
`TestProjectLongValuationArithmeticLimits`. Production source and all other test
bytes match `/private/tmp/issue308-review-fix-baseline.json`; all prior handoff
content is preserved above this checkpoint.

Повторные проверки финальных файлов на базе `e410b45f83e8d558d9153c4f5fdba57c33d32803`,
Go 1.26.11, `MOMENTUM_TEST_POSTGRES_DSN` unset:

- targeted race, `go test ./...`, `go test -race ./...` — PASS;
- ordinary CI coverage — не ниже 70%, точное значение в
  `/private/tmp/issue308-review-fix-ready.json`;
- vet, build, gofmt, whitespace, `git diff --check`, golangci-lint — PASS;
- offline govulncheck v1.1.4 с локальной базой — 0 reachable/package findings;
  GO-2026-5024 остаётся module-only advisory для Windows.

Все новые логи сохранены вне checkout в `/private/tmp/issue308-reviewfix-logs/`.
READY proof: `/private/tmp/issue308-review-fix-ready.json`. Работу остановить до
нового независимого GREEN; не stage, commit, push, открывать PR или публиковать.
