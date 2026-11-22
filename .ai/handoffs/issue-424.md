# Issue #424 — paper operator commands, класс B
- Ветка `feat/424-paper-operator-state`, база `38146798010dba0332e18c6f5a6787323137f0cb`, checkout `/private/tmp/bybit-issue-424`.
- Owner `/root/paper424_developer`; реализованы подтверждённые PAUSE_ENTRIES/HALT/RESUME, CAS и исторический exact replay с исходным audit/time.
- Изменены только `internal/momentum/storage/paper_commands.go`, новый `internal/momentum/storage/paper_operator_test.go` и этот handoff; production diff 28+/16−=44 строки, тест целиком 284 строки.
- Сохранены INIT/read, canonical config binding, legacy JSON classifier, SRX fence и callback после insert/readback до COMMIT; exact replay callback не вызывает.
- Targeted actual PostgreSQL PASS; полный `go test -coverprofile=… ./...` PASS (91.8%, storage 92.2%); полный `go test -race ./...` PASS на втором свежем PostgreSQL 17.11.
- Gofmt/diffcheck, `go vet ./...`, `go build ./...`, golangci-lint 2.10.1 (0 issues), govulncheck (0 vulnerabilities) PASS; Go 1.26.11, совместимые с Go 1.24 исходники.
- Проверены четыре operator↔guard/risk порядка и два опубликованных guard↔risk regression; published `paper_commands_test.go` не менялся; rollback/lost response, reopen, duplicate/CAS и повреждения fail-closed.
- Логи `/private/tmp/issue424-{targeted,full,race,lint,vuln}.log`; fresh PG receipts `/private/tmp/issue424-{coverage,race}-pg-*`, обе собственные fixture удалены после проверки ID/labels и диагностики.
- Ограничения: RecoveryID — структурная ссылка, не fresh recovery proof; close-all — запрос HALTED, не CLOSED; CLI/fill/finalizer остаются #87/#92. Следующий шаг: независимый review, затем публикация G11 и CI; commit/stage/push не выполнялись.
