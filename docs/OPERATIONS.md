# Эксплуатация read-only Testnet

Этот runbook предназначен только для существующего read-only/paper режима.
`dry_run: false` не включает реальные заявки, а сервер не должен получать
mainnet keys.

## Подготовка сервера

Нужен Linux-хост с Docker Compose, NTP и постоянным локальным диском. Выберите
регион, допустимый правилами Bybit для вашего аккаунта; не используйте сетевые
обходы региональных ограничений. API-ключи, если нужны для signed readiness,
создавайте только для Testnet и передавайте через secret manager или shell
environment, не через YAML и не через Git.

Создайте каталог данных и назначьте ему владельца, совпадающего с `BOT_UID` и
`BOT_GID`. Затем скопируйте `ops/server.env.example` за пределы репозитория,
заполните `BOT_DATA_DIR` абсолютным путём и выполните:

```bash
set -a
. /etc/bybit-liquidity/server.env
set +a
docker compose -f docker-compose.yml -f ops/docker-compose.server.yml up -d --build
docker compose -f docker-compose.yml -f ops/docker-compose.server.yml ps
docker compose -f docker-compose.yml -f ops/docker-compose.server.yml logs --tail=100 bot
```

Compose не публикует входящие порты. Metrics endpoint слушает `:8080` только
внутри container network; внешний scraping допускается только через отдельно
утверждённый защищённый collector/reverse proxy.

## Проверка и мониторинг

После запуска убедитесь, что `storage initialized`, `paper trading initialized`
и первый `market snapshot initialized` есть в логах. Проверка базы:

```bash
docker compose -f docker-compose.yml -f ops/docker-compose.server.yml exec bot \
  /app/bot -config /app/configs/config.yaml -check-storage
```

Метрики `/metrics` не содержат balance, leverage, API payload или error detail.
Контролируйте freshness market data, счётчики market-data/storage/WebSocket
ошибок и state readiness. Alert должен срабатывать при устаревшем snapshot,
росте reconnect/errors, failed readiness или нехватке диска. Останавливайте
инстанс перед переносом базы и не запускайте два writer-процесса на одном SQLite
файле.

## Backup и перенос SQLite

Для простого консистентного переноса остановите бот, проверьте базу через
`-check-storage`, сделайте backup SQLite средствами SQLite (Backup API или
`VACUUM INTO`), проверьте checksum и перенесите backup в `BOT_DATA_DIR` нового
сервера. После восстановления повторите `-check-storage` и запустите ровно один
экземпляр. Raw-copy запущенной базы не является процедурой backup.
