# Инструкции участникам разработки

Эти правила общие для инструментов и Issue. Компилируемый код, тесты,
`go.mod`, Docker-файлы и `configs/config.yaml` показывают фактическую
реализацию; подтверждённые бизнес-правила и решения — в
[DOMAIN](docs/DOMAIN.md) и [DECISIONS](docs/DECISIONS.md). Неизвестное
фиксируйте как `TODO` с конкретным вопросом, не заменяя догадкой.

## Состояние и карта чтения

Проект — каркас MVP Bybit V5 Testnet, а не готовая торговая система. Он
собирает public candles/OI/liquidations и сохраняет сигналы и виртуальные
paper-trade расчёты. Есть signed read-only account/balance/one-way position/current
leverage и fail-closed preflight/readiness. Биржевого исполнения нет: `PlaceOrder`
и `ClosePosition` не подключены, `dry_run: false` не включает торговлю.
Сохраняйте Testnet и read-only режим; не добавляйте live broker calls или
order/close transport без отдельного Issue и явного одобрения владельца.
Не описывайте paper-trade как исполнение и не выводите fill из отсутствующей
заявки. Секреты храните только в environment variables; не записывайте их в Git
или логи. Live trading, deploy, изменение risk limits и destructive migrations
требуют явного одобрения владельца; workflow сам таких полномочий не выдаёт.

| Нужно | Источник |
| --- | --- |
| Краткая архитектура и подтверждённые решения | [.ai/CONTEXT.md](.ai/CONTEXT.md) |
| Handoffs новых Issue | [`.ai/handoffs/`](.ai/handoffs/) и правила ниже; `.ai/HANDOFF.md` — legacy |
| Историческая карта стартовой маршрутизации Issues | [.ai/ISSUE-ROUTING.md](.ai/ISSUE-ROUTING.md) |
| Бизнес-правила и формулы | [docs/DOMAIN.md](docs/DOMAIN.md) |
| Компоненты и зависимости | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Go и внешние контракты | [docs/API.md](docs/API.md) |
| Команды и конфигурация | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) |
| Принятые ограничения и TODO | [docs/DECISIONS.md](docs/DECISIONS.md) |

## Границы кода

Сохраняйте Go 1.24 compatibility и `gofmt`; предпочитайте стандартную библиотеку,
небольшие пакеты и constructor injection. Новую dependency обоснуйте; если она
влияет на архитектуру, обновите `docs/DECISIONS.md`. Интерфейсы принадлежат
потребителю и минимальны; не вводите глобальное изменяемое состояние. Внешние/долгие операции
принимают `context.Context` первым. `strategy` и `risk` остаются без I/O, биржевой
адаптер — без торговых правил, SQL — в `storage`, координация — в `execution`
или composition root. Возвращайте ошибки с контекстом на границах, используйте
проверяемые sentinel errors для ожидаемых ветвей, структурированный `slog` и UTC.
Сохраняйте доменные типы и единицы либо явно документируйте изменение в DOMAIN и
API. Для Go добавляйте краткие русские комментарии к каждой функции/методу,
включая private и test helpers; комментарий экспортируемого идентификатора
начинается с его имени. Пишите табличные или точечные unit-тесты рядом с пакетом.
Внешние API заменяйте fake/mock-реализациями; реальные заявки запрещены. Не редактируйте IDE-файлы и чужие изменения (`.idea/` вне проекта).

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
   150 test lines. Если не помещается — designer сужает/делит контракт; бюджеты не
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

TODO до будущего подключения торговли: private order/close transport, целевое
leverage и установка, восстановление после перезапуска и безопасная интеграционная
проверка Testnet. Это не разрешение реализовывать торговлю. Для Momentum будущая
граница — planner → immutable `TradePlan` → authorization → execution; она не
описывает реализованное поведение и не даёт live-полномочий. Действующая политика
ручного подтверждения каждого плана определяется только актуальным контрактом и
решениями проекта.
