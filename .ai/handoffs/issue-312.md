# Issue #312 — mark-price content

- Контракт: https://github.com/DAlekseenko/bybit_bot_liquidity/issues/312,
  `codex-cm015-mark-price-content:v1`; checkpoint `issue-312-c1`, READY до интеграции.
- Маршрут: готовый узкий контракт, один native developer owner
  `/root/issue312_developer`; chat `01a1f533-bec0-7642-a50f-3e8291b68460`.
- Копия: `/private/tmp/bybit-issue-312`, независимые `.git` и index;
  ветка `codex/312-mark-price-content`.
- Base: `55c157919fe889448a0e61478d2e1191061e96a3`, fetched origin/master
  при DEV. Очередь #55 comment 6668604794 rev320 подтверждает три reservations.

Ровно три новых пути:

- `internal/momentum/data/mark_price_content.go` — pure codec, 151 строка.
- `internal/momentum/data/mark_price_content_test.go` — synthetic tests, 286 строк.
- `.ai/handoffs/issue-312.md` — единственный handoff этой задачи.

Кодек проверяет identity, точный endpoint, UTC-grid D/60, окно и Confirmed,
точные положительные OHLC через WireDecimal/ParsePrice, ценовые границы и
дубликаты. Сортирует отдельную проекцию, возвращает compact JSON и SHA-256;
ошибка не раскрывает вход и не возвращает частичный payload. Гaps сохраняются,
nil/empty дают `candles:[]`. SQL, HTTP, runtime и торговля не добавлены.
Caller отвечает за provenance, состав и согласованный snapshot; digest сам
по себе не доказывает coverage, sealed membership или freeze/replay readiness.

Design `/private/tmp/g10-mark-content-design.vdtsmjev` проверен по контракту
и текущим model types. Независимый Python json.dumps дал 482-byte golden
и SHA-256 `4288d7d241f411b9b70fb0c18846a364f571ba3254953816eabdd6dc8fd23121`.
Добавлены endpoint variants, numeric-error input immutability, JSON escaping
и набор из 1001 свечи без page cap. Все 269 baseline blobs сохранены.

Проверки на Go 1.26.11, raw evidence `/private/tmp/issue312-gates/`:

- Targeted race `go test -race -count=1 -v ./internal/momentum/data -run
  TestEncodeMarkPriceContent` — PASS.
- Full ordinary `go test -count=1 -coverprofile=... ./...`, DSN unset — PASS;
  coverage 2781/3907 = 71.1799334528%, выше порога 70%.
- Full `go test -race -count=1 ./...`, DSN unset — PASS.
- `go vet ./...`, `go build ./...`, golangci-lint v2.10.1 — PASS, 0 lint issues.
- govulncheck — PASS, 0 reachable/imported vulnerabilities; одна module-only
  vulnerability, которую код не вызывает.
- gofmt, whitespace новых файлов, exact-path/budget/base-blob audit — PASS.

Первые sandbox full test/race запрещали localhost listener существующего
TestOpenPostgresFailures; повтор с разрешённым localhost прошёл. Raw failure
логи сохранены отдельно. GOCACHE и lint cache вынесены в `/private/tmp`.
Реальная PostgreSQL integration не запускалась: изменение pure, SQL границ нет;
ordinary suite запускался без MOMENTUM_TEST_POSTGRES_DSN. Бот не запускался.
GitHub CI и независимое exact-blob review ожидают ACTIVE; это не GREEN.

Следующий шаг: STOP на READY, без staging/commit/push/PR и без изменения очереди.
После единственного ACTIVE от G10: refreshed base/no-op или обычный merge,
необходимые проверки, handoff и независимый reviewer; публикация только после
GREEN. Рабочая копия пользователя сохранена. Blocker: ожидание integration slot.
READY-манифест и хеши evidence: `/private/tmp/issue312-ready.json`.

## issue-312-c2 — актуальная передача после ACTIVE

Предыдущий checkpoint c1 выше сохранён целиком как история. Его точные READY,
три файла и raw evidence сохранены с проверкой исходных SHA256 в
`/private/tmp/issue312-active-preserved-ready/`; исходный READY не переписан.

G10 выдал sole ACTIVE; canonical #55 comment 6668604794 rev355 проверен целиком.
Owner, chat, branch, копия и три reservations прежние. Refreshed origin/master
`4897b35a62f28c74e69d897ed20a013bd59571c7` принят обычным fast-forward без
конфликтов; оба Go-файла побайтно равны c1, все исходные acceptance cases сохранены.
Все 294 tracked blobs новой базы неизменны; diff содержит только три own paths.

На новой базе Go1.26.11 повторно PASS: targeted race, full unit `-count=1`,
full race `-count=1`, ordinary coverage (DSN unset) 3015/4261 = 70.758038019244%,
vet/build, whole cmd/internal gofmt, new-file diff/whitespace, lint v2.10.1
(0 issues), govulncheck (0 reachable/imported, 1 module-only vulnerability).
Raw logs, статусы с exit code и профиль: `/private/tmp/issue312-active-gates/`;
финальные hashes/evidence: `/private/tmp/issue312-active-ready.json`.
PG integration не запускалась: pure codec без SQL; synthetic tests не требуют
БД. Runtime, бот, trading, deploy и реальное Testnet исполнение не запускались.

Следующий шаг c2: свежий независимый exact-file review этих трёх blobs;
только после GREEN тот же owner публикует обычный commit/push и PR к master
с `Refs #312`, сверяет URL/base/head/files и STOP. CI/merge/checkpoint/closure
принадлежат G10. PR/head/CI до review неизвестны. Основной checkout не менялся.
