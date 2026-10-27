# Передача работы

## Checkpoint — Issue #186, этап A

- Репозиторий: `DAlekseenko/bybit_bot_liquidity`; ветка
  `codex/186-agent-model-preparation`; worktree
  `/Users/alekseenkodenis/.codex/worktrees/8dc5/bybit_bot_liquidity`.
- Проверенная база и стартовый HEAD: `e3d52ecd08c282e2a3bceeacfe818e8ee4eed66b`
  (`origin/master`); стартовая ветка была чистой.
- Контракт: Issue [#186](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/186).
  Этап A — только подготовка профилей и локальной конфигурации; действующая
  маршрутизация в `AGENTS.md` не меняется. Senior остаётся dormant до этапа B
  (#187). Bybit Testnet и read-only ограничения сохраняются.
- Whitelist: `.codex/agents/system-designer.toml`,
  `.codex/agents/reviewer.toml`, `.codex/agents/developer-senior.toml`,
  `.codex/config.toml`, `.ai/HANDOFF.md`. Архивы не менять.
- Официальная схема TOML: [config reference](https://learn.chatgpt.com/docs/config-file/config-reference)
  и [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents).
  Ограничения sandbox наследуются от родителя: роль не может их расширить,
  а отдельной OS-изоляции настройка роли не даёт.
- Текущее состояние: подготовлен локальный diff; независимая проверка,
  публикация и GitHub CI ещё не выполнены. Go gates пропущены: изменение
  config/docs-only. До PR publisher обязан получить GREEN на точном diff.
  После GREEN любое изменение требует повторного review. Publisher записывает
  `PR открыт` после создания PR; такое обновление HANDOFF проходит новый review
  и при необходимости публикуется отдельным обычным commit без amend.
- Следующий шаг: независимая проверка whitelist, моделей/effort, схемы TOML и
  бюджета; затем публикация по действующему `AGENTS.md`. Merge и закрытие Issue
  остаются за orchestrator после подтверждённого merge gate.
