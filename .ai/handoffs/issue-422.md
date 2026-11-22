# Issue #422 v3 — review P2 исправлен, готово к повторному review
- Owner /root/paper422_developer; feat/422-paper-command-state, /private/tmp/bybit-issue-422; база 10fcfef4e0e3b04176b8bc023a57a1cb8cb39b09.
- Только paper_commands.go, paper_commands_test.go и этот handoff: 224 production / 300 test строк / 3 файла, нормальный gofmt.
- P2: TrimSpace применяется только к классификации SYSTEM JSON-prefix; decode и canonical byte comparison проверяют неизменённый raw audit.
- Named bad-system усилен на whitespace+NULL-ID; actual PostgreSQL regression PASS, остальные read cases и INIT/fence scope сохранены.
- Full PG+coverage и fresh full PG-race PASS: 17 пакетов, 0 ошибок/пропусков, по242 БД; coverage total91.7%; все3 prefix-fix fixtures удалены с ID/label proof.
- Gofmt/whitespace, vet/build/lint0issues/offline govulncheck PASS на финальных source/base; логи и PG receipt: /private/tmp/issue422-prefix-fix-gates/.
- Прежний READY/source сохранён и побайтно сверен в /private/tmp/issue422-v3-before-prefix-fix; legacy SYSTEM с opaque ID/plaintext остаётся fail-closed.
- OPERATOR writes/4lock orders — #424 после merge422; real fill/finalizer — #87. Новый READY: /private/tmp/issue422-ready.json.
- Без stage/commit/push/PR; файлы больше не меняются. Далее independent changed-diff review и отдельный commit grant G11, затем CI/merge координатора.
