# Issue #464 — класс B, подтверждённое завершение backend ClockSkew, READY
- Owner/task: sole G11 developer `/root/paper84_integration`; worktree `/private/tmp/bybit-issue-464`, branch `test/464-owner-clockskew-termination`, base `970df28ad0520b01773927ecae8f45dde3373fc0`.
- Scope: только `internal/momentum/storage/owner_test.go` (188 строк, diff +4/-1) и `.ai/handoffs/issue-464.md`; production diff 0, Issue #84 и её artifacts не изменялись.
- В ClockSkew scalar проверяет `pg_terminate_backend(pid,5000)=true`, затем отсутствие advisory lock и backend в `pg_locks`/`pg_stat_activity` до прежнего Acquire; это тестовый timeout, не runtime default.
- Все исходные test functions, прочие statements/assertions сохранены; отсутствуют retry ErrOwnerBusy, blind sleep, новые helper/dependency/runtime/broker вызовы.
- PASS actual PostgreSQL 17.11: ClockSkew `go test -race -count=30 -v ...` — 30/30 без SKIP, 11,168 с; все owner/producer/ownership suites под race — PASS, 4,068 с.
- PASS на отдельных свежих PostgreSQL: полный `go test -count=1 -v -coverprofile=... ./...` — 91,7% total / 91,1% storage; полный `go test -race -count=1 -v ./...`; vet/build/lint/gofmt/whitespace.
- PASS govulncheck с локальной публичной snapshot DB; online freshness и ручные биржевые contract tests не проверялись. Go 1.26.11 local, совместимость Go 1.24 сохранена.
- Raw commands/stdout/stderr и hashmanifest: `/private/tmp/issue464-logs`; три собственных PostgreSQL fixtures удалены с проверкой отсутствия. Negative protocol proof и исходная ошибка diagnostic-finalizer сохранены в `/private/tmp/issue84-clockskew-diagnosis`; естественный старый CI flake локально не заявляется воспроизведённым.
- Blocker: локальных нет; следующий шаг — независимый review и разрешённая обычная публикация отдельного PR с `Closes #464`, затем official Codex review/CI текущего head у координатора. Commit/push/PR не выполнялись.
