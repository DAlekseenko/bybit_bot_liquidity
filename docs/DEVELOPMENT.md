# Локальная разработка

Инструкции предполагают запуск из корня репозитория. Общий статус проекта и
самый короткий запуск приведены в [README.md](../README.md).

## Окружение

Обязательно:

- toolchain Go 1.26.7; директива `go 1.24.13` в [go.mod](../go.mod) сохраняет
  совместимость исходников с Go 1.24, а `toolchain` выбирает поддерживаемую
  версию стандартной библиотеки;
- POSIX-совместимая командная строка для приведённых примеров.

Опционально — Docker для воспроизводимой сборки образа и внешний SQLite CLI для
ручной диагностики базы.

Перед первой сборкой Go загружает CGo-free SQLite-драйвер и его транзитивные
зависимости из `go.mod`/`go.sum`.

## Запуск и сборка

```bash
go run ./cmd/bot -config configs/config.yaml
go build ./...
```

Запущенный процесс проверяет конфигурацию, пишет логи и стартует read-only цикл
market data: при старте создаётся схема SQLite, public REST свечи/OI читаются
сразу и затем каждые 30 секунд, public WebSocket накапливает ликвидации, а
рассчитанные сигналы сохраняются.
Остановите процесс `Ctrl+C`. Отсутствие торговых действий сейчас ожидаемо:
приватные методы и отправка ордеров не подключены.

## Docker Compose

Основной контейнерный запуск:

```bash
docker compose config
docker compose up -d --build
docker compose ps
docker compose logs -f bot
```

`docker-compose.yml` создаёт основной сервис `bot` и опциональный CLI-сервис
`sqlite` из профиля `tools`. Входящие порты не публикуются: контейнеру бота нужен
только исходящий HTTPS/WSS-доступ к Bybit Testnet. Файл `configs/config.yaml`
монтируется в `/app/configs/config.yaml` read-only, а SQLite хранится в
локальном `./data/trades.db`, который монтируется в
`/app/data/trades.db`. Постоянный контейнер БД не нужен: используемый
`modernc.org/sqlite` работает внутри процесса. Compose запускает `bot` с
`BOT_UID`/`BOT_GID` из `.env`, чтобы непривилегированный процесс мог записывать
в bind mount. Для Linux/macOS задайте результат `id -u` и `id -g`; значения
текущего пользователя должны владеть каталогом `data`.

Открыть SQLite CLI к работающей базе можно без установки `sqlite3` на host:

```bash
docker compose run --rm sqlite
```

Сервис подключает тот же локальный каталог read-only и сразу открывает
`/data/trades.db`. Примеры команд внутри CLI:

```sql
.tables
.schema signals
SELECT id, created_at, signal_type, reason
FROM signals
ORDER BY id DESC
LIMIT 20;
.quit
```

При установленном локальном CLI база доступна напрямую:

```bash
sqlite3 -readonly ./data/trades.db
```

При использовании GUI-клиента также открывайте файл только для чтения, пока бот
работает.

Для одиночного запроса:

```bash
docker compose run --rm sqlite \
  /usr/bin/sqlite3 -readonly -header -column /data/trades.db \
  "SELECT id, signal_type, reason, oi_growth_percent, oi_condition_met, current_volume, volume_sma, volume_ratio, volume_condition_met, candle_direction, short_liquidations_usd, long_liquidations_usd, short_liquidation_condition_met, long_liquidation_condition_met, long_setup_met, short_setup_met FROM signals ORDER BY id DESC LIMIT 20;"
```

SQLite остаётся embedded-файлом, а `sqlite` — только запускаемый по требованию
инструмент диагностики. Не запускайте `docker compose up sqlite`: CLI не
является сервером и должен завершаться после работы.

Обычная остановка сохраняет локальный файл:

```bash
docker compose down
```

Опция `-v` теперь не удаляет `./data/trades.db`, потому что bind mount не
является Docker volume. Для удаления истории требуется отдельное явное удаление
локального файла.

Встроенный healthcheck вызывает read-only `PRAGMA quick_check` через бинарник:

```bash
docker compose exec bot /app/bot \
  -config /app/configs/config.yaml \
  -check-storage
```

Healthcheck запускает `PRAGMA quick_check`; он проверяет доступность и
целостность SQLite, но не подтверждает свежесть market data или доступность
Bybit. Логи Docker ограничены пятью файлами по 10 MB.

Ответ Bybit/CloudFront `403` с сообщением о блокировке страны означает, что
контейнер и SQLite могут быть healthy, но текущий сетевой egress не допускается
Bybit. Для market-data теста нужен разрешённый Bybit сетевой регион в рамках
применимых правил; смена Testnet endpoint или отключение TLS-проверки не являются
исправлением.

Файл доступен на host напрямую. Для согласованной резервной копии остановите
бот, скопируйте файл и снова запустите сервис:

