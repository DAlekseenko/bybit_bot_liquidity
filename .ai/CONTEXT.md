# Контекст проекта

Краткая выжимка для нового участника или автоматизированного инструмента. Общие
правила работы — в [AGENTS.md](../AGENTS.md).

## Суть

- Go-модуль `bybit_bot_liquidity`, версия языка 1.24.
- Цель: MVP торгового бота для Bybit V5 Testnet, один символ и одна стратегия.
- Фактически: приложение загружает конфигурацию, мигрирует SQLite, создаёт public
  Bybit REST/WebSocket clients, накапливает ликвидации за настроенное окно
  (`5` минут в примере), запускает read-only market loop и при новом timestamp
  настроенного OI-окна сохраняет и логирует сигнал.
- Bybit adapter реализует public REST для linear 1m candles/OI, WebSocket
  `allLiquidation` и signed read-only account info/Unified balance/one-way
  position/current leverage; `PlaceOrder`, `ClosePosition` и исполнение сделок
  остаются заглушками. SQLite хранит сигналы вместе с полным снимком
  OI/volume/candle/liquidations и paper-trade plan/outcome; реальных сделок нет.
  Проект нельзя считать готовым к торговле.
- Public instrument rules реализованы; executor нормализует будущий request перед
  `PlaceOrder`, но сам executor ещё не подключён к `cmd/bot`.
- YAML настраивает OI-окно, период volume SMA, окно/порог ликвидаций и risk
  sizing. Стратегия использует только подтверждённые свечи; fee/slippage
  учитываются как фиксированные резервы размера позиции, не как фактические
  расходы.
- Runtime использует `EvaluateDetailed`: даже при `NONE` сохраняются все
  доступные индикаторы, thresholds и condition flags, поэтому причина
  отсутствия входа доступна для SQL-анализа без парсинга текста `reason`.
- Секция `trading` задаёт виртуальный баланс `$1,000`, одну paper-позицию,
  cooldown и дневной лимит. Paper-trade рассчитывает entry/SL/TP/fees/slippage и
  обновляет exit reason/net PnL по последующим свечам. Реальному executor секция
  не передаётся; `dry_run: false` не включает биржевую торговлю.
- Реализованы public last price и signed Unified available balance; main создаёт
  authenticated client при наличии env credentials и один раз выполняет
  execution readiness до запуска фоновых горутин.
- Signed `GetPosition` возвращает активную one-way позицию или `nil`; hedge mode
  отклоняется и настройки аккаунта автоматически не меняются.
- `AccountPreflight` принимает Unified status 3/4/5/6 только с
  `REGULAR_MARGIN`; margin mode автоматически не меняется.
- `LeveragePreflight` проверяет фактическое one-way leverage по
  `minLeverage`/`maxLeverage`/`leverageStep`; целевое плечо не выбирается,
  set endpoint не вызывается.
- `ExecutionReadinessPreflight` агрегирует account, отсутствие позиции,
  положительный available balance, market price и leverage/rules; required
  margin не рассчитывает. Startup-reporting логирует только symbol и безопасный
  reason; отказ не останавливает public loop.
- Optional private credentials читаются только из env, форматируются с
  редактированием; загрузчик жёстко разрешает только официальные Testnet endpoints.
- `docker-compose.yml` запускает непривилегированный `bot`, хранит SQLite в
  локальном `./data/trades.db` через bind mount и монтирует конфигурацию
  read-only; профиль `tools` предоставляет read-only SQLite CLI, отдельной СУБД
  нет.
  Дополнительный корпоративный CA поддерживается как BuildKit secret и
  gitignored read-only runtime mount без отключения TLS verification.

## Карта чтения

- вход и статус: [README.md](../README.md);
- компоненты и разрывы: [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md);
- точные бизнес-правила: [docs/DOMAIN.md](../docs/DOMAIN.md);
- Go-контракты и Bybit: [docs/API.md](../docs/API.md);
- команды и конфигурация: [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md);
- решения и открытые вопросы: [docs/DECISIONS.md](../docs/DECISIONS.md);
- текущее состояние и передача: [HANDOFF.md](HANDOFF.md).

## Код

`cmd/bot` — composition root и read-only loop; `internal/exchange` — типы и интерфейс;
`marketdata` — снимок и окно ликвидаций; `strategy` — сигнал; `risk` — план
позиции; `papertrade` — виртуальный lifecycle; `execution` — ограничения и
ордер; `storage` — SQL; `config`/`logger` — инфраструктура.

Главные ограничения: Testnet, один символ, одна позиция, настраиваемый cooldown
(`10` минут в примере), никаких реальных API в unit-тестах. Неизвестные детали
помечать `TODO`.

## Быстрая проверка

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
test -z "$(gofmt -l cmd internal)"
docker compose config
```

GitHub Actions выполняет полный локальный набор проверок на push и pull request.
В репозитории нет Makefile, отдельного линтера и отдельной команды миграции;
схема SQLite применяется автоматически при старте.
