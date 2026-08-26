# Контекст проекта

Краткая выжимка для нового участника или автоматизированного инструмента. Общие
правила работы — в [AGENTS.md](../AGENTS.md).

## Суть

- Go-модуль `bybit_bot_liquidity`, версия языка 1.24.
- Цель: MVP торгового бота для Bybit V5 Testnet, один символ и одна стратегия.
- Фактически: вычислительные пакеты и тесты есть, но приложение лишь загружает
  конфигурацию и ждёт остановки.
- Bybit client реализует public REST для linear 1m candles и OI; private methods
  остаются заглушками. WebSocket, SQLite-драйвер, миграции и торговый цикл не
  подключены. Проект нельзя считать готовым к торговле.

## Карта чтения

- вход и статус: [README.md](../README.md);
- компоненты и разрывы: [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md);
- точные бизнес-правила: [docs/DOMAIN.md](../docs/DOMAIN.md);
- Go-контракты и Bybit: [docs/API.md](../docs/API.md);
- команды и конфигурация: [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md);
- решения и открытые вопросы: [docs/DECISIONS.md](../docs/DECISIONS.md);
- текущая цель: [CURRENT_TASK.md](CURRENT_TASK.md), передача: [HANDOFF.md](HANDOFF.md).

## Код

`cmd/bot` — composition root; `internal/exchange` — типы и интерфейс;
`marketdata` — снимок; `strategy` — сигнал; `risk` — план позиции; `execution` —
ограничения и ордер; `storage` — SQL; `config`/`logger` — инфраструктура.

Главные ограничения: Testnet, один символ, одна позиция, cooldown 10 минут,
никаких реальных API в unit-тестах. Неизвестные детали помечать `TODO`.

## Быстрая проверка

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
test -z "$(gofmt -l cmd internal)"
```

GitHub Actions выполняет полный локальный набор проверок на push и pull request.
В репозитории нет Makefile, отдельного линтера, внешних Go-зависимостей и рабочей
команды миграции.