```bash
docker compose stop bot
cp ./data/trades.db ./data/trades.backup.db
docker compose start bot
sqlite3 -readonly -header -column ./data/trades.backup.db \
  "SELECT id, created_at, signal_type, signal_price, hypothetical_entry, stop_loss, take_profit, estimated_fees, estimated_slippage, virtual_exit_price, exit_reason, virtual_pnl FROM signals ORDER BY id DESC LIMIT 20;"
```

Контейнер работает от непривилегированного пользователя, с read-only root
filesystem, `no-new-privileges`, writable bind mount `/app/data` и временным
`tmpfs /tmp`. `Dockerfile` собирает CGo-free бинарник в
`golang:1.26.7-alpine`, копирует CA bundle для HTTPS в runtime без дополнительных
сетевых установок и запускает его в `alpine:3.22`.

Опциональные read-only credentials:

```bash
cp .env.example .env
```

Заполните обе Bybit-переменные либо оставьте обе пустыми. Для `BOT_UID` и
`BOT_GID` укажите результаты `id -u` и `id -g`. Compose автоматически читает
локальный `.env`; файл исключён из Git и Docker build context. Реальные ордера
не отправляются.

Если build или runtime пишет `x509: certificate signed by unknown authority`
из-за корпоративного TLS proxy:

1. экспортируйте только публичный корневой CA организации в
   `certs/local-ca.pem`;
2. задайте `CUSTOM_CA_FILE=./certs/local-ca.pem` в `.env`;
3. повторите `docker compose up -d --build`.

BuildKit передаёт CA в build stage как secret, не добавляя его в слой. На
runtime весь `certs/` монтируется read-only, а Go читает его через
`SSL_CERT_DIR` вместе со стандартным CA bundle. Файлы сертификатов исключены из
Git и Docker build context. Не отключайте TLS verification и не помещайте в
каталог private keys или клиентские сертификаты. При обычном доступе
`CUSTOM_CA_FILE=/dev/null`, а каталог остаётся без локального CA.

Прямой запуск образа без Compose остаётся доступен, но базу нужно смонтировать
явно:

```bash
docker build -t bybit-liquidity-bot .
docker run --rm --name bybit-liquidity-bot \
  --user "$(id -u):$(id -g)" \
  -v "$(pwd)/data:/app/data" \
  bybit-liquidity-bot
```

Публикация образа, автоматический backup и production-мониторинг не настроены.

## Конфигурация и переменные окружения

Канонический пример — [configs/config.yaml](../configs/config.yaml).

| Секция | Ключи, которые читает код |
| --- | --- |
| корень | `symbol` |
| `exchange` | `testnet`, `base_url`, `websocket_url` |
| `strategy` | `oi_window_minutes`, `oi_growth_percent`, `volume_sma_period`, `volume_multiplier`, `liquidation_window_minutes`, `liquidation_threshold_usd` |
| `risk` | `risk_per_trade_percent`, `stop_loss_percent`, `take_profit_percent`, `estimated_round_trip_fee_percent`, `slippage_allowance_percent` |
| `trading` | `dry_run`, `virtual_balance_usd`, `max_open_positions`, `cooldown_minutes`, `max_trades_per_day` |
| `storage` | `driver`, `dsn` |
| `logging` | `level` |

Загрузчик написан на стандартной библиотеке и понимает только текущую плоскую
структуру секций и некавыченные скалярные значения. Не используйте anchors,
массивы, многострочные значения или сложное вложение YAML. Неизвестные секции и
ключи сейчас молча игнорируются; известные числовые ключи должны содержать
корректные числа. Старые ключи `strategy.liquidation_window` и
`strategy.liquidation_threshold` отклоняются с подсказкой о новой схеме.

Проверяются `symbol` в верхнем регистре без внешних пробелов, положительность и
конечность числовых порогов,
`volume_sma_period` в диапазоне 1–998, положительное
`liquidation_window_minutes`, неотрицательные fee/slippage reserves и защитная
граница exchange. `oi_window_minutes` должен быть одним из интервалов Bybit:
`5`, `15`, `30`, `60`, `240`, `1440`. `trading.max_open_positions` обязан быть
`1`; virtual balance, cooldown и дневной лимит должны быть положительными. Если
`dry_run` отсутствует, безопасный дефолт равен `true`; в каноническом примере
явно указано `false`, а `virtual_balance_usd` равен `1000`. Paper-trade применяет
виртуальный баланс, позиционный лимит, cooldown и дневной лимит только к
аналитическим сделкам. Реальный executor не подключён, поэтому секция не
разрешает биржевые заявки.
`testnet` обязан быть `true`, REST URL —
`https://api-testnet.bybit.com`, WebSocket URL —
`wss://stream-testnet.bybit.com/v5/public/linear`. User info, query и fragment в
URL запрещены.
Storage driver обязан быть `sqlite`, DSN — непустым. Уровень логирования должен
быть одним из `debug`, `info`, `warn`, `error`.

Для будущего private API загрузчик читает только две переменные:

