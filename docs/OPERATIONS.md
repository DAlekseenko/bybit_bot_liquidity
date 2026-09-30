# Эксплуатация на сервере

Это эксплуатация read-only цикла Bybit Testnet и paper-trade аналитики. Бот не
отправляет и не закрывает биржевые ордера; `dry_run: false` этого не меняет.
Ручной запуск ниже остаётся доступен; gated image deploy описан в разделе далее.

## Подготовка и запуск

Работайте из корня checkout. Создайте вне репозитория `/etc/bybit-bot/server.env`
по образцу `ops/server.env.example`, задайте абсолютный `BOT_DATA_DIR` и права
`0600`. Оставьте ключи Bybit пустыми для public-only режима. Если нужна
диагностика readiness, укажите только отдельную пару ключей с read-only
разрешениями; она выполняется один раз при старте.

Проверьте итоговую конфигурацию до запуска:

```sh
docker compose --env-file ops/server.env.example -f docker-compose.yml -f ops/docker-compose.server.yml config
```

Запуск, состояние, логи и остановка:

```sh
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml up -d --build
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml ps
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml logs --tail=100 bot
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml down
```

Compose overlay не публикует порты. Метрики слушают `127.0.0.1:8080` внутри
контейнера и доступны только в его network namespace; внешнего scrape endpoint нет.

## Резервная копия SQLite

Задайте на хосте отдельный каталог резервных копий с ограниченным доступом.
SQLite `.backup` согласованно копирует работающую базу; проверяйте именно копию:

```sh
set -eu
BACKUP_DIR=/srv/bybit-bot/backups
install -d -m 0700 "$BACKUP_DIR"
chmod 0700 "$BACKUP_DIR"
BACKUP_FILE="trades-$(date -u +%Y%m%dT%H%M%SZ).db"
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml run --rm -v "$BACKUP_DIR:/backup" sqlite /usr/bin/sqlite3 /data/trades.db ".backup /backup/$BACKUP_FILE"
chmod 0600 "$BACKUP_DIR/$BACKUP_FILE"
docker compose --env-file /etc/bybit-bot/server.env -f docker-compose.yml -f ops/docker-compose.server.yml run --rm -v "$BACKUP_DIR:/backup:ro" sqlite /usr/bin/sqlite3 -readonly "/backup/$BACKUP_FILE" 'PRAGMA quick_check;'
```

Результат проверки должен быть `ok`. Перед восстановлением остановите bot,
сохраните текущий файл отдельно и проверьте выбранную копию. Не подменяйте
работающую БД: восстановите файл в `BOT_DATA_DIR`, выставьте владельца,
соответствующего `BOT_UID:BOT_GID`, затем запустите bot и проверьте его состояние.
Восстановление заменяет историю после точки копирования; храните копии вне
каталога данных и не удаляйте их до проверки результата.

## Ручная доставка immutable image

До первого запуска администратор проверяет тариф/доступность **Required
reviewers** для private repository и настраивает Environment `testnet-server` с
required reviewer. Если защита недоступна, deploy остаётся заблокированным.
После проверки выставьте Environment variable
`REQUIRED_REVIEWERS_CONFIRMED=true`; workflow завершится до SSH без неё. Не
делайте репозиторий public для обхода ограничения.

