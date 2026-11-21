# Процедура Issue workflow

Нормативная часть [AGENTS.md](../AGENTS.md). Обязательна для orchestrator,
system-designer, developer и reviewer при работе над Issue; ссылки «по AGENTS.md»
на handoff, роли и публикацию относятся к этому файлу. Общие ограничения проекта
остаются в AGENTS.md и действуют для всех классов без исключений.

Строгость процесса зависит от класса риска Issue (решение владельца 2026-11-21).

## Классы риска

| Класс | Что входит | Процесс |
| --- | --- | --- |
| **A — офлайн** | `model`, `planner`, `backtest`, отчёты, импорт публичных данных, команды без торговых вызовов, документация | [Лёгкий](#лёгкий-процесс-классы-a-и-b) |
| **B — состояние** | Схемы и аддитивные миграции PostgreSQL, repository, paper runtime, scheduler, восстановление paper-состояния | [Лёгкий](#лёгкий-процесс-классы-a-и-b) с ограничениями B |
| **C — деньги** | Private/signed API, заявки, исполнение, reconciliation с биржей, risk limits, kill switch, Testnet/Mainnet runtime, destructive migrations | [Полный](#полный-процесс-класс-c) + явное одобрение владельца |

- Класс указывается в теле Issue строкой `Класс риска: A|B|C`. Если его нет,
  orchestrator определяет класс по таблице и записывает в body до начала работы.
- При сомнении выбирается более высокий класс. Если работа A/B по ходу затрагивает
  область C, developer останавливается, и Issue переклассифицируется.
- Issue, уже начатая по полному процессу, доводится до merge по нему же.

## Лёгкий процесс (классы A и B)

1. **Старт.** Прочитайте `.ai/CONTEXT.md`, тело Issue, `git status --short --branch`
   и относящийся код. Одна Issue — одна ветка и один PR от свежего default branch.
   System-designer подключается только при реальной неопределённости контракта
   или архитектуры; готовая Issue сразу идёт к developer.
2. **Размер.** Issue — законченный срез функциональности, без дробления на
   подзадачи по умолчанию.
   - A: ориентир до ~600 production lines и ~30 минут review; тесты — на
     поведение и важные границы, обычно не больше production-кода.
   - B: не более 6 файлов, 250 production и 300 test lines.
   - Исчерпывающие граничные тесты обязательны для денежной арифметики.
3. **Реализация.** Один developer реализует, пишет тесты, запускает команды из
   AGENTS.md (gofmt, `go test ./...`, `go test -race ./...`, `go vet ./...`,
   `go build ./...`) и сам просматривает diff перед review.
4. **PR.** Описание PR — это handoff: контракт и класс, что сделано, изменённые
   файлы, проверки и пропуски с причинами, риски, следующий шаг; строка
   `Closes #<n>`. Отдельный handoff-файл не нужен.
5. **Review.** Один независимый reviewer из свежего контекста, read-only, без
   self-review; сверяет diff с контрактом. Для B дополнительно проверяет
   транзакционность, идемпотентность и поведение после рестарта. Исправления после
   review повторно проверяются только по изменённому diff. Штатный reviewer —
   Codex GitHub review (`chatgpt-codex-connector`): он запускается при открытии
   PR, повтор — комментарием `@codex review` после исправлений. Автор кода не
   засчитывает себе review.
6. **Gate.** Review без открытых замечаний и все CI checks зелёные на head PR.
   Для Codex review это последний комментарий `codex-review-state:v1`, где
   `code_review.status` = `completed`, `code_review.commit_sha` равен head SHA PR
   и `has_unresolved_findings` = `false`. Заморозка хэшей, очередь интеграции,
   `ACTIVE_SINGLE_SLOT` и reservations не применяются. Исключение: PR, меняющие
   список миграций `internal/momentum/storage`, мерджатся по одному после rebase
   на свежий default branch.
7. **Merge.** Владелец 2026-11-21 дал постоянное полномочие: PR классов A и B
   вливает агент-исполнитель сам, когда gate шага 6 выполнен на текущем head SHA;
   иначе merge не выполняется. Класс C вливает только владелец. Issue
   закрывается через `Closes #<n>`. Checkpoint-комментарии, readback и архивация
   чатов не требуются. После merge агент коротко сообщает владельцу PR, merge SHA
   и итог проверок.
8. **Прерывание.** Если работа останавливается до PR, оставьте
   `.ai/handoffs/issue-<n>.md` (5–10 строк: ветка, сделано, проверки, blocker,
   следующий шаг). Файл можно перезаписывать; архивы и версии не нужны.

Модель и effort выбирает тот, кто запускает задачу; ниже названия моделей
относятся только к классу C.

## Полный процесс (класс C)

Ниже — действующая процедура без изменений текста. Она применяется к классу C и
к любой Issue, для которой владелец потребует полного процесса.

## Per-Issue handoff

Для новой Issue единственная текущая передача — `.ai/handoffs/issue-<n>.md`, где
`<n>` — положительный десятичный номер без ведущих нулей. Approved body фиксирует
точные handoff/archive paths в whitelist и общем file budget. Если body не задаёт
canonical path, orchestrator вносит точный path и whitelist в body и читает body
обратно ДО implementation; уже данное owner authorization не требует повторного
согласования. Старый body, явно требующий global HANDOFF, сначала требует узкой
approved-body поправки/readback; не переопределяйте его и не переносите прежний
GREEN. Существующие artifacts сохраняются. `.ai/HANDOFF.md` и legacy archives
неизменны; общего append/index, массовой миграции и копирования чужого статуса нет.

При resume сначала выбирайте существующий own canonical file; legacy fallback используйте
только если его ещё нет. Продолжайте те же artifacts и checkpoint. ID `issue-<n>-c<k>`
нумерует этапы, не SHA; переход этапа увеличивает k. История — опубликованная запись
или явно завершённый и переданный checkpoint; её нельзя задним числом назвать draft
или удалить. Review fixes текущего этапа сохраняют ID. Если historical record остаётся
в файле, архив не нужен. При замене/удалении до edit сохраните весь source-before file
побайтно в approved archive, измерьте SHA256 и сравните source/archive bytes; новый
handoff ссылается на архив. Canonical path:
`.ai/handoffs/archive/issue-<n>-YYYY-MM-DD-v<k>.md` (UTC; k положительный без ведущих
нулей и равен max(existing version)+1, не первому свободному gap). Совпадающий retry
переиспользуйте. Несовпадающий payload не перезаписывайте: выберите следующий k,
сначала обновите exact whitelist в body и readback, затем сохраняйте.

Компактный handoff содержит Issue/contract marker и checkpoint, task/chat ID, owner/
роль, worktree/branch/base, exact paths, проверки и пропуски с причинами, blocker/
следующий шаг и ссылки на сохранённую историю. Не включайте неизвестные до GREEN
PR/head/CI facts или self-referential hashes. GREEN покрывает все точные blobs; после
него любая правка, включая conflict resolution, требует нового independent GREEN.

## Завершение Issue и архивация чата

Только orchestrator архивирует выделенный Issue-чат после GitHub `mergedAt` и
`mergeCommit`, ancestry merge SHA в refreshed `origin/master`, идемпотентного
единственного marker merge-checkpoint comment с readback, Issue
closed/completed с readback, завершённых
developer/reviewer turns и отсутствия pending reply/operations. Архивируйте через
поддерживаемый инструмент; независимый readback подтверждает exact thread ID и
`archived=true`. Используйте thread ID выделенного implementation-чата из его own
handoff; parent/orchestrator и чужие Issue chats исключены. До точного archive readback
queue task не завершена. При сбое сохраните blocker/следующий шаг во внешнем checkpoint
и повторяйте тот же chat без дублей. Опубликованный handoff не меняйте ради
merge/archive facts. Developer после публикации останавливается.

## Единый Issue workflow

1. Перед началом прочитайте `.ai/CONTEXT.md`, approved body Issue и выбранный
   per-Issue handoff, затем `git status --short --branch` и base/relevant diff. Для
   новой Issue без своего файла `.ai/HANDOFF.md` допустим только как однократный
   legacy bootstrap; создайте `.ai/handoffs/issue-<n>.md` до interruption/review. Проверьте
   branch/base, открытые PR и правила репозитория; не трогайте грязный основной
   checkout. Issue body — единственный контракт; один Issue на чат, ветку и PR.
   Оркестратор сам фиксирует стартовый маршрут, не запуская designer заранее:
   существующие owner/chat/branch/PR → продолжить те же artifacts; требуемый
   вход владельца → `WAIT_OWNER`; невыполненная зависимость → `WAIT`; конкретная
   нерешённая архитектурная/контрактная неопределённость → targeted designer;
   доказанное превышение бюджета → designer для bounded decomposition без потери
   cases; готовый однозначный approved contract с выполненными зависимостями и
   помещающимся scope → один developer напрямую. Отсутствие private function name
   само по себе не основание для designer; создание Issue и прежний `READY` не
   утверждают scope. В стартовом результате кратко укажите маршрут, причину и
   blocker/следующий шаг. Классификацию из `ISSUE-ROUTING.md` применяйте только
   после точечной проверки актуального Issue/решений, его зависимостей, owner/PR и
   относящегося diff; исторический снимок не заменяет контракт или состояние.
2. Читайте только нужный диапазон файлов: начните с точечного `rg`, затем откройте
   релевантные строки. GitHub запрашивайте только нужные поля. Агенту передавайте
   Issue, релевантный diff и ограничения без полной истории. Успешные проверки
   записывайте кратко, полный лог сохраняйте при ошибке; неизменные документы/CI
   повторно не читайте без причины. Историю HANDOFF открывайте только при конкретной
   необходимости.
3. Неопределённость, архитектуру или декомпозицию направляйте `system-designer`;
   готовую узкую Issue — одному developer owner (`developer-middle` по умолчанию).
   Только orchestrator активирует dormant `developer-senior` для concurrency,
   idempotency или recovery и записывает основание и подтверждённый механизм.
   Senior profile закрепляет medium; high effort допустим только с записанной
   причиной и подтверждённым launch method, который действительно выбирает high.
   Role-file настройки при этом приоритетны. Не меняйте настроенные accounts и
   `primary_execution`; менять их можно только в отдельно одобренном scope.
   Роли новых запусков: designer `gpt-6.1-sol/high`, middle `gpt-6-luna/medium`,
   senior `gpt-6.1-sol/medium`, reviewer `gpt-6.1-sol/high`; sandbox наследуется.
   Custom role file имеет приоритет над явным/default/parent model и effort; не
   обещайте high override без подтверждённого способа запуска. Middle→senior
   сохраняет Issue/task/branch/worktree/PR и передаёт файлы, проверки, blocker.
   Один developer owner. Независимый reviewer проверяет с fresh context, read-only;
   он не редактирует, не stage/commit/push/PR и не делает self-review.
4. Держите review в пределах 15 минут: не более 6 файлов, 250 production lines и
   300 test lines. Если не помещается — designer сужает/делит контракт; бюджеты не
   ослаблять. Добавляйте или обновляйте тесты для изменённого поведения; явно
   перечисляйте пропущенные проверки. Для Go обязательно: gofmt, targeted tests, `go test ./...` и
   `go vet ./...`. Выполняйте также обязательные CI/docs проверки (build, race,
   lint, security, coverage и прочие, если заданы). Проверяйте ссылки, whitespace
   и contract coverage; не сообщайте PASS при blocker. Не запускайте бота для теста.
5. До review обновите handoff выбранной Issue как компактный проверенный
   checkpoint; reviewer GREEN называет base, branch, Issue, exact paths/hashes всех
   итоговых файлов, проверки, риски и blockers. После GREEN заморозьте все blobs;
   любое изменение требует fresh independent review финальных blobs. Не записывайте
   self-referential commit SHA.
6. Тот же developer owner после GREEN публикует только одобренные paths/hashes
   обычным commit и push текущей ветки без force; создаёт один PR в default branch,
   читает обратно URL и exact head SHA и останавливается. Запрещены amend, push
   в master, merge и закрытие Issue; post-publication проверки принадлежат
   orchestrator. PR/head/CI факты, неизвестные до GREEN, per-Issue handoff не переписывают.
7. Orchestrator после публикации сверяет URL/base/head/files с контрактом и GREEN;
   требует все CI checks GREEN на том же head SHA, fail-closed при отсутствующих,
   старых или неоднозначных данных. Только при реальной необходимости участия
   пользователя он пишет один русский PR conversation comment с marker
   `<!-- codex-user-action:v1 -->`, секциями `Контекст`, `От вас`, `До ответа`,
   evidence, exact SHA и ровно одним конкретным действием, которое требуется от
   пользователя. Перед записью найдите marker и обновите существующий; после
   прочитайте и проверьте URL, PR, head и полный текст. Укажите заблокированный
   шаг и безопасный путь, если ответа не будет. Обязательное участие — 🟡,
   необязательное — 🟢; обычные review/CI не требуют комментария.
8. Только orchestrator выполняет merge gate, merge, post-merge verification,
   checkpoint и закрытие Issue после полномочия владельца. Требуйте GitHub
   `mergedAt` и `mergeCommit`, fetch и ancestry merge commit в обновлённом
   `origin/master` с записью SHA; открытый PR или совпавший head не доказательство
   merge. Для squash проверяйте сам merge commit. При недоступном GitHub/ancestry
   Issue не закрывайте. Идемпотентно создайте/обновите связанный Issue comment с
   `<!-- codex-merge-checkpoint:v1 -->`, PR URL, merge SHA и подтверждённым
   `origin/master` SHA; прочитайте обратно и проверьте поля перед closure.
   Orchestrator выбирает следующую ready Issue по приоритету/зависимостям, затем
   самой старой; остановленные решения не менять. Независимая работа допустима
   только без пересечения файлов и контрактов. На прерывании запишите Issue, task,
   branch, worktree, PR/head если известны, проверки, blocker и следующий шаг; при
   возобновлении продолжайте с теми же artifacts и перепроверьте SHA.

## Очередь интеграции и shared paths

Для параллельной работы, reservations целых общих путей и единственного
финального integration slot применяйте нормативный
[.ai/INTEGRATION.md](INTEGRATION.md). Только текущий coordinator выбирает
Issue и меняет canonical queue comment. Финальное обновление базы, review и
публикация требуют явного ACTIVE_SINGLE_SLOT grant/readback. Shared path требует
reservation до первого edit. Очередь процедурная, не атомарная и не расширяет
Issue authority. Завершение, merge, closure и archive остаются у orchestrator.
