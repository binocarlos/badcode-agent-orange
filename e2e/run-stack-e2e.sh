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

# reload_agentd_with_script SCRIPT_JSON — recreates agentd carrying (or no
# longer carrying) a mock model script, then waits for the stack to answer.
#
# The script is stack configuration read ONCE at boot (mock mode only), so
# handing the model a tool call means restarting agentd. Passing "" restores the
# ordinary canned model.
#
# Returns non-zero if agentd does not come back. A malformed script is a
# deliberate boot failure, and without this check the stack would simply be left
# dead with a timeout as the only clue.
reload_agentd_with_script() {
  AGENTKIT_MOCK_MODEL_SCRIPT="$1" "${COMPOSE[@]}" up -d --no-deps agentd >/dev/null 2>&1
  local deadline=$(( $(date +%s) + 60 ))
  until curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep 2
  done
}

# cmd_test [mode] [--mock-script FILE] [-- <playwright args>]
#
# --mock-script is deliberately PER RUN rather than baked into `up`: the script
# is agentd-wide and read at boot, so leaving one loaded would quietly change
# the model's behaviour for every later run and for anyone else sharing the
# stack. So this loads it, runs the tests, and restores the plain model
# afterwards — even if the tests fail.
#
# Specs that need a scripted tool call gate on STACK_MOCK_SCRIPT, which is set
# only for the duration of such a run.
cmd_test() {
  local recorded="" mode="" script="" args=()
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"
  while [ $# -gt 0 ]; do
    case "$1" in
      --mock-script) script="${2:-}"; shift 2 ;;
      --) shift; args+=("$@"); break ;;
      *) [ -z "$mode" ] && mode="$1" || args+=("$1"); shift ;;
    esac
  done
  mode="${mode:-${recorded:-mock}}"

  if [ -n "$recorded" ] && [ "$mode" != "$recorded" ]; then
    echo "running stack is in '$recorded' mode, not '$mode' — run: $0 up $mode" >&2
    return 1
  fi
  curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1 ||
    { echo "no stack listening at $WEB_URL — run: $0 up $mode" >&2; return 1; }
  ensure_test_deps

  local script_json=""
  if [ -n "$script" ]; then
    [ "$mode" = "mock" ] ||
      { echo "--mock-script only applies in mock mode (stack is '$mode')" >&2; return 1; }
    [ -f "$script" ] || { echo "no such mock script: $script" >&2; return 1; }
    script_json="$(cat "$script")"
    echo "── stack e2e: loading mock model script $script into agentd ──"
    if ! reload_agentd_with_script "$script_json"; then
      # agentd refuses to boot on a malformed script, by design. Say why, and
      # put the stack back — a bad script must cost you a run, not the stack.
      echo "agentd did not come back with that script. Its own words:" >&2
      "${COMPOSE[@]}" logs --tail 5 agentd 2>&1 | sed 's/^/    /' >&2
      echo "── stack e2e: restoring the plain mock model ──" >&2
      reload_agentd_with_script "" ||
        echo "agentd is still down; try: $0 up mock" >&2
      return 1
    fi
    # Restore the plain model however the run ends.
    trap 'echo "── stack e2e: unloading mock model script ──"; reload_agentd_with_script "" || true' RETURN
  fi

  echo "── stack e2e [$mode]: running playwright against $WEB_URL ──"
  (cd "$ROOT/e2e" && STACK_BASE_URL="$WEB_URL" STACK_E2E_MODE="$mode" \
    STACK_MOCK_SCRIPT="${script_json:+1}" \
    npx playwright test --config playwright.stack.config.ts "${args[@]}")
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

# cmd_clean removes leftover session containers — and then restarts agentd,
# which is NOT optional.
#
# Pulling containers out from under a running agentd leaves its placement state
# describing instances that no longer exist, and it then refuses to provision
# ANY new session: every create fails with "has no running instance and no
# snapshot", including brand-new sessions, until it is restarted. That is a
# wedged stack produced by a command advertised as routine maintenance, so the
# restart happens here rather than in a note somebody has to read.
#
# Prefer deleting sessions through the API (the e2e suite's ProjectClient.cleanup
# does this in afterEach); reach for `clean` when a previous run left containers
# behind and nothing is going to delete them for you.
cmd_clean() {
  echo "── stack e2e: removing leftover session containers inside DinD ──"
  "${COMPOSE[@]}" exec -T dind sh -c \
    'docker ps -aq --filter name=sandbox- | xargs -r docker rm -f' || true

  # Wait for the removals to actually finish before restarting anything.
  # `docker rm -f` returns before the daemon is done, and agentd FATALS on boot
  # if it tries to reclaim a container that is mid-removal:
  #   recover (worker w1): dind recover: reclaim stopped container <id>:
  #   Error response from daemon: removal of container <id> is already in progress
  # Restarting straight after the rm is therefore a reliable way to kill agentd.
  local waited=0
  while [ "$waited" -lt 60 ]; do
    local left
    left="$("${COMPOSE[@]}" exec -T dind sh -c \
      'docker ps -aq --filter name=sandbox- | wc -l' 2>/dev/null | tr -d '[:space:]')"
    [ "${left:-0}" = "0" ] && break
    sleep 2
    waited=$((waited + 2))
  done

  # Only worth restarting something that is actually up.
  if "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx agentd; then
    echo "── stack e2e: restarting agentd (its placement state now names dead containers) ──"
    "${COMPOSE[@]}" restart agentd >/dev/null || true
    wait_ready
  fi
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
