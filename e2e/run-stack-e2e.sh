#!/usr/bin/env bash
# Stack e2e lifecycle. Fast loop: bring the stack up once, run the browser test
# against it as many times as you like, tear down when done.
#
#   ./e2e/run-stack-e2e.sh up [mode]        build + start the stack, wait ready
#   ./e2e/run-stack-e2e.sh test [mode]      run playwright against the RUNNING stack
#   ./e2e/run-stack-e2e.sh down [--purge]   capture logs + stop (--purge also wipes volumes)
#   ./e2e/run-stack-e2e.sh clean            remove leftover session containers inside DinD
#   ./e2e/run-stack-e2e.sh run [mode|all]   clean-room: up → test → purge-down (the CI job)
#
# Legacy compat: a bare mode argument (`./e2e/run-stack-e2e.sh mock`) means `run <mode>`.
#
# Modes (default: mock):
#   mock          no model credentials → deterministic mock model. The CI signal.
#   api-key       real Anthropic API, billed to ANTHROPIC_API_KEY.
#   subscription  real Anthropic, billed to the Claude subscription via
#                 CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`).
#   all           (run only) the three modes in sequence, fail-fast.
#
# The mode is baked into agentd's env at `up`; switching modes is another `up`
# (compose restarts only agentd). `test` with no mode uses the running stack's
# recorded mode. Real-mode credentials come from the shell env or ./.env.
#
# Tests create their own run-scoped projects and delete their sessions in
# teardown, so repeated `test` runs against one stack don't collide.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$ROOT/docker-compose.yml" -f "$ROOT/docker-compose.stack-e2e.yml" --project-name agent-orange-stack-e2e)
WEB_URL="${STACK_BASE_URL:-http://localhost:8080}"
MODE_FILE="$ROOT/e2e/.stack-e2e-mode"

# env_or_dotenv VAR — prints $VAR, falling back to the VAR= line in ./.env.
env_or_dotenv() {
  local val="${!1:-}"
  if [ -z "$val" ] && [ -f "$ROOT/.env" ]; then
    val=$(grep -E "^$1=" "$ROOT/.env" | tail -1 | cut -d= -f2-)
  fi
  printf '%s' "$val"
}

# export_mode_creds MODE — exactly one credential per mode, for the compose overlay.
export_mode_creds() {
  export STACK_E2E_ANTHROPIC_API_KEY=""
  export STACK_E2E_CLAUDE_CODE_OAUTH_TOKEN=""
  case "$1" in
    mock) ;;
    api-key)
      STACK_E2E_ANTHROPIC_API_KEY="$(env_or_dotenv ANTHROPIC_API_KEY)"
      [ -n "$STACK_E2E_ANTHROPIC_API_KEY" ] ||
        { echo "api-key mode needs ANTHROPIC_API_KEY (shell env or .env)" >&2; return 1; }
      ;;
    subscription)
      STACK_E2E_CLAUDE_CODE_OAUTH_TOKEN="$(env_or_dotenv CLAUDE_CODE_OAUTH_TOKEN)"
      [ -n "$STACK_E2E_CLAUDE_CODE_OAUTH_TOKEN" ] ||
        { echo "subscription mode needs CLAUDE_CODE_OAUTH_TOKEN (shell env or .env; get one with: claude setup-token)" >&2; return 1; }
      ;;
    *) echo "unknown mode: $1 (want mock|api-key|subscription)" >&2; return 1 ;;
  esac
}

ensure_test_deps() {
  cd "$ROOT/e2e"
  if [ ! -d node_modules ]; then
    echo "── stack e2e: installing e2e deps ──"
    yarn install --frozen-lockfile 2>/dev/null || npm install
  fi
  npx playwright install chromium
  cd "$ROOT"
}

wait_ready() {
  local deadline=$(( $(date +%s) + 300 ))
  until curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "stack did not become ready within 300s" >&2
      "${COMPOSE[@]}" ps >&2 || true
      "${COMPOSE[@]}" logs --tail 100 agentd web >&2 || true
      return 1
    fi
    sleep 2
  done
  echo "stack is up: $WEB_URL"
}

cmd_up() {
  local mode="${1:-mock}"
  export_mode_creds "$mode"
  echo "── stack e2e [$mode]: building + starting stack ──"
  "${COMPOSE[@]}" up --build -d
  echo "── stack e2e [$mode]: waiting for the stack to be ready ──"
  wait_ready
  echo "$mode" > "$MODE_FILE"
}

cmd_test() {
  local recorded=""
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"
  local mode="${1:-${recorded:-mock}}"
  if [ -n "$recorded" ] && [ "$mode" != "$recorded" ]; then
    echo "running stack is in '$recorded' mode, not '$mode' — run: $0 up $mode" >&2
    return 1
  fi
  curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1 ||
    { echo "no stack listening at $WEB_URL — run: $0 up $mode" >&2; return 1; }
  ensure_test_deps
  echo "── stack e2e [$mode]: running playwright against $WEB_URL ──"
  (cd "$ROOT/e2e" && STACK_BASE_URL="$WEB_URL" STACK_E2E_MODE="$mode" \
    npx playwright test --config playwright.stack.config.ts)
}

cmd_down() {
  local recorded="unknown"
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"
  echo "── stack e2e: capturing stack logs → e2e/stack-e2e-logs-$recorded.txt ──"
  "${COMPOSE[@]}" logs --no-color > "$ROOT/e2e/stack-e2e-logs-$recorded.txt" 2>&1 || true
  if [ "${1:-}" = "--purge" ]; then
    echo "── stack e2e: tearing down (purging volumes) ──"
    "${COMPOSE[@]}" down -v --remove-orphans || true
  else
    echo "── stack e2e: tearing down (volumes kept — next up skips rebuilds) ──"
    "${COMPOSE[@]}" down --remove-orphans || true
  fi
  rm -f "$MODE_FILE"
}

cmd_clean() {
  echo "── stack e2e: removing leftover session containers inside DinD ──"
  "${COMPOSE[@]}" exec -T dind sh -c \
    'docker ps -aq --filter name=sandbox- | xargs -r docker rm -f' || true
}

cmd_run() {
  local mode="${1:-mock}"
  local modes=("$mode")
  [ "$mode" = "all" ] && modes=(mock api-key subscription)
  ensure_test_deps
  trap 'cmd_down --purge >/dev/null 2>&1 || true' EXIT
  for m in "${modes[@]}"; do
    cmd_up "$m"   # re-up between modes restarts only agentd (env change)
    cmd_test "$m"
  done
  trap - EXIT
  cmd_down --purge
}

CMD="${1:-run}"
case "$CMD" in
  up|test|down|clean|run) shift || true; "cmd_$CMD" "$@" ;;
  mock|api-key|subscription|all) cmd_run "$CMD" ;;  # legacy: bare mode = clean-room run
  *) echo "usage: $0 <up|test|down|clean|run> [mode] — or a bare mode for 'run'" >&2; exit 1 ;;
esac
