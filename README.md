# Bybit trading engine — Crypto Momentum

Go-система автоматической торговли на Bybit V5 по заранее утверждённому
детерминированному алгоритму. Цель — в [PROJECT_GOAL.md](PROJECT_GOAL.md).

Первая стратегия — **Crypto Momentum**: портфель из трёх USDT perpetual, дневной
ROC30, доля `equity/3` на символ, недельная ребалансировка, LONG или USDT.
Спецификация — [docs/crypto-momentum/SPEC.md](docs/crypto-momentum/SPEC.md),
задачи — GitHub Issues (эпик #55).

Сейчас система работает только с публичными данными Bybit Testnet и
офлайн-бэктестом; заявки не отправляются.

## Структура

| Путь | Что внутри |
| --- | --- |
| `cmd/momentum` | CLI; сейчас только `collect-once` |
| `internal/momentum/*` | model, planner, data, history, storage (PostgreSQL), backtest, signals, config |
| `internal/exchange`, `internal/exchange/bybit` | Адаптер Bybit V5: public/signed REST, WebSocket, правила инструментов |
| `internal/controlapi` | Политика авторизации операторского API |
| `configs/momentum.paper.yaml` | Пример конфигурации Momentum |
| `.ai/`, `AGENTS.md` | Правила и контекст для разработчиков и AI-агентов |

Прежний бот Liquidity (`cmd/bot`, SQLite, Docker) удалён; его код доступен по тегу
`legacy-liquidity-final`.

## Проверки

```bash
test -z "$(gofmt -l cmd internal)"
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Тесты PostgreSQL пропускаются без `MOMENTUM_TEST_POSTGRES_DSN` — одноразовой
локальной базы с именем `momentum_test_*`. В CI их запускает
[momentum-postgres.yml](.github/workflows/momentum-postgres.yml).

## Запуск

Однократный сбор публичных данных и запись signal batch в PostgreSQL:

```bash
MOMENTUM_POSTGRES_DSN='postgres://…' \
  go run ./cmd/momentum collect-once -config configs/momentum.paper.yaml -timeout 2m
```

Торговых вызовов команда не делает.
