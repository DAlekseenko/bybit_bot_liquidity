# #468 · Чтение результата paper-цикла

Класс B. Ветка `feat/468-paper-completion-read` от `67ab45fb56e315d640e0c1765208cd6a335607f3`. Рабочая копия `/private/tmp/bybit-issue-468`.

`internal/momentum/storage/paper_completion_read.go` добавляет `ReadPaperCompletion`. Один SELECT от completion проверяет в одном снимке точные байты input/config, SQL-связи и время. При отсутствии строки возвращается пустой результат. Повреждённые связи, неподходящий аргумент и недоступность БД имеют отдельные ошибки. Канонический receipt декодируется с привязкой к input, исходу и плану. Для будущего финансового действия требуется свежая проверка в его транзакции.

`internal/momentum/storage/paper_completion_read_test.go` проверяет PC1–PC4 на одноразовом PostgreSQL 17.11. Зафиксированы обе пары исходов, точные байты и SHA после переподключения, дробь 1/3 и 4096-битное значение, UTF-8 reason >10 КБ, отсутствие строки, подмена input, нулевые результаты ошибок, повреждения зависимостей и SQL metadata, другой canonical config с корректным SHA, provisional/read-only TX. Целевые тесты прошли. Размер после gofmt — 94 production и 296 test lines.

Сигнатуры общих фикстур в пакете storage:

```go
func paperInputFixture(t *testing.T, db *sql.DB, sequence uint64, previous paperwire.InputRecord, receipt, account string, bound bool) paperwire.InputRecord
func paperInputCompletion(t *testing.T, db *sql.DB, input paperwire.InputRecord, kind string) (RunCompletion, string)
func paperInputSeed(t *testing.T, q interface { ExecContext(context.Context, string, ...any) (sql.Result, error) }, in paperwire.InputRecord)
```

`bound` связывает ModelID с hash профиля. При nil DB fixture строит только codec-valid input. Completion helper сохраняет plan revision и возвращает метаданные для отдельной записи completion. Seed вставляет input через переданный DB/TX.

Проверки прошли на Go 1.26.11 из go.mod. Gofmt и whitespace без замечаний. Целевые тесты выполнили четыре группы, включая 34 подстановки повреждённых данных. На отдельных PostgreSQL 17.11 прошли `go test -count=1 ./...`, `go test -race -count=1 ./...` и полный coverage 91.8% при пороге 70%. CI driver smoke подтвердил server_version_num=170011. Удаление каждого собственного контейнера подтверждено по ID. `go vet ./...`, `go build ./...`, golangci-lint 2.10.1 и govulncheck прошли. Проверка уязвимостей использовала локальную копию публичной базы с отметкой 2026-11-11T02:51:11Z. Команды и логи находятся в `/private/tmp/issue468-logs`.

Локальный статус READY. Следующий шаг — завершить независимое review, затем проверить CI на опубликованном head PR.
