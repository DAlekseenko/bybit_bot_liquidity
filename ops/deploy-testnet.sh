#!/usr/bin/env bash
set -euo pipefail

# Проверяет аргументы локального запуска и передаёт скрипт через проверенный SSH.
local_deploy() {
  local script="${BASH_SOURCE[0]}"
  [[ -n "${SSH_CONFIG:-}" && -n "${DEPLOY_PATH:-}" && -n "${IMAGE_DIGEST:-}" ]]
  [[ "$IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]
  [[ "$DEPLOY_PATH" =~ ^/[A-Za-z0-9._/-]+$ && "$DEPLOY_PATH" != / && "$DEPLOY_PATH" != *..* ]]
  "${SSH_BIN:-ssh}" -F "$SSH_CONFIG" testnet-server bash -s -- --remote "$DEPLOY_PATH" "$IMAGE_DIGEST" < "$script"
}

# Выполняет backup SQLite, переключение на digest и проверку здоровья на сервере.
remote_deploy() {
  local path="$1" digest="$2" env_file="${SERVER_ENV_FILE:-/etc/bybit-bot/server.env}" compose backup_dir backup_file previous="" health_timeout deploy_user security_options
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ && "$path" =~ ^/[A-Za-z0-9._/-]+$ && "$path" != / && "$path" != *..* ]]
  [[ -r "$env_file" && -d "$path" && -r "$path/docker-compose.release.yml" && -r "$path/config.yaml" ]]
  set -a
  # Загружает только администраторский файл окружения, оставленный на сервере.
  . "$env_file"
  set +a
  [[ "${BOT_DATA_DIR:-}" == /* && -d "$BOT_DATA_DIR" ]]
  security_options=$(docker info --format '{{json .SecurityOptions}}') || { echo 'docker info недоступен; режим пользователя не определён' >&2; return 1; }
  [[ "$security_options" != *rootless* ]] || { echo 'rootless Docker не поддерживается этим non-root deployment' >&2; return 1; }
  deploy_user="$(id -u):$(id -g)"
  [[ "$deploy_user" == "${BOT_UID:-1000}:${BOT_GID:-1000}" ]] || { echo 'UID/GID deploy-пользователя должны совпадать с BOT_UID/BOT_GID' >&2; return 1; }
  [[ -f "$BOT_DATA_DIR/trades.db" ]] || { echo 'SQLite database отсутствует; выполните только документированный ручной bootstrap' >&2; return 1; }
  [[ -w "$BOT_DATA_DIR" && -w "$BOT_DATA_DIR/trades.db" ]] || { echo 'deploy-пользователь не может записывать в каталог данных и SQLite database' >&2; return 1; }
  local database_owner
  database_owner=$(stat -c '%u:%g' "$BOT_DATA_DIR/trades.db" 2>/dev/null || stat -f '%u:%g' "$BOT_DATA_DIR/trades.db")
  [[ "$database_owner" == "$deploy_user" ]] || { echo 'SQLite database должна принадлежать BOT_UID:BOT_GID' >&2; return 1; }
  local database_check
  database_check=$(docker run --rm --user "$deploy_user" -v "$BOT_DATA_DIR:/data" keinos/sqlite3:3.53.4 /usr/bin/sqlite3 /data/trades.db 'BEGIN IMMEDIATE; ROLLBACK; PRAGMA quick_check;')
  [[ "$database_check" == ok ]] || { echo 'SQLite database недоступна для записи или quick_check завершился ошибкой' >&2; return 1; }
  exec 9>"$path/.deploy.lock"
  flock -n 9 || { echo 'deploy уже выполняется' >&2; return 1; }
  compose=(compose --env-file "$env_file" --project-directory "$path" -f "$path/docker-compose.release.yml")
  backup_dir="$path/backups"
  install -d -m 0700 "$backup_dir"
  chmod 0700 "$backup_dir"
  backup_file="trades-$(date -u +%Y%m%dT%H%M%SZ)-$$.db"
  # Создаёт новое имя атомарно и завершает deploy при любой коллизии.
  (umask 077; set -o noclobber; : > "$backup_dir/$backup_file")
  # Создаёт согласованную копию SQLite до переключения контейнера.
  docker run --rm --user "$deploy_user" -v "$BOT_DATA_DIR:/data" -v "$backup_dir:/backup" keinos/sqlite3:3.53.4 /usr/bin/sqlite3 /data/trades.db ".backup /backup/$backup_file"
  chmod 0600 "$backup_dir/$backup_file"
  local check
  check=$(docker run --rm --user "$deploy_user" -v "$backup_dir:/backup:ro" keinos/sqlite3:3.53.4 /usr/bin/sqlite3 -readonly "/backup/$backup_file" 'PRAGMA quick_check;')
  [[ "$check" == ok ]] || { echo 'quick_check backup завершился ошибкой' >&2; return 1; }
  [[ -r "$path/.last-image-digest" ]] && previous=$(<"$path/.last-image-digest")
  [[ "$previous" =~ ^sha256:[0-9a-f]{64}$ ]] || previous=""
  export DEPLOY_PATH="$path" IMAGE_REF="ghcr.io/dalekseenko/bybit_bot_liquidity@$digest"
  health_timeout="${HEALTH_TIMEOUT_SECONDS:-180}"
  # Ожидает healthy и свежую метрику последнего успешного market-data снимка.
  wait_ready() {
    local until=$(( $(date +%s) + health_timeout )) id status metric now actual_image
    while :; do
      id=$("${DOCKER_BIN:-docker}" "${compose[@]}" ps -q bot)
      if [[ -n "$id" ]]; then
        actual_image=$("${DOCKER_BIN:-docker}" inspect --format '{{.Config.Image}}' "$id" 2>/dev/null || true)
        status=$("${DOCKER_BIN:-docker}" inspect --format '{{.State.Health.Status}}' "$id" 2>/dev/null || true)
        metric=$("${DOCKER_BIN:-docker}" exec "$id" wget -q -O - http://127.0.0.1:8080/metrics 2>/dev/null | awk '$1 == "bybit_bot_market_data_last_success_unixtime" {print $2; exit}' || true)
        now=$(date +%s)
        if [[ "$actual_image" == "$IMAGE_REF" && "$status" == healthy && "$metric" =~ ^[0-9]+$ ]] && (( metric > 0 && now - metric <= 150 )); then return 0; fi
      fi
      (( $(date +%s) >= until )) && return 1
      sleep 5
    done
  }
  # Применяет только указанный image digest без сборки на хосте.
  activate() {
    IMAGE_REF="ghcr.io/dalekseenko/bybit_bot_liquidity@$1"
    export IMAGE_REF
    "${DOCKER_BIN:-docker}" "${compose[@]}" pull bot || return 1
    "${DOCKER_BIN:-docker}" "${compose[@]}" up -d --no-build --remove-orphans bot || return 1
    wait_ready
  }
  if activate "$digest"; then
    printf '%s\n' "$digest" > "$path/.last-image-digest"
    chmod 0600 "$path/.last-image-digest"
    echo 'Testnet read-only image запущен; SQLite backup сохранён.'
    return 0
  fi
  echo 'Новая версия не прошла health/freshness; выполняется только rollback image.' >&2
  if [[ -z "$previous" ]]; then
    echo 'Нет предыдущего digest: остановитесь и выполните ручную диагностику.' >&2
    return 1
  fi
  if ! activate "$previous"; then
    echo 'Rollback image не прошёл health/freshness; БД не восстановлена, требуется ручное вмешательство.' >&2
    return 1
  fi
  echo "Вернут image $previous; база не восстанавливалась." >&2
  return 1
}

if [[ "${1:-}" == --remote ]]; then
  remote_deploy "${2:-}" "${3:-}"
else
  local_deploy
fi
