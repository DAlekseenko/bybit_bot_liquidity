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

Заменяйте `.ai/HANDOFF.md` компактным проверенным checkpoint при передаче или
завершении Issue, а также перед остановкой или лимитом; не дописывайте историю.
Предыдущую историю сохраняйте побайтно в датированном `.ai/HANDOFF-ARCHIVE-*.md`;
архив обычно не нужен при старте новой Issue. Git и GitHub остаются источниками
истины: не создавайте self-referential commits только ради собственного SHA.
После merge orchestrator обновляет checkpoint по подтверждённому merge gate.

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
- Route work through `system-designer`, one `developer-middle` or orchestrator-activated `developer-senior`, and independent `reviewer`; the developer owner publishes after GREEN and the orchestrator owns post-publication checks.
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

1. One GitHub Issue per branch, PR, and Codex task. Its approved body is the
   sole contract. At Issue start, read `.ai/CONTEXT.md`, `.ai/HANDOFF.md`, the
   Issue, `git status --short --branch`, and relevant base/diff; read other docs
   only for a discovered dependency.
2. Route uncertainty, architecture, or decomposition to `system-designer`
   (`gpt-6.1-sol`, high); ready narrow Issues to `developer-middle`
   (`gpt-6-luna`, medium). The orchestrator may activate dormant
   `developer-senior` (`gpt-6.1-sol`, medium) for complex concurrency,
   idempotency, or recovery work, recording why and the confirmed selection
   mechanism. Medium remains the default. Use high effort only with a recorded
   reason and a confirmed launch method that selects high; custom role-file
   effort takes precedence, so never promise an explicit high override. Custom
   role-file model and effort take precedence over explicit spawn/default/parent
   values. A middle to senior
   handoff keeps the same Issue, task, branch, worktree, and PR, and records
   files, checks, and a concrete blocker. There is one developer owner. These
   profiles apply to new launches; sandbox mode inherits from the parent and
   does not guarantee isolation from it.
3. The developer implements only the approved contract. Keep review within 15
   minutes, normally <=6 files, <=250 production lines, and <=150 test lines;
   split larger work. Keep Testnet/read-only and all existing broker, financial,
   secret, and human-confirmation policies. `dry_run: false` never enables live
   execution. Never infer a fill from an absent order; only an executed broker
   operation proves execution. For Go, add concise Russian comments to every
   function and method, including private functions and test helpers; exported
   comments start with the identifier name. Run gofmt, targeted tests,
   `go test ./...`, and `go vet ./...`.
4. The independent `reviewer` (`gpt-6.1-sol`, high) reviews from fresh context,
   read-only, without edits, staging, commits, pushes, PR writes, or self-review.
   GREEN names the base, branch, contract, exact approved paths, and hashes of
   final contents including `.ai/HANDOFF.md`, plus verification, residual risks,
   and blockers. Prepare HANDOFF before review. After GREEN, freeze it and every
   approved blob: any changed content requires another independent review of
   the exact final blobs before publication. No post-GREEN handoff edits or
   self-referential SHA commits.
5. The same developer owner publishes only reviewer-approved blobs with an
   ordinary commit, pushes the current Issue branch without force, opens a PR
   against the default branch, verifies URL and exact head SHA, and stops. Do
   not amend, push to master, merge, close Issues, select the next Issue, or
   perform post-merge verification. Since HANDOFF is frozen at GREEN, unknown
   PR/head/CI facts remain unknown; report them after publication without
   rewriting approved content.
6. The orchestrator owns post-publication verification: compare exact PR
   URL/base/head/files with the approved contract, confirm independent GREEN,
   and require every CI check GREEN on that same head SHA. Fail closed on
   missing, stale, ambiguous, or mismatched evidence. Publish a Russian PR
   conversation comment only when user participation is genuinely required.
   Use marker `<!-- codex-user-action:v1 -->`, sections `Контекст`, `От вас`,
   `До ответа`, evidence and exact head SHA, exactly one concrete action, and
   the blocked step or safe path if there is no reply. Mark mandatory
   participation `🟡` and optional `🟢`. Before writing, find the marker comment
   and update it when state changes; never duplicate it. After writing, read
   back and verify its URL, target PR, head, and full body. Ordinary review
   notes, normal CI, and cases needing no participation produce no comment.
7. The orchestrator alone performs merge gate, merge, post-merge verification,
   checkpoint, and Issue closure after owner authorization. Require GitHub
   `mergedAt` and `mergeCommit`; fetch and verify merge-commit ancestry from
   refreshed `origin/master`, recording its SHA. Open/absent PR or matching
   head is not merge proof; for squash, verify the merge commit. If GitHub or
   refreshed ancestry is unavailable, do not close. Idempotently create/update
   the linked Issue checkpoint comment with marker
   `<!-- codex-merge-checkpoint:v1 -->`, PR URL, merge SHA, and verified
   `origin/master` SHA; read it back and verify all fields before closure.
8. The orchestrator selects the next ready Issue by priority/dependencies, then
   oldest when tied. Leave work blocked on user decisions unchanged; independent
   work requires no file or contract overlap. At interruption, record Issue,
   task, branch, worktree, PR/head if known, completed checks, blocker, and next
   action; resume the same artifacts and recheck current SHAs. Preserve all
   existing safety policies and never grant trade, deploy, risk-limit, or live
   execution authority through this workflow.
