# Передача работы
## Текущий checkpoint — Issue #184, 2026-10-26

- Репозиторий: `DAlekseenko/bybit_bot_liquidity`; рабочая ветка
  `codex/184-compact-handoff`, worktree
  `/Users/alekseenkodenis/.codex/worktrees/1b94/bybit_bot_liquidity`.
- Проверенная база `origin/master` и стартовый HEAD:
  `3b546eb092fd41d9a3c5d2bf0ae2706b17f24291`; ветка стартовала чистой.
- Снимок #184 на 2026-10-26: docs-only diff получил независимый review GREEN;
  стадия — перед публикацией. Whitelist: этот файл, архив, `AGENTS.md`, `.ai/CONTEXT.md`.
  Go gates пропущены из-за docs-only scope; актуальные PR/head/CI читать в GitHub.
- Источник архива: исходный blob `7989c3798d6a4b7dec86a555a9a5244066deb515`,
  1137 строк. Архив создан без правок в
  [HANDOFF-ARCHIVE-2026-10-26.md](HANDOFF-ARCHIVE-2026-10-26.md); его текущий blob
  проверен и совпадает с исходником. Старые статусы внутри архива исторические.
- Соседнее расположение архива сохраняет относительные ссылки исходника.
  Активный файл — только текущий подтверждённый checkpoint и следующий шаг.

## Подтверждённое состояние master

- #59 CM-004 merged как PR #129, merge commit
  `556304183f5db6151eb4fb6ea68a0ab2987418a8`; ancestry в проверенной базе PASS.
  Реализованы strict declarative Momentum config guards и sample; runtime нет.
- #130 HM-001 merged как PR #180 в текущую базу
  `3b546eb092fd41d9a3c5d2bf0ae2706b17f24291`; ancestry PASS.
- Live readback 2026-10-26: #60, #131, #182 и #183 открыты. Отсутствие открытой
  Issue или PR здесь не считается доказательством выполнения.

## Текущие блокеры и продолжение

- #182 CM-005A: worktree
  `/Users/alekseenkodenis/.codex/worktrees/85c5/bybit_bot_liquidity`,
  ветка `codex/182-daily-kline-page`; checkpoint там же в `.ai/HANDOFF.md`.
  Timestamp overflow исправлен в draft сравнением через безопасное вычитание,
  но постоянные boundary tests и согласованный split не завершены. Независимый
  reviewer ранее BLOCKING; GREEN, commit, push, PR и CI отсутствуют. Продолжать
  по checkpoint, перенести overflow regression и завершить согласованный split.
- #131 HM-002: worktree
  `/Users/alekseenkodenis/.codex/worktrees/5064/bybit_bot_liquidity`,
  ветка `codex/131-hermes-mcp-compatibility`; checkpoint там же в
  `.ai/HANDOFF.md`. Pinned Go SDK/Hermes compatibility остаётся UNVERIFIED.
  Draft child contracts: `/private/tmp/hm002-child-contracts.json`; после двух
  правок нужен final read-only review. Дети не созданы. Следом сверить drafts,
  повторить поиск дублей, создать только отсутствующие Issues и связать B с A;
  не выполнять children в #131.
- #183 CM-005B — открытая зависимая Issue диапазона и пагинации; сверить её
  зависимости с согласованными частями #182 до продолжения. #60 остаётся
  родительской Issue дневных свечей и не закрыта.
- Эта передача относится только к #184. Не переносить незакоммиченный AGENTS.md
  из основного checkout; не менять чужие ветки/worktrees.

## Ограничения и следующий шаг

- Сохранять Bybit Testnet и read-only режим. Не добавлять private order/close
  transport, live broker calls, исполнение, deployment, секреты или risk changes.
- Для новой Issue проверить текущие GitHub Issue/PR/branch и Git, прочитать
  `.ai/CONTEXT.md`, этот checkpoint и точный контракт; архив читать только при
  конкретной необходимости.
- Передать publisher утверждённые paths, evidence и blockers для одного commit/PR;
  затем github-reviewer проверяет exact PR head/base/files и CI. Итог публикации
  передаётся orchestrator без нового metadata commit ради собственного SHA.
- GitHub CI финального head обязателен. Merge gate, checkpoint после merge,
  закрытие Issue и выбор следующей Issue остаются за основным orchestrator.
- База, статус Issue и блокеры требуют повторной live-проверки при возобновлении;
  этот checkpoint не заменяет текущий Git/GitHub readback.
