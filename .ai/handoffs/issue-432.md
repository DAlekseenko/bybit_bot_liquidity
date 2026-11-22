# Issue #432 — класс A, READY для независимого review
Ветка `feat/432-paper-public-source`, checkout `/private/tmp/bybit-issue-432`, base `72dc3410aa2a66df8419d7adbbc26ea3c42fecf1`.
Реализованы публичный Testnet reader, общий D loader/readiness, pure validator, backward pagination, explicit clocks, funding preflight и точные JSON/HTTP byte caps; торговых вызовов нет.
Изменены только `internal/momentum/paper/source.go`, `source_validate.go`, `source_limits.go`, соответствующие три `_test.go` и этот `.ai/handoffs/issue-432.md`.
Проверки: gofmt, targeted, all, race, vet, build, lint v2.10.1, govulncheck, coverage 76.1% и whitespace PASS; логи `/private/tmp/issue432-*.log`.
Production 736/750 normal-gofmt lines; tests 675/750. PG opt-in и реальные Testnet contract tests отключены; source покрыт fake HTTP.
Сохранён draft до exact-limits: `/private/tmp/issue432-before-exact-limits/`; проверен exact ASCII/escaped16MiB boundary и независимый bounded JSON fixture.
Blockers отсутствуют; следующий шаг — независимое review root. Stage/commit/push/PR не выполнялись; текущий Config допускает3symbols, source не вводит этот default.
