# Передача работы

## Дополнение 2026-08-31: 01 market-data resilience

В ветке `codex/01-market-data-resilience` добавлены безопасные улучшения
read-only market-data контура:

- Collector нормализует candles: сортирует и дедуплицирует интервал, предпочитая
  подтверждённую копию свечи.
- Idempotent public REST reads имеют budget из трёх попыток и экспоненциальную
  задержку с jitter; retry применяется только к 429/5xx, известным временным
  Bybit errors и сетевым timeout.
- Polling ждёт заданный интервал после окончания предыдущего snapshot, не делая
  catch-up burst.
- Public liquidation WebSocket переподключается с bounded exponential backoff и
  jitter; после разрыва окно ликвидаций очищается, потому что Bybit не даёт
  исторический replay и прежние события нельзя считать полным окном.

Реальная торговля не добавлена.

## Результат

На 2026-07-22 завершён первый инкремент `internal/exchange/bybit`. Реализованы
read-only методы публичного Bybit V5 REST:

- `GetCandles`: linear, 1m, limit 1–1000, преобразование строковых OHLCV и
  turnover, хронологическая сортировка, вычисление `Confirmed` по server time;
- `GetOpenInterest`: linear, `intervalTime=5min`, `limit=1`, преобразование
  значения и timestamp.

Общий transport проверяет абсолютный HTTP(S) base URL, входной символ, HTTP 2xx,
Bybit `retCode`, category/symbol ответа, размер payload и конечность чисел.
Добавлены `ErrInvalidArgument`, `ErrEmptyResult`, `ErrResponseTooLarge`,
`*HTTPError` и `*APIError`. Ошибки context/transport остаются доступны через
`errors.Is`/`errors.As`.

Приватные `PlaceOrder`, `GetPosition`, `ClosePosition` по-прежнему возвращают
`ErrNotImplemented`. `main` новый адаптер не создаёт, поэтому поведение запуска
приложения не изменилось.

## Изменённые файлы

- `internal/exchange/bybit/client.go` — public REST и обработка границ;
- `internal/exchange/bybit/client_test.go` — unit-тесты через fake
  `http.RoundTripper`, без сокетов и реального API;
- `AGENTS.md`, `README.md` — актуальный статус проекта;
- `docs/API.md`, `docs/ARCHITECTURE.md`, `docs/DOMAIN.md`,
  `docs/DEVELOPMENT.md`, `docs/DECISIONS.md` — контракт, поток, единицы,
  тестирование и решение D-008;
- `.ai/CONTEXT.md`, `.ai/HANDOFF.md` — актуальный контекст.

Неотслеживаемые/сторонние `.idea/*` не редактировались. Новых Go-зависимостей
нет.

## Проверки

- `go test ./internal/exchange/bybit` — успешно;
- `go test -cover ./internal/exchange/bybit` — успешно, 84,0% statements;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- локальные Markdown-ссылки и `git diff --check` — успешно.

Go-команды в ограниченной среде использовали репозиторный `.gocache`; обычные
команды проекта от этого не меняются.

Дополнительно выполнены два ручных read-only запроса к Bybit Testnet для kline и
open interest. Оба вернули `retCode: 0`, а фактические envelope и поля совпали с
парсером. Ключи, приватные endpoints и торговые операции не использовались.

## Известные ограничения

- Контракт намеренно фиксирован на `category=linear`, candles `1m` и OI `5min`.
- Retry/backoff, rate-limit policy и метрики ещё не определены.
- `Confirmed` — вычисляемый признак: REST kline не передаёт его отдельно.
- Live-запросы не входят в автоматический test suite; unit-тесты полностью
  изолированы через fake transport.
- WebSocket liquidation stream отсутствует.
- Приватная аутентификация и методы ордеров/позиций отсутствуют.
- `marketdata`, стратегия и executor не собраны в рабочий цикл приложения.
- SQLite-драйвер и миграции на том этапе ещё не были подключены (см. новое
  дополнение ниже).

## Следующий шаг

