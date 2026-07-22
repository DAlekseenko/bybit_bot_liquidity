# Передача работы

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
- `.ai/CONTEXT.md`, `.ai/CURRENT_TASK.md`, `.ai/HANDOFF.md` — актуальный контекст.

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
- SQLite-драйвер и миграции не подключены.

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
- `git diff --check` — успешно.
