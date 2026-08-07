#!/usr/bin/env bash
# The comparison rig (work plan 13, C1). Runs ONE task through N topologies × M
# repetitions against the running mock stack and emits a ranked report.
#
#   ./e2e/experiments/run.sh build                    typecheck + compile only
#   ./e2e/experiments/run.sh test                     the offline unit layer (no stack)
#   ./e2e/experiments/run.sh list                     the comparison configs available
#   ./e2e/experiments/run.sh compare <config> [args]  the real thing
#
# `compare` needs a stack already up in MOCK mode:
#
#   ./e2e/run-stack-e2e.sh up mock
#   ./e2e/experiments/run.sh compare actor-critic-vs-sham-vs-solo
#
# Extra args are passed to compare.ts: --reps N, --out DIR, --base-url URL.
#
# Why this loads the mock script itself instead of going through
# run-stack-e2e.sh: that script's --mock-script flag is bolted to `test`, which
# runs playwright, and the rig is deliberately NOT a playwright spec. The
# reload+restore discipline below is copied from it verbatim in spirit — the
# script is agentd-wide boot configuration, so leaving one loaded would quietly
# change the model for every later run and for anyone else sharing the stack.
# The restore therefore runs on EXIT, including the failing runs, which is
# exactly the hole that cost run-stack-e2e.sh a debugging session.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E="$(cd "$HERE/.." && pwd)"
ROOT="$(cd "$E2E/.." && pwd)"
WEB_URL="${STACK_BASE_URL:-http://localhost:8080}"
MODE_FILE="$E2E/.stack-e2e-mode"
COMPOSE=(docker compose -f "$ROOT/docker-compose.yml" -f "$ROOT/docker-compose.stack-e2e.yml"
  --project-name "${STACK_COMPOSE_PROJECT:-agent-orange-stack-e2e}")

ensure_deps() {
  if [ ! -d "$E2E/node_modules" ]; then
    echo "── experiments: installing e2e deps ──"
    (cd "$E2E" && (yarn install --frozen-lockfile 2>/dev/null || npm install))
  fi
}

build() {
  ensure_deps
  echo "── experiments: typecheck + compile ──"
  "$E2E/node_modules/.bin/tsc" -p "$HERE/tsconfig.json"
  # dist/ is CommonJS while e2e/ as a whole is ESM — see tsconfig.json's note.
  printf '{"type":"commonjs"}\n' >"$HERE/dist/package.json"
}

wait_ready() {
  local deadline=$(($(date +%s) + 120))
  until curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then return 1; fi
    sleep 2
  done
}

# reload_agentd_with_script SCRIPT_JSON — recreate agentd carrying (or no longer
# carrying) a mock model script, then wait for the stack to answer.
reload_agentd_with_script() {
  AGENTKIT_MOCK_MODEL_SCRIPT="$1" "${COMPOSE[@]}" up -d --no-deps agentd >/dev/null 2>&1
  wait_ready
}

cmd_compare() {
  local config="${1:-}"
  [ -n "$config" ] || { echo "usage: $0 compare <config> [--reps N] [--out DIR]" >&2; return 1; }
  shift

  build

  local recorded=""
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"
  if [ -n "$recorded" ] && [ "$recorded" != "mock" ]; then
    echo "the running stack is in '$recorded' mode. The rig is Tier A: mock only." >&2
    echo "    ./e2e/run-stack-e2e.sh up mock" >&2
    return 1
  fi
  curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1 ||
    { echo "no stack listening at $WEB_URL — run: ./e2e/run-stack-e2e.sh up mock" >&2; return 1; }

  # The config module is the single source of truth for which script it needs.
  local script
  script="$(node "$HERE/dist/experiments/compare.js" "$config" --print-mock-script)"
  [ -f "$ROOT/$script" ] || { echo "no such mock script: $script" >&2; return 1; }

  echo "── experiments: loading mock model script $script into agentd ──"
  if ! reload_agentd_with_script "$(cat "$ROOT/$script")"; then
    echo "agentd did not come back with that script. Its own words:" >&2
    "${COMPOSE[@]}" logs --tail 5 agentd 2>&1 | sed 's/^/    /' >&2
    reload_agentd_with_script "" || echo "agentd is still down; try: ./e2e/run-stack-e2e.sh up mock" >&2
    return 1
  fi
  trap 'echo "── experiments: unloading mock model script ──"; reload_agentd_with_script "" || true' EXIT

  local status=0
  node "$HERE/dist/experiments/compare.js" "$config" "$@" || status=$?
  return "$status"
}

cmd_test() {
  build
  echo "── experiments: offline unit layer ──"
  node --test "$HERE"/dist/experiments/*.test.js
}

cmd_list() {
  build
  echo "comparison configs:"
  for f in "$HERE"/configs/*.ts; do
    [ -e "$f" ] || continue
    echo "  $(basename "$f" .ts)"
  done
}

cmd_build() { build; }

CMD="${1:-}"
case "$CMD" in
  build | test | list | compare) shift; "cmd_$CMD" "$@" ;;
  *) echo "usage: $0 <build|test|list|compare> [args]" >&2; exit 1 ;;
esac
