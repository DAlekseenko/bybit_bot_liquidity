# Handoff — Issue #357, CM-022A v2

- Точный контракт: [Issue #357](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/357), marker `codex-cm022a-frozen-disk-loader:v2`, parent #77. Контракт перечитан; canonical queue #55 revision 405 подтверждён, integration slot `none`.
- Checkout `/private/tmp/bybit-issue-357`, branch `codex/357-frozen-dataset-loader`; base/HEAD `ce9265b3eeb4228adee5567764eddef926ef19cf`, tree `8652b416ef39e1edbd9254ba56bf210810197736`.
- Только три разрешённых пути: `internal/momentum/backtest/dataset.go`, `internal/momentum/backtest/dataset_test.go`, `.ai/handoffs/issue-357.md`. Изменения не staged и не committed; публикация не начиналась.
- Артефакт v1 и его логи сохранены отдельно: `/private/tmp/issue357-ready.json`, SHA256 `db609eda6a6a93ebf722ae5941c93c5c07bc81cd4f42716c9bb855bd9d3dac94`.

## Изменения и v2-аудит

Загрузчик сохраняет исходное поведение: доверенный pin валидируется `data.DecodeManifest`, читаются только manifest и distinct referenced objects, затем существующий `data.CheckManifestContentBindings` проверяет provenance/content. Новые ветви по Issue v2:

- provenance mismatch fixture записывает canonical wrong-symbol payload в `objects/<wrongHash>.bin`, repin-ит manifest на этот hash; загрузчик читает объект, SHA проходит, и отказ приходит от content binding;
- unreadable manifest проверяется `tracingFS.reject` на настоящей операции `Open`; `MapFile.Mode=000` удалён как недействующая имитация;
- добавлена проверка уже истёкшего deadline с `errors.Is(err, context.DeadlineExceeded)`;
- при одновременных read error и отмене `readFrozenFile` возвращает sentinel вместе с исходной context classification.

Остальные acceptance cases v1 сохранены без сокращений и минификации. Фактический размер v2: 89 production lines, 409 физических test lines, 3 файла; лимиты v2 — 100/420/3, независимое ревью рассчитано ≤15 минут.

## Проверки финальных v2-файлов

Toolchain `/private/tmp/issue271-toolchain/go/bin/go` Go 1.26.11; GOCACHE `/private/tmp/issue271-toolchain/gocache`. `MOMENTUM_TEST_POSTGRES_DSN` unset.

- gofmt, diff check, targeted unit/race — PASS.
- Полные `go test -cover ./...` и `go test -race ./...` — PASS; backtest package coverage 100%.
- `go vet ./...`, `go build ./...` — PASS.
- golangci-lint v2.10.1 — PASS, 0 issues.
- Offline govulncheck с `/private/tmp/g10-public-vulndb.Q6sE6qf4/db` — PASS, 0 reachable vulnerabilities; GO-2026-5024 в `golang.org/x/sys@v0.36.0` найден только на уровне модуля, вызова из кода не обнаружено.
- PostgreSQL integration не запускалась: изменение использует `fs.FS`/`fstest.MapFS` и не меняет SQL/storage. Существующий `TestOpenPostgresFailures` прошёл с разрешённым loopback socket.

Логи v2: `/private/tmp/issue357-v2-full-coverage.log`, `/private/tmp/issue357-v2-full-race.log`, `/private/tmp/issue357-v2-vet.log`, `/private/tmp/issue357-v2-build.log`, `/private/tmp/issue357-v2-lint.log`, `/private/tmp/issue357-v2-security.log`.

## Статус

DEV v2 READY. Остановиться до отдельного ACTIVE: не выполнять review, staging, commit, push, PR, очередь или другие чаты.

## Issue #357 — точный контракт v3, DEV READY

- Handoff принадлежит native chat `01a1f7c1-9e2b-7221-aa87-ee9069961efa`, developer owner `/root/issue357_developer`, роль `developer-middle` medium. Текущий exact marker `codex-cm022a-frozen-disk-loader:v3`; Issue прочитан целиком, очередь #55 revision 408 подтвердила `#357 ACTIVE_SINGLE_SLOT`.
- Дизайнерский план `/private/tmp/g10-357-budget.md` перед правками проверен SHA256 `b9ec15a6983d37379d87e0c0ab965076b035ca210bb3c209dc4c36d5da19a979`. Production loader сохранён побайтно от v2: SHA256 `813adff651275205a1f56f5aec3bd341c70a0d04fd5a9716b17ea8a1f1c8488f`.
- Тесты перестроены через типизированный fixture с исходными object bytes, один `repin`, fake `fs.ReadFileFS`, общую zero/sentinel/context/error assertion и плоские filesystem failure cases. Сохранена вся acceptance matrix v2: exact bytes/repeated load/output isolation, exact distinct hash keys/read-once/no unreferenced reads, все четыре manifest отказа, missing/unreadable manifest и missing/empty/mismatched content/evidence/model, истинный canonical provenance mismatch с объектом под правильным wrongHash path, pre-canceled и expired DeadlineExceeded, отмена при успешном/ошибочном ReadFile, финальная проверка после binding, nil context/source и непрозрачные evidence/model bytes.
- Изменённые ровно три HELD paths. Итоговый бюджет после gofmt: production 89/100, tests 300/300 физических строк, файлы 3/3; strict AGENTS cap 300 соблюдён. Исходный v1 JSON/логи и v2 JSON/логи не перезаписаны; сохранены `/private/tmp/issue357-ready.json` SHA256 `db609eda6a6a93ebf722ae5941c93c5c07bc81cd4f42716c9bb855bd9d3dac94` и `/private/tmp/issue357-v2-ready.json` SHA256 `4265fa3222d8f4402e0ab3872db4c53d2a21fa39b19ed3f75c04cdac2b2d419b`.
- Финальные gates v3: targeted unit/race, full unit, full coverage (backtest 100%), full race, vet, build, gofmt, diff check, golangci-lint 0 issues и offline govulncheck — PASS. DSN `MOMENTUM_TEST_POSTGRES_DSN` unset. PostgreSQL вне изменения: тестируемая граница — `fs.FS`/`fstest.MapFS`, SQL/storage не менялись; loopback разрешён только для существующего `TestOpenPostgresFailures`. Security scan: 0 reachable vulnerabilities; GO-2026-5024 в `golang.org/x/sys@v0.36.0` только module-level, вызовов из кода не обнаружено.
- Логи v3: `/private/tmp/issue357-v3-targeted-unit.log`, `/private/tmp/issue357-v3-targeted-race.log`, `/private/tmp/issue357-v3-full-unit.log`, `/private/tmp/issue357-v3-full-coverage.log`, `/private/tmp/issue357-v3-full-race.log`, `/private/tmp/issue357-v3-vet.log`, `/private/tmp/issue357-v3-build.log`, `/private/tmp/issue357-v3-lint.log`, `/private/tmp/issue357-v3-security.log`.
- Тот же branch/worktree; base/HEAD неизменны. Индекс пуст (`git diff --cached` пуст), изменения не staged и не committed. Статус: DEV v3 READY; STOP до fresh independent review. Никакого reviewer, staging, commit, push, PR, queue или других чатов не запускалось.
