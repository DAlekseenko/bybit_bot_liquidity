# Issue #440 — класс B; developer `/root/paper440_developer`
Ветка `feat/440-owner-session-registration`, worktree `/private/tmp/bybit-issue-440`, base `8f97010c67ff86b76e4b4e2d42bb29e717534e04`.
Источник M2: PR #406 head `073152937619fef7ea89244dba9f181dfd26077c`, owner.go blob `8a00b230d80c03eb9b2b73509d506da4dbaf6c14`. История исходного commit сохранена.
Изменены только `internal/momentum/storage/owner_session.go`, `internal/momentum/storage/owner_session_test.go` и этот handoff; 107 production / 230 test physical lines.
Регистрация проверяет scoped exclusive lock на dedicated conn, отклоняет активную caller-TX, атомарно закрывает stale history и добавляет монотонную session; результат не разрешает исполнение.
PASS: targeted actual PostgreSQL 17.11 (lock negatives, history/reopen/skew, rollback/cancel/duplicate, commit failure/disconnect), полный actual-PG `go test ./...`, gofmt, vet, build, lint, govulncheck, coverage 74.5% без PG.
PASS: полный actual-PG `go test -race ./...`; собственные disposable fixtures удалены по точным ID после проверки labels. Логи: `/private/tmp/issue440-logs/`.
Блокеров нет; далее независимый reviewer и orchestrator. Stage/commit/push/PR не выполнялись; parent #84 остаётся WAIT, миграции/owner.go/scheduler не изменялись.