```text
BYBIT_API_KEY
BYBIT_API_SECRET
```

Они опциональны для текущего read-only режима, но должны быть заданы вместе.
Поля `exchange.api_key` и `exchange.api_secret` в YAML отклоняются. `.env`
исключён из Git и автоматически не загружается; переменные должен предоставить
shell, контейнерный runtime или secret manager. Значения credentials маскируются
при обычном форматировании структуры и authenticated client, но всё равно не
должны логироваться. Полная пара позволяет signed account-info, wallet-balance,
position-info и current-leverage requests. При startup она включает один
execution readiness check до запуска фоновых горутин. Успех логируется по symbol;
при отказе логируется только безопасный `reason`, а public loop продолжает
работать. Ордера по-прежнему не реализованы и при обычном запуске не отправляются.

## Миграции и локальные данные

DDL находится внутри `storage.Store.Migrate` в `internal/storage/storage.go`.
Метод идемпотентно создаёт таблицы `trades` и `signals` и автоматически вызывается
при старте приложения. Используется CGo-free `modernc.org/sqlite`; имя драйвера —
`sqlite`, DSN примера — `data/trades.db`. Отдельной команды миграции и версий
схемы пока нет (`TODO`). Для существующей таблицы `signals` миграция проверяет
`PRAGMA table_info` и добавляет nullable-поля paper-trade и indicator snapshot
через `ALTER TABLE`, не удаляя старую историю. Новые строки содержат исходные
OI, volume/candle/liquidation значения, вычисленные thresholds/ratios и флаги
условий; в исторических строках эти колонки равны `NULL`. Файлы `data/*.db` и
сопутствующие WAL/SHM артефакты игнорируются; `data/.gitkeep` сохраняет каталог.

## Тесты и статический анализ

### Live Testnet contract tests

Обычный `go test ./...` никогда не делает сетевых запросов и не использует
credentials. Контрактные проверки находятся за build tag `integration` и требуют
явного opt-in:

```bash
BYBIT_TESTNET_CONTRACT_TESTS=1 go test -tags=integration ./internal/exchange/bybit
```

При наличии полной пары `BYBIT_API_KEY`/`BYBIT_API_SECRET` дополнительно
проверяются только signed read-only endpoints. Заявки, изменение leverage и
любые торговые операции в этих тестах запрещены. В GitHub Actions workflow
`Testnet contract tests` запускается вручную и использует environment
`testnet-contract`; секреты должны быть заданы именно в нём.

### Paper-trade аналитика

`storage.VirtualStats` агрегирует только завершённые virtual-позиции из
`signals`; таблица `trades` не участвует и зарезервирована для будущего
биржевого жизненного цикла. Незакрытая virtual-позиция после рестарта остаётся
видимой как open signal, но не восстанавливается автоматически и не влияет на
PnL: до задачи восстановления это явный предмет ручного разбора, а не фиктивное
закрытие. Воспроизводимые выборки находятся в
[`docs/sql/paper-trading-report.sql`](sql/paper-trading-report.sql).

Полный локальный набор:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Тесты сейчас покрывают `cmd/bot` read-only/paper-trade loop, `config`,
`marketdata`, `strategy`, `risk`, `papertrade`, `execution` с
account/leverage preflight и instrument
normalization/readiness, startup readiness reporting, SQLite storage и
public/signed REST и public WebSocket Bybit-адаптера. REST тестируется через fake
`http.RoundTripper`, WebSocket — через fake connection/dialer, readiness — через
fake source/checker, без сокетов и реальных сетевых запросов. Покрытие `logger` и
полного `main` отсутствует.

Для отчёта покрытия:

```bash
go test -cover ./...
```

CI дополнительно проверяет суммарное покрытие не ниже 70%, `govulncheck` и
`golangci-lint`; эти проверки требуют сетевого доступа только в GitHub Actions
для загрузки инструментов.

Workflow [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) выполняет
форматирование, unit-тесты, race detector, `go vet` и сборку на каждом push и
pull request.

## Форматирование

Изменённые Go-файлы форматируйте стандартным инструментом:

```bash
gofmt -w path/to/file.go
```

Проверка всего исходного дерева без записи:

```bash
test -z "$(gofmt -l cmd internal)"
```

Markdown-форматтер не настроен. Сохраняйте относительные ссылки и проверяйте, что
их цели существуют.

## Рабочий процесс

1. Сверьте задачу с запросом пользователя или внешним issue и границы с
   [ARCHITECTURE.md](ARCHITECTURE.md).
2. Изменяйте минимальный набор пакетов, сохраняя направление зависимостей.
3. Добавляйте тесты без реального Bybit API.
4. Запустите форматирование и проверки выше.
5. Обновите соответствующий канонический документ и
   [.ai/HANDOFF.md](../.ai/HANDOFF.md).

Makefile, скрипты релиза, отдельный линтер, порог покрытия и интеграционная среда
Testnet отсутствуют. Их политика остаётся открытой задачей.
