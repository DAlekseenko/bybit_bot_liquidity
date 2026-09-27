# Инструкции для участников разработки

Файл задаёт общие, независимые от конкретного AI-инструмента правила работы с
репозиторием. Для начала Issue прочитайте компактный контекст, последний handoff,
контракт Issue и состояние Git. Остальные документы открывайте только при
обнаруженной зависимости задачи:

- бизнес-правила — [docs/DOMAIN.md](docs/DOMAIN.md);
- компоненты и границы — [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md);
- программные и внешние контракты — [docs/API.md](docs/API.md);
- локальные команды — [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md);
- принятые и открытые решения — [docs/DECISIONS.md](docs/DECISIONS.md);
- компактный контекст и передача работы — [.ai/CONTEXT.md](.ai/CONTEXT.md) и
  [.ai/HANDOFF.md](.ai/HANDOFF.md).

## Фактическое состояние

Это каркас MVP торгового бота для Bybit Testnet, а не готовая торговая система.
Точка входа читает конфигурацию, настраивает логирование, создаёт публичный
Bybit REST-адаптер и публичный WebSocket ликвидаций, мигрирует SQLite и запускает
read-only цикл market data каждые 30 секунд: свечи/OI и окно ликвидаций
загружаются по настроенным параметрам, предыдущий OI хранится в памяти, а сигнал
рассчитывается, сохраняется и логируется при новом timestamp OI. Стратегия
использует только подтверждённые свечи; risk sizing учитывает настроенные
резервы комиссии и проскальзывания. Paper-trade контур с виртуальным балансом
сохраняет расчётный вход/выход и net PnL в `signals`. Подписанные read-only account
info/wallet balance/one-way position/leverage реализованы; preflight допускает
только Unified `REGULAR_MARGIN` и проверяет текущее плечо по instrument limits;
агрегированный readiness дополнительно требует отсутствие позиции,
положительный available balance и корректную market price. При наличии env
credentials readiness один раз выполняется на startup и пишет только безопасный
итог; его ошибка не останавливает public read-only loop. Создание и закрытие
ордеров не подключены. `dry_run: false` не активирует реальную торговлю. Не
описывайте paper-trade как биржевое исполнение и не снимайте защитный режим
Testnet без отдельного явного решения.

Если документация расходится с кодом, источники имеют такой приоритет:

1. Компилируемый код, тесты, `go.mod`, `Dockerfile`, `docker-compose.yml` и
   `configs/config.yaml` — что реализовано сейчас.
2. [docs/DOMAIN.md](docs/DOMAIN.md) — подтверждённые бизнес-правила.
3. [docs/DECISIONS.md](docs/DECISIONS.md) — принятые ограничения и причины.
4. [.ai/HANDOFF.md](.ai/HANDOFF.md) — временное состояние и передача работы.

Неизвестное помечайте `TODO` с конкретным вопросом; не подменяйте его догадкой.

## Структура проекта

| Путь | Ответственность |
| --- | --- |
| `cmd/bot` | Composition root и жизненный цикл процесса |
| `configs` | Версионируемый пример конфигурации Testnet |
| `internal/config` | Загрузка и проверка конфигурации |
| `internal/exchange` | Общие биржевые типы и полный интерфейс клиента |
| `internal/exchange/bybit` | Public REST/WebSocket, signed account/balance/position/leverage reads; private trade methods не реализованы |
| `internal/marketdata` | Получение снимка и окно ликвидаций без решений |
| `internal/strategy` | Чистая генерация `LONG`, `SHORT` или `NONE` |
| `internal/risk` | Размер позиции и уровни SL/TP |
| `internal/papertrade` | Виртуальные входы/выходы и net PnL для анализа |
| `internal/execution` | Account/leverage/readiness preflight, ограничения позиции и отправка заявки |
| `internal/storage` | SQLite-драйвер, SQL-схема, сделки, сигналы и статистика |
| `internal/logger` | Настройка структурированного `slog` |
| `pkg` | Резерв для стабильного публичного API; сейчас кода нет |
| `docs`, `.ai` | Долгоживущая документация и временный контекст работы |

Подробные зависимости и потоки не дублируются здесь: см.
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Стиль и границы кода

- Поддерживайте совместимость с Go 1.24 из `go.mod`; применяйте `gofmt`.
- Предпочитайте стандартную библиотеку и небольшие пакеты. Новую зависимость
  добавляйте только вместе с обоснованием и обновлением журнала решений, если она
  влияет на архитектуру.
- Передавайте зависимости через конструкторы. Интерфейсы объявляйте со стороны
  потребителя и держите минимальными.
- Не добавляйте глобальное изменяемое состояние. Внешние и потенциально долгие
  операции принимают `context.Context` первым аргументом.
- `strategy` и `risk` не выполняют I/O. Биржевой адаптер не содержит торговых
  правил. SQL остаётся внутри `storage`, координация — в `execution` или в
  composition root.
