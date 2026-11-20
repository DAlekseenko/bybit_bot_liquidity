# Issue #371 — READY_FOR_INTEGRATION

- Контракт: [Issue #371](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/371), marker `codex-cm023a-replay-bundle-publication:v1`; parent #77.
- Native task: `01a1f887-d09b-7a90-8953-e481d7d48c40`, host `local`; developer `/root/issue371_developer`, role `developer-middle/medium`; publication owner `/root/issue371_developer`.
- Checkout `/private/tmp/bybit-issue-371`; branch `codex/371-offline-replay-bundle-publication`; base `0c6c66892502aa2eac676abe6f1c4848c00af71a`, tree `6c3eefe475bcace28bb004d3fb8d7e0c8ad98d7e`. Исходная точная база и master AGENTS/.ai/INTEGRATION проверены из read-only mirror; origin checkout — trusted GitHub URL.
- Изменены только `internal/momentum/backtest/replay_publish.go` (131 physical lines), `internal/momentum/backtest/replay_publish_test.go` (300), этот handoff. Лимиты соблюдены: ≤250 production, ≤300 test, 3 paths.

## Реализация

Добавлен opaque `ReplayBundle` с private map и `WriteReplayBundle`. Writer принимает только точные пять непустых payloads и канонические SHA256SUMS, затем публикует проверенный набор из шести файлов через приватный lock, sibling staging directory, readback и same-parent rename. Существующий каталог принимается лишь при точном совпадении обычных файлов; чужой lock, ссылки, дополнительные/частичные/конфликтующие файлы и ошибки не перезаписываются. Контекстные ошибки сохраняют `errors.Is`; операция остаётся локальной файловой записью без построения replay result или доступа к сети/БД.

Тесты используют синтетические bytes только для disk boundary. Проверяются канонический набор, повтор/idempotency, конфликтующий результат, ошибки входа и пути, ссылки, чужой lock, сохранность непустого partial output и каталога с дополнительным файлом, конкурентные совместимый и несовместимый writers, отмена во время записи и после readback, expired deadline и поздний filesystem failure перед публикацией. Никакой builder или финансовая семантика не добавлялись.

## Проверки

Все логи ниже относятся к финальным Go-файлам и находятся в `/private/tmp/`:

- Targeted race ×3: PASS, exit 0 — `/private/tmp/issue371-v2-targeted-race.log`.
- `go test ./...`: PASS, exit 0 — `/private/tmp/issue371-v2-full-test.log`.
- `go test -race ./...`: PASS, exit 0 — `/private/tmp/issue371-v2-full-race.log`.
- `go vet ./...`: PASS, exit 0 — `/private/tmp/issue371-v2-vet.log`.
- `go build ./...`: PASS, exit 0 — `/private/tmp/issue371-v2-build.log`.
- golangci-lint 2.10.1, `/private/tmp/golangci-lint-2.10.1-darwin-arm64/golangci-lint` (SHA256 `5fd0b6a09353eb0101d3ae81d5e3cf4707b77210c66fb92ae152d7280d959419`, version/hash evidence `/private/tmp/issue371-v2-lint-version.log`): PASS, exit 0, 0 issues — `/private/tmp/issue371-v2-lint.log`.
- Offline govulncheck: PASS, exit 0 — `/private/tmp/issue371-v2-vuln.log`; reachable vulnerabilities: 0; один требуемый модуль помечен scanner как не вызываемый кодом.
- Обычное покрытие `go test -coverprofile=/private/tmp/issue371-v2-coverage.out ./...`: PASS, exit 0 — `/private/tmp/issue371-v2-coverage.log`; exact statement numerator/denominator каждого пакета указан в v2 READY JSON.
- Полный race и обычные тесты потребовали одобренного local bind `127.0.0.1:0` из существующего `internal/momentum/storage/TestOpenPostgresFailures`; одобренный escalation прошёл. PostgreSQL DSN отсутствовал. Отдельный scoped PostgreSQL coverage не запускался: файловый publisher не использует storage; существующий storage test в full suite использует только локальный fake TCP listener, не PostgreSQL.
- `gofmt -l cmd internal`: PASS, exit 0 — `/private/tmp/issue371-gofmt.log`; `git diff --no-index --check` для всех трёх новых файлов: whitespace ошибок нет — `/private/tmp/issue371-diff-check.log` (git возвращает ожидаемый status 1 на наличии diff). Повторный readback GitHub подтвердил Issue URL/contract marker; status содержит только три approved paths плюс их handoff.

## Статус и передача

READY_FOR_INTEGRATION, blocker отсутствует после двух требуемых RED test fixes; финальная очередь G10 предоставила ACTIVE_SINGLE_SLOT #371 revision 447. Текущий v2 кандидат замораживается для нового независимого ревью. Merge с новой базы, staging, commit, push, PR и публикация не выполнялись. Builder, CLI, фактический исторический replay и Stage 5 не входят в этот Issue. Следующий шаг: G10 перечитывает `/private/tmp/issue371-v2-ready.json` и проводит независимое ревью.
