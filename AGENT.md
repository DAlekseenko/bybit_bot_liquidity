# AGENT.md

## Project Overview

Разрабатывается MVP алгоритмического торгового бота для Bybit V5 API.

Цель проекта — проверить работоспособность торговой системы на основе рыночных сигналов и автоматического исполнения сделок.

На текущем этапе НЕ требуется создание production-ready решения.

Приоритеты:

1. Простота.
2. Надежность.
3. Проверяемость гипотез.
4. Минимальное количество зависимостей.
5. Чистая архитектура.

---

# Main Goal

Создать работающего торгового бота, который:

* получает рыночные данные от Bybit;
* рассчитывает торговые сигналы;
* открывает позиции на Testnet;
* закрывает позиции по TP/SL;
* сохраняет результаты торговли;
* предоставляет статистику по сделкам.

---

# Technology Stack

Language:

* Go 1.24+

Database:

* SQLite

Exchange:

* Bybit V5 API

Deployment:

* Docker

Configuration:

* YAML

Logging:

* slog

Testing:

* Go testing package

---

# MVP Scope

Разрешено реализовывать только следующий функционал.

## Market Data

Источники данных:

### Candles

Endpoint:

/v5/market/kline

Timeframe:

1m

### Open Interest

Endpoint:

/v5/market/open-interest

Update interval:

30 seconds

### Liquidations

Source:

Bybit Websocket Liquidation Stream

---

# Trading Strategy

## Long Signal

Открывать LONG если одновременно выполняются условия:

1. Open Interest вырос более чем на 3%.

2. Объем текущей свечи:

Volume > SMA20(volume) * 2

3. Бычья свеча:

Close > Open

4. Суммарные short-liquidations за последние 5 минут превышают 100 000 USD.

---

## Short Signal

Открывать SHORT если одновременно выполняются условия:

1. Open Interest вырос более чем на 3%.

2. Объем текущей свечи:

Volume > SMA20(volume) * 2

3. Медвежья свеча:

Close < Open

4. Суммарные long-liquidations за последние 5 минут превышают 100 000 USD.

---

# Risk Management

Использовать фиксированный риск.

## Position Size

Риск на сделку:

1% от баланса.

## Stop Loss

0.8%

## Take Profit

1.6%

Соотношение риск/прибыль:

1:2

---

# Trading Restrictions

Разрешено иметь только одну открытую позицию.

Запрещено открывать новую позицию если:

* уже есть активная позиция;
* предыдущая позиция была закрыта менее 10 минут назад.

---

# Storage

Использовать SQLite.

## trades

Поля:

* id
* symbol
* side
* entry_price
* exit_price
* qty
* pnl
* opened_at
* closed_at

## signals

Поля:

* id
* symbol
* signal_type
* reason
* created_at

---

# Project Structure

Использовать следующую структуру:

cmd/bot

internal/

exchange/
bybit/

marketdata/

strategy/

risk/

execution/

storage/

logger/

config/

pkg/

---

# Module Responsibilities

## exchange/bybit

Отвечает только за взаимодействие с биржей.

Не содержит торговой логики.

Должен предоставлять интерфейсы:

* GetCandles()
* GetOpenInterest()
* PlaceOrder()
* GetPosition()
* ClosePosition()

---

## marketdata

Получение и агрегация данных.

Не содержит торговых решений.

---

## strategy

Формирует сигналы.

Не отправляет ордера.

На выходе:

LONG
SHORT
NONE

---

## risk

Расчет:

* размера позиции;
* stop loss;
* take profit.

---

## execution

Исполнение сигналов.

Открытие и закрытие позиций.

---

## storage

Работа с SQLite.

---

# Architecture Rules

Обязательно соблюдать:

1. Dependency Injection.

2. Интерфейсы между модулями.

3. Минимальная связанность компонентов.

4. Отсутствие глобального состояния.

5. Контекст context.Context должен передаваться через все внешние вызовы.

6. Все горутины должны корректно завершаться.

7. Любой websocket должен автоматически переподключаться.

---

# Logging

Логировать:

## Market Events

* OI changed
* Volume spike
* Liquidation detected

## Trading Events

* Signal generated
* Position opened
* Position closed
* Stop loss hit
* Take profit hit

## Errors

* API errors
* Websocket disconnects
* Order errors

---

# Testing Requirements

Минимальное покрытие:

* strategy
* risk
* execution

Для внешних интеграций использовать моки.

Запрещено использовать реальные API в unit-тестах.

---

# Configuration Example

exchange:
testnet: true

symbol:
BTCUSDT

strategy:
oi_growth_percent: 3
volume_multiplier: 2
liquidation_threshold: 100000

risk:
risk_per_trade_percent: 1
stop_loss_percent: 0.8
take_profit_percent: 1.6

---

# Out Of Scope

Не реализовывать:

* Funding Rate
* Long/Short Ratio
* Order Book Imbalance
* Liquidity Clusters
* Multiple Symbols
* Multiple Strategies
* Portfolio Management
* Machine Learning
* Telegram Bot
* Web UI
* Kafka
* PostgreSQL
* Microservices

Все перечисленные функции относятся к будущим версиям.

---

# Definition of Done

Задача считается завершенной если:

1. Код компилируется.

2. Все тесты проходят.

3. Нет race conditions.

4. Бот может работать на Bybit Testnet.

5. Открываются и закрываются реальные тестовые позиции.

6. Сделки сохраняются в SQLite.

7. Формируется торговая статистика:

* Total Trades
* Win Rate
* Profit Factor
* Total PnL
* Average Trade
* Max Drawdown

---

# AI Development Rules

При генерации кода:

* использовать idiomatic Go;
* избегать избыточных абстракций;
* не создавать сложную инфраструктуру заранее;
* не реализовывать функционал вне MVP;
* всегда сначала создавать интерфейс, затем реализацию;
* писать код небольшими независимыми шагами;
* каждый новый модуль должен компилироваться отдельно;
* перед добавлением новой функциональности убедиться, что существующие тесты проходят.

Главный принцип:

Build the simplest working solution first.
