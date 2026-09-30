#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/deploy" "$tmp/data" "$tmp/backups"
printf 'BOT_DATA_DIR=%s\nBOT_UID=%s\nBOT_GID=%s\nBYBIT_API_KEY=\nBYBIT_API_SECRET=\n' "$tmp/data" "$(id -u)" "$(id -g)" > "$tmp/server.env"
chmod 600 "$tmp/server.env"
printf 'database' > "$tmp/data/trades.db"
touch "$tmp/deploy/config.yaml" "$tmp/deploy/docker-compose.release.yml"
printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
printf '%s\n' '#!/usr/bin/env bash' 'while [[ "$1" != bash ]]; do shift; done' 'shift' 'exec /bin/bash "$@"' > "$tmp/bin/ssh"
cat > "$tmp/bin/docker" <<'SH'
#!/usr/bin/env bash
set -e
state=${TEST_STATE:?}
if [[ "$1" == info ]]; then
  [[ "${FAKE_ROOTLESS:-0}" == 1 ]] && echo '["name=rootless"]' || echo '[]'
elif [[ "$1" == run ]]; then
  [[ "$*" == *"--user $(id -u):$(id -g) "* ]] || exit 1
  [[ "${FAIL_BACKUP:-}" != 1 ]] || exit 1
  for arg in "$@"; do
    if [[ "$arg" == .backup* ]]; then touch "$TEST_BACKUP_DIR/${arg##*/}"; exit 0; fi
  done
  echo ok
elif [[ "$1" == compose ]]; then
  command=""
  for arg in "$@"; do [[ "$arg" != pull && "$arg" != up && "$arg" != ps ]] || { command="$arg"; break; }; done
  case "$command" in
    pull) [[ "${FAIL_MODE:-}" != pull || "$IMAGE_REF" != *0002 ]] ;;
    up) [[ "${FAIL_MODE:-}" != up || "$IMAGE_REF" != *0002 ]] || exit 1; printf '%s' "$IMAGE_REF" > "$state/current" ;;
    ps) echo container-id ;;
  esac
elif [[ "$1" == inspect ]]; then
  current=$(<"$state/current")
  if [[ "$2" == --format && "$3" == *Config.Image* ]]; then echo "$current"; else [[ "${FAIL_HEALTH:-}" != all && ( -z "${BAD_DIGEST:-}" || "$current" != *"$BAD_DIGEST"* ) ]] && echo healthy || echo unhealthy; fi
elif [[ "$1" == exec ]]; then
  if [[ "${FAIL_METRIC_FIRST:-0}" == 1 ]]; then
    count=0; [[ ! -r "$state/metric-calls" ]] || count=$(<"$state/metric-calls"); count=$((count + 1)); printf '%s' "$count" > "$state/metric-calls"
    [[ "$count" != 1 ]] || exit 1
  fi
  echo "bybit_bot_market_data_last_success_unixtime $(date +%s)"
