# Crypto Momentum: границы контракта v1

Статус: **проект контракта CM-001**, не разрешение на торговлю. Полное ТЗ и
требования A01–A16 находятся в [Issue #55](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55);
этот документ фиксирует только границы для следующих узких Issues. Открытые
решения и их владельцы — в [DECISIONS.md](DECISIONS.md).

## Область и разделение ответственности

- Отдельный `cmd/momentum`. Legacy `cmd/bot` удалён (тег `legacy-liquidity-final`);
  его SQLite, SL/TP и preflight `REGULAR_MARGIN` не являются контрактами Momentum.
- Один чистый planner получает проверенный общий UTC-срез трёх символов и
  portfolio snapshot, не выполняет I/O и возвращает неизменяемый `TradePlan`.
  План хранит account/environment, strategy/run/config/profile/model IDs,
  scheduled_at, equity cutoff, prices, targets, формулу, ревизию и hash.
  Изменение требует новой ревизии, а не мутации сохранённого плана.
- Граница: `planner -> immutable TradePlan -> confirmation/authorization ->
  execution engine -> подтверждённые fills -> ledger`. Только execution engine
  может разместить, отменить или заменить биржевой ордер. Ack не является fill.
  Для private исполнения нужны отдельные подтверждения среды, счёта, лимитов и
  полномочий; этот документ их не даёт.
- Backtest и paper получают только simulated execution adapter: даже при
  credentials они не имеют trade transport. Testnet-адаптер до отдельного
  разрешения выключен; live отсутствует. Неизвестная позиция/заявка, stale data,
  неподтверждённое сокращение или потеря состояния блокируют новые входы.
- Config snapshot/hash и отчёт не содержат ключей, подписей или полных учётных
  данных. Testnet и mainnet имеют разные явные guards; mainnet невозможно
  включить переключением существующего `dry_run`.

### Universe символов из конфигурации (CM-008D)

`config.CanonicalSymbols` принимает ровно три разных ASCII имени: base из
`A-Z`/`0-9` минимум с одним символом, затем `USDT`. Ввод не обрезается и не
исправляется; результат — новая отсортированная копия, ошибка возвращает nil.
Это проверка синтаксиса, не доступности инструмента на Bybit или Testnet.
Парсер использует этот контракт вместо фиксированного whitelist, сохраняя
остальные guards и snapshot semantics. Это config-only prerequisite: оба
planner API `BaselineQuantity` и `BaselineLongTargets` пока не интегрированы с
выбранным universe; для end-to-end selectable planning требуется отдельная
planner-integration Issue.

## Точная арифметика: инженерное предложение

Источник денег, цен, Qty, fee и funding — проверенная десятичная строка;
парсинг в `math/big.Rat` сохраняет значение точно. Промежуточный `equity/3`
остаётся рациональным, а не округляется до float. Каноническая сериализация
рационального промежуточного значения — сокращённая пара числитель/знаменатель;
внешние Qty и деньги — конечные десятичные строки после явно заданного
округления. Qty для входа округляется **вниз** по актуальному `qtyStep`, не
вверх до минимума; после округления повторно проверяются min/max Qty/notional.
Формат и масштаб денежных `NUMERIC`, округление P&L и wire-контракт отдельно
уточняет storage/model Issue; до этого нельзя сохранять промежуточные дроби с
произвольным скрытым округлением. Legacy `float64` не использовать как источник
wire Qty или бюджетного допуска.

Equity для симуляции = wallet + unrealized P&L по mark; available margin —
отдельный допуск заявки, notional и заблокированная маржа не прибавляются к
equity. Ledger применяет каждый fill/fee/funding/transfer ровно один раз по
устойчивому ID. Контрольное тождество: `Δequity = realized P&L + Δunrealized
P&L - fees + funding_cashflow + net_transfers`; slippage уже в цене исполнения,
резерв не списывается как ещё одна комиссия. При неполной истории тождество
помечать `UNVERIFIED`, а не подгонять остаток.

## Baseline delta 1x: утверждённое правило D-01

Владелец утвердил D-01 2026-10-30: [decision/readback](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/63#issuecomment-6532663082).
Разрешение относится к pure planning, без торговых полномочий.

На плановый понедельник фиксируется один cutoff после завершения воскресной
UTC-свечи и получения свежего общего mark/equity snapshot; тот же snapshot и
`equity_reference/3` фиксируются на весь цикл. Для `start now` нужен отдельный
явный onboarding ID и свежий snapshot. Funding/fees после cutoff отражаются в
ledger, но молча не пересчитывают уже сохранённый план. Equity `<=0` блокирует
входы. Неактивная доля остаётся USDT; её, dust и недостижимую дельту не
перераспределять.

Утверждённая формула резерва при одинаковой оценочной цене `P` и комиссии одной
стороны `f`: для исходной позиции `Q0`, целевой `Qt` требовать
`P*Qt + f*P*max(Qt-Q0,0) + f*P*max(Q0-Qt,0) + f*P*Qt <= B`, где
`B=equity_reference/3`. Это резерв на текущую дельту и будущее закрытие, а не
списание комиссии. Для `equity=9000`, `P=100`, `f=0.001`, `B=3000`,
`qtyStep=0.001`: при `Q0=0` максимум после округления `Qt=29.940`, при
`Q0=10` — `Qt=29.950`. При `Q0=40` максимум `Qt=29.960` и сокращение
`10.040`; даже тогда новые покупки ждут подтверждения всех сокращений.
Пример не задаёт slippage, фактическую комиссию или биржевую маржу.
Cutoff, start-now и frozen-cycle правила утверждены для pure planning.
Pure baseline aggregate реализован в #63; enforcement этих lifecycle правил
остаётся за пределами #63.

### Скалярный расчёт CM-008B

В #206 реализован чистый `planner.BaselineQuantity(budgetUSDT, price, currentQty,
feeRate, qtyStep model.Decimal) (model.Decimal, error)`. B измеряется в USDT,
P — USDT за единицу базового актива; Q0, Qt и step — базовый актив,
f — безразмерная комиссия одной стороны, переданная явно. Примеры не задают
runtime fee/slippage. B не заменяется notional или available margin.

Аргументы проверяются в порядке сигнатуры: каждый Decimal валиден, B/Q0/f >=0,
P/step >0; step имеет точную конечную decimal форму. Остальные аргументы могут
быть рациональными. После проверки b=B/P, c=f*Q0; при b<c допустимого Qt нет.
При b<(1+f)*Q0 raw=b-c, иначе raw=(b+c)/(1+2*f). Только итоговая Qt округляется
вниз через FloorQty; исходная Q0 может быть вне сетки. Dust даёт ноль;
нулевой B допустим только при нулевом начальном резерве. f не ограничивается
выдуманным cap; min/max Qty/notional здесь не проверяются.

`BaselineSizingReason` имеет причины `ReasonInvalidSizingInput` и
`ReasonInfeasibleReserve`, распознаваемые errors.Is/errors.As. Error возвращает
строковое значение причины. Любая ошибка возвращает именно `model.Decimal{}`;
арифметические ошибки Decimal, включая overflow промежуточного результата за
4096 бит, передаются без обёртки или замены. Успех — точное неизменяемое
неотрицательное количество на шаге; входы не меняются. Ресурсный лимит Decimal
может отклонить математически допустимый расчёт, fallback отсутствует.

Резерв не списывается как фактическая fee. Скаляр не рассчитывает equity/3,
не проверяет snapshot/полноту позиций и не строит TradePlan: это остаётся #63,
включая запрет входов при equity<=0. I/O и execution отсутствуют.

### Полные LONG-цели CM-008E

`planner.BaselineLongTargets(symbols []string, snapshot model.PortfolioSnapshot,
qtySteps map[string]model.Decimal, feeRate model.Decimal) ([]model.Target, error)`
использует ровно три канонических символа, полученных из
`config.CanonicalSymbols`. Caller передаёт выбранный universe, например
`cfg.Values().Symbols`; порядок входного списка не влияет на отсортированный
результат. Синтаксическая валидность не подтверждает доступность инструмента
на Bybit/Testnet.

После canonicalization helper сохраняет прежние проверки: непустой Hash
снимка; точные keysets выбранного universe в Prices/Positions/qtySteps,
включая явно переданный zero для flat позиции; затем валидность и
положительность equity. Missing position не означает flat. Ошибка
canonicalization и structural failure возвращают строго nil и существующий
`ReasonInvalidBaselineInput`; `ReasonNonPositiveEquity` при equity<=0 и другие
typed reasons сохраняют прежний контракт.

Точный бюджет B=equity/3 в USDT передаётся в существующий
`BaselineQuantity` по отсортированному выбранному universe; P — USDT за базовый
актив, Q0/Qt/step — базовый актив, f — безразмерная доля одной стороны. Scalar
validation, reserve и округление не дублируются. Любая Decimal или scalar ошибка
передаётся тем же error object без обёртки и возвращает строго nil вместо
частичных целей. Входы не меняются, результаты независимы от caller state.

Configurable core helper готов: он принадлежит #218 и получает явно заданный
universe. В #63 чистый `planner.BaselineTargets(symbols []string,
snapshot model.PortfolioSnapshot, active map[string]bool,
qtySteps map[string]model.Decimal, feeRate model.Decimal) ([]model.Target, error)`
применяет active flags к этим полным целям.

Перед расчётом wrapper канонизирует symbols общим `config.CanonicalSymbols`; при
ошибке возвращает nil и `ReasonInvalidBaselineInput`. Active должен содержать
ровно выбранные три canonical keys. Затем wrapper всегда вызывает
`BaselineLongTargets(symbols, ...)`, в том числе при всех false. Ошибка helper
передаётся тем же объектом со строго nil targets. Только после успеха false
quantities обнуляются по `Target.Symbol`; true quantities, сортировка и число
долей не меняются, перераспределения нет. Universe поступает из явной
`cfg.Values().Symbols`; snapshot, active и qtySteps не служат источником
universe. Функция pure и не загружает YAML, не выполняет I/O и не выбирает
fallback symbols.

Cutoff lifecycle, min/max Qty/notional/delta normalization, TradePlan
construction, runtime и execution остаются вне wrapper.

## Детерминированный порядок и отказ

Подтверждённый владельцем D-07 задаёт только порядок offline backtest для
одинакового UTC timestamp: (1) применить funding к старым позициям; (2) оценить
эти позиции по mark и вычислить wallet/equity после funding; (3) проверить stop;
(4) выполнить rebalance только если stop не сработал. Если stop совпал по времени
с rebalance, первым применяется stop. Подтверждение: [каноническая запись
владельца](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55#issuecomment-6668604794);
этап funding/valuation — [Issue #356](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/356).

Стабильные event IDs и версия последовательности в manifest — технические
требования для воспроизводимости backtest, а не дополнительные финансовые
решения владельца. Существующая проверка reset отдельно требует исполнить
уменьшения и сверить fills/нулевые позиции до reset; она не входит в новое
подтверждение D-07, а post-close equity и сопоставимость reset остаются открытым
D-02. D-07 не разрешает торговое исполнение.

Остающееся предложением D-08: различать deadlines готовности данных, RPC timeout, ожидание fill/reconciliation,
максимальный возраст snapshot и плана. Числа — D-04/D-08, не подставлять
умолчания. Истечение data deadline запрещает новые входы без старых свечей.
Timeout отправки означает `UNKNOWN`, а не отказ или исполнение: сохранить intent,
сверить по LinkID/order history/fills, запретить конфликтующий повтор. После
рестарта незавершённый цикл сверять до любого нового входа; пропущенную неделю
не догонять автоматически.

## Режимы и изоляция счёта

| Режим | Данные/исполнение | Допуск |
| --- | --- | --- |
| backtest | frozen dataset / simulation | Без сети и trade API; manifest и hashes обязательны. |
| paper | public data / simulation | Credentials не добавляют trade API. |
| Testnet | проверенные public+private reads / отдельный engine | Заблокирован до D-04, D-06, D-08 и явного разрешения ручной проверки. |
| live | отсутствует | D-11 и отдельный security/risk/deploy gate; CM-001 не разрешает. |

Для isolated счёта нужен отдельный validated snapshot, не legacy account-wide
`totalAvailableBalance`. Источники-кандидаты (точные поля проверить по текущему
Bybit V5 перед адаптером):

| Данные | Источник | Fail-closed правило |
| --- | --- | --- |
| Режим/статус | account info | Unified, isolated, one-way и среда подтверждены; расхождение блокирует. |
| Equity/wallet/доступная маржа | wallet balance, USDT coin | Пустое/неподдерживаемое поле не равно нулю; available отдельно от equity. |
| Позиции и leverage | position list по всем трём символам | Чужая/неизвестная позиция или неверное плечо блокирует входы. |
| Mark, заявки, fills | ticker/mark и private order/execution history | Freshness и ownership обязательны; отсутствие active order не доказывает fill. |

Один выделенный USDT execution subaccount; дополнительные счета и смешение
капитала не входят в v1. Никаких автоматических switch margin/mode/leverage,
переводов, live-заявок или destructive migrations.
