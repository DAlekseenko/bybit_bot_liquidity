# API, события и интеграции

У приложения нет входного HTTP/gRPC API и нет брокера сообщений. Здесь API
означает внутренние Go-контракты и предусмотренные внешние вызовы Bybit.
Семантика сущностей и единицы описаны в [DOMAIN.md](DOMAIN.md).

## Внутренние Go-контракты

### Биржа

`internal/exchange.Client` задаёт полный контракт адаптера:

| Метод | Назначение | Текущий результат Bybit-адаптера |
| --- | --- | --- |
| `GetCandles(ctx, symbol, limit)` | получить массив свечей | public REST реализован |
| `GetOpenInterest(ctx, symbol)` | получить одно значение OI | public REST реализован |
| `PlaceOrder(ctx, OrderRequest)` | открыть позицию с Qty, SL и TP | `ErrNotImplemented` |
| `GetPosition(ctx, symbol)` | вернуть позицию либо `nil` | `ErrNotImplemented` |
| `ClosePosition(ctx, symbol)` | закрыть позицию | `ErrNotImplemented` |

Все методы принимают `context.Context`. `bybit.NewClient` принимает base URL и
необязательный `*http.Client`; при `nil` создаётся клиент с timeout 10 секунд.

Public REST-контракт намеренно фиксирован под MVP:

- `GetCandles` вызывает `GET /v5/market/kline` с `category=linear`, `interval=1`
  и переданным `limit` от 1 до 1000. Bybit отдаёт свечи от новых к старым, адаптер
  возвращает их по возрастанию `OpenTime`. Семь строковых полей преобразуются в
  OHLCV/turnover; `Confirmed` вычисляется по серверному `time` и концу минуты.
- `GetOpenInterest` вызывает `GET /v5/market/open-interest` с `category=linear`,
  `intervalTime=5min`, `limit=1`. Значение и timestamp преобразуются из строк;
  единица OI для linear-инструмента — базовый актив.

Символ должен быть непустым и в верхнем регистре. Ответ ограничен 2 MiB,
категория и символ ответа сверяются с запросом, числовые значения должны быть
конечными и неотрицательными, а свечные цены — положительными.

Ошибки границы представлены так:

- `ErrInvalidArgument`, `ErrEmptyResult`, `ErrResponseTooLarge` — проверяемые
  sentinel-ошибки;
- `*HTTPError` — HTTP-статус вне 2xx и ограниченное тело ответа;
- `*APIError` — HTTP 2xx с ненулевым Bybit `retCode`;
- ошибки transport/context и JSON/schema оборачиваются с контекстом; для них
  сохраняется совместимость с `errors.Is`/`errors.As`.

Потребители используют более узкие интерфейсы:

- `marketdata.Exchange`: только `GetCandles`, `GetOpenInterest`;
- `execution.Exchange`: `PlaceOrder`, `GetPosition`, `ClosePosition`;
- `execution.SignalStore`: `SaveSignal`.

Это позволяет тестировать бизнес-логику fake-реализациями без сетевого API.

### Рыночные данные и стратегия

`marketdata.Collector.Snapshot(ctx, candleLimit, liquidations)` последовательно
запрашивает свечи и OI и возвращает `Snapshot`. События ликвидаций он не получает
сам: вызывающий код передаёт готовый срез.

`strategy.Strategy.Evaluate(Input)` — синхронный вычислительный контракт. Вход:
символ, минимум 21 свеча, предыдущее и текущее OI, ликвидации и `Now`. Выход —
`Signal{Symbol, Type, Reason}`. `Reason` не версионирован и не должен парситься
как стабильный протокол.

### Риск и исполнение

`risk.Manager.Plan(balance, entryPrice, side)` возвращает `Plan{Qty, StopLoss,
TakeProfit, RiskUSD}` либо `risk.ErrInvalidInput`.

`execution.Executor.Execute(ctx, signal, balance, entryPrice)`:

