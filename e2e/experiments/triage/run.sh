#!/usr/bin/env bash
# The triage rig (work plan 13, SC1) — executes
# docs/product/19-scenario-library.md §3 SC-1.
#
#   ./e2e/experiments/triage/run.sh build           typecheck + compile only
#   ./e2e/experiments/triage/run.sh test            the offline unit layer (no stack)
#   ./e2e/experiments/triage/run.sh gen <config>    regenerate the tickets only
#   ./e2e/experiments/triage/run.sh list            the triage configs available
#   ./e2e/experiments/triage/run.sh run <config>    the real thing
#
# The mock smoke, which costs nothing and proves the machinery:
#
#   ./e2e/run-stack-e2e.sh up mock
#   ./e2e/experiments/triage/run.sh run triage-smoke-6
#
# The live run, which costs tokens and is ATTENDED (work plan 13's L3 posture):
#
#   ./e2e/run-stack-e2e.sh up subscription
#   TRIAGE_LIVE_RUN=1 ./e2e/experiments/triage/run.sh run triage-24
#
# Compilation is delegated to the C1 rig's run.sh, which owns
# experiments/tsconfig.json and already sweeps this directory with it. One
# tsconfig, one dist/ — the C1↔B1 collision in docs/product/13's log is what
# that rule is for, and this directory adds no tsconfig of its own.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENTS="$(cd "$HERE/.." && pwd)"
E2E="$(cd "$EXPERIMENTS/.." && pwd)"
ROOT="$(cd "$E2E/.." && pwd)"
WEB_URL="${STACK_BASE_URL:-http://localhost:8080}"
MODE_FILE="$E2E/.stack-e2e-mode"
DIST="$EXPERIMENTS/dist/experiments/triage"
COMPOSE=(docker compose -f "$ROOT/docker-compose.yml" -f "$ROOT/docker-compose.stack-e2e.yml"
  --project-name "${STACK_COMPOSE_PROJECT:-agent-orange-stack-e2e}")

build() { "$EXPERIMENTS/run.sh" build; }

# config_field CONFIG FIELD — one field of the config module, from the module.
config_field() {
  node "$DIST/triage.js" "$1" --print-config | node -e \
    'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(String(JSON.parse(s)[process.argv[1]]??"")))' "$2"
}

# generate CONFIG — triagelabgen renders the manifest into the dataset
# directory. Always regenerated: the tickets are gitignored, deterministic, and
# a stale directory is refused by the loader anyway (truths.json carries
# checksums).
generate() {
  local config="$1" manifest datasets
  manifest="$(config_field "$config" manifest)"
  datasets="$(config_field "$config" datasetDir)"
  [ -n "$manifest" ] && [ -n "$datasets" ] || { echo "$config declares no manifest/datasetDir" >&2; return 1; }
  command -v go >/dev/null 2>&1 || {
    echo "go is not on PATH — the ticket generator is a Go command (go/cmd/triagelabgen)" >&2; return 1; }
  echo "── triage: generating tickets from $manifest ──"
  ( cd "$ROOT/go" && go run ./cmd/triagelabgen -manifest "$ROOT/$manifest" -out "$ROOT/$datasets" )
}

wait_ready() {
  local deadline=$(($(date +%s) + 120))
  until curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then return 1; fi
    sleep 2
  done
}

# reload_agentd_with_script SCRIPT_JSON — recreate agentd carrying (or no longer
# carrying) a mock model script, then wait for the stack to answer. Copied from
# the calibration rig for the same reason it exists there: --mock-script is
# bolted to the playwright `test` command, and this is not a playwright spec.
reload_agentd_with_script() {
  AGENTKIT_MOCK_MODEL_SCRIPT="$1" "${COMPOSE[@]}" up -d --no-deps agentd >/dev/null 2>&1
  wait_ready
}

cmd_run() {
  local config="${1:-}"
  [ -n "$config" ] || { echo "usage: $0 run <config> [--arms a,b] [--limit N] [--out DIR]" >&2; return 1; }
  shift

  build

  local mode script recorded=""
  mode="$(config_field "$config" mode)"
  script="$(config_field "$config" mockScript)"
  [ -f "$MODE_FILE" ] && recorded="$(cat "$MODE_FILE")"

  curl -fsS "$WEB_URL/auth/config" >/dev/null 2>&1 ||
    { echo "no stack listening at $WEB_URL — run: ./e2e/run-stack-e2e.sh up mock" >&2; return 1; }

  # The credential-mode gate. A mock config against a live stack burns tokens on
  # numbers that were going to be authored anyway; a live config against a mock
  # stack produces a triage run of the mock. Both are refusals, not warnings.
  if [ "$mode" = "mock" ]; then
    if [ -n "$recorded" ] && [ "$recorded" != "mock" ]; then
      echo "$config is a MOCK config and the running stack is in '$recorded' mode." >&2
      echo "    ./e2e/run-stack-e2e.sh up mock" >&2
      return 1
    fi
  else
    if [ -z "$recorded" ] || [ "$recorded" = "mock" ]; then
      echo "$config is a LIVE config and the running stack is in '${recorded:-unknown}' mode." >&2
      echo "    ./e2e/run-stack-e2e.sh up subscription   # or: up api-key" >&2
      return 1
    fi
    if [ "${TRIAGE_LIVE_RUN:-}" != "1" ]; then
      echo "$config spends real tokens against a real model, and work plan 13's L3 posture says" >&2
      echo "attended runs only. Set TRIAGE_LIVE_RUN=1 to say you are watching:" >&2
      echo "    TRIAGE_LIVE_RUN=1 $0 run $config" >&2
      return 1
    fi
  fi

  generate "$config"

  if [ -n "$script" ]; then
    [ -f "$ROOT/$script" ] || { echo "no such mock script: $script" >&2; return 1; }
    echo "── triage: loading mock model script $script into agentd ──"
    if ! reload_agentd_with_script "$(cat "$ROOT/$script")"; then
      echo "agentd did not come back with that script. Its own words:" >&2
      "${COMPOSE[@]}" logs --tail 5 agentd 2>&1 | sed 's/^/    /' >&2
      reload_agentd_with_script "" || echo "agentd is still down; try: ./e2e/run-stack-e2e.sh up mock" >&2
      return 1
    fi
    # On EXIT, including failures: the script is agentd-wide boot configuration,
    # and leaving one loaded quietly changes the model for everyone.
    trap 'echo "── triage: unloading mock model script ──"; reload_agentd_with_script "" || true' EXIT
  fi

  local status=0
  node "$DIST/triage.js" "$config" "$@" || status=$?
  return "$status"
}

cmd_test() {
  build
  echo "── triage: offline unit layer ──"
  node --test "$DIST"/*.test.js
}

cmd_gen() {
  build
  generate "${1:?usage: $0 gen <config>}"
}

cmd_list() {
  build
  echo "triage configs:"
  for f in "$HERE"/configs/*.ts; do
    [ -e "$f" ] || continue
    local name
    name="$(basename "$f" .ts)"
    echo "  $name ($(config_field "$name" mode))"
  done
}

cmd_build() { build; }

CMD="${1:-}"
case "$CMD" in
  build | test | list | gen | run) shift; "cmd_$CMD" "$@" ;;
  *) echo "usage: $0 <build|test|list|gen|run> [args]" >&2; exit 1 ;;
esac
