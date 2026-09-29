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
| `GetOpenInterest(ctx, symbol, windowMinutes)` | получить одно значение OI для окна | public REST реализован |
| `GetInstrumentRules(ctx, symbol)` | получить шаги leverage/цены/количества и лимиты | public REST реализован |
| `GetLastPrice(ctx, symbol)` | получить last/mark/index price | public REST реализован |
| `GetLeverage(ctx, symbol)` | получить текущее one-way leverage | signed read-only REST реализован |
| `GetAccountInfo(ctx)` | получить Unified status и margin mode | signed read-only REST реализован |
| `GetAccountBalance(ctx)` | получить available/equity/wallet USD | signed read-only REST реализован |
| `PlaceOrder(ctx, OrderRequest)` | контракт будущей заявки с Qty, SL, TP и `LinkID` | `ErrNotImplemented` |
| `GetPosition(ctx, symbol)` | вернуть one-way позицию либо `nil` | signed read-only REST реализован |
| `ClosePosition(ctx, symbol)` | закрыть позицию | `ErrNotImplemented` |

Все методы принимают `context.Context`. `bybit.NewClient` принимает base URL и
необязательный `*http.Client` и возвращает `(*Client, error)`: base URL
проверяется сразу, поэтому невалидный клиент не создаётся. При `nil` создаётся
HTTP-клиент с timeout 10 секунд.

REST-контракт намеренно фиксирован под MVP:

- `GetCandles` вызывает `GET /v5/market/kline` с `category=linear`, `interval=1`
  и переданным `limit` от 1 до 1000. Bybit отдаёт свечи от новых к старым, адаптер
  возвращает их по возрастанию `OpenTime`. Семь строковых полей преобразуются в
  OHLCV/turnover; `Confirmed` вычисляется по серверному `time` и концу минуты.
- `GetOpenInterest` вызывает `GET /v5/market/open-interest` с `category=linear`
  и `limit=1`. `windowMinutes` преобразуется в поддерживаемый Bybit
  `intervalTime`: `5min`, `15min`, `30min`, `1h`, `4h` или `1d`; остальные
  значения отклоняются до HTTP-запроса. Значение и timestamp преобразуются из
  строк; единица OI для linear-инструмента — базовый актив.
- `GetInstrumentRules` вызывает `GET /v5/market/instruments-info` с
  `category=linear`, symbol и `limit=1`. Адаптер проверяет единственную запись и
  преобразует status, `leverageFilter`, `priceFilter` и `lotSizeFilter`.
- `GetLastPrice` вызывает public `GET /v5/market/tickers` для одного linear symbol
  и возвращает last/mark/index price с server time.
- `GetLeverage` подписывает `GET /v5/position/list` с `category=linear` и
  symbol. Единственная запись `positionIdx=0` преобразуется в текущее плечо и
  server time; hedge mode и неоднозначный payload отклоняются. Метод не вызывает
  `set-leverage`.
- `GetAccountInfo` подписывает `GET /v5/account/info` без query-параметров и
  преобразует `unifiedMarginStatus`, `marginMode` и `updatedTime`. Адаптер
  проверяет известные значения margin mode, но не применяет торговую политику.
- `GetAccountBalance` подписывает `GET /v5/account/wallet-balance` для
  `accountType=UNIFIED` и возвращает account-level available/equity/wallet USD.
  HMAC payload: timestamp + API key + receive window + каноническая query string.
- `GetPosition` подписывает `GET /v5/position/list` с `category=linear` и symbol.
  Для `positionIdx=0` запись с нулевым size возвращает `nil`, активная запись
  преобразуется из side/size/avgPrice/createdTime. `positionIdx=1/2` отклоняется как
  неподдерживаемый hedge mode; адаптер не меняет режим аккаунта.

Символ должен быть непустым и в верхнем регистре. Ответ ограничен 2 MiB,
категория и символ ответа сверяются с запросом, числовые значения должны быть
конечными и неотрицательными, а свечные цены — положительными.

Ошибки границы представлены так:

- `ErrInvalidArgument`, `ErrEmptyResult`, `ErrResponseTooLarge` и
  `ErrUnsupportedPositionMode` — проверяемые sentinel-ошибки;