fi
SH
chmod +x "$tmp/bin/ssh" "$tmp/bin/docker"
printf '%s\n' '#!/usr/bin/env bash' '[[ "$1" == -n && "$2" == 9 ]]' > "$tmp/bin/flock"; chmod +x "$tmp/bin/flock"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$tmp/bin/sleep"; chmod +x "$tmp/bin/sleep"
run_deploy() {
  PATH="$tmp/bin:$PATH" SSH_BIN=ssh SSH_CONFIG=fake SERVER_ENV_FILE="$tmp/server.env" TEST_BACKUP_DIR="${TEST_BACKUP_DIR:-$tmp/deploy/backups}" DEPLOY_PATH="$tmp/deploy" IMAGE_DIGEST="sha256:$(printf '%064d' 2)" HEALTH_TIMEOUT_SECONDS="${HEALTH_TIMEOUT_SECONDS:-0}" bash "$root/ops/deploy-testnet.sh" >/dev/null 2>&1
}
success_case() {
  TEST_STATE="$tmp/success"; export TEST_STATE; mkdir -p "$TEST_STATE"
  printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
  FAKE_ROOTLESS=0 run_deploy
  grep -q 'sha256:' "$TEST_STATE/current"
  [[ -n "$(find "$tmp/deploy/backups" -type f -print -quit)" ]]
  local current; current=$(<"$TEST_STATE/current"); [[ "$(<"$TEST_STATE/current")" == "$current" ]]
}
first_deploy_case() {
  local TEST_BACKUP_DIR="$tmp/first-backups"
  TEST_STATE="$tmp/first"; export TEST_STATE; mkdir -p "$TEST_STATE"
  rm "$tmp/deploy/.last-image-digest"
  mkdir -p "$TEST_BACKUP_DIR"
  run_deploy
  [[ -n "$(find "$TEST_BACKUP_DIR" -type f -print -quit)" ]]
  grep -q 'sha256:' "$TEST_STATE/current"
}
metric_retry_case() {
  TEST_STATE="$tmp/metric-retry"; export TEST_STATE; mkdir -p "$TEST_STATE"
  printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
  FAIL_METRIC_FIRST=1 HEALTH_TIMEOUT_SECONDS=10 run_deploy
  [[ "$(<"$TEST_STATE/metric-calls")" -ge 2 ]]
  grep -q 'sha256:' "$TEST_STATE/current"
}
rootless_refusal_case() {
  local TEST_BACKUP_DIR="$tmp/rootless-backups" TEST_STATE="$tmp/rootless"; export TEST_STATE; mkdir -p "$TEST_STATE" "$TEST_BACKUP_DIR"
  if FAKE_ROOTLESS=1 run_deploy; then exit 1; fi
  [[ ! -e "$TEST_STATE/current" && -z "$(find "$TEST_BACKUP_DIR" -type f -print -quit)" ]]
}
missing_database_case() {
  local TEST_BACKUP_DIR="$tmp/missing-db-backups" TEST_STATE="$tmp/missing-db"; export TEST_STATE; mkdir -p "$TEST_STATE" "$TEST_BACKUP_DIR"
  mv "$tmp/data/trades.db" "$tmp/data/trades.db.saved"
  if run_deploy; then exit 1; fi
  mv "$tmp/data/trades.db.saved" "$tmp/data/trades.db"
  [[ ! -e "$TEST_STATE/current" && -z "$(find "$TEST_BACKUP_DIR" -type f -print -quit)" ]]
}
backup_failure_case() {
  TEST_STATE="$tmp/backup"; export TEST_STATE; mkdir -p "$TEST_STATE"
  printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
  if FAIL_BACKUP=1 run_deploy; then exit 1; fi
  [[ ! -e "$TEST_STATE/current" ]]
}
rollback_case() {
  TEST_STATE="$tmp/rollback"; export TEST_STATE; mkdir -p "$TEST_STATE"; BAD_DIGEST="sha256:$(printf '%064d' 2)"
  printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
  export BAD_DIGEST
  if BAD_DIGEST="$BAD_DIGEST" run_deploy; then exit 1; fi
  [[ "$(<"$TEST_STATE/current")" == "ghcr.io/dalekseenko/bybit_bot_liquidity@sha256:$(printf '%064d' 1)" ]]
}
rollback_failure_case() {
  TEST_STATE="$tmp/rollback-fail"; export TEST_STATE; mkdir -p "$TEST_STATE"; FAIL_HEALTH=all
  printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
  export FAIL_HEALTH
  if FAIL_HEALTH=all run_deploy; then exit 1; fi
  grep -q 'sha256:' "$TEST_STATE/current"
}
grep -Fq 'known_host_lookup="[$DEPLOY_HOST]:$DEPLOY_PORT"' "$root/.github/workflows/testnet-deploy.yml" && grep -Fq 'set -o noclobber; : > "$backup_dir/$backup_file"' "$root/ops/deploy-testnet.sh"
old_container_failure_cases() {
  local mode
  for mode in pull up; do
    TEST_STATE="$tmp/old-$mode"; export TEST_STATE; mkdir -p "$TEST_STATE"
    printf 'sha256:%064d\n' 1 > "$tmp/deploy/.last-image-digest"
    printf '%s' "ghcr.io/dalekseenko/bybit_bot_liquidity@sha256:$(printf '%064d' 1)" > "$TEST_STATE/current"
    if FAIL_MODE="$mode" run_deploy; then exit 1; fi
    grep -q 'sha256:' "$TEST_STATE/current"
    grep -q "$(printf '%064d' 1)" "$tmp/deploy/.last-image-digest"
  done
}
success_case; first_deploy_case; metric_retry_case; rootless_refusal_case; missing_database_case; backup_failure_case; rollback_case; rollback_failure_case; old_container_failure_cases
echo 'fake SSH/Docker deployment cases passed'
