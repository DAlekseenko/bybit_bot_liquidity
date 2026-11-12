# Handoff — Issue #251

- Контракт: [Issue #251](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/251), marker `codex-go12611-security-prerequisite:v1`.
- Checkpoint ID: `issue-251-c1`.
- Чат задачи: `01a1d099-fbc0-7973-9fca-d1caf923208c`, сверено через Codex thread lookup; заголовок «Issue #251 — обновление Go до 1.26.11».
- Владелец: `/root/issue251_developer`, профиль `developer-middle` (`gpt-6-luna`, medium).
- Worktree: `/Users/alekseenkodenis/.codex/worktrees/issue251-go-12611-security/bybit_bot_liquidity`.
- База: `9205117947474e1b483cb0747731d5ab1ba49023`.
- Ветка: `codex/251-go-12611-security`.
- Изменения: `go.mod` обновляет только toolchain с Go 1.26.10 до 1.26.11; `Dockerfile` обновляет только build image до официального `golang:1.26.11-alpine`. Минимальная директива `go 1.24.13` и остальные файлы не менялись.
- Проверенный инструмент: `go version go1.26.11 darwin/arm64`; источник бинарника `/private/tmp/issue249-go12611/bin/go`.

## Проверки

- `test -z "$(gofmt -l cmd internal)"` — PASS.
- `go test ./...` — PASS.
- Обычный `go test -coverprofile=coverage.out ./...` — PASS, общее покрытие 79.0% при пороге CI 70%. Это значение исключает opt-in PostgreSQL интеграционные тесты.
- `govulncheck ./...` с govulncheck 1.1.4 — exit 0. Найдено 0 вызываемых уязвимостей и одна module-only advisory, которую код не вызывает.
- `golangci-lint run` с golangci-lint 2.10.1 — PASS, 0 issues.
- `go test -race ./...` — PASS.
- `go vet ./...` — PASS.
- `go build ./...` — PASS.
- PostgreSQL 17.11 запущен из `postgres:17.11@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, подтверждённый image ID совпал. Данные контейнера размещались в tmpfs 1 GiB; порт опубликован только на `127.0.0.1`; использован synthetic test account и DSN. Прямой `pq` smoke прошёл и сообщил `server_version_num=170011`. Полный opt-in `MOMENTUM_TEST_POSTGRES_DSN=… go test -race -count=1 -v ./internal/momentum/...` прошёл отдельно, все тесты завершились PASS без skips. Контейнер остановлен и удалён после проверки.
- `docker manifest inspect docker.io/library/golang:1.26.11-alpine` — PASS, Docker Hub вернул OCI index для официального образа.
- `git diff --check` — PASS. Whitespace нового untracked handoff отдельно проверен после создания — PASS.

Все Go-проверки выполнены в окружении с `GOCACHE` и линтер-кэшем в `/private/tmp/issue251-checks`; результаты сохранены там. Первые попытки в ограниченном окружении упёрлись в недоступные глобальный Go cache, loopback и vulnerability DB. Повтор с разрешёнными ресурсами завершился PASS без пропуска проверок.

## Передача

Рабочая область должна содержать ровно три согласованных файла: `go.mod`, `Dockerfile` и `.ai/handoffs/issue-251.md`. Изменения не закоммичены и не опубликованы. Следующий шаг — независимое ревью итоговых файлов и их hashes. После GREEN публикацию выполняет developer owner по отдельному разрешению.

Предыдущий отчёт review сохранён байт-в-байт до исправления handoff в `/private/tmp/issue251-review-final.before-checkpoint-fix.json`, SHA256 `16acd8a660cb04b9bfbfbd5589d500b89f1a082030aa63d9d66a8f6d784c7083`. Предыдущий отчёт DEVREADY также сохранён в `/private/tmp/issue251-ready.pre-checkpoint-fix.json`, SHA256 `0e3426370b376418ba6d7dab7f6646b25f4f6fd206a85553d2174b5631caa5b2`.
