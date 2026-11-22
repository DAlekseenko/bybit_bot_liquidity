# Issue #462 — схема paper inputs, класс B
Ветка: `feat/462-paper-input-schema`; база: `970df28ad0520b01773927ecae8f45dde3373fc0`.
Реализована только additive M15 `paper_run_inputs`: exact bytes/SHA, scope/sequence, predecessor FK и immutable triggers.
Пути: `internal/momentum/storage/paper_input_schema.go`, `paper_input_schema_test.go`, `migrations.go`, `.ai/handoffs/issue-462.md`.
Raw SQL tests самостоятельны; будущий repository #458 не используется; M1–14 не изменены.
Targeted/full PostgreSQL 17.11, coverage 91,7%, отдельный full race, go test/vet/build, lint/security, gofmt/diff-check прошли; бюджет 35 production / 215 test lines / 4 пути.
Логи, команды и первоначальные ошибки: `/private/tmp/issue462-logs`; fixtures удаляются по own container ID/token с readback отсутствия.
STOP_READY: orchestrator подтвердил actual master `970df28ad0520b01773927ecae8f45dde3373fc0`; #461 не является зависимостью. Следующий шаг: независимый review. Commit/push/PR не выполнялись.