- Возвращайте ошибки вызывающему коду; добавляйте контекст при пересечении границ.
  Ожидаемые ветви могут иметь проверяемые sentinel-ошибки.
- Используйте структурированные поля `slog`, время хранения — UTC. Не логируйте
  секреты, подписи запросов или полные учётные данные.
- Сохраняйте текущие доменные типы и единицы измерения либо явно оформляйте их
  изменение в [docs/DOMAIN.md](docs/DOMAIN.md) и
  [docs/API.md](docs/API.md).
- Пишите табличные или точечные unit-тесты рядом с пакетом. Внешние API заменяйте
  fake/mock-реализациями; реальные заявки в автоматических тестах запрещены.
- Не редактируйте IDE-файлы и чужие несвязанные изменения. `.idea/` сейчас не
  является частью проекта.

## Ограничения MVP

MVP рассчитан на один символ и одну стратегию, фиксированный риск, не более одной
открытой позиции и паузу после закрытия. Фактические формулы и перечень того, что
не входит в MVP, находятся только в [docs/DOMAIN.md](docs/DOMAIN.md).

До подключения торговли необходимо отдельно решить отмеченные `TODO`: private
order/close transport поверх уже реализованной GET-аутентификации, целевое
leverage и его установка, восстановление состояния после перезапуска и
безопасную интеграционную проверку Testnet. Источники
account/balance/price/position/current leverage, account/leverage preflight,
агрегированный read-only readiness, базовые instrument limits и нормализация уже
реализованы, но торговый private wire-format пока отсутствует.

## Команды

Запускайте из корня репозитория:

```bash
go run ./cmd/bot -config configs/config.yaml
go build ./...
go test ./...
go test -race ./...
go vet ./...
gofmt -w <изменённые-go-файлы>
```

Для проверки форматирования без изменения файлов:

```bash
test -z "$(gofmt -l cmd internal)"
```

Docker-команды и оговорки среды приведены в
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md). GitHub Actions выполняет тот же набор
проверок на каждом push и pull request. Специализированный линтер и Makefile в
репозитории отсутствуют.

## Готовность изменения

Перед передачей работы:

1. Проверьте соответствие изменения контракту Issue и границам архитектуры.
2. Добавьте или обновите тесты для изменённого поведения.
3. Выполните относящиеся к Issue проверки и явно запишите пропущенные.
4. Обновите только затронутые источники документации и проверьте ссылки.
5. Передайте handoff по текущему Issue; выбор следующего Issue и
   post-merge-проверка остаются за orchestrator.

## How

- One GitHub Issue per branch, PR, and Codex task. The approved Issue body is
  the sole implementation contract; do not add scope from adjacent Issues.
- At Issue start, read `.ai/CONTEXT.md`, `.ai/HANDOFF.md`, the assigned Issue,
  `git status --short --branch`, and the base/relevant diff. Read other docs only
  when the Issue contract or a discovered dependency requires them.
- Use `system-designer` to define bounded work, `developer` to implement only
  that contract, `reviewer` for independent read-only review, then `publisher`
  for handoff and publication after a green review.
- Keep review under 15 minutes: normally <=6 files, <=250 production lines,
  and <=150 test lines. Ask `system-designer` to split work that exceeds this.
- Keep the bot on Bybit Testnet and read-only by default. This workflow grants
  no authority to add/place orders or close positions. `dry_run: false` does
  not enable live execution.
- Never infer a fill from an absent order; only an executed broker operation is
  evidence of execution. Keep secrets in environment variables and out of Git
  and logs.
- Live trading, risk-limit changes, and destructive migrations require explicit
  human approval. Prefer the Go standard library absent a clear need.
- Future invariant only: planner → immutable `TradePlan` → explicit human
  confirmation → execution engine. This describes no implemented behavior and
  grants no trading authority.
- For Go changes, add concise Russian comments to every function and method,
  including private functions and test helpers. Exported comments start with
  the identifier name. Run gofmt, targeted tests, `go test ./...`, and
  `go vet ./...`.

## Workflow

1. `system-designer` defines a bounded Issue contract.
2. `developer` implements only that contract.
3. `reviewer` reviews from a fresh context without editing the reviewed files.
4. Repeat developer/reviewer until blocking findings are resolved.
5. On GREEN, `reviewer` returns the exact approved files, verification evidence,
   residual risks, branch, diff, blockers, and an exact handoff for `publisher`.
   Reviewer does not declare the Issue complete or choose the next Issue.
6. `publisher` updates `.ai/HANDOFF.md` with publication state, stages only
   approved files plus that handoff, commits, pushes the current Issue branch
   without force, and opens a PR against the default branch. It verifies the PR
   URL and head SHA, records `PR открыт`, then stops. Missing access or required
   approval is a blocker; publication must not be claimed.
7. `publisher` never merges, selects the next Issue, performs post-merge
   verification, rewrites history, force-pushes, deletes data, handles secrets,
   changes financial rules, or enables live execution. The orchestrator chooses
   the next Issue and owns post-merge verification.
