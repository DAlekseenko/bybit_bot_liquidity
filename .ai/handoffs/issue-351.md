# Issue #351 — DEV checkpoint `issue-351-c1`

## Контракт и разрешённая область

- Exact Issue: [#351](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/351), marker `codex-cm015-history-capture:v1`; parent #70. Реализован capture одного caller-owned repeatable-read read-only снимка trade daily/hourly, mark daily/hourly и funding для трёх явно заданных символов. Существующие storage readers выполняют source/provenance/mode validation; capture не владеет транзакцией и не меняет reader API.
- DEV grant подтверждён live G10 queue #55 comment `6668604794`, revision `391`; integration slot `none`. Owner `/root/issue351_developer`, роль `developer-middle` medium, native task/chat `01a1f744-e582-7ee1-ad31-5b8fbfcb78a4`, coordinator `01a1ee9e-f0d5-71c2-ba96-ceadad8f8f5f`.
- Изолированная копия `/private/tmp/bybit-issue-351`, branch `codex/351-history-capture`, base `625e16910b9cb40055e3abf57c9b5b8e5ef9121e`, tree `0dcd7f0fc3f315a18e71b98b6a289762b5dc84cc`.
- Разрешены ровно три пути: `internal/momentum/history/capture.go`, `internal/momentum/history/capture_test.go`, этот handoff. Индекс пуст. Production 98 строк; test 293 физических строки (cap 300).

## Реализация и покрытие контракта

`CaptureHistoryTx` валидирует аргументы и канонизирует ровно три символа, читает только существующими Tx readers короткими окнами до 200 рядов, возвращает новые per-symbol slices с исходными значениями и при любой ошибке отдаёт nil result с wrapped safe sentinel. Caller сохраняет владение Tx на всех путях. Funding берётся inclusive до `E-1ms`; никаких cadence, interpolation, частичного результата или network/source calls нет.

Сохранены все portable A–D сценарии Issue: pure 45-call loop, exact arguments/chunking, все 45 error positions и cancellation; реальный RR snapshot между двумя соединениями и boundary/error cases; синтетические 7,155 rows с точными исходными значениями; тесты не читают внешние fixtures, `/private/tmp`, сеть или часы для membership. Lint потребовал только локализовать `step` внутрь ветки candle readers: убраны неиспользуемые funding-инициализация и значение по умолчанию, вычисление во всех candle cases осталось прежним. Других тестовых сценариев не удалено.

## Проверки

Использован Go `go1.26.11 darwin/arm64`, DSN unset для полного обычного набора. Финальные проверки завершились успешно:

- Targeted `go test -race -count=1 -json -run '^TestCaptureHistory' ./internal/momentum/history`: `TestCaptureHistoryLoop`, `TestCaptureHistorySnapshot`, `TestCaptureHistoryScale` PASS, 0 skip/fail на PostgreSQL 17.11 (`server_version_num=170011`). Loopback-only контейнер создан с уникальной меткой для этого запуска, затем удалён; точное отсутствие контейнера и attached volumes подтверждено.
- `go test -count=1 ./...`, полный `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...`, `test -z "$(gofmt -l cmd internal)"`, `git diff --check`, `golangci-lint v2.10.1 run ./...`: PASS.
- Ordinary coverage (DSN unset): `3454/4834 = 71.452213487795%`.
- Offline pinned govulncheck binary `/private/tmp/issue334-active-govulncheck`, SHA256 `f145cbc7889e38906101aa41b1097f9f820c3a64a8d862c9099361fa0c65f3b2`; ran with `-db file:///private/tmp/g10-public-vulndb.Q6sE6qf4/db`, `GOPROXY=off`, `GOSUMDB=off`; PASS, 0 code-reachable/imported findings and 1 module-only advisory. No network lookup or dependency update.
- Full command logs, exit codes, targeted PostgreSQL report and exact hashes are catalogued in `/private/tmp/issue351-ready.json`; evidence directory is `/private/tmp/issue351-evidence/`.

## Итоговые blobs

Final SHA256 and Git blob IDs are recorded in the external manifest. The two Go files are unstaged. No files outside the three approved paths were changed. No commit, push, PR, review request or queue write was performed.

## Следующий шаг

`READY_FOR_INTEGRATION` — остановиться и передать G10. Независимое ревью, слот интеграции и публикационные действия остаются за последующей отдельной координацией. Saved-input/freeze acceptance остаётся ответственностью G10 после merge; этот DEV checkpoint не утверждает freeze/replay admission.
