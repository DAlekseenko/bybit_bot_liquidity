# Issue #84 — CM-032, класс B, READY к независимому review
- Task/owner: `/root/paper84_integration`, единственный G11 integration developer; исходная история M2 сохранена.
- Worktree/branch: `/private/tmp/bybit-issue-84`, `feat/84-execution-owner`; существующий PR #406, новых artifacts нет.
- База: `e2483bcf09573c134f9d0fd454b1cb0a9c26967a`; pending ordinary merge parents: `073152937619fef7ea89244dba9f181dfd26077c` и база, без commit/push.
- Own paths: `internal/momentum/storage/owner.go` (216 строк), `internal/momentum/storage/owner_test.go` (185 строк), `.ai/handoffs/issue-84.md`; incoming master сохранён.
- Регистрация делегирована #440 на том же conn; `lastSeen=callerNow`, lock lifecycle и in-flight ограничения сохранены; MarkReconciled — только локальный latch, proof обязан проверить caller.
- Все 6 исходных тестов/164 строки сохранены побайтно; новый nil Release проверяет ErrOwnerArgument, историю/last_seen_at, Done, lock и действующий permit; canceled nonnil поведение сохранено.
- PASS: targeted 7 тестов, полный actual PostgreSQL `go test -count=1 -v -coverprofile=... ./...` (91,6%; storage 91,1%), отдельный fresh PostgreSQL `go test -race -count=1 -v ./...`, vet/build/lint/gofmt/whitespace; Go 1.26.11 local, Go 1.24 совместимость сохранена.
- PASS govulncheck с локальной публичной snapshot DB; online freshness и ручные биржевые contract tests не проверялись. Raw commands/stdout/stderr: `/private/tmp/issue84-integration-logs`; два собственных PostgreSQL fixtures удалены, отсутствие проверено. После PG-проходов добавлена только поясняющая строка nolint для нормативного nil-теста; final lint PASS.
- Blocker: локальных нет; следующий шаг — независимый review и санкционированная обычная публикация в существующий PR #406, затем official Codex review/CI нового head у координатора. Реальных биржевых вызовов/новых transport нет.
