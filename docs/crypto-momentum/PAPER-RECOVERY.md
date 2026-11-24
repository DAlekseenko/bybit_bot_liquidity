# Восстановление paper-журнала из резервной копии

`Recover` читает сохранённые входы по порядку, сверяет фактические исполнения с исходной моделью и проверяет текущий head повторно. На неинициализированной области чтение возвращает `storage.ErrPaperFactsAbsent`. При наличии одной конфигурации доступны её хэш, управление и регистрация владельца. Для продолжения run до сохранения первого входа нужны оригинальный cutoff, совпавший config hash и подтверждённые байты профиля. После сохранения входа финансовую модель задают его байты. Для открытого head доступны `Current` и `Progress`, для предыдущего завершённого цикла — `Last`.

## Проверка A16 на одноразовом PostgreSQL 17.11

Установите клиентские инструменты PostgreSQL 17.11, создайте отдельный локальный сервер и пустую административную базу `momentum_test_capture`. Задайте окружение libpq для этого сервера и `MOMENTUM_TEST_POSTGRES_DSN` в формате loopback URI из README. Команды требуют доступа только к этому тестовому серверу. Выполняйте их в Bash из корня репозитория. На время проверки сервер используется одним оператором.

```bash
set -euo pipefail
test "$(psql -XAt -d momentum_test_capture -c 'SHOW server_version_num')" = 170011
pg_dump --version
pg_restore --version
work=$(mktemp -d)
go test -c -o "$work/paper.test" ./internal/momentum/paper
names() {
  psql -XAt -d momentum_test_capture -c \
    "SELECT datname FROM pg_database WHERE datname ~ '^momentum_test_[0-9a-f]{24}$' ORDER BY datname"
}
for kind in Wide Thirds; do
  names > "$work/before"
  "$work/paper.test" -test.v -test.run="^TestRecorded${kind}$"
  names > "$work/after"
  comm -13 "$work/before" "$work/after" > "$work/new"
  test "$(wc -l < "$work/new")" -eq 1
  export PAPER_A16_SOURCE=$(cat "$work/new")
  export PAPER_A16_CASE=$(printf '%s' "$kind" | tr '[:upper:]' '[:lower:]')
  unset PAPER_A16_TARGET
  fifo="$work/$kind.fifo"
  mkfifo "$fifo"
  exec 3<>"$fifo"
  "$work/paper.test" -test.v -test.run='^TestRecoveryProof$' <"$fifo" >"$work/$kind.owner" 2>&1 &
  holder=$!
  for ((attempt=0; attempt<60; attempt++)); do
    if grep -Fxq "A16 READY $PAPER_A16_SOURCE" "$work/$kind.owner"; then break; fi
    kill -0 "$holder"
    sleep 1
  done
  grep -Fxq "A16 READY $PAPER_A16_SOURCE" "$work/$kind.owner"
  pg_dump -Fc -d "$PAPER_A16_SOURCE" -f "$work/$kind.dump"
  wc -c "$work/$kind.dump"
  openssl dgst -sha256 "$work/$kind.dump"
  export PAPER_A16_TARGET="momentum_test_$(openssl rand -hex 12)"
  createdb --template=template0 "$PAPER_A16_TARGET"
  pg_restore --exit-on-error -d "$PAPER_A16_TARGET" "$work/$kind.dump"
  "$work/paper.test" -test.v -test.run='^TestRecoveryProof$' | tee "$work/$kind.restore"
  printf x >&3
  wait "$holder"
  exec 3>&-
  unset PAPER_A16_SOURCE PAPER_A16_TARGET PAPER_A16_CASE
done
```

Исходный процесс удерживает собственный advisory lock до завершения сверки. Процесс чтения проверяет восстановленную БД до приобретения своего владельца, затем повторяет проверку после takeover. Сохраните размер и SHA256 обоих архивов, статусы команд, имена баз, версии и вывод теста. Тест выводит хэши proof и receipt, количества run, input, intent, fill, проводок FILL/FEE и регистраций, финансовый и аудиторский хэши, идентификаторы активной и вытесненной сессий. При takeover меняются регистрация и proof. Полное совпадение финансового хэша охватывает исходные количества, экономику, SQL-время и байты итогов.

После прерывания завершите удерживающий процесс, если он ещё запущен, и сохраните неуспешный протокол. После проверки освободите выделенный одноразовый сервер. Для возобновления работы потребитель приобретает собственный живой owner, сверяет session с новым proof и выполняет reconciliation. Атомарная смена режима отдельно проверяет управляющую ревизию и данные в своей транзакции.