1. при наличии store сохраняет сигнал;
2. для `NONE` возвращает нулевой `Order` без ошибки;
3. проверяет cooldown и позицию;
4. строит risk plan и вызывает `PlaceOrder`.

Ожидаемые ошибки: `ErrUnsupportedSignal`, `ErrCooldownActive` и
`ErrPositionAlreadyOpen`. Ошибки store, risk manager и exchange передаются вверх.

`MarkPositionClosed(time)` и `SetNow(func() time.Time)` изменяют внутреннее
состояние executor; второй метод предназначен для детерминированных тестов.

### Хранилище

`storage.Store` предоставляет:

- `Open(driverName, dsn)` / `New(*sql.DB)` и `Close()`;
- `Migrate(ctx)`;
- `SaveSignal(ctx, strategy.Signal)`;
- `SaveTrade(ctx, storage.Trade)`;
- `Stats(ctx) (storage.Stats, error)`.

SQL-схема и ограничения готовности описаны в
[DEVELOPMENT.md](DEVELOPMENT.md#миграции-и-локальные-данные). Транзакционный
контракт между отправкой ордера и записью сделки не определён (`TODO`).

## События

Формального event bus и версионированных схем событий нет. Между пакетами
передаются синхронные Go-значения (`Snapshot`, `Signal`, `Order`, `Trade`).

Существующие структурированные логи:

| Сообщение | Поля | Источник |
| --- | --- | --- |
| `bot initialized` | `symbol`, `testnet` | `cmd/bot` |
| `bot stopped` | — | `cmd/bot` |
| `position opened` | `symbol`, `side`, `qty` | `execution` |

JSON-формат применяется логгером приложения; тесты executor могут передать другой
handler. Логи не являются стабильным внешним API. Схемы market events, закрытия
позиции, SL/TP и ошибок ещё не реализованы (`TODO`).

## Интеграция Bybit

Репозиторий фиксирует Bybit V5 Testnet и следующие целевые источники данных:

| Данные | Предусмотренный канал | Статус |
| --- | --- | --- |
| свечи 1m | REST `/v5/market/kline` | адаптер и unit-тесты готовы; wiring отсутствует |
| open interest | REST `/v5/market/open-interest`, опрос предполагался каждые 30 секунд | адаптер использует `5min`; scheduler отсутствует |
| ликвидации | публичный WebSocket linear liquidation stream | клиент отсутствует |
| ордера и позиции | приватный Bybit V5 API | точные endpoints и payloads не зафиксированы (`TODO`) |

Адреса среды находятся в [configs/config.yaml](../configs/config.yaml): REST
`https://api-testnet.bybit.com` и WebSocket
`wss://stream-testnet.bybit.com/v5/public/linear`. REST base URL используется
адаптером при прямом вызове, но приложение его пока не создаёт и не вызывает.

Public-реализация сверена с официальными контрактами
[Get Kline](https://bybit-exchange.github.io/docs/v5/market/kline) и
[Get Open Interest](https://bybit-exchange.github.io/docs/v5/market/open-interest)
на 2026-07-22.

Перед реализацией нужно сверить актуальную официальную спецификацию Bybit и
зафиксировать:

- названия WebSocket topics и семантику стороны ликвидации;
- API key/secret, подпись, timestamp/recv-window и синхронизацию времени;
- rate limits, retry/backoff и классификацию ошибок;
- idempotency/link order ID и поведение при неоднозначном timeout;
- шаги количества/цены и минимальный номинал;
- правила market order, attached TP/SL, частичных исполнений и закрытия позиции.

Всё перечисленное — `TODO`; не следует выводить детали из имён методов.

## Совместимость контрактов

Публичных потребителей у `pkg/` нет. Изменения внутри `internal/` не обещают
семантическую версионность, но должны одновременно обновлять компиляционные
проверки интерфейсов, unit-тесты и этот документ. Если появится внешний API, для
него потребуется отдельная схема версий и политика совместимости (`TODO`).
