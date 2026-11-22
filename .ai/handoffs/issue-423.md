# Issue #423 — схема курсора mark price
- Ветка `feat/423-mark-import-schema`, актуальная база `3eac31ce5fe4062be4089ca67eb4c835764280df`; полные PG проверки запускались на предыдущей базе `f648f864`.
- Класс B; добавлена только аддитивная PostgreSQL миграция v13 для курсора импорта mark price.
- Файл схемы скопирован побайтно из PR #402, blob `55404edb86150acbe117848dcbfaf35e8b882d3a`.
- Целевые реальные PG тесты, полный `go test ./... -coverprofile` (92.0%) и полный `go test -race ./...` на отдельных свежих PG прошли; полный `go test ./...` без PG также прошёл.
- PASS: `gofmt`, `go vet ./...`, `go build ./...`, golangci-lint, govulncheck (0 issues); lint выдал лишь cache permission warnings.
- Изменены только схема, каталог миграций, новые тесты и этот handoff; тесты — 154 строки, production diff — 43 строки.
- Одноразовые PG серверы удалены после проверок; полные PG проверки были на `f648f864`; fast-forward до `3eac31ce` сохранил blob hashes всех 4 файлов.
- Исторический импорт и coordinator здесь не реализуются; PR CI должен подтвердить свежую базу.
- Следующий шаг: независимый review; публикацию выполняет orchestrator после GREEN и grant.