- `*HTTPError` — HTTP-статус вне 2xx и ограниченное тело ответа;
- `*APIError` — HTTP 2xx с ненулевым Bybit `retCode`;
- ошибки transport/context и JSON/schema оборачиваются с контекстом; для них
  сохраняется совместимость с `errors.Is`/`errors.As`.

Потребители используют более узкие интерфейсы:

- `marketdata.Exchange`: только `GetCandles`, `GetOpenInterest`;
- `execution.AccountInfoSource`: только `GetAccountInfo`;
- `execution.LeverageSource`: `GetLeverage`, `GetInstrumentRules`;
- `execution.ExecutionReadinessSource`: account/leverage sources плюс
  `GetAccountBalance`, `GetLastPrice`, `GetPosition`;
- `execution.Exchange`: `GetInstrumentRules`, `PlaceOrder`, `GetPosition`,
  `ClosePosition`;
- `execution.SignalStore`: `SaveSignal`.

Это позволяет тестировать бизнес-логику fake-реализациями без сетевого API.

`OpeningOrderLinkID(intentID, order)` кодирует поля без неоднозначности и строит
SHA-256 `LinkID`. Вызывающая сторона сохраняет `intentID` при retry и задаёт
разные ID для разных intents. Это только детерминированный ключ: функция не
является разрешением на исполнение, не создаёт `TradePlan` или human confirmation,
не хранит ключ, не обнаруживает конфликты и не выполняет reconciliation. Ключ не
доказывает acceptance, fill или биржевую idempotence.

### Конфигурация credentials

`config.Load` получает `BYBIT_API_KEY` и `BYBIT_API_SECRET` только из process
environment. `config.Credentials` предоставляет `Configured`, `APIKey` и
`APISecret`; `String`/`GoString` выводят только признак настроенности. Отсутствие
обоих значений допустимо для read-only режима, одно значение без второго —
ошибка. YAML-поля с credentials запрещены.

До возврата конфигурации проверяется Testnet guard: `testnet=true`, официальный
HTTPS REST host и официальный WSS linear public path. `cmd/bot` создаёт
authenticated client при наличии полной пары и один раз выполняет execution
readiness до запуска фоновых горутин. Без credentials private reads полностью
пропускаются. `PlaceOrder` и `ClosePosition` остаются `ErrNotImplemented`.

`config.TradingConfig` читает `dry_run`, `virtual_balance_usd`,
`max_open_positions`, `cooldown_minutes` и `max_trades_per_day`. Загрузчик
допускает только одну позицию MVP и положительные balance/time/day limits.
`cmd/bot` применяет virtual balance и лимиты к paper-trade, но эти поля пока не
входят в контракт реального `execution.Executor`; `dry_run: false` в YAML не
означает, что биржевой торговый runtime активен.

### Рыночные данные и стратегия

`marketdata.NewCollector(exchange, symbol, oiWindowMinutes)` фиксирует
настроенный интервал OI. `Collector.Snapshot(ctx, candleLimit, liquidations)`
последовательно запрашивает свечи и OI и возвращает `Snapshot`. События
ликвидаций он не получает сам: вызывающий код передаёт готовый срез.

`bybit.LiquidationClient.Stream(ctx, symbol, output)` подписывается на
`allLiquidation.{symbol}`, отправляет JSON `ping`, проверяет symbol/side/price/qty
и передаёт `exchange.Liquidation` в канал. Один вызов соответствует одному
соединению; reconnect выполняет `cmd/bot` с задержкой 5 секунд.

`marketdata.LiquidationWindow` потокобезопасно хранит события за
`strategy.liquidation_window_minutes`. `cmd/bot` передаёт копию окна в каждый
`Snapshot`, поэтому стратегия получает предыдущий/текущий OI и актуальные
ликвидации.

`strategy.Strategy.Evaluate(Input)` — синхронный вычислительный контракт. Вход:
символ, свечи, предыдущее и текущее OI, ликвидации и `Now`. Стратегия отбирает
только `Confirmed`-свечи и требует `VolumeSMAPeriod + 1` таких свечей. Выход —
`Signal{Symbol, Type, Reason}`. `strategy.Config.LiquidationWindow` задаёт тот
же cutoff, что и retention накопителя; boundary включается. `Reason` не
версионирован и не должен парситься как стабильный протокол.

