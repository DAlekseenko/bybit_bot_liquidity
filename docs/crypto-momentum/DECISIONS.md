# Crypto Momentum: решения и вопросы

Статус CM-001: D-01 утверждён для pure planning, D-07 утверждён как порядок
учёта/backtest; остальные вопросы ниже остаются предложениями к review, не
применёнными runtime-решениями.
Каноническое ТЗ — [Issue #55](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55),
локальный контракт — [SPEC.md](SPEC.md). Исторические решения legacy MVP в
[общем журнале](https://github.com/DAlekseenko/bybit_bot_liquidity/tree/legacy-liquidity-final/docs/DECISIONS.md) (тег `legacy-liquidity-final`) не отменены и не распространяются автоматически
на Momentum.

| ID | Предложение / требуемый ответ | Блокирует |
| --- | --- | --- |
| D-01 | **Утверждено владельцем 2026-10-30:** baseline cutoff, frozen-cycle/start-now и резерв на дельту/будущее закрытие; [decision/readback](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/63#issuecomment-6532663082), формула и fixtures в SPEC. Только pure planning. | Скалярная Qty реализована #206; агрегатор, snapshot и TradePlan остаются #63. |
| D-02 | **Владелец:** подтвердить post-close equity для reset и сопоставимость с прежним тестом. | Reset golden comparison, не первый baseline replay. |
| D-03 | **Владелец:** предоставить прежние исходники, данные, период, расходы, funding, rounding, event order и допуски. До этого `NOT_VERIFIED`. | Утверждение исторической эквивалентности. |
| D-04 | **Владелец/оператор:** численные fee, slippage, freshness, REST/RPC deadlines и fallback для версии профиля. | Реалистичный paper и Testnet gate. |
| D-05 | **Владелец:** stop по умолчанию disabled; threshold/action и resume требуют отдельного решения. | Активация stop на бирже. |
| D-06 | **Владелец/оператор:** выделенный subaccount, isolated/one-way, фактическая доступность BTC/ETH/HYPE на нужной среде и географическая применимость; adapter повторно проверяет API. | Ручная Testnet проверка. |
| D-07 | **Утверждено владельцем для offline backtest:** (1) применить funding к старым позициям; (2) оценить их mark и вычислить post-funding wallet/equity; (3) проверить stop; (4) выполнить rebalance только если stop не сработал. [Каноническое подтверждение](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55#issuecomment-6668604794); этап 4 — [Issue #356](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/356). Это не решение по reset/D-02 и не разрешение торговли. | Эталон backtest. |
| D-08 | **Инженерное предложение:** timeout отправки = `UNKNOWN`, без слепого retry; численный stale-plan/absence contract ещё открыт. | Private отправки и recovery. |
| D-09 | **Инженерный выбор в отдельной Issue:** PostgreSQL driver/version, точный NUMERIC scale, migrations и rollback без потери данных. | Storage реализация. |
| D-10 | **Владелец + инженер:** значение «вход», модель liquidation, допуск P&L и cutoff. | Итоговый report. |
| D-11 | **Только владелец:** mainnet eligibility, отдельное явное разрешение live и изменений risk limits. Ни ТЗ, ни CM-001 их не дают. | Любой live этап. |

Инженерное предложение для арифметики: точные decimal strings → `math/big.Rat`
в вычислении → канонические рациональные промежуточные значения и Qty, округлённые
вниз по шагу. Ни одно предложение не снимает Testnet/read-only guard. Дальнейший
код допускается только в отдельных Issues с явным утверждённым контрактом;
вопросы владельцу остаются открытыми до его ответа и проверенного readback.

## Операторский контур — 2026-11-22

Владелец утвердил прямого Telegram-бота для управления и закрытый сайт для
статистики. Hermes и MCP исключены из операторского контура. План ведётся
в [эпике #127](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/127).
[HM-001](issues/HM-001.md) сохраняет предыдущий операторский контракт.

Telegram обращается к общему Go-сервису оператора. Первый пилот на Paper
показывает статус и позиции, отправляет уведомления и позволяет запретить
новые входы. Пауза сохраняет сопровождение открытых позиций и сверку.
Возобновление требует серверной сверки, одноразового подтверждения и отдельного
контракта. Локальное управление доступно при отказе Telegram.

Сайт получает проверенную статистику через API чтения с проверкой доступа.
Он различает режимы, указывает время обновления и покрытие данных.
Полномочия на развёртывание, торговые операции и изменение лимитов задаются
отдельно.
