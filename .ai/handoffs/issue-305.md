# Передача работы по Issue #305

Контракт: [Issue #305](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/305), marker `codex-cm012-common-candle-warmup:v1`.

## Изменённые файлы

- `internal/momentum/history/common_candle_warmup.go` — согласованное чтение трёх дневных warmup окон через `ReadCandleWarmup`; единый точный cutoff; все статусы; окна выдаются только при полной готовности.
- `internal/momentum/history/common_candle_warmup_test.go` — проверки universe, статусов, ошибок, namespace/cutoff, отсутствия частичных результатов и PostgreSQL.
- `.ai/handoffs/issue-305.md` — этот handoff.

База — `master` `55c157919fe889448a0e61478d2e1191061e96a3`, где присутствует проверенная Go-пара #298. Ветка `codex/305-common-candle-warmup`, отдельная копия `/private/tmp/bybit-issue-305`. Изменено 77 production и 192 тестовые строки; контрактные лимиты соблюдены.

## Проверки

- Целевой race #305: `go test -race -count=1 -v ./internal/momentum/history -run '^TestReadCommonCandleWarmup'` — PASS, все три теста, включая PostgreSQL integration; 0 skips.
- Полный `go test -race -count=1 -v ./internal/momentum/history` с disposable PostgreSQL — PASS; PostgreSQL `17.11`, `server_version_num=170011`, образ `postgres:17.11`, digest `ff87138949ad9a46f32ff23bd6852b52ce024a13dffe9b4087be98292cdd3b4f`; 0 skips. Disposable контейнер остановлен и удалён, отсутствие проверено.
- `go test ./...` с PostgreSQL DSN unset — PASS.
- Обычный `go test -coverprofile=... ./...` с DSN unset — PASS: 2766/3891 = 71.087124133%, порог CI 70% пройден.
- `go test -race ./...` с DSN unset — PASS.
- `go vet ./...`, `go build ./...`, gofmt, `git diff --check` — PASS.
- golangci-lint v2.10.1 — PASS, 0 issues.
- govulncheck v1.1.4 — PASS: 0 достижимых уязвимостей; одна уязвимость в требуемом модуле не вызывается кодом.

Расширенный, не требуемый контрактом race-набор всех Momentum пакетов с PostgreSQL завершился ошибкой среды: множество параллельных storage-тестов заполнило заданный локальный tmpfs 1 GiB. Затронутый контейнер удалён. Прицельный полный PostgreSQL-набор `internal/momentum/history` после этого повторно прошёл без пропусков.

## Состояние

Файлы и проверки собраны в `/private/tmp/issue305-ready.json`; Git index пуст, содержимое зафиксировано хешами. Кандидат остановлен в READY для независимого ревью. Staging, commit, push, PR и правки после READY не выполнялись; ожидать отдельного ACTIVE перед публикацией.

## Финальный checkpoint на активной базе

- Developer owner: `/root/issue305_developer`, native role `developer-middle`.
- Native Issue task/chat: `01a1f295-1c1e-7002-aae9-9779f1a19e45`, host `local`.
- Активная очередь G10 rev378 назначила единственный slot #305 (`ACTIVE_SINGLE_SLOT`) и сохранила ровно три HELD paths из этого handoff.
- Разрешённая база обновлена fast-forward из read-only mirror до `4657d490cfb4e11ddfec114354b14c9c15969a31`, tree `de94fb5a46677c9d0b70d9377766cf8006ce1e4a`. Она потомок исходной базы `55c157919fe889448a0e61478d2e1191061e96a3`; между ними нет изменений трёх путей Issue. Mirror и primary checkout не менялись.
- Текст handoff до этого checkpoint сохранён побайтно; SHA256 прежнего префикса `56e45c4a056c067657c2b0c61523584be79668f61d559ceaa0081516949596e6`.

Повторные проверки на финальной базе:

- Целевые `TestReadCommonCandleWarmup*` race-тесты: PASS, PostgreSQL 17.11, `server_version_num=170011`, 0 skips.
- Полный `internal/momentum/history` race-набор с PostgreSQL 17.11: PASS, 0 skips. Disposable контейнер `issue305-pg-final` удалён; отсутствие подтверждено.
- `go test ./...` и `go test -race ./...`: PASS, PostgreSQL DSN unset.
- Обычный full coverage с DSN unset: `3282/4626 = 70.946822309%`, порог CI 70% пройден.
- `go vet ./...`, `go build ./...`, gofmt, `git diff --check`: PASS.
- golangci-lint v2.10.1: PASS, 0 issues.
- govulncheck v1.1.4 с `GOPROXY=off` и официальной локальной базой `/private/tmp/g10-public-vulndb.Q6sE6qf4/db`: PASS, 0 достижимых уязвимостей; одна уязвимость в требуемом модуле не вызывается кодом.

Финальные логи находятся в `/private/tmp/issue305-final-evidence/`. Новый manifest `/private/tmp/issue305-ready-final.json` содержит точные SHA256/blob hashes трёх файлов, базу, ветку, пустой index и результаты. Кандидат остановлен для независимого ревью; stage/commit/push/PR не выполнялись. Не менять файлы после GREEN; публикация только после отдельного разрешения owner.
