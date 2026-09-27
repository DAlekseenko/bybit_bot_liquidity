# Передача работы

## Issue #18 — Упростить старт Issue и разграничить роли агентов

- Ветка: `codex/18-agent-role-boundaries`; база: `50fe17bc549d1ecb94c008c61d8f8ec8b467746a` (`origin/master`).
- Одобренные файлы Issue: `AGENTS.md`, `.codex/agents/reviewer.toml`,
  `.codex/agents/publisher.toml`, `.codex/agents/system-designer.toml`.
- Независимый read-only reviewer: GREEN, blockers отсутствуют. Подтверждены
  54 additions / 51 deletions, TOML parse, 15 локальных ссылок, `git diff --check`,
  отсутствие Go/test изменений, запрещённых путей и secret value.
- Ветка и рабочее дерево сверены с handoff; иных локальных файлов нет.
- Поиск PR через GitHub connector (`head:codex/18-agent-role-boundaries`, все
  состояния) не нашёл существующего PR.
- Issue commit: `c310a914d9afc83470cf4868d37573cd0e02ec66`; push в origin выполнен
  без force.
- PR открыт: [#25](https://github.com/DAlekseenko/bybit_bot_liquidity/pull/25),
  base `master`. Созданный PR readback подтвердил head
  `c310a914d9afc83470cf4868d37573cd0e02ec66`.
- Handoff-only commits отправлены fast-forward. Persisted PR readback подтвердил
  URL выше и текущий head ветки. Merge и post-merge verification не выполнять.
- Остаточные риски: изменения затрагивают только инструкции и конфигурацию
  агентских ролей; Go-код и тесты не менялись. Следующий Issue выбирает orchestrator.

## Issue #19 — Роль post-publication github-reviewer

- Ветка: `codex/19-github-reviewer`; база: `fdfb36c4a5218a8b81e50481305759ab1c7a9a58`.
- Одобренные Issue-файлы: `AGENTS.md` и `.codex/agents/github-reviewer.toml`.
- Перед публикацией сверено: HEAD совпадал с базой; изменены только два
  одобренных файла, иных локальных изменений не было. В `AGENTS.md` 21
  добавление и 4 удаления; TOML содержит конфигурацию отдельной read-only роли.
- Независимый reviewer: GREEN; 58 изменённых строк; TOML parse и diff checks
  чистые; Go и тесты не менялись. Остаточный риск по handoff reviewer: роль
  декларативна, её поведение зависит от будущего исполнения агентом.
- Проверки publisher: `git diff --check` и `git diff --cached --check` прошли.
  Локальная верификация кода не запускалась, поскольку изменения только в
  правилах и TOML; доказательства reviewer сохранены выше.
- Issue commit: `c86451d2ab27015d6511ecb32627bccfb5113d6b`; push в origin
  выполнен без force. PR открыт: [#26](https://github.com/DAlekseenko/bybit_bot_liquidity/pull/26),
  base `master`. Persisted readback подтвердил URL и head SHA issue commit.
- На head issue commit CI `CI` выполнялся (`in_progress`); combined commit
  statuses вернули пустой список. После handoff-only commit проверить CI на новом
  head; GREEN локального reviewer подтверждён.
- Handoff-only commit включает только `.ai/HANDOFF.md`; после push сверить
  новый PR head SHA и CI на нём.
- Остаточные риски: роль декларативна, её поведение зависит от будущего
  исполнения агентом. Go-код и тесты не менялись.
- Блокеры: отсутствуют. Merge и post-merge verification не выполнять;
  post-publication handoff передать `github-reviewer` после финального readback.

- Подтверждённое завершение: PR #26 получил `mergedAt`
  `2026-09-27T21:08:42Z`, `mergeCommit`
  `2db7c5efa68e0da462fda01e3675a5cddc005c2d`; remote refs обновлены, этот merge
  commit достижим из `origin/master`, проверенный SHA `origin/master` —
  `2db7c5efa68e0da462fda01e3675a5cddc005c2d`. Checkpoint Issue #19 прочитан:
  [комментарий](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/19#issuecomment-5859856937).

## Issue #20 — Merge gate и точное продолжение работы

- Issue: [#20](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/20).
- База проверена: `origin/master` и HEAD на старте совпали с
  `2db7c5efa68e0da462fda01e3675a5cddc005c2d`; рабочее дерево было чистым.
- Ветка: `codex/20-merge-gate-resume`; worktree:
  `/Users/alekseenkodenis/.codex/worktrees/81f9/bybit_bot_liquidity`.
- Codex task thread: `01a0e454-93c9-79c3-beec-6c26ffc9df71`; стартовый
  HEAD/base SHA: `2db7c5efa68e0da462fda01e3675a5cddc005c2d`.
- Одобренные файлы: `AGENTS.md`, `.ai/HANDOFF.md`; контракт ограничивает
  документацию 130 изменёнными строками, без Go и тестов.
- Стадия: свежая независимая проверка исправленного полного diff завершена
  GREEN; reviewer одобрил ровно `AGENTS.md` и `.ai/HANDOFF.md`. Подтверждены
  открытая Issue #20, открытый PR #27 без merge и отсутствие checkpoint.
  Reviewer подтвердил будущий diff: 69 добавлений, 0 удалений (`AGENTS.md` 22,
  `.ai/HANDOFF.md` 47), без Go и тестов; `git diff --check` чистый, untracked
  отсутствуют. Эта проверка была до публикационного handoff-коммита.
- Остаточный риск: изменение документирует процесс; проверка не создаёт
  гарантию доступности GitHub или обновления remote refs в будущих запусках.
- Публикация: PR [#27](https://github.com/DAlekseenko/bybit_bot_liquidity/pull/27)
  открыт в `master` и не слит. После fast-forward push handoff-коммита GitHub
  подтвердил PR и branch head `588e52fbc73cb41873e8234365f65b0d5d4eb464`,
  base `2db7c5efa68e0da462fda01e3675a5cddc005c2d`; PR содержит 69 добавлений,
  0 удалений, 2 файла. Локальный HEAD совпадает, рабочее дерево чистое. Issue
  #20 остаётся открытой; merge и merge checkpoint для неё отсутствуют.
- Resume: сверить Issue/task, branch и worktree; fetch, проверить свежий
  `origin/master` и SHA. Найти и повторно использовать существующие PR и marker
  comment, не создавать дубликаты; сверить сохранённые проверки и blocker,
  выполнить точное следующее действие и продолжить с теми же объектами.
- Проверки: diff `origin/master...HEAD` содержит только `AGENTS.md` и
  `.ai/HANDOFF.md`; `git diff --check` пройден. Go и тесты не менялись.
- Следующее действие: передать `github-reviewer` PR #27, финальный head
  `588e52fbc73cb41873e8234365f65b0d5d4eb464` был подтверждён readback до
  текущего handoff-коммита; после его push повторно прочитать PR URL/base/head,
  сверить PR body и передать актуальный SHA вместе с GREEN handoff. Issue
  остаётся открытой; merge, checkpoint и post-merge verification не выполнялись.

## Issue #28 — CI quality gates

- Контракт GitHub Issue [#28](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/28)
  («#22 / PR #7: выделить CI quality gates»); ветка `codex/28-quality-gate`,
  база reviewer handoff `52936af8820ed7b1c2ab600c6b605976ca156ec1`.
- Независимый reviewer: GREEN; одобрены ровно `.github/workflows/ci.yml` и
  `.golangci.yml`. Diff: 33 additions, 0 deletions, 2 файла, в рамках контракта.
  Проверки unit, race, vet, build, lint и coverage — green. `govulncheck`
  проверяется CI с сетью на опубликованном точном head SHA.
- Перед публикацией: текущая ветка и HEAD сверены с базой; локальные изменения
  только в двух одобренных файлах, иных uncommitted файлов нет. `git diff
  --check` чистый. Staged были только два одобренных файла и этот handoff.
  Issue-коммит `f727a1e2c32be23d7d14977ac227e15748bcf23a`; push в origin
  выполнен без force. Review повторно не выполнялся.
- Остаточные риски: результат сетевой `govulncheck` и полный CI требуют проверки
  на точном опубликованном head. Нет изменений Go runtime или тестов; Testnet и
  read-only ограничения не затронуты.
- Публикация: PR [#39](https://github.com/DAlekseenko/bybit_bot_liquidity/pull/39)
  открыт к `master`, не draft, mergeable. GitHub readback подтвердил branch
  `codex/28-quality-gate`, base SHA
  `52936af8820ed7b1c2ab600c6b605976ca156ec1` и head SHA при последнем readback
  `fd584be3f17190ce6998dd449c1c9efe6c3694d8`. PR diff состоит из двух Issue
  файлов и этого handoff. Запуск CI #102 на указанном SHA `pending`, combined
  statuses пусты. После этого readback будет опубликован ещё один handoff-only
  commit; итоговые head SHA и CI требуют свежего readback и передаются
  github-reviewer.
- Блокеры: отсутствуют; GitHub readback и Actions доступны.

## Issue #29 — безопасный реестр метрик

- Issue: [#29](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/29);
  ветка `codex/29-safe-metrics-registry`; база `372ddd7c0b02ef150becec461cd65e61509ba984`
  после подтверждённого merge PR #39.
- Перед публикацией проверены текущая ветка и HEAD, `origin/master` и
  merge-base: все указывают на базу выше. Изменения находятся только в двух
  reviewer-approved untracked файлах: `internal/observability/metrics.go`
  (95 строк) и `internal/observability/metrics_test.go` (98 строк); иных
  незакоммиченных файлов нет. Существующий PR этой ветки не найден.
- Независимый reviewer: GREEN, blockers отсутствуют; одобрены ровно эти два
  файла. Diff — 193 additions / 0 deletions, в пределах лимитов Issue. Проверки
  reviewer: gofmt, целевые тесты, целевые race-тесты, `go test ./...`,
  `go vet ./...` и `git diff --check` прошли. Изменение содержит bounded safe
  metrics registry и точный HTTP `/metrics`, остальные paths дают 404; доступ
  синхронизирован RWMutex. Runtime wiring, listener, Compose, CI и зависимости
  не добавлены.
- Остаточный риск: handler удерживает RLock во время записи ограниченного
  ответа. Listener не подключён. Секреты, account balance, trading data,
  payload/error detail и labels из внешнего ввода не включаются.
- Публикация: Issue commit и PR ещё не созданы; подготовлено к staging только
  два одобренных Issue-файла и `.ai/HANDOFF.md`. После успешного PR creation
  указать `PR открыт`, проверить URL/base/head SHA и Actions для того же head,
  затем передать точный readback github-reviewer. Блокеры: отсутствуют.
- Merge, закрытие Issue, выбор следующего Issue и post-merge verification
  остаются за orchestrator.
