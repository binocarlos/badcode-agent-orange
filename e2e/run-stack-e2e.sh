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

# reload_agentd — recreate agentd carrying whatever AGENTKIT_* overrides the
# caller has in its environment, then wait for the stack to answer. Every
# per-run reconfiguration goes through here so there is one place that knows how
# long to wait and what "back up" means.
reload_agentd() {
  "${COMPOSE[@]}" up -d --no-deps agentd >/dev/null 2>&1
  local deadline=$(( $(date +%s) + 60 ))
  until curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep 2
  done
}

# The base of the narrowed pool --port-pool builds. Deliberately far from the
# 30001-30100 default: a leftover container from an ordinary run holds a port in
# the default range, and a narrowed pool that overlapped it would fail for that
# reason instead of the one under test.
PORT_POOL_BASE=40000

# The §8.6 retirement streak this stack runs with. Set in
# docker-compose.stack-e2e.yml; mirrored here ONLY to tell the spec what to
# expect, so the two cannot drift silently into a test that waits for a fifth
# firing that will never come. Keep the two in step.
SCHEDULE_MAX_PROVISION_FAILURES=2

# reload_agentd_with SCRIPT POOL_SIZE IDLE — ONE reload carrying every override
# at once.
#
# An empty argument means "the stack's default for this knob", and it is passed
# as an EMPTY STRING rather than unset: agentd treats empty as unset (compose's
# `${VAR:-}` cannot distinguish the two), and leaving a stale value exported
# would silently reconfigure every later run on this stack — the kind of leak
# this suite exists to refuse.
#
# This replaced a pair of per-knob helpers that could not be combined, and the
# refusal to combine them was hiding a bug rather than expressing a limit: each
# installed its own `trap … RETURN`, and a second RETURN trap REPLACES the first
# rather than adding to it, so two overrides would have restored only the last.
# The architect-archivist spec needs a mock script AND a short idle timeout
# together, so both had to go. One reload, one trap, all the knobs.
reload_agentd_with() {
  local script="${1:-}" pool="${2:-}" idle="${3:-}"
  local start="" end=""
  if [ -n "$pool" ]; then
    start="$PORT_POOL_BASE"
    end="$(( PORT_POOL_BASE + pool - 1 ))"
  fi
  AGENTKIT_MOCK_MODEL_SCRIPT="$script" \
    AGENTKIT_PORT_RANGE_START="$start" \
    AGENTKIT_PORT_RANGE_END="$end" \
    AGENTKIT_SESSION_IDLE_TIMEOUT="$idle" \
    reload_agentd
}

