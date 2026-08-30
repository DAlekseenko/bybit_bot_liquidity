# Bybit Liquidity Bot

MVP алгоритмического торгового бота на Go для проверки стратегии, использующей
свечи, open interest и ликвидации Bybit. Целевая среда — Bybit Testnet, один
линейный инструмент (`BTCUSDT` в примере конфигурации).

> **Состояние проекта:** это компилируемый каркас, но ещё не работающий торговый
> бот. Запуск загружает конфигурацию и запускает read-only цикл на Bybit Testnet:
> свечи, open interest и ликвидации поступают из public API, сигнал сохраняется и
> логируется. Public ticker и signed read-only Unified balance реализованы как
> будущие входы executor; account preflight допускает только Unified account с
> `REGULAR_MARGIN`, position и current-leverage read models — только one-way
> mode. Leverage preflight сверяет текущее плечо с instrument limits, но не
> меняет его. Execution readiness объединяет эти проверки с balance и market
> price; при наличии env credentials он один раз выполняется на startup и
> безопасно логирует итог. `PlaceOrder` и `ClosePosition` возвращают
> `ErrNotImplemented`, исполнение сделок отсутствует. Не используйте проект для
> реальных средств.

## Что уже есть

- доменные типы и интерфейс биржи;
- публичные Bybit V5 REST-запросы linear 1m candles и open interest с
  настраиваемым поддерживаемым Bybit-интервалом и unit-тестами;
- public instrument info и нормализация Qty/SL/TP по биржевым шагам и лимитам;
- public linear ticker, подписанные read-only Unified account info/balance,
  one-way position и текущее leverage;
- fail-closed account preflight для Unified `REGULAR_MARGIN` и leverage preflight
  по диапазону/шагу инструмента;
- агрегированный read-only execution readiness для account, позиции, balance,
  market price и leverage с безопасным startup-reporting;
- сборка снимка свечей и open interest;
- read-only цикл в `cmd/bot`, который раз в 30 секунд получает market data и
  рассчитывает сигнал при переходе к следующему OI-окну, сохраняет и логирует его;
- публичный Bybit V5 WebSocket `allLiquidation` с heartbeat, reconnect и
  потокобезопасным настраиваемым окном ликвидаций (`5` минут в примере);
- правила сигналов `LONG`/`SHORT`/`NONE` и unit-тесты;
- расчёт фиксированного риска с резервами комиссии/проскальзывания, SL/TP и
  unit-тесты;
- paper-trade аналитика с виртуальным балансом `$1,000`: расчётный вход,
  SL/TP, комиссии, проскальзывание, причина выхода и net virtual PnL сохраняются
  в SQLite без отправки ордеров;
- исполнение сигнала с проверкой открытой позиции и cooldown;
- SQLite-хранилище: startup-миграция, сохранение каждого сигнала вместе с полным
  снимком OI/volume/candle/liquidations и расчёт статистики;
- конфигурация Testnet, JSON-логирование и Dockerfile.

