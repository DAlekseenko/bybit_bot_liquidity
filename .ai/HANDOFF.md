# Передача работы

## Checkpoint — Issue #215, готов к независимому review

- Контракт: [Issue #215](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/215).
  Task/chat `01a1a802-3787-78f1-ae5f-689997af5be7`; worktree
  `/Users/alekseenkodenis/.codex/worktrees/issue-start-routing/bybit_bot_liquidity`,
  branch `codex/215-issue-start-routing`, base и prepublication HEAD
  `a8d6e65e3dc569e04f9ca38d892493b833855965` (`origin/master` при обновлении); PR
  не создан. Один developer owner; scope — ровно четыре файла: `AGENTS.md`,
  `.ai/ISSUE-ROUTING.md`, `.ai/HANDOFF.md`,
  `.ai/HANDOFF-ARCHIVE-2026-11-05-issue-routing.md`.
- Добавлена детерминированная стартовая маршрутизация: существующие artifacts →
  continue; owner input/dependency → wait; конкретная неопределённость или
  доказанный overbudget → targeted designer; ready approved contract → один
  developer. Существующие роли, модели, effort, sandbox, бюджеты, read-only/Testnet,
  reviewer, freeze, публикация, CI и merge gates сохранены.
- `.ai/ISSUE-ROUTING.md` содержит 107 Issues из read-only аудита от 2026-11-04,
  историческая база `af2166d212d4e9258c4660f556ba23486aa9f6f8`; все строки имеют
  ссылку на Issue, причину и открытые зависимости. Источник — два сохранённых JSON
  результата аудита; карта advisory и не утверждает новые контракты. Текущая ветка
  обновлена fast-forward до свежего `origin/master` после PR #213. Новый archive
  byte-exact сохраняет HANDOFF этой базы; уже существующий архив PR #213 сохранён.
- Проверки до review: source parity, 107 уникальных строк и категории/причины/deps,
  шесть маршрутов, внутренние ссылки, whitespace, byte-exact archive и whitelist
  diff. Go-код не менялся; Go gates локально не запускались. CI Go workflow будет
  выполняться на PR head; локальные тесты не повторялись для docs-only diff.
- Blocker: ожидается независимый fresh read-only review exact final blobs.
  Следующий шаг: передать reviewer четыре пути и SHA256; после GREEN заморозить
  blobs. Тот же owner выполнит обычный commit/push без amend/force, откроет один PR
  в master, прочитает URL/exact head SHA и остановится. Post-publication CI —
  orchestrator; merge и закрытие Issue не входят в scope.