`EvaluateDetailed(Input)` возвращает `Evaluation{Signal, Indicators}`.
`Indicators` содержит исходные значения, timestamps, вычисленные OI growth,
volume SMA/ratio/threshold, направление свечи, обе суммы ликвидаций, пороги и
флаги прохождения условий LONG/SHORT. Все доступные индикаторы вычисляются даже
при `NONE`; `Complete=false` обозначает недостаток свечей для candle/volume
части. `Evaluate` остаётся совместимой обёрткой и возвращает только
`EvaluateDetailed(input).Signal`.

### Риск и исполнение

`risk.Manager.Plan(balance, entryPrice, side)` возвращает `Plan{Qty, StopLoss,
TakeProfit, RiskUSD}` либо `risk.ErrInvalidInput`. Размер позиции использует
сумму `StopLossPercent + EstimatedRoundTripFeePercent +
SlippageAllowancePercent`; резервы не меняют цены SL/TP.

`execution.AccountPreflight.Check(ctx)` читает `AccountInfo` через узкий
`AccountInfoSource`. Допускаются Unified статусы `3`, `4`, `5`, `6` только с
`REGULAR_MARGIN`; classic/неизвестный status, `ISOLATED_MARGIN`,
`PORTFOLIO_MARGIN` и отсутствующий `updatedTime` отклоняются. Проверка не меняет
account mode, margin mode или leverage; `cmd/bot` вызывает её только внутри
startup execution readiness при наличии credentials.

`execution.LeveragePreflight.Check(ctx, symbol)` через узкий `LeverageSource`
получает фактическое one-way leverage и актуальные instrument rules. Символы
должны совпадать, время и числовые ограничения — присутствовать, status должен
быть `Trading`, а плечо — находиться в диапазоне и соответствовать шагу от
`minLeverage`. Проверка возвращает исходный `LeverageInfo`, не выбирает целевое
плечо и не меняет биржевые настройки; `cmd/bot` вызывает её только внутри
startup execution readiness при наличии credentials.

`execution.ExecutionReadinessPreflight.Check(ctx, symbol)` агрегирует
`AccountPreflight`, отсутствие открытой позиции, account balance, market price и
`LeveragePreflight`. Успешный результат содержит `ExecutionReadiness{Symbol,
Account, Balance, Price, Leverage}`. Balance должен иметь server time,
неотрицательные конечные account totals и `AvailableUSD > 0`; все три цены
должны быть положительными, конечными, относиться к запрошенному symbol и иметь
server time. Instrument rules проверяются внутри leverage preflight и намеренно
не включаются в snapshot: executor получает их заново непосредственно перед
нормализацией заявки.

Readiness не проверяет достаточность available balance для неизвестного размера
ордера, не задаёт freshness window, не выбирает target leverage и не вызывает
private trade/set endpoints. При наличии credentials `cmd/bot` вызывает его один
раз до запуска фоновых горутин. Успех логируется с symbol; ошибка преобразуется
в безопасный `reason` и не останавливает текущий public read-only loop. Snapshot
и полный source error в runtime-лог не попадают.

`execution.Executor.Execute(ctx, signal, balance, entryPrice)`:

1. при наличии store сохраняет сигнал;
2. для `NONE` возвращает нулевой `Order` без ошибки;
3. проверяет cooldown и позицию;
4. строит risk plan;
5. получает актуальные instrument rules, округляет количество вниз, SL/TP — до
   ближайшего tick, затем проверяет price/quantity/notional limits;
6. вызывает `PlaceOrder` только с нормализованным запросом.

Попытки исполнения сериализуются. После успешного `PlaceOrder` executor локально
блокирует новые заявки до `MarkPositionClosed`, чтобы не зависеть от задержки
появления позиции в ответе биржи.

Ожидаемые ошибки: `ErrUnsupportedSignal`, `ErrCooldownActive` и
`ErrPositionAlreadyOpen`, а также ошибки нормализации instrument limits. Ошибки
store, risk manager и exchange передаются вверх.

Account preflight возвращает `ErrAccountInfoSourceRequired`,
`ErrInvalidAccountInfo`, `ErrUnsupportedAccountMode` или
`ErrUnsupportedMarginMode`; ошибки source сохраняются для `errors.Is`.

