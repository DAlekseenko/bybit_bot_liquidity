# Issue #426 — paper foundation v2, готово к независимому review
- Класс A; feat/426-paper-record-codec, /private/tmp/bybit-issue-426, base10fcfef4e0e3b04176b8bc023a57a1cb8cb39b09.
- Только paperwire/{wire,codec,amount,limits}.go и два парных test-файла; 654 production/644 tests (лимиты 700/700), без records stubs.
- Полные явные DTO, profile codec/hash, bounded JSON и точный ratio/SQL parser; one-symbol profile не зависит от config.CanonicalSymbols.
- R1: полный token budget 42+40×symbols проверяется до clone/sort/emission; 6552 symbols roundtrip/hash PASS, 6553 даёт nil bytes/empty hash и resource sentinel.
- Проверены canonical/deep-copy/revisions/durations/symbols, repeating4096-bit Rat, exact2^-4095 с trailing zeros, resource cap/cap+1 до typed decode, fake HTTPcounter0.
- Gofmt, targeted/full unit+coverage, full race, vet/build/lint/security и whitespace PASS; логи в /private/tmp/issue426-gates/. Coverage: всего75.9%, paperwire95.5%; govulncheck использовал локальную сохранённую базу, lint0issues.
- PostgreSQL opt-in не включался: SQL не меняется; реальные Testnet contracts явно выключены, бот не запускался.
- Старый721-line STOP и pre-R1 snapshot сохранены root в /private/tmp/issue426-v1-before-split и /private/tmp/issue426-v2-before-token-fix; record codec/adapters/cases принадлежат Issue427, не этой foundation.
- Следующее: повторный independent review после CHANGES_REQUESTED R1, затем публикация G11 по отдельному grant; stage/commit/push/PR не выполнялись, source после READY не меняется.