# cmd_test [mode] [--mock-script FILE] [--port-pool N] [-- <playwright args>]
#
# Both reconfiguration flags are deliberately PER RUN rather than baked into
# `up`: each is agentd-wide and read at boot, so leaving one loaded would
# quietly change the stack for every later run and for anyone else sharing it.
# So this applies them, runs the tests, and restores the ordinary stack
# afterwards — even if the tests fail.
#
#   --mock-script FILE  let the mock model emit tool_use (see mock-scripts/).
#                       Specs that need it gate on STACK_MOCK_SCRIPT.
#   --port-pool N       boot agentd with a session port pool of exactly N,
#                       instead of the default 100. This is what makes the
#                       exhaustion path reachable: filling a pool of three takes
#                       seconds, filling a hundred takes a hundred containers.
#                       Specs that need it gate on STACK_PORT_POOL.
#   --idle-timeout D    boot agentd with AGENTKIT_SESSION_IDLE_TIMEOUT=D instead
#                       of the default 30m. This is what makes "a conversation
#                       goes quiet" reachable: an interactive session emits its
#                       `worker.finished` from the archive sweep when it has been
#                       idle this long, and nothing downstream — the archivist
#                       above all — wakes until it does. agentd REFUSES anything
#                       under 1m (gc.go: the sweep only runs once a minute), and
#                       the Runner's tick scales to the timeout, so 1m is the
#                       floor and each quiescence costs up to ~2 minutes of wall
#                       clock. Specs that need it gate on STACK_IDLE_TIMEOUT.
cmd_test() {
  local recorded="" mode="" script="" pool="" idle="" args=()
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"
  while [ $# -gt 0 ]; do
    case "$1" in
      --mock-script) script="${2:-}"; shift 2 ;;
      --port-pool) pool="${2:-}"; shift 2 ;;
      --idle-timeout) idle="${2:-}"; shift 2 ;;
      --) shift; args+=("$@"); break ;;
      *) [ -z "$mode" ] && mode="$1" || args+=("$1"); shift ;;
    esac
  done
  mode="${mode:-${recorded:-mock}}"

  if [ -n "$pool" ]; then
    case "$pool" in
      ''|*[!0-9]*) echo "--port-pool wants a number of ports, got: $pool" >&2; return 1 ;;
    esac
    [ "$pool" -ge 1 ] || { echo "--port-pool must be at least 1" >&2; return 1; }
  fi
  if [ -n "$idle" ]; then
    # Shape only. agentd owns the real validation (and its refusal message
    # explains the one-minute floor better than a duplicate here could), but
    # catching an obvious typo before a container restart saves a minute.
    case "$idle" in
      *[0-9]s|*[0-9]m|*[0-9]h|off|0) ;;
      *) echo "--idle-timeout wants a duration like 1m, or \"off\", got: $idle" >&2; return 1 ;;
    esac
  fi

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
  fi

  local pool_range=""
  if [ -n "$pool" ]; then
    # Any session still running holds a port in the OLD range. Narrowing the
    # pool under them would leave agentd's accounting and the host disagreeing,
    # so refuse rather than produce an exhaustion that is nobody's fault.
    local live
    live="$("${COMPOSE[@]}" exec -T dind sh -c 'docker ps -q --filter name=sandbox- | wc -l' 2>/dev/null | tr -d '[:space:]')"
    if [ -n "$live" ] && [ "$live" != "0" ]; then
      echo "--port-pool needs an empty stack; $live session container(s) are still running." >&2
      echo "    ./e2e/run-stack-e2e.sh clean" >&2
      return 1
    fi
    pool_range="$PORT_POOL_BASE-$(( PORT_POOL_BASE + pool - 1 ))"
  fi

  # ONE reload carrying every override, and ONE restore trap. Two traps do not
  # compose: a second `trap … RETURN` replaces the first, so the older per-knob
  # version could only ever have restored the last override installed — which is
  # why it refused to combine them at all rather than fix the trap.
  if [ -n "$script_json" ] || [ -n "$pool" ] || [ -n "$idle" ]; then
    local what=""
    [ -n "$script_json" ] && what="$what mock-script=$script"
    [ -n "$pool" ] && what="$what port-pool=$pool ($pool_range)"
    [ -n "$idle" ] && what="$what idle-timeout=$idle"
    echo "── stack e2e: reloading agentd with$what ──"
    if ! reload_agentd_with "$script_json" "$pool" "$idle"; then
      # agentd refuses to boot on a malformed script or an out-of-range
      # duration, by design. Say why, and put the stack back — a bad override
      # must cost you a run, not the stack.
      echo "agentd did not come back with that configuration. Its own words:" >&2
      "${COMPOSE[@]}" logs --tail 5 agentd 2>&1 | sed 's/^/    /' >&2
      echo "── stack e2e: restoring the ordinary stack ──" >&2
      reload_agentd_with "" "" "" || echo "agentd is still down; try: $0 up mock" >&2
      return 1
    fi
    # Restore however the run ends. A stack left on a three-port pool fails the
    # fourth session of every later run; one left on a scripted model changes
    # what the model says for whoever runs next; one left on a one-minute idle
    # timeout archives their containers out from under them.
    trap 'echo "── stack e2e: restoring the ordinary stack ──"; reload_agentd_with "" "" "" || true' RETURN
  fi

  echo "── stack e2e [$mode]: running playwright against $WEB_URL ──"
  # The status is captured rather than allowed to propagate, and that is
  # load-bearing rather than style. Under `set -e` a failing command inside a
  # function EXITS the script instead of returning from it, and a RETURN trap
  # does not fire on exit — so every restore above would be skipped on exactly
  # the runs that need it most: the failing ones. That is not hypothetical. It
  # left agentd on a three-port pool after the first red run of the port-pool
  # spec, and `--mock-script` had the same hole for as long as it has existed:
  # a failing scripted run left the scripted model loaded for whoever ran next.
  local status=0
  (cd "$ROOT/e2e" && STACK_BASE_URL="$WEB_URL" STACK_E2E_MODE="$mode" \
    STACK_MOCK_SCRIPT="${script_json:+1}" \
    STACK_PORT_POOL="$pool" STACK_PORT_RANGE="$pool_range" \
    E2E_PORT_POOL_SIZE="$pool" \
    STACK_IDLE_TIMEOUT="$idle" \
    STACK_SCHEDULE_MAX_PROVISION_FAILURES="$SCHEDULE_MAX_PROVISION_FAILURES" \
    npx playwright test --config playwright.stack.config.ts "${args[@]}") || status=$?
  return "$status"
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