Leverage preflight возвращает `ErrLeverageSourceRequired`,
`ErrInvalidLeverageInfo`, `ErrInvalidInstrumentRules`,
`ErrInstrumentNotTrading`, `ErrLeverageOutOfRange` или
`ErrLeverageStepMismatch`; ошибки source также сохраняются для `errors.Is`.

Execution readiness возвращает `ErrExecutionReadinessSourceRequired`,
`ErrExecutionSymbolRequired`, `ErrInvalidAccountBalance`,
`ErrNoAvailableBalance`, `ErrInvalidMarketPrice` или ошибки вложенных
account/position/leverage checks. Ошибки каждого source сохраняются для
`errors.Is`.

`MarkPositionClosed(time)` атомарно снимает локальную блокировку позиции и
запускает cooldown. `SetNow(func() time.Time)` меняет источник времени и
предназначен для детерминированных тестов.

### Paper-trade аналитика

`papertrade.Engine.PlanSignal(signal, candle)` принимает только `LONG`/`SHORT` и
подтверждённую сигнальную свечу, применяет position/cooldown/UTC-day limits,
добавляет проскальзывание к hypothetical entry и вызывает `risk.Manager` с
виртуальным балансом. Результат `papertrade.Plan` содержит signal/entry price,
SL/TP, estimated fees/slippage и Qty.

`Advance(candles)` проверяет только последующие подтверждённые свечи и возвращает
pending `Outcome{SignalID, ExitPrice, Reason, PnL, ClosedAt}`. Один outcome
повторяется до `ConfirmClosed`, чтобы временная ошибка SQLite не потеряла
закрытие. Одновременно поддерживается одна виртуальная позиция; состояние не
восстанавливается после рестарта.

### Хранилище

`storage.Store` предоставляет:

- `Open(driverName, dsn)` / `New(*sql.DB)` и `Close()`;
- `Check(ctx)` — read-only SQLite `PRAGMA quick_check`;
- `Migrate(ctx)`;
- `SaveSignal(ctx, strategy.Signal)`;
- `SaveSignalRecord(ctx, storage.SignalRecord) (id, error)`;
- `CloseVirtualSignal(ctx, id, storage.VirtualOutcome)`;
- `SaveTrade(ctx, storage.Trade)`;
- `Stats(ctx) (storage.Stats, error)`.

Пакет регистрирует CGo-free драйвер `modernc.org/sqlite` под именем `sqlite`.
`Open` валидирует непустые driver/DSN и выполняет `Ping`; ошибки операций получают
контекст и сохраняют исходную ошибку для `errors.Is`/`errors.As`.

