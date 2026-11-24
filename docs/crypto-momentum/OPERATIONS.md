# Эксплуатация Crypto Momentum

Документ описывает локальный запуск Momentum в Docker Compose. Реальные деньги
не затрагиваются: Compose не передаёт контейнеру ключей Bybit и не включает
Mainnet. Исполнение на Testnet подключается отдельно (CM-054).

## Docker Compose (CM-057)

Состав — отдельный opt-in проект `momentum`, legacy `docker-compose.yml`, его
volumes и данные не используются:

| Файл | Назначение |
| --- | --- |
| `Dockerfile.momentum` | Сборка `cmd/momentum`; базовые образы закреплены по digest, итоговый образ distroless nonroot (UID 65532) |
| `Dockerfile.momentum.dockerignore` | В контекст сборки попадают только `go.mod`, `go.sum`, `cmd/`, `internal/`, `configs/`; `data/`, `dist/` и `.env` исключены |
| `ops/docker-compose.momentum.yml` | Сервисы `postgres` и `momentum` |
| `ops/momentum.env.example` | Пример env; рабочий `ops/momentum.env` в Git не попадает |

Свойства:

- PostgreSQL 17 с данными в именованном volume `momentum_momentum-pgdata`; порты
  наружу не публикуются, база слушает только `127.0.0.1`. `momentum` делит с ней
  сетевой namespace (`network_mode: service:postgres`), потому что storage
  принимает только loopback DSN.
- `momentum` ждёт healthcheck базы, сам применяет миграции и выполняет
  `collect-once` по `configs/momentum.paper.yaml`: только публичные данные Bybit
  Testnet, без торговых вызовов. Контейнер read-only, `cap_drop: ALL`,
  `no-new-privileges`, без перезапуска.
- Ключей Bybit в Compose нет, поэтому отправка заявок невозможна.

### Запуск

```bash
cp ops/momentum.env.example ops/momentum.env   # задать MOMENTUM_POSTGRES_PASSWORD
docker compose -f ops/docker-compose.momentum.yml --env-file ops/momentum.env up --build momentum
```

Успешный сбор печатает одну JSON-строку с `batch_hash` и `cutoff_utc`. Каждый
запуск сохраняет batch под его hash; тот же batch повторно не записывается, а
изменившиеся публичные данные дают новый batch. Пароль — только `[A-Za-z0-9._-]`: он
подставляется в DSN без экранирования.

### Остановка и данные

```bash
docker compose -f ops/docker-compose.momentum.yml --env-file ops/momentum.env down      # данные сохраняются
```

`down -v` удаляет volume базы безвозвратно — только для одноразовых проверок.
Для smoke используйте отдельное имя проекта (`-p momentum-smoke`), чтобы не
задеть рабочий volume. Резервное копирование и восстановление — CM-058.

### Обновление образов

Digest базовых образов меняется отдельным PR: обновить `FROM` и образ
`postgres`, собрать образ и повторить smoke. Мажорная версия PostgreSQL на
существующем volume не обновляется без резервной копии и плана миграции.
