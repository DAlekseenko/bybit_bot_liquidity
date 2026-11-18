# Issue #285 handoff — issue-285-c2

<!-- codex-cm013-funding-read:v1 -->

## Текущая контрольная точка

- `issue-285-c1` — завершённый исторический checkpoint READY на исходной базе `e5cdf74b5d808f4b554149f04a2480c0b076a497`, передан координатору через `/private/tmp/issue285-ready.json` с сохранённым SHA256 `8cc7025ce6c981ca9c034656122b25af8a1ee24b277235295398219b64c5c091`.
- `issue-285-c2` — текущий ACTIVE checkpoint на финальную подготовку к review. Это continuation того же implementation task и review-fix checkpoint, без смены ID.
- Issue/contract: [#285](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/285), marker `codex-cm013-funding-read:v1`.
- Implementation task/chat: `01a1e6b7-24ed-7bd2-aaa9-b2ae1ea0cff9` (`local`). Sole developer owner: `/root/issue285_developer`, native role `developer-middle`.
- Coordinator grant: G9 `ACTIVE_SINGLE_SLOT`, revision 281, verified comment `6668604794`.
- Worktree `/private/tmp/bybit-issue-285`; branch `codex/285-funding-history-read`; initial base `e5cdf74b5d808f4b554149f04a2480c0b076a497`; current fast-forward base/HEAD `ab627a03197642fa6cc6dc4432ce0662db899e54`.
- Exact Issue paths are `internal/momentum/storage/funding_read.go`, `internal/momentum/storage/funding_read_test.go`, and `.ai/handoffs/issue-285.md`.
- Current checks pass on these Go blobs: six targeted PostgreSQL 17.11 race tests, zero skips, verified cleanup; full tests and race; exact ordinary coverage 2576/3674 (70.11431682%); vet, build, gofmt, golangci-lint v2.10.1, and govulncheck v1.1.4. Evidence logs are in `/private/tmp/issue285-final-evidence/`.
- Current blocker is independent reviewer HOLD on handoff metadata. This correction adds the canonical task/chat, owner/role, checkpoint IDs, and current state. Next step is read-only review of refreshed candidate `/private/tmp/issue285-final-candidate-v2.json`.
- Index is empty; the three exact paths remain unstaged. No commit, push, or PR has been made.

Historical checkpoint text below is retained in chronological order. The initial READY record is `issue-285-c1`; ACTIVE integration, final verification, and the current review correction belong to `issue-285-c2`.

# Issue #285 — чтение funding диапазона

## Исходное состояние

- Base: `e5cdf74b5d808f4b554149f04a2480c0b076a497`.
- Branch: `codex/285-funding-history-read`.
- Изолированная копия: `/private/tmp/bybit-issue-285`.
- Issue marker: `codex-cm013-funding-read:v1`.

## Реализация

`ReadFundingRange` читает один `FundingStream` в собственной транзакции PostgreSQL `REPEATABLE READ READ ONLY`. Проверяются provenance `bybit_v5/linear`, включительный диапазон UTC миллисекунд, точные decimal ставки и лимит до 200 событий. Все отказы возвращают nil slice и безопасный `ErrFunding`. Пустой результат отражает только локальный SQL snapshot и не заявляет полноту или coverage.

Изменены ровно три зарезервированных файла:

- `internal/momentum/storage/funding_read.go` — 84 строки.
- `internal/momentum/storage/funding_read_test.go` — 266 строк.
- `.ai/handoffs/issue-285.md`.

## Проверки

- Целевые `TestReadFundingRange` race-тесты: 6 PASS, 0 SKIP на PostgreSQL `17.11 (Debian 17.11-1.pgdg13+2)`, `server_version_num=170011`, Go 1.26.11. Использовался pinned image `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`. Loopback порт был случайным, данные находились во временном tmpfs. Удаление контейнера проверено.
- `go test ./...`: PASS.
- `go test -coverprofile`: PASS, общий coverage 70.2% при пороге 70%.
- `go test -race ./...`: PASS.
- `go vet ./...`: PASS.
- `go build ./...`: PASS.
- gofmt check: PASS.
- golangci-lint v2.10.1: PASS, 0 issues.
- govulncheck v1.1.4: PASS. Официальная vulnerability database проверена при разрешённом доступе к `vuln.go.dev`; 0 уязвимостей в достижимом коде. Одна уязвимость присутствует в требуемом модуле, но не затрагивает вызываемый код.

Полный READY manifest с SHA256 final blobs находится вне checkout в `/private/tmp/issue285-ready.json`.

## Границы и состояние передачи

Нет изменений схемы, конфигурации, API, импорта, pagination/replay, coverage metadata, торговли, deploy, credentials или risk. Все три файла unstaged, Git index пуст. Не выполнять stage, commit, push, PR, публикацию или изменения очереди до отдельного разрешения ACTIVE. Остаётся независимая проверка точных blobs.

## ACTIVE checkpoint — fast-forward and final candidate

По разрешению G9 ревизии 281 и `ACTIVE_SINGLE_SLOT` продолжена та же ветка на подтверждённом master `ab627a03197642fa6cc6dc4432ce0662db899e54`. Fast-forward от READY base `e5cdf74b5d808f4b554149f04a2480c0b076a497` прошёл успешно. Между базой и новым master находятся четыре ancestor commits; их изменённые пути не пересекаются с тремя файлами Issue #285. Для этого master координатор подтвердил CI 2/2. Содержимое исходного READY manifest `/private/tmp/issue285-ready.json` сохранено без изменений.

После fast-forward необходимо повторно выполнить целевой PostgreSQL 17.11 race-набор без пропусков и cleanup, а также полный test, race, coverage, lint, security, vet, build и formatting gates. Результаты и SHA256/Git blob hashes кандидата фиксируются отдельно в `/private/tmp/issue285-final-candidate.json`. Остановка на финальном кандидате обязательна до независимого review. Не stage, commit, push или PR до verdict reviewer.

## Final candidate verification

Все обязательные повторные проверки выполнены на fast-forward master `ab627a03197642fa6cc6dc4432ce0662db899e54`:

- Целевые PostgreSQL 17.11 race-тесты: 6 PASS, 0 SKIP; `server_version_num=170011`; pinned disposable container удалён и его отсутствие проверено.
- `go test ./...`, coverage, `go test -race ./...`, `go vet ./...`, `go build ./...`, gofmt, golangci-lint v2.10.1 и govulncheck v1.1.4: PASS. Coverage 70.0% при минимуме 70%. govulncheck сообщил 0 уязвимостей в достижимом коде; одна уязвимость в обязательном модуле не вызывается кодом.
- Логи проверок сохранены вне checkout в `/private/tmp/issue285-final-evidence/`.
- Исходный `/private/tmp/issue285-ready.json` имеет SHA256 `8cc7025ce6c981ca9c034656122b25af8a1ee24b277235295398219b64c5c091` и сохранён без изменений.

Финальные SHA256, Git blob hashes, diff base и состояние index записаны в `/private/tmp/issue285-final-candidate.json`. Кандидат остановлен для независимого review. Index пуст, все три точных пути unstaged; commit и публикация не выполнялись.

## Coverage threshold checkpoint

После предыдущей итоговой проверки координатор измерил точную долю обычного coverage как `2571/3674 = 69.97822537%`. Отчёт CI округлялся до 70.0%, поэтому добавлена отдельная pure test `TestFundingReadCanceledContextWithoutPool`. Проверка pre-canceled context и nil pool сохранена, теперь она также исполняется без PostgreSQL в обычном тестовом наборе. PostgreSQL целевые случаи и их отдельный шеститестовый запуск не менялись. Файл тестов теперь 273 строки, остаётся в лимите 300.

После этого изменения полный Go набор повторно прошёл, точное coverage превышает 70% (подробное отношение сохранено в final candidate evidence), а PostgreSQL 17.11 race-набор повторно прошёл 6/6 без пропусков с подтверждённым удалением контейнера. Линтер и govulncheck также повторно прошли. Эти проверки относятся к актуальным финальным blobs.
