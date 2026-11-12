# Issue #249 — DEV handoff

## Контракт и состояние

- Issue: [#249, схема журнала пакетов Momentum](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/249), marker `codex-momentum-signal-journal-schema:v1`.
- База: `5fd2ae683d7c7f2c681557be769f3f24f689ac3b`; ветка `codex/249-signal-journal-schema`; рабочая копия создана отдельно от пользовательского primary checkout.
- Migration suffix 5 добавлен после v1–v4. Существующие DDL v1–v4 побайтно не редактировались. Изменены только пять зарезервированных Go-файлов и этот handoff.
- Осталось шесть путей, тестовый diff — 117 затронутых физических строк, включая удаления; production diff — 9 добавленных строк. Оба бюджета Issue соблюдены.
- DEVREADY. Независимый review, commit, push и PR ещё не выполнялись. Ожидается serial final slot после #247 и отдельный ROOT grant.

## Реализация

Добавлена `momentum.signal_batches` с 32-байтовым `BYTEA` primary key, непустым `BYTEA` payload, `created_at` по умолчанию от transaction timestamp и встроенной проверкой `batch_hash = pg_catalog.sha256(payload)`. Мигратор применяет только новую версию 5. Тесты сохраняют исходные сценарии и проверяют upgrade/collision, сохранность данных и migration prefix, repeat, hash vector без pgcrypto, бинарный roundtrip, конфликтный `DO NOTHING`, ограничения hash/payload/time, UTC instant и документированную границу сырых SQL updates.

Код не добавляет producer, decoder, repository API, JSON/canonical-input validation, WORM/immutability claim, retention policy, зависимости или биржевые вызовы.

## Проверки

- Go `go1.26.10 darwin/arm64`; исходная совместимость проекта Go 1.24 сохранена.
- `gofmt -w` на пяти Go-файлах; `test -z "$(gofmt -l cmd internal)"` и `git diff --check` — PASS.
- `go test -count=1 -v ./internal/momentum/storage -run 'TestSignalJournalMigration|TestSignalJournalSchema'` — PASS, оба теста без skip. Raw log `/private/tmp/issue249-targeted-r1.log`, SHA256 `e94d5620b80f4a297c74546f8b60789e077369c300327af0da30dc4ddbfb80a8`.
- `go test -count=1 ./...` — PASS, все пакеты без skip. Raw log `/private/tmp/issue249-full.log`, SHA256 `a7215707e8e9f2dda8ad0c83e284b8235e916d143cc18b01db8f15491ab8136c`.
- `go test -race -count=1 ./...` — PASS, все пакеты без skip. Raw log `/private/tmp/issue249-race.log`, SHA256 `bc8703bd5203371bc87da9d3296b53703444119d33ba003d388d0b5c358cfa7f`.
- `go test -race -count=1 ./internal/momentum/...` — PASS, все Momentum-пакеты без skip. Raw log `/private/tmp/issue249-momentumrace.log`, SHA256 `2e929817cbc35876229fb48a75826681c91ba234e5ec5c723bdcd66ffea6cc4b`.
- `go test -coverprofile=/private/tmp/issue249-full-coverage.out -count=1 ./...` — PASS; total statement coverage 82.8%. Log `/private/tmp/issue249-coverage.log`, SHA256 `6cfae54db0635d16773ba40fa154fe3fdc7d5bbd996dfd6fafd9635cff5f940f`; profile SHA256 `63ebe03a05aa4648fd42ec69b85164102b72cacd6df3cf96abea523d8d61a14d`.
- `go vet ./...` и `go build ./...` — PASS. Оба raw output-файла пустые; SHA256 каждого `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- CI `golangci-lint v2.10.1` — PASS, 0 issues. Log `/private/tmp/issue249-lint.log`, SHA256 `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`.
- CI `govulncheck v1.1.4` — exit 0, ни одной вызываемой уязвимости; сканер сообщает одну уязвимость в графе требуемых модулей, код её не вызывает. Log `/private/tmp/issue249-security.log`, SHA256 `b5ae54ddcfa4cb39e85f84ad2d9e43556bd817c480eaab2a71bab63bfc29353b`.

## PostgreSQL evidence

Каждый успешный PostgreSQL-этап использовал отдельный контейнер образа `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, tmpfs `/var/lib/postgresql/data` размером 1 GiB, только loopback publish, без host mounts. Прямой SQL во всех случаях вернул `server_version = 17.11 (Debian 17.11-1.pgdg13+2)` и `server_version_num = 170011`. DSN был синтетическим, с `sslmode=disable`; ambient `PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD` установлены в poison values.

| Этап | Container ID | Loopback | DSN | Результат |
| --- | --- | --- | --- | --- |
| Targeted, успешный retry | `f68d0c066d1a80e551ba549843dedad505dc58c9787d1861f333bf537e14d6bb` | `127.0.0.1:60957` | `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:60957/momentum_test_launcher?sslmode=disable` | PASS |
| Full tests | `6a11457e969dee5d4620c3c3d9d1b3b43627268d7f514454b5d65f42073e379b` | `127.0.0.1:61377` | `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:61377/momentum_test_launcher?sslmode=disable` | PASS |
| Full race | `6ab80c47d9f57620f950df647505dff960f227e885474b8766f957ff3e4eb228` | `127.0.0.1:61866` | `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:61866/momentum_test_launcher?sslmode=disable` | PASS |
| Momentum race | `efc2eb3d6a2c0f097ced044d7fbe9dedf983184f62704495aa666afb63639f8c` | `127.0.0.1:63999` | `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:63999/momentum_test_launcher?sslmode=disable` | PASS |
| Full coverage | `0c3013c71d07f09c5b346585430c5884fb49ad1e6f8f37debb134b3b109877cc` | `127.0.0.1:64500` | `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:64500/momentum_test_launcher?sslmode=disable` | PASS, total 82.8% |

Все пять перечисленных контейнеров остановлены; последующий `docker inspect <name>` подтвердил `No such object`. Начальная попытка targeted-теста остановлена до подключения из-за неверного database path в DSN, тесты не исполнялись. Ошибка и cleanup сохранены: `/private/tmp/issue249-targeted.log`, SHA256 `e7d04f0659559ce3fb6141c58985e0eb5c4ea55d56b8b8aaa81cc23b0b09a343`. Повтор выполнен на новом контейнере из таблицы и прошёл.

## Следующий шаг

Сверить точные шесть путей и хеши с Issue #249, передать независимому reviewer после освобождения serial slot. После GREEN заморозить approved blobs и ждать отдельного разрешения на публикацию. Не менять workflow: торговля и deploy не выполнялись.

## Refresh на verified master 2026-11-11

- ROOT подтвердил `9205117947474e1b483cb0747731d5ab1ba49023`. Ветка fast-forward обновлена с `5fd2ae683d7c7f2c681557be769f3f24f689ac3b` без merge-коммита. Commit добавил только `.ai/handoffs/issue-247.md` и `internal/momentum/signals/{batch.go,batch_test.go}`. Шесть зарезервированных путей #249 остались disjoint и принадлежат прежнему owner.
- После обновления базы заново прошли targeted #249 тесты, `go test -count=1 ./...`, полный race, Momentum race и полное coverage. Все пакеты завершились без skip. Total statement coverage — 83.2%. Свежие PostgreSQL runtime/SQL/DSN/cleanup записи и логи сохранены в `/private/tmp/issue249-ready.json` и `/private/tmp/issue249-refresh-ready.json`.
- Targeted log `/private/tmp/issue249-refresh-targeted.log`, SHA256 `d180ab1a46f060c25535fc8765ed20b7c20d53f4a1243b10831b2a5a267e505f`. Full test log `/private/tmp/issue249-refresh-full.log`, SHA256 `b0c52625f4103f376f57bc14ce5b89684339b3bb0679c75e043470b1318c8c45`. Full race log `/private/tmp/issue249-refresh-race.log`, SHA256 `86c75dc1434828cf4efd94baa88a0bb16111932ef887865e746c748906395719`. Momentum race log `/private/tmp/issue249-refresh-momentumrace.log`, SHA256 `a08ab368577db0d99e849d5264a23f95459e61d4d4e3fae09825a58c5828fd48`. Coverage log `/private/tmp/issue249-refresh-coverage.log`, SHA256 `0c61710b19e689d3f8445baa498bd6532eede4672ddf872b8b108899e6abaf60`; profile SHA256 `60849314b22e6c07b2e918b67c1d4d6f1fd1aa634bfb115a3b62205cf91dcb12`.
- Все пять PostgreSQL стадий использовали отдельный контейнер `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, tmpfs 1 GiB, loopback-only port, no host mounts. Каждый прямой SQL version read вернул PostgreSQL `17.11 (Debian 17.11-1.pgdg13+2)` и `170011`. В каждом Go-тесте использовался фактический DSN вида `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:<stage-port>/momentum_test_launcher?sslmode=disable`. Ambient `PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD` были poisoned. Каждый контейнер остановлен, `docker inspect` после cleanup вернул `No such object`.
- Targeted: container `46071ee27882e20a0b047bb643bad5cff7bf263f0d7eb31e965f5839272fb676`, port `59002`. Full: `dbbf394bfaf72cda0d3833e81442d9aeb8837017b14bb30452b1916820f0cec9`, port `59132`. Full race: `23186c4ea78d3212c88800a0296c883cb71c78c1d67984877f4810fa394098bb`, port `59505`. Momentum race: `53e2bed328bf557f7aa0ec7115cef5650ab53bbe4470f8483e46c20b03a3e0c5`, port `59871`. Coverage: `99083d3c0e958d1726234f136e86e6dc4cf3f6c589eff08ec895f4b24398cf2f`, port `60186`.
- После fast-forward `gofmt` по `cmd/internal`, `git diff --check`, полный `go vet ./...`, `go build ./...` и `golangci-lint v2.10.1` прошли. Go output logs vet/build пустые. Линтер дал `0 issues`.

### Security scan и toolchain

- `.github/workflows/ci.yml` запускает `actions/setup-go@v7` с `go-version-file: go.mod`. Файл содержит `go 1.24.13` и `toolchain go1.26.10`. По правилам setup-go v7 используется toolchain directive, поэтому configured CI проверка использует Go 1.26.10. Источник: [actions/setup-go go-version-file](https://github.com/actions/setup-go/blob/main/docs/advanced-usage.md).
- Настоящий CI `govulncheck v1.1.4` под Go 1.26.10 завершился exit 3 с достижимой уязвимостью стандартной библиотеки `GO-2026-6859` в `net/http`, относящейся к `http.MaxBytesReader` header race. Релизные заметки указывают, что Go 1.26.11 включает исправления net/http: [Go release history](https://go.dev/doc/devel/release). Raw output `/private/tmp/issue249-refresh-security.log`, SHA256 `0cfe2a1818657570001b8fab373f3e670007980711f30795bb4af745b8a43af5`. Скан также сообщает одну module-only advisory, которую код не вызывает. Это unresolved configured CI gate.
- Дополнительный локальный scan под официальным Go 1.26.11 завершился exit 0, без достижимых уязвимостей и с одной недостижимой module-only advisory. Архив `go1.26.11.darwin-arm64.tar.gz` загружен только в `/private/tmp`, SHA256 `e3a42e67a7a8acf450ebada2b074c7ca863000ca514907b7185b45e8f5cdff57` совпал с [официальной страницей Go downloads](https://go.dev/dl/). Этот supplemental результат не заменяет configured CI gate. Log `/private/tmp/issue249-refresh-security-go12611.log`.
- Первые вызовы предустановленных lint/security бинарей использовали system Go 1.23.1 и завершились до анализа из-за версии `go.mod`. Исправленные вызовы через Go1.26.10 wrapper дали итоговые lint PASS и указанный security FAIL. Исходные ошибки сохранены в `/private/tmp/issue249-refresh-lint.log` и `/private/tmp/issue249-refresh-security.log`.

Перед review необходимо обновить `/private/tmp/issue249-ready.json` и этот handoff с текущими SHA всех шести финальных файлов. Code diff после предыдущего DEVREADY не менялся. До нового ROOT grant публикация и другие внешние изменения запрещены.

### Log inventory clarification

Исходные ошибки вызова binaries под Go 1.23.1 и ошибки package loader записаны отдельно после этого абзаца: `/private/tmp/issue249-refresh-lint-go123.log` SHA256 `03b43b958f7a9713ab7ccad821e640a44ec442570f2eccecbe1ddffc22755380`, `/private/tmp/issue249-refresh-lint-path.log` SHA256 `e704b31895a90f47b79f6ecb4e2b987d5f35274be943e57aeaa8e8564d494cc0`, `/private/tmp/issue249-refresh-security-go123.log` SHA256 `79d6b6ccbb5471b18fd1f8b483a64b61d261f034734ae2231af8ce47438b1275` и `/private/tmp/issue249-refresh-security-path.log` SHA256 `14324df53d2b318eb49dcdba6df6b3d550d48dda84a3f420b860ad37f0974351`. Имена lint/security логов выше относятся к окончательным результатам этих проверок, а не к первоначальным ошибкам запуска.
- Configured Go1.26.10 security scan raw log SHA256 `0cfe2a1818657570001b8fab373f3e670007980711f30795bb4af745b8a43af5`. Supplemental Go1.26.11 scan `/private/tmp/issue249-refresh-security-go12611.log`, SHA256 `b5ae54ddcfa4cb39e85f84ad2d9e43556bd817c480eaab2a71bab63bfc29353b`. Официальный архив SHA256 перепроверен локально: `e3a42e67a7a8acf450ebada2b074c7ca863000ca514907b7185b45e8f5cdff57`.

## Resume queue154 после #251

- ROOT возобновил #249 на единственном ACTIVE slot. Verified master `a9d498773de0c60337a7e62aa204876f4e56ea78` fast-forwarded из `9205117947474e1b483cb0747731d5ab1ba49023` без merge-коммита. Он принёс Go toolchain `1.26.11` в `go.mod` и Dockerfile. Код #249 по-прежнему ограничен прежними шестью путями и suffix5; миграции 1–4 не менялись.
- Старый handoff сохранён побайтно как prefix. Проверка подтвердила исходный SHA256 `4a73f05533c21fc2327fce36adb998847d2d303f144f871812a90651a256985f`. Предыдущий manifest, handoff и все 29 файлов доказательств скопированы byte-for-byte в `/private/tmp/issue249-prior-evidence-20261112/`; backup manifest SHA256 `89db02dd8edd090ab0183860a6f2d9ae834f2c197c0489266f3143b4109ea090`. Новые логи записаны под `issue249-resume-*`, старые результаты не перезаписаны.
- Reviewer read-only проверил предыдущие blobs и не нашёл дефекта схемы или acceptance. Reviewer отметил две неточности handoff, исправленные этим appended checkpoint: manifest существует в `/private/tmp/issue249-ready.json`; файл `/private/tmp/issue249-refresh-ready.json` не создавался. Также `govulncheck` был локальным воспроизведением команды из configured CI, а не запуском GitHub Actions. GitHub Actions run evidence отсутствует.
- Go `go1.26.11 darwin/arm64` использован для повторных тестов и gates. Targeted `TestSignalJournalMigration` и `TestSignalJournalSchema` PASS, лог `/private/tmp/issue249-resume-targeted.log`, SHA256 `801bbc344d71b33194298bca91a5a44a70c105067909fa9791109472ee3b6f7b`. Full `go test -count=1 ./...` PASS, лог `/private/tmp/issue249-resume-full.log`, SHA256 `46b9633a9d3cd14f0660f381156d383081f10583f6b695c2d6d18f27cac37723`.
- Full `go test -race -count=1 ./...` PASS, лог `/private/tmp/issue249-resume-race.log`, SHA256 `14a2aa2c8613e4ae0b4d46f8917b76555683a51cf532ae55a340ec6ce0c95af6`. Momentum race PASS, лог `/private/tmp/issue249-resume-momentumrace.log`, SHA256 `0fb84d02a6f547c76d707dfba47832a74377073e96b2329b34d17386ce6069af`. Coverage PASS at 83.2%, log `/private/tmp/issue249-resume-coverage.log`, SHA256 `c37a210204a477c7089235a006a77e706f80e3bf831418ef3d4e63d20c08e4f2`; profile SHA256 `ce29fc1ce171c2132ea9fb957ad5de90d1a5abf7ab248d635e57c5e0b167383e`. Все пакеты завершились без skip.
- Каждый тестовый этап использовал отдельный fresh контейнер `postgres:17@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f`, tmpfs `/var/lib/postgresql/data` размером 1 GiB, loopback publish и no host mounts. Прямой SQL на каждом сервере вернул `17.11 (Debian 17.11-1.pgdg13+2)` и `170011`. DSN формата `postgres://momentum_test_user:issue249_synthetic_password@127.0.0.1:<port>/momentum_test_launcher?sslmode=disable`; ambient PG selectors были poisoned.
- PostgreSQL stage IDs и ports: targeted `559e278800afdf2b8962277b3d9cca6af5bc8b37fbb913bcc1be6ebb8ebb5917` / `65229`; full `2fa6b880bf234bf18f2e7eb53700f4563dbe92c976ed3712c3fb7dc5411a2795` / `65375`; full race `f96329b426cb9290eb48efc4bf82da2d41a340284e2f056d1c2a94cf4ba95b78` / `49366`; Momentum race `17a70e1740d1b7e6dd4b989b73e3afcbb9ceb9394210ed1c096a22a81c7174c9` / `50012`; coverage `36d31e82b297df54385dbf19f6b7271fb56227025a10df4a89157e4224d42f89` / `50599`. Все остановлены; `docker inspect` каждого после cleanup вернул `No such object`.
- `gofmt`, diff-check и whitespace audit каждого из шести путей PASS. `go vet ./...` и `go build ./...` PASS. `golangci-lint v2.10.1` запущен через `go run` wrapper с Go1.26.11 и PASS `0 issues`, лог `/private/tmp/issue249-resume-lint-run.log`, SHA256 `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`.
- Configured `govulncheck v1.1.4` локально воспроизведён `go run` wrapper с Go1.26.11 и PASS с 0 reachable findings. Он сообщает одну module-only advisory, которую код не вызывает. Лог `/private/tmp/issue249-resume-security-run.log`, SHA256 `b5ae54ddcfa4cb39e85f84ad2d9e43556bd817c480eaab2a71bab63bfc29353b`. Первоначальные вызовы cached binaries напрямую завершались до анализа с `package unsafe is not in std`; эти stdout сохранены в `/private/tmp/issue249-resume-lint-prebuilt.log` и `/private/tmp/issue249-resume-security-prebuilt.log`, затем были пройдены полные go run wrapper gates выше.
- Текущие шесть final blob hashes и обновлённый receipt записаны в `/private/tmp/issue249-ready.json`. Передача reviewer следует только после freeze этих шести путей. Commit, push и PR не выполнялись, нужен новый ROOT grant.