Используйте отдельный rootful Linux VPS только для Bybit Testnet и этого сервиса;
не размещайте на нём другие приложения. Создайте отдельного deploy-пользователя
без `sudo`, заранее проверьте host key
по независимому каналу и сохраните его строку в Environment secret
`DEPLOY_KNOWN_HOSTS`. Запрещён runtime `ssh-keyscan`. Rootful Docker socket
даёт deploy-пользователю фактические права root. Эта автоматизация требует
rootful Docker; rootless режим явно отклоняется, пока не решён совместимый
non-root доступ к данным. Задайте `BOT_UID`/`BOT_GID` равными UID/GID
deploy-пользователя и владельцу каталога данных. Compose сохраняет non-root
процесс бота. Deploy user не получает `sudo`, но доступ к rootful Docker socket
эквивалентен root и не считается least-privilege.
Подготовьте принадлежащие пользователю `DEPLOY_PATH`, каталог данных и
`/etc/bybit-bot/server.env` с mode `0600`; последний содержит `BOT_DATA_DIR`,
UID/GID и при желании только Bybit Testnet read-only credentials. Ключи не
генерируются workflow-ом. GHCR pull на сервере настройте отдельно с read-only
доступом к пакету. Не открывайте входящие порты бота.

До первого workflow deploy администратор должен подтвердить, что нового каталога
данных достаточно и в нём нет истории или backup для восстановления. Bootstrap
разрешён только для новой пустой БД. Он создаёт файл от имени `BOT_UID:BOT_GID`,
задаёт `user_version=0`, проверяет целостность и возможность открыть write
transaction. Первая прикладная миграция останется startup-задачей бота. При
наличии файла, прежней истории или backup не запускайте bootstrap: сначала
разберите и восстановите нужную историю вручную.

```bash
install -d -m 0700 "$BOT_DATA_DIR"
test ! -e "$BOT_DATA_DIR/trades.db"
test ! -e "$DEPLOY_PATH/backups"
docker run --rm --user "$BOT_UID:$BOT_GID" -v "$BOT_DATA_DIR:/data" keinos/sqlite3:3.53.4 /usr/bin/sqlite3 /data/trades.db 'PRAGMA user_version=0; PRAGMA quick_check; BEGIN IMMEDIATE; ROLLBACK;'
test "$(stat -c '%u:%g' "$BOT_DATA_DIR/trades.db")" = "$BOT_UID:$BOT_GID"
docker run --rm --user "$BOT_UID:$BOT_GID" -v "$BOT_DATA_DIR:/data" keinos/sqlite3:3.53.4 /usr/bin/sqlite3 /data/trades.db 'PRAGMA quick_check; BEGIN IMMEDIATE; ROLLBACK;'
```

Bootstrap и повторная проверка печатают `ok`. Deploy повторно
проверяет наличие, владельца, write access и целостность БД до создания
backup-каталога или файла и
до смены image. Если БД отсутствует, deploy завершится без backup artifact и без
переключения image. Это также требуется при самом первом deploy: после ручного
bootstrap скрипт создаст проверенную резервную копию до запуска image.

Добавьте в Environment secrets `DEPLOY_HOST`, `DEPLOY_USER`,
`DEPLOY_SSH_PRIVATE_KEY`, `DEPLOY_KNOWN_HOSTS`, опциональный `DEPLOY_PORT`;
variable `DEPLOY_PATH` задаёт абсолютный каталог релиза. SSH secrets не попадают
в Git, artifacts или команды. После review merge администратор запускает
Actions → Testnet read-only deploy → Run workflow, выбрав `master`. Workflow
проверяет исходники, публикует image, а deploy job требует Environment approval
и передаёт серверу только GHCR digest. Отсутствующий gate flag/secret останавливает
процесс.

Перед каждым переключением, включая первый deploy, script делает SQLite
`.backup` в `$DEPLOY_PATH/backups` (mode `0700` для каталога, `0600` для файла)
и требует `PRAGMA quick_check = ok`. После запуска проверяет Compose health и свежесть
`bybit_bot_market_data_last_success_unixtime` за 150 секунд. При ошибке он
возвращает предыдущий image digest, не подменяя БД; если старого digest нет или
rollback/схема требуют вмешательства, остановитесь и диагностируйте вручную.
Restore из backup — отдельная процедура из раздела выше; image rollback не
восстанавливает данные. Проверяйте Actions logs, `docker compose ps/logs` и
внутреннюю `/metrics` через `docker exec`; email и внешние уведомления не
настраиваются.