SQL-схема и ограничения готовности описаны в
[DEVELOPMENT.md](DEVELOPMENT.md#миграции-и-локальные-данные). Транзакционный
контракт между отправкой ордера и записью сделки не определён (`TODO`).

`SignalRecord.Indicators` опционален для совместимости с будущим executor, но
read-only runner всегда передаёт результат `EvaluateDetailed`. SQLite хранит
индикаторы отдельными типизированными колонками; существующие строки после
миграции имеют в них `NULL`.

## События

Формального event bus и версионированных схем событий нет. Между пакетами
передаются синхронные Go-значения (`Snapshot`, `Evaluation`, `Signal`, `Order`,
`Trade`).

Существующие структурированные логи:

| Сообщение | Поля | Источник |
| --- | --- | --- |
| `bot initialized` | `symbol`, `testnet` | `cmd/bot` |
| `storage initialized` | `driver` | `cmd/bot` |
| `paper trading initialized` | `virtual_balance_usd` | `cmd/bot` |
| `execution readiness confirmed` | `symbol` | `cmd/bot` |
| `execution readiness failed` | `symbol`, `reason` | `cmd/bot` |
| `save signal failed` | `symbol`, `type`, `error` | `cmd/bot` |
| `liquidation websocket disconnected` | `symbol`, `error`, `retry_in` | `cmd/bot` |
| `market snapshot initialized` | `symbol`, `candles`, `open_interest` | `cmd/bot` |
| `market snapshot failed` | `symbol`, `error` | `cmd/bot` |
| `open interest window has not advanced` | `symbol`, `timestamp` | `cmd/bot` |
| `stale open interest snapshot ignored` | `symbol`, `previous_timestamp`, `current_timestamp` | `cmd/bot` |
| `OI changed` | `symbol`, `previous`, `current` | `cmd/bot` |
| `signal generated` | `symbol`, `type`, `reason` | `cmd/bot` |
| `virtual signal opened` | `symbol`, `signal_id`, `entry`, `stop_loss`, `take_profit` | `cmd/bot` |
| `virtual signal closed` | `signal_id`, `exit_reason`, `exit_price`, `virtual_pnl` | `cmd/bot` |
| `bot stopped` | — | `cmd/bot` |
| `position opened` | `symbol`, `side`, `qty` | `execution` |

JSON-формат применяется логгером приложения; тесты executor могут передать другой
handler. Логи не являются стабильным внешним API. Схемы market events, закрытия
позиции, SL/TP и ошибок ещё не реализованы (`TODO`).

## Интеграция Bybit

Репозиторий фиксирует Bybit V5 Testnet и следующие целевые источники данных:

| Данные | Предусмотренный канал | Статус |
| --- | --- | --- |
| свечи 1m | REST `/v5/market/kline` | адаптер и unit-тесты готовы; `cmd/bot` вызывает read-only loop |
| open interest | REST `/v5/market/open-interest`, опрос каждые 30 секунд | интервал берётся из `oi_window_minutes`; сигнал считается только при новом timestamp OI |
| ликвидации | WebSocket `allLiquidation.{symbol}` | клиент, heartbeat, reconnect и окно из `liquidation_window_minutes` подключены; пример `5` |
| last/mark/index price | REST `/v5/market/tickers` | public адаптер готов; startup readiness вызывает только при наличии credentials |
| account mode | signed REST `/v5/account/info` | `REGULAR_MARGIN` preflight реализован; startup readiness вызывает при наличии credentials |
| доступный баланс | signed REST `/v5/account/wallet-balance` | Unified account-level USD реализован; startup readiness вызывает при наличии credentials |
| позиция | signed REST `/v5/position/list` | one-way read model реализован; startup readiness вызывает при наличии credentials |
| текущее leverage | signed REST `/v5/position/list` | one-way limits preflight реализован; startup readiness вызывает при наличии credentials |
| создание и закрытие ордеров | приватный Bybit V5 API | точные endpoints и payloads не зафиксированы (`TODO`) |

Адреса среды находятся в [configs/config.yaml](../configs/config.yaml): REST
`https://api-testnet.bybit.com` и WebSocket
`wss://stream-testnet.bybit.com/v5/public/linear`. REST base URL используется
адаптерами, которые создаются в `cmd/bot`.

REST-реализация сверена с официальными контрактами
[Get Kline](https://bybit-exchange.github.io/docs/v5/market/kline) и
[Get Open Interest](https://bybit-exchange.github.io/docs/v5/market/open-interest),
[Get Instruments Info](https://bybit-exchange.github.io/docs/v5/market/instrument),
[Get Tickers](https://bybit-exchange.github.io/docs/v5/market/tickers),
[Get Account Info](https://bybit-exchange.github.io/docs/v5/account/account-info),
[Get Wallet Balance](https://bybit-exchange.github.io/docs/v5/account/wallet-balance),
[Get Position Info](https://bybit-exchange.github.io/docs/v5/position),
[All Liquidation](https://bybit-exchange.github.io/docs/v5/websocket/public/all-liquidation),
[Integration Guidance](https://bybit-exchange.github.io/docs/v5/guide) на
2026-08-27.

Перед реализацией торговых методов нужно сверить актуальную официальную
спецификацию Bybit и зафиксировать:

- rate limits, retry/backoff и классификацию ошибок;
- idempotency/link order ID и поведение при неоднозначном timeout;
- шаги количества/цены и минимальный номинал;
- целевое leverage и условия допустимого изменения;
- правила market order, attached TP/SL, частичных исполнений и закрытия позиции.

Синхронизация времени для уже реализованной GET-подписи и всё перечисленное
остаются `TODO`; не следует выводить торговые детали из имён методов.

## Совместимость контрактов

Публичных потребителей у `pkg/` нет. Изменения внутри `internal/` не обещают
семантическую версионность, но должны одновременно обновлять компиляционные
проверки интерфейсов, unit-тесты и этот документ. Если появится внешний API, для
него потребуется отдельная схема версий и политика совместимости (`TODO`).
