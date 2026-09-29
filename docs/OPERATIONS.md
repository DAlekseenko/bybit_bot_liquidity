# Эксплуатация на сервере

Это ручной запуск существующего read-only цикла Bybit Testnet и paper-trade
аналитики. Бот не отправляет и не закрывает биржевые ордера; `dry_run: false`
этого не меняет. Инструкция не описывает публикацию или автоматический deploy.

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
