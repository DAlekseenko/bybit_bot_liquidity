# Передача работы

## Checkpoint — Issue #217, готов к независимому review

- Контракт: [Issue #217](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/217), CM-008D. Ветка `codex/217-configurable-symbols`, база и стартовый HEAD `8323a163d9ba815c258a326fc62305de6829beb2`; PR пока нет. Scope — ровно шесть файлов из Issue. Предыдущая передача сохранена побайтно в [архиве](HANDOFF-ARCHIVE-2026-11-05-cm008d.md).
- Добавлен pure `config.CanonicalSymbols`: ровно три уникальных ASCII base `A-Z/0-9` + `USDT`, сортировка копии, отказ без нормализации; Parse использует общий валидатор. Старые прочие guards и snapshot behavior сохранены.
- Изменение config-only. `BaselineQuantity` и `BaselineLongTargets` пока не интегрированы с выбираемым universe; нужна отдельная planner-integration Issue. Нет broker/API calls, торговли, изменения моделей, scalar, данных, planner, runtime, account или risk.
- Проверки Go 1.26.10 пройдены: gofmt, targeted/full tests, race, vet, build, lint и coverage 80.8%. govulncheck: нет вызываемых уязвимостей; один Windows-only module advisory GO-2026-5024 не считается all-clear. PostgreSQL CI smoke локально пропущен как opt-in и остаётся exact-head CI gate. Testnet dispatch не запускать.
- Следующий шаг: независимый fresh-context read-only GREEN по точным путям и SHA. После GREEN blobs замораживаются; тот же owner публикует обычным commit/push и открывает один PR в master. Merge, закрытие Issue и post-publication checks не входят в роль developer.
