# #480 · класс B · LOCAL READY

Ветка `feat/480-paper-run-lifecycle`, база `d1d877f01aa841e00870de464f18cf91038d1b96`, рабочая копия `/private/tmp/bybit-issue-480`.
В `internal/momentum/storage/paper_run_lifecycle.go` добавлено чтение конкретного запуска в REPEATABLE READ READ ONLY и внутри переданной транзакции. Проверяются исходный config, вход, последняя ревизия и фактический итог с SQL-временем.
В `internal/momentum/storage/paper_input_read.go` размер причины предшественника проверяется в том же SQL-запросе. Тесты LF1–LF4 в `internal/momentum/storage/paper_run_lifecycle_test.go` проверяют сохранённые байты, фазы, повреждения, лимиты и изоляцию. Размер diff после gofmt — 194 production, 294 тестовых строк.
На четырёх отдельных PostgreSQL 17.11 прошли targeted, go test ./..., race и coverage. Целевые тесты без skips, покрытие модуля 91.8%. Удаление каждого контейнера проверено по ID.
На Go 1.26.11 прошли gofmt, vet, build, golangci-lint и govulncheck с локальной базой от 2026-11-11. Вывод, ID и хэши сохранены в `/private/tmp/issue480-logs`.
Следующий шаг — независимый review и публикация по указанию координатора. PR и CI на этой стадии ещё отсутствуют.
