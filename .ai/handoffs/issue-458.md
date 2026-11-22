# #458 · класс B · LOCAL READY

Ветка `feat/458-paper-input-journal`, база `23519f0e061512bbb34912a1d4c3484087892564`, clone `/private/tmp/bybit-issue-458`.
В `internal/momentum/storage/paper_input.go` реализован StorePaperInput. Одна READ COMMITTED транзакция удерживает controls SHARE, сверяет run/config/onboarding и цепочку receipt, записывает canonical bytes и читает их до commit. Исторический exact retry возвращает исходное время. При неподтверждённом commit возвращается Unavailable.
В `internal/momentum/storage/paper_input_test.go` WR1–WR5 проверяют исходные bytes/hash/time, concurrent INSERT, исторический fork, admission receipt, обе блокировки, отмену, потерю подтверждения и ошибки чтения. Размер после gofmt — 111 production, 300 тестовых строк, всего три пути с этим handoff.
До редакции тестов по review пройдены targeted, go test ./..., go test -race ./... и coverage на четырёх отдельных PostgreSQL 17.11. Удаление подтверждено по ID. Покрытие модуля 91.7%.
Go 1.26.11: gofmt, vet, build, golangci-lint 2.10.1 и govulncheck с локальной базой от 2026-11-11 прошли. Выводы, идентификаторы и SHA-256 записаны в `/private/tmp/issue458-astra-logs`.
В итоговой редакции каждый случай WR2, WR3 и проверки аргументов занимает свою строку. Повторно прошли все шесть тестов writer на отдельном PostgreSQL 17.11, gofmt и golangci-lint. Удаление контейнера подтверждено. Итоговый манифест — `/private/tmp/issue458-astra-logs/review-correction/manifest.json`.
Следующий шаг — повторное независимое review, затем публикация по указанию координатора. CI и официальный review относятся к будущему head PR.