Следующий инкремент владельцем не выбран (`TODO`). Ближайший безопасный кандидат —
покрыть `marketdata.Collector` тестами и собрать отменяемый read-only цикл,
который хранит предыдущее OI и только логирует рассчитанный сигнал. WebSocket,
приватная торговля и хранение следует планировать отдельными задачами после
закрытия соответствующих решений в
[docs/DECISIONS.md](../docs/DECISIONS.md#открытые-решения).

## Дополнение 2026-07-22: исправление config

По запросу исправлена ошибка загрузчика конфигурации: неизвестные ключи в секциях
`strategy` и `risk` больше не пытаются парситься как числа и игнорируются
согласованно с остальными секциями. Известные числовые ключи по-прежнему
возвращают ошибку при некорректном значении.

Изменены:

- `internal/config/config.go`;
- `internal/config/config_test.go`;
- `docs/DEVELOPMENT.md`;
- `.ai/HANDOFF.md`.

Проверки после правки:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-22: read-only market loop

Создана ветка `codex/mvp-readonly-loop`. В `cmd/bot` подключён следующий
инкремент MVP без торгового исполнения:

- создаются `bybit.Client`, `marketdata.Collector` и `strategy.Strategy`;
- первый snapshot инициализирует предыдущий OI;
- последующие snapshots запускаются каждые 30 секунд, передают предыдущий и
  текущий OI в стратегию и логируют `signal generated`;
- WebSocket ликвидаций отсутствует, поэтому loop передаёт пустой список
  ликвидаций;
- `execution.Executor`, SQLite и приватные Bybit методы не вызываются.

Изменены:

- `.gitignore` — `.idea/` исключён из Git;
- `cmd/bot/main.go`, `cmd/bot/loop.go`, `cmd/bot/loop_test.go`;
- `internal/marketdata/collector_test.go`;
- `AGENTS.md`, `README.md`, `docs/API.md`, `docs/ARCHITECTURE.md`,
  `docs/DEVELOPMENT.md`, `docs/DECISIONS.md`;
- `.ai/CONTEXT.md`, `.ai/HANDOFF.md`.

Проверки после правки:

- `gofmt -w cmd internal/marketdata`;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-22: SQLite storage

Создана ветка `codex/mvp-sqlite-storage`. Подключён CGo-free драйвер
`modernc.org/sqlite` v1.39.1 с сохранением совместимости Go 1.24:

- `storage.Open` валидирует driver/DSN, открывает и проверяет соединение;
- `cmd/bot` при старте применяет идемпотентную миграцию и закрывает БД;
- read-only loop сохраняет каждый рассчитанный сигнал, не вызывая executor;
- добавлены тесты миграции, записи сигнала/сделок и статистики на временной БД;
- D-010 фиксирует выбор драйвера и оставшиеся ограничения миграций.

Проверки после SQLite-инкремента:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: public liquidation WebSocket

Создана ветка `codex/mvp-liquidation-websocket`. Подключён актуальный public
Bybit V5 topic `allLiquidation.{symbol}`:

- `Buy` преобразуется в ликвидированную LONG-позицию, `Sell` — SHORT;
- payload проверяется по symbol, side, timestamp, price и qty;
- клиент отправляет JSON heartbeat каждые 20 секунд;
- `cmd/bot` переподключается через 5 секунд и корректно завершает обе фоновые
  горутины по root context;
- `marketdata.LiquidationWindow` хранит потокобезопасную копию событий за пять
  минут, которая передаётся в каждый snapshot;
- unit-тесты используют fake dialer/connection и не открывают сокеты.

Проверки после WebSocket-инкремента:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: private config safety

Read-only pipeline зафиксирован commit `d39141a`, создана ветка
`codex/mvp-private-config`. Реализован безопасный конфигурационный фундамент:

- credentials читаются только из `BYBIT_API_KEY`/`BYBIT_API_SECRET`;
- отсутствие обоих допустимо, неполная пара отклоняется;
- `exchange.api_key`/`exchange.api_secret` в YAML запрещены;
- `Credentials.String`/`GoString` не раскрывают значения;
- разрешены только `testnet=true` и официальные REST/WSS linear Testnet endpoints;
- private transport и торговые вызовы по-прежнему отсутствуют.

Проверки после private config safety:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: удаление CURRENT_TASK

По решению владельца удалён дублирующий `.ai/CURRENT_TASK.md`. Текущий scope
задаётся запросом пользователя или внешним issue, фактическое состояние и
следующие шаги передаются через `.ai/HANDOFF.md`. Ссылки и инструкции обновлены.

## Дополнение 2026-07-23: instrument rules

Private config safety зафиксирован commit `de47211`, создана ветка
`codex/mvp-instrument-rules`. Реализован следующий безопасный инкремент:

- `GetInstrumentRules` читает linear `/v5/market/instruments-info`;
- проверяются symbol/category/status и все price/lot numeric limits;
- executor перед будущим `PlaceOrder` запрашивает свежие rules;
- Qty округляется вниз до `qtyStep`, SL/TP — до ближайшего `tickSize`;
- после квантования проверяются min/max quantity, notional, price range и
  направление SL/TP;
- десятичное квантование использует стандартный `math/big`, новой зависимости нет;
- tests используют fake HTTP/exchange и не отправляют реальные заявки.

Проверки после instrument rules:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: execution inputs

Instrument rules зафиксированы commit `6a0e313`, создана ветка
`codex/mvp-execution-inputs`. Реализованы источники будущих входов executor:

- public `GetLastPrice` читает linear ticker last/mark/index price;
- signed `GetAccountBalance` читает `UNIFIED.totalAvailableBalance` в USD;
- V5 HMAC GET signature использует timestamp, API key, receive window 5000 и
  каноническую query string;
- подпись покрыта фиксированным тест-вектором;
- client formatting не раскрывает API key/secret;
- `cmd/bot` создаёт authenticated client только при полной env-паре;
- wallet endpoint автоматически не вызывается, trade methods остаются
  `ErrNotImplemented`.

Проверки после execution inputs:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: position read model

Execution inputs зафиксированы commit `d2ece42`, создана ветка
`codex/mvp-position-read-model`. Реализован следующий read-only инкремент:

- signed `GetPosition` читает linear `/v5/position/list` по одному symbol;
- активная one-way запись преобразуется в `exchange.Position`;
- нулевая one-way запись возвращает `nil`;
- hedge mode отклоняется через `ErrUnsupportedPositionMode`, switch-mode не
  вызывается;
- проверяются category, symbol, positionIdx, side, size, avgPrice и createdTime;
- `PlaceOrder` и `ClosePosition` остаются `ErrNotImplemented`;
- unit-тесты используют fake HTTP transport и фиксированный вектор подписи.

Проверки после position read model:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-23: account preflight

Position read model зафиксирован commit `e0d6769`, создана ветка
`codex/mvp-account-preflight`. Реализован следующий read-only инкремент:

- `exchange.AccountInfo` хранит Unified status, margin mode и updated time;
- signed `GetAccountInfo` читает `/v5/account/info` без query-параметров;
- ответ проверяется и преобразуется в типы `exchange`;
- `execution.AccountPreflight` допускает Unified status `3/4/5/6` только с
  `REGULAR_MARGIN`;
- classic/неизвестный status, isolated/portfolio margin и отсутствующий updated
  time отклоняются sentinel-ошибками;
- preflight не вызывает switch/set endpoints и не подключён к `cmd/bot`;
- unit-тесты используют fake source/HTTP transport и фиксированный HMAC-вектор.

Проверки после account preflight:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bot` — успешно;
- `git diff --check` — успешно.

## Дополнение 2026-07-27: leverage preflight

Account preflight зафиксирован commit `dec2f93`, работа продолжена в ветке
`codex/mvp-leverage-preflight`. Реализован read-only leverage-инкремент без
изменения настроек аккаунта:

- `exchange.InstrumentRules` дополнен `MinLeverage`, `MaxLeverage` и
  `LeverageStep`;
- signed `GetLeverage` читает linear `/v5/position/list` по одному symbol,
  принимает только one-way `positionIdx=0` и возвращает server time;
- `GetInstrumentRules` преобразует актуальный `leverageFilter`;
- `execution.LeveragePreflight` через узкий `LeverageSource` проверяет symbol,
  timestamp, статус `Trading`, положительные limits, диапазон и точный
  десятичный шаг от `minLeverage`;
- source и ожидаемые ошибки сохраняются для `errors.Is`; malformed data и
  неподдерживаемая конфигурация отклоняются fail-closed;
- целевое leverage не выбрано, `set-leverage` не реализован и preflight не
  подключён к `cmd/bot`.

Изменены:

- `internal/exchange/types.go`;
- `internal/exchange/bybit/client.go`, `client_test.go`;
- `internal/execution/leverage.go`, `leverage_test.go`;
- `AGENTS.md`, `README.md`, `docs/API.md`, `docs/ARCHITECTURE.md`,
  `docs/DOMAIN.md`, `docs/DEVELOPMENT.md`, `docs/DECISIONS.md`;
- `.ai/CONTEXT.md`, `.ai/HANDOFF.md`.

Проверки после leverage preflight:

- `go test ./internal/execution ./internal/exchange/bybit` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/bybit-bot-leverage-preflight ./cmd/bot` — успешно;
- `git diff --check` — успешно.

Следующий шаг требует отдельного бизнес-решения: выбрать целевое плечо и
допустимость его изменения либо оставить его внешней настройкой, затем проверить
достаточность маржи и подключить account/leverage preflight к будущему торговому
orchestrator. Private order/close transport по-прежнему отсутствует.

## Дополнение 2026-07-27: execution readiness

Leverage preflight зафиксирован commit `fced968`, создана ветка
`codex/mvp-execution-readiness`. Реализована единая read-only граница готовности
без runtime wiring:

- `ExecutionReadinessSource` объединяет минимальные account/leverage sources с
  balance, ticker и position reads;
- `ExecutionReadinessPreflight.Check` последовательно проверяет Unified
  `REGULAR_MARGIN`, отсутствие позиции, account balance, market price и текущее
  leverage по instrument rules;
- успех возвращает value snapshot с symbol, account, balance, price и leverage;
- malformed balance/price, нулевой available balance и вложенные
  account/position/leverage ограничения имеют проверяемые sentinel-ошибки;
- source errors оборачиваются с этапом и сохраняются для `errors.Is`;
- required margin, freshness window и target leverage намеренно не вычисляются;
  order/set endpoints не вызываются.

Изменены:

- `internal/execution/readiness.go`, `readiness_test.go`;
- `AGENTS.md`, `README.md`, `docs/API.md`, `docs/ARCHITECTURE.md`,
  `docs/DOMAIN.md`, `docs/DEVELOPMENT.md`, `docs/DECISIONS.md`;
- `.ai/CONTEXT.md`, `.ai/HANDOFF.md`.

Проверки после execution readiness:

- `go test ./internal/execution` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/bybit-bot-execution-readiness ./cmd/bot` — успешно;
- `git diff --check` — успешно.

Следующий безопасный инкремент — решить runtime-политику read-only readiness:
когда он вызывается при наличии credentials, является ли ошибка startup-fatal и
как логируется результат без account values. До подключения executor также
остаются target leverage, freshness/required margin и private order/close
transport.

## Дополнение 2026-07-27: startup readiness reporting

Execution readiness зафиксирован commit `cb87cca`, создана ветка
`codex/mvp-readiness-runtime`. Реализовано безопасное startup wiring:

- без credentials execution readiness полностью пропускается;
- при полной env-паре `cmd/bot` создаёт authenticated client и один раз вызывает
  readiness после миграции SQLite, но до запуска WebSocket goroutines;
- успех логируется как `execution readiness confirmed` только с symbol;
- отказ логируется как `execution readiness failed` с symbol и ограниченным
  `reason`; snapshot, balance/leverage values и исходный source error не
  логируются;
- отказ non-fatal для текущего public read-only loop, потому что executor и
  торговые endpoints отсутствуют;
- ошибки классифицируются по безопасным sentinel-категориям, неизвестная source
  ошибка становится `source_error`.

Изменены:

- `cmd/bot/main.go`;
- `cmd/bot/readiness.go`, `readiness_test.go`;
- `AGENTS.md`, `README.md`, `docs/API.md`, `docs/ARCHITECTURE.md`,
  `docs/DOMAIN.md`, `docs/DEVELOPMENT.md`, `docs/DECISIONS.md`;
- `.ai/CONTEXT.md`, `.ai/HANDOFF.md`.

Проверки после startup readiness reporting:

- `go test ./cmd/bot ./internal/execution` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/bybit-bot-readiness-runtime ./cmd/bot` — успешно;
- `git diff --check` — успешно.

Реальный Testnet startup с credentials не выполнялся: автоматические тесты
используют fake checker и не открывают сеть. Перед подключением executor
readiness должен стать обязательным торговым gate; также остаются target
leverage, freshness/required margin и private order/close transport.

## Дополнение 2026-07-28: configurable strategy/risk parameters

Startup readiness reporting зафиксирован commit `c27b0f2`. По запросу владельца
в канонический YAML добавлена точная схема параметров:

- `strategy.oi_window_minutes`, `oi_growth_percent`, `volume_sma_period`,
  `volume_multiplier`, `liquidation_window_minutes`,
  `liquidation_threshold_usd`;
- `risk.risk_per_trade_percent`, `stop_loss_percent`,
  `take_profit_percent`, `estimated_round_trip_fee_percent`,
  `slippage_allowance_percent`.

Реализовано:

- загрузчик имеет безопасные дефолты, применяет значения YAML, валидирует
  конечность чисел, поддерживаемые Bybit OI-интервалы, SMA 1–998, положительное
  liquidation-окно и неотрицательные fee/slippage reserves;
- `GetOpenInterest` принимает минуты и преобразует их в `5min`, `15min`,
  `30min`, `1h`, `4h` или `1d`;
- collector получает OI-окно из конфига, а runner рассчитывает сигнал только
  после перехода к новому timestamp OI;
- стратегия использует только `Confirmed`-свечи и настраиваемый SMA-период;
- одно liquidation-окно передаётся в accumulator и strategy;
- risk sizing добавляет fee/slippage к оценочному проценту потерь, уменьшая
  `Qty`, но не меняя SL/TP.

Изменены `AGENTS.md`, `configs/config.yaml`, `cmd/bot`, `internal/config`,
`internal/exchange`, `internal/exchange/bybit`, `internal/marketdata`,
`internal/strategy`, `internal/risk`, `README.md`, затронутые `docs/*` и
`.ai/*`. Новых зависимостей нет.

Проверки:

- фокусные package tests — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/bybit-bot-configurable-strategy-risk ./cmd/bot` — успешно;
- локальные Markdown-ссылки и `git diff --check` — успешно.

В sandbox Go-команды использовали `GOCACHE=/private/tmp/bybit-bot-go-cache`;
обычные команды проекта не меняются.

Изменения не закоммичены. Executor по-прежнему не подключён, поэтому risk-поля
готовы для его будущей композиции, но не запускают торговлю. Верхняя граница
`liquidation_window_minutes` пока не определена; большое значение увеличит
потребление памяти. После рестарта runner сначала должен получить предыдущий
timestamp OI.

## Дополнение 2026-07-28: trading configuration

В `configs/config.yaml` добавлена запрошенная секция:

```yaml
trading:
  dry_run: false
  virtual_balance_usd: 1000
  max_open_positions: 1
  cooldown_minutes: 10
  max_trades_per_day: 10
```

`internal/config.TradingConfig` загружает все пять полей. Без явного YAML
`dry_run` использует безопасный дефолт `true`; остальные дефолты — `1000`, `1`,
`10` и `10`. Валидация сохраняет односимвольную границу
`max_open_positions == 1`, требует положительные cooldown/дневной лимит и
защищает преобразование минут в `time.Duration` от переполнения. Значение
`risk_per_trade_percent: 1.0`, уже настроенное владельцем в YAML, сохранено.

Секция намеренно пока не передаётся в реальный `execution.Executor`: private
order/close transport и торговый orchestrator отсутствуют, поэтому
`dry_run: false` не активирует торговлю. Перед первым `PlaceOrder` trading gates
должны стать единым fail-closed execution gate.

Изменены `configs/config.yaml`, `internal/config/config.go`,
`internal/config/config_test.go`, затронутые постоянные документы и `.ai/*`.
Новых зависимостей нет.

Проверки после добавления `trading`:

- `go test ./internal/config` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/bybit-bot-trading-config ./cmd/bot` — успешно;
- локальные Markdown-ссылки и `git diff --check` — успешно.

## Дополнение 2026-07-28: paper-trade signal analytics

По выбору владельца используется `trading.virtual_balance_usd: 1000`.
Реализован `internal/papertrade` и подключён к каждому read-only snapshot:

- signal price — Close последней подтверждённой сигнальной свечи;
- hypothetical entry ухудшается на configured slippage;
- `risk.Manager` рассчитывает virtual Qty, SL и TP от баланса `$1,000`;
- estimated fees/slippage сохраняются в USD;
- последующие подтверждённые свечи закрывают позицию по SL/TP;
- если свеча касается обоих уровней, выбирается `STOP_LOSS`;
- virtual PnL включает ухудшенный entry и round-trip fee;
- position/cooldown/UTC-day limits применяются к paper-входам в памяти.

Таблица `signals` расширена колонками `signal_price`, `hypothetical_entry`,
`stop_loss`, `take_profit`, `estimated_fees`, `estimated_slippage`,
`virtual_qty`, `signal_candle_at`, `virtual_exit_price`, `exit_reason`,
`virtual_pnl`, `virtual_closed_at`. `Migrate` добавляет отсутствующие nullable
колонки к существующей таблице без потери строк.

Runner сохраняет plan и затем обновляет outcome. Pending outcome повторяется до
успешного SQLite update. Реальные ордера не отправляются. После рестарта
активная paper-позиция/cooldown/day counter пока не восстанавливаются, а
`storage.Stats` не агрегирует virtual PnL.

Изменены `configs/config.yaml`, `internal/config`, новый `internal/papertrade`,
`internal/storage`, `cmd/bot`, постоянные документы и `.ai/*`. Новых
зависимостей нет. Проверки после реализации:

- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `go test ./internal/papertrade ./internal/storage ./internal/config ./cmd/bot`
  — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o
  /private/tmp/bybit-bot-paper-analytics ./cmd/bot` — успешно;
- локальные Markdown-ссылки и `git diff --check` — успешно.

## Дополнение 2026-07-28: Docker Compose

Добавлены `docker-compose.yml`, `.env.example`, `certs/README.md` и усилен
`Dockerfile`. Compose запускает один непривилегированный `bot` без входящих
портов, монтирует конфигурацию и локальные CA read-only, хранит
`/app/data/trades.db` в именованном volume, ограничивает Docker logs и использует
read-only root filesystem, `tmpfs /tmp` и `no-new-privileges`.

В `storage.Store` добавлен read-only `Check(ctx)` на основе
`PRAGMA quick_check`; `cmd/bot -check-storage` используется healthcheck без
запуска Bybit clients. Runtime CA bundle копируется из Go build image без
сетевого `apk`. Дополнительный корпоративный CA передаётся в build как BuildKit
secret и читается на runtime через `SSL_CERT_DIR`. Локальные `certs/*.pem`,
`*.crt`, `*.cer` исключены из Git и Docker build context; private keys туда
помещать нельзя.

Проверки:

- `docker compose config --quiet` с обычным и custom-CA режимом — успешно;
- `docker build --check .` — успешно, warnings отсутствуют;
- `CUSTOM_CA_FILE=./certs/local-ca.pem docker compose build` — успешно;
- Compose smoke-test создал volume, мигрировал SQLite, получил status `healthy`,
  а явный `-check-storage` завершился успешно;
- TLS REST/WSS с локальным корпоративным CA проходит верификацию; Bybit Testnet
  после этого вернул внешний `403 CloudFront` о блокировке страны текущего
  egress, поэтому market snapshot в этой сети не получен;
- smoke-test остановлен через `docker compose down`, volume
  `bybit-liquidity-bot_bot-data` сохранён;
- `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...` и
  CGo-free Linux build — успешно.

Для текущей корпоративной сети перед Compose build нужен gitignored публичный
CA bundle в `certs/local-ca.pem` и
`CUSTOM_CA_FILE=./certs/local-ca.pem`. Обычная сеть использует
`CUSTOM_CA_FILE=/dev/null`. Реальные ордера по-прежнему не реализованы.

## Дополнение 2026-07-28: локальный SQLite-файл и Compose CLI

По запросу владельца существующая база безопасно скопирована из остановленного
именованного volume в gitignored `./data/trades.db`. Compose переключён на bind
mount `./data:/app/data`, поэтому файл доступен на host и не удаляется командой
`docker compose down -v`. Старый именованный volume не удалён и остаётся
резервной копией до отдельного явного решения.

`BOT_UID`/`BOT_GID` задают непривилегированному процессу права владельца
локального каталога. В локальном `.env` установлены UID/GID текущего
пользователя; `.env.example` документирует переносимую настройку.

Добавлен опциональный сервис `sqlite` профиля `tools` на закреплённом образе
`keinos/sqlite3:3.53.4`. Команда `docker compose run --rm sqlite` открывает
`/data/trades.db` только для чтения; это CLI, а не постоянный сервер.

README, `docs/DEVELOPMENT.md`, D-024 и `.ai/CONTEXT.md` обновлены под локальный
файл и команды анализа.

Проверено после переноса:

- `PRAGMA quick_check` локального `./data/trades.db` — `ok`, сохранено 9
  сигналов;
- бот пересоздан с bind mount и UID/GID владельца, status — `healthy`;
- встроенный `-check-storage` — успешно;
- Compose SQLite CLI прочитал тот же файл в read-only режиме и также получил
  `quick_check=ok`, 9 сигналов.

## Дополнение 2026-07-29: индикаторы в signals

По запросу владельца добавлен полный диагностический snapshot для каждого
сигнала. `strategy.Evaluate` сохранён как совместимая обёртка, а новый
`EvaluateDetailed` рассчитывает `Evaluation{Signal, Indicators}`. Расчёт
индикаторов больше не short-circuit: при провале OI всё равно вычисляются
volume/SMA, candle direction и обе суммы ликвидаций.

`Indicators` содержит:

- previous/current OI, timestamps, growth, configured threshold и pass flag;
- confirmed/required candle counts;
- current volume, SMA, ratio, threshold, multiplier и pass flag;
- open/close/direction/time сигнальной свечи;
- short/long liquidation USD, threshold и отдельные pass flags;
- LONG/SHORT setup flags, evaluation time и начало liquidation window.

Read-only runner передаёт snapshot в `storage.SignalRecord`. Startup migration
добавляет typed nullable-колонки к `signals`; старые строки сохраняются с
`NULL`. При недостатке свечей `indicators_complete=0`, а недоступные
candle/volume-поля сохраняются как `NULL`. `signal_candle_at` теперь
заполняется и для `NONE`.

Добавлены тесты полного расчёта при провале OI, передачи runner → storage,
сохранения всех typed indicator columns и обновления старой схемы. D-025,
README, DOMAIN/API/ARCHITECTURE/DEVELOPMENT и `.ai/CONTEXT.md` обновлены.

Проверки:

- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- CGo-free Linux build `./cmd/bot` — успешно;
- `test -z "$(gofmt -l cmd internal)"`, `docker compose config --quiet` и
  `git diff --check` — успешно;
- перед live migration создана gitignored копия
  `data/trades.pre-indicators-20260729.db`;
- Compose image пересобран, bot — `healthy`, `-check-storage` — успешно;
- live schema содержит `oi_growth_percent`, `volume_sma`,
  `short_liquidations_usd`, `long_setup_met` и остальные новые колонки.

Текущий egress получает внешний Bybit/CloudFront `403 country blocked` и
WebSocket `bad handshake`; поэтому после миграции новый фактический сигнал с
индикаторами ещё не записан. Это сетевое ограничение, не ошибка SQLite:
автоматические тесты проверяют insert/read полного snapshot.

## Дополнение 2026-08-27: интеграция runtime и повторное ревью

В текущую ветку интегрирован безопасный read-only/Testnet runtime из
`origin/codex/mvp-readiness-runtime`: public REST/WebSocket, SQLite, paper-trade
аналитика и signed read-only readiness. `PlaceOrder` и `ClosePosition` остаются
`ErrNotImplemented`; реальная торговля не включена.

При разрешении конфликтов и повторном ревью исправлены:

- сохранены проверки `NaN`/`Inf`, пустого symbol и локальная сериализация
  executor; неуспешный `PlaceOrder` больше не оставляет ложную открытую позицию;
- `GetLeverage` переведён с несуществующего `/v5/position/symbol-info` на
  официальный `/v5/position/list`, а position time — с `openTime` на строковый
  `createdTime`;
- закрытый liquidation channel больше не создаёт бесконечный поток нулевых
  событий;
- ошибка SQLite при сохранении сигнала не продвигает OI baseline, поэтому тот же
  OI snapshot можно повторно записать;
- конфигурация рано отклоняет lowercase/whitespace symbol, неподдерживаемый
  storage driver, пустой DSN и неверный logging level;
- normalizer проверяет entry price по min/max price инструмента;
- добавлены тесты logger и `Liquidation.USDValue`;
- язык исходников сохранён на `go 1.24.13`, но CI/Docker/обычный auto toolchain
  используют Go 1.26.7: Go 1.24 снят с поддержки и содержит достижимые
  уязвимости стандартной библиотеки. Решение записано как D-027.

Проверки после интеграции:

- `go mod verify` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- CGo-free Linux amd64 build `./cmd/bot` — успешно;
- `test -z "$(gofmt -l cmd internal)"` — успешно;
- `docker compose config --quiet` — успешно;
- `git diff --check` — успешно;
- GitHub Actions `CI` — успешно.

## Дополнение 2026-08-30: fail-fast URL в Bybit-клиенте

`bybit.NewClient` теперь возвращает `(*Client, error)` и проверяет base URL
до создания клиента. Проверенный `url.URL` хранится в `Client` как значение;
`endpoint` копирует его перед добавлением path и query. Невалидный URL больше не
создаёт объект с отложенной ошибкой при первом REST-вызове.

Изменены тесты и benchmark конструктора, обновлён контракт в `docs/API.md`.

Проверки:

- `go mod verify` — успешно;
- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `test -z "$(gofmt -l cmd internal)"` и `git diff --check` — успешно;
- `BenchmarkEndpoint` — 557.6 ns/op, 408 B/op, 11 allocs/op на Apple M1.

Pull request: [#4 Make Bybit client URL validation fail fast](https://github.com/DAlekseenko/bybit_bot_liquidity/pull/4).

## Дополнение 2026-08-30: исправление после merge-конфликта

После merge-конфликта в `client.go` были удалены настройки signed read-only
клиента (`recvWindow`, `now`, `NewAuthenticatedClient` и безопасное
форматирование). В `client_test.go` отсутствовали соответствующие helpers, а
один вызов `GetOpenInterest` потерял обязательный параметр окна OI. Восстановлен
согласованный контракт runtime-клиента и его test helpers.

Проверки после исправления:

- `go test ./...` — успешно;
- `go test -race ./...` — успешно;
- `go vet ./...` — успешно;
- `go build ./...` — успешно;
- `test -z "$(gofmt -l cmd internal)"` и `git diff --check` — успешно.
