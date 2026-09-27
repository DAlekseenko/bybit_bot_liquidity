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
- Перед публикацией сверено: HEAD совпадает с базой; изменены только два
  одобренных файла, иных локальных изменений нет. В `AGENTS.md` 21 добавление и
  4 удаления; TOML содержит конфигурацию отдельной read-only роли.
- Независимый reviewer: GREEN; 58 изменённых строк; TOML parse и diff checks
  чистые; Go и тесты не менялись. Остаточный риск по handoff reviewer: роль
  декларативна, её поведение зависит от будущего исполнения агентом.
- Локальная проверка публикации: ожидается. Локальная верификация кода не
  запускалась, поскольку изменения только в правилах и TOML; доказательства
  reviewer сохранены выше.
- Незакоммиченные файлы до публикации: `AGENTS.md`,
  `.codex/agents/github-reviewer.toml`; `.ai/HANDOFF.md` обновлён publisher.
- Публикация: ожидается; PR ещё не создан. После создания записать `PR открыт`,
  URL и подтверждённый head SHA. Merge и post-merge verification не выполнять.
- Блокеры: на момент handoff не выявлены.