Разрыв между компонентами и готовым MVP перечислен в
[архитектуре](docs/ARCHITECTURE.md#текущее-состояние-и-разрывы).

## Требования

- безопасный toolchain Go 1.26.7; уровень совместимости исходников сохранён на
  Go 1.24.13 через `go.mod`;
- Git — только для получения и совместной разработки рабочей копии;
- Docker — опционально, для контейнерной сборки.

Для SQLite используется CGo-free драйвер `modernc.org/sqlite`, совместимый со
сборкой Alpine. Для WebSocket используется `github.com/gorilla/websocket`;
остальной прикладной код опирается преимущественно на стандартную библиотеку.

## Установка и быстрый запуск

Получите рабочую копию репозитория и из её корня выполните:

```bash
go test ./...
go run ./cmd/bot -config configs/config.yaml
```

Ожидаемый результат — JSON-запись об инициализации. Процесс продолжит ждать
`SIGINT`/`SIGTERM`; остановите его `Ctrl+C`. При запуске выполняются read-only
REST- и WebSocket-запросы к публичному Bybit Testnet API; рассчитанные сигналы
сохраняются в `data/trades.db`. Ордера при текущей реализации не отправляются.

Контейнерный вариант с локально доступным SQLite-файлом:

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f bot
```

Compose запускает один непривилегированный контейнер бота без входящих портов.
Конфигурация монтируется read-only, а контейнерный `/app/data/trades.db`
соответствует локальному файлу `./data/trades.db`. Файл сохраняется после
`docker compose down` и доступен локальным SQLite-инструментам. Отдельный
постоянный контейнер СУБД не используется: SQLite встроен в приложение. Для
ручных запросов доступен опциональный SQLite CLI к тому же каталогу:

```bash
docker compose run --rm sqlite
```

CLI открывает `/data/trades.db` в режиме только чтения. Выйти можно командой
`.quit`; бот при этом продолжает работать.

Если `sqlite3` установлен локально, тот же файл можно открыть напрямую:

```bash
sqlite3 -readonly ./data/trades.db
```

Последние сигналы со всеми условиями входа:

```sql
SELECT created_at, signal_type, reason,
       oi_growth_percent, oi_growth_threshold_percent, oi_condition_met,
       current_volume, volume_sma, volume_ratio, volume_threshold,
       volume_condition_met, candle_direction,
       short_liquidations_usd, long_liquidations_usd,
       liquidation_threshold_usd,
       short_liquidation_condition_met, long_liquidation_condition_met,
       long_setup_met, short_setup_met
FROM signals
ORDER BY id DESC
LIMIT 20;
```

Проверить целостность контейнерной базы можно без остановки:

```bash
docker compose exec bot /app/bot \
  -config /app/configs/config.yaml \
  -check-storage
```

Для опционального signed read-only readiness скопируйте `.env.example` в `.env`
и заполните обе переменные. Public-only запуск работает с пустыми значениями.
Если Docker работает через корпоративный TLS proxy, положите его публичный CA в
`certs/local-ca.pem` и задайте `CUSTOM_CA_FILE=./certs/local-ca.pem` в `.env`;
TLS-проверка при этом остаётся включённой.
Подробные команды остановки, диагностики и удаления данных приведены в
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#docker-compose).

## Конфигурация

Версионируемый пример безопасно указывает на Testnet. Загрузчик жёстко отклоняет
`testnet: false`, mainnet endpoints и API credentials в YAML. Опциональная пара
`BYBIT_API_KEY`/`BYBIT_API_SECRET` читается только из окружения и используется
только для read-only account info, wallet balance, position info и current
leverage. Полная пара включает один startup readiness check; ошибка логируется
без account values и не останавливает public loop. Торговые private endpoints ещё
не реализованы. Нельзя задавать только одну переменную из пары.

Текущий загрузчик поддерживает используемое проектом простое подмножество YAML,
а не произвольный YAML. Параметры OI, volume SMA, ликвидаций, риска, SL/TP и
резервов sizing находятся в секциях `strategy` и `risk`. Секция `trading`
содержит `dry_run`, виртуальный баланс, лимит позиций, cooldown и дневной лимит
сделок. Виртуальный контур использует эти параметры для аналитики; реальный
executor не подключён, поэтому даже `dry_run: false` не активирует отправку
ордеров. Все поля и правила локальной настройки описаны в
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#конфигурация-и-переменные-окружения).

## Документация

- [Архитектура](docs/ARCHITECTURE.md) — компоненты, зависимости, потоки и границы.
- [Предметная область](docs/DOMAIN.md) — термины и точные бизнес-правила.
- [Разработка](docs/DEVELOPMENT.md) — окружение, запуск, тесты и миграции.
- [API и интеграции](docs/API.md) — Go-контракты, события и Bybit.
- [Решения](docs/DECISIONS.md) — принятые ограничения и открытые вопросы.
- [Инструкции участникам](AGENTS.md) — правила изменения проекта.
- [Краткий контекст](.ai/CONTEXT.md) и [передача работы](.ai/HANDOFF.md).

## Безопасность

Не коммитьте ключи, `.env` и файлы базы из `data/`; они исключены через
`.gitignore`. Загрузчик допускает только официальные REST/WebSocket endpoints
Bybit Testnet. Реальная торговля, модель разрешений API-ключа и аварийное
отключение не спроектированы и остаются вне текущей готовности проекта.
