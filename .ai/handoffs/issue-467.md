# Issue #467. Чтение immutable paper input и head

Класс B. Ветка `feat/467-paper-input-read`, база `c6ab45856c2045cfb85202ae9177b7421838cca9` (PR #470). Рабочая копия `/private/tmp/bybit-issue-467`.

В `internal/momentum/storage/paper_input_read.go` функции ReadPaperInput и ReadPaperInputHead проверяют canonical payload, hash, конфигурацию и immutable run, включая onboarding_id. Обе используют один SELECT на переданном соединении или транзакции. Для следующего входа тот же запрос получает predecessor и completion, затем общий decodePaperCompletion сверяет receipt с планом и исходом. Чтение не устанавливает fence для дальнейшего исполнения.

В `internal/momentum/storage/paper_input_read_test.go` проверены IR1–IR5 на опубликованных фикстурах #468. Матрица содержит 41 случай повреждения, включая SQL NULL и пустые байты, обе формы onboarding, неверные NUMERIC и подмену hash декодированного receipt. Отдельно проверены codec-valid opaque ModelID, оба исхода, время записи, точные байты после переподключения, изоляция второго пула и чтение из read-only транзакции. Шесть прежних случаев SQL receipt покрывает TestPaperCompletionReadCorrupt из #468.

Добавлено 141 строка production и 271 строка в полном тестовом файле. Третий путь — `.ai/handoffs/issue-467.md`. Исходный снимок WIP сохранён в `/private/tmp/issue467-astra-wip.9TQhzu`. Команды, сырые результаты, матрица и контрольные суммы находятся в `/private/tmp/issue467-astra-logs`.

Локальные проверки завершены. На Go 1.26.11 прошли gofmt, targeted, go test ./..., go test -race ./..., coverage, go vet ./..., go build ./... и golangci-lint v2.10.1. Покрытие всего модуля 91.9%, файла reader 100%. Четыре независимых прогона использовали свежий PostgreSQL 17.11. Удаление каждого контейнера подтверждено по ID. Govulncheck с локальной базой от 2026-11-11 завершился без находок. При первом lint выявлены два SA1012 в тестах намеренно пустого context. Обе проверки перенесены в таблицу аргументов и повторены на итоговых файлах.

Статус LOCAL READY. Следующий шаг — независимое review, затем публикация по указанию координатора. Проверка CI относится к опубликованному head PR.
