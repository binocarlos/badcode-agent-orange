#!/usr/bin/env bash
#
# publish-base.sh — build the Agent Orange session base images and push them to
# YOUR container registry.
#
# Downstream projects build their own image FROM one of these. Until you have run this once, there is nothing for them to point at:
# the standalone stack only ever builds `agentkit-sandbox:dev` INSIDE its
# Docker-in-Docker daemon, where no other project can reach it.
#
#   REGISTRY=<host>/<project>/<repo> ./deploy/publish-base.sh [tag]
#
# Two images go up, mirroring the layering in installations/README.md
# (sandbox → core → your project):
#
#   session-base   the harness alone — node, the in-image control server,
#                  /workspace, the healthcheck and CMD. Everything must
#                  ultimately derive from this.
#   session-core   session-base plus product-neutral CLI tools (curl, jq, git,
#                  ripgrep, the coreutils family). This is the one you want
#                  unless you have a reason to start barer.
#
# Both get the tag you asked for AND `latest`. Prefer the specific tag in a
# project's Dockerfile: `latest` is a moving target, which is the whole reason
# Agent Orange records the digest a session actually launched from.
#
# PUSH_LATEST=false leaves `latest` alone. Use it for development publishes —
# a shared registry means moving `latest` moves it for everyone, which is
# exactly what a `:dev` tag is supposed to avoid. `./stack publish-base` sets it.
#
# This is deliberately a manual command, not CI. Publishing a base image is
# infrequent, and the credential it needs is your own docker login.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

: "${REGISTRY:?set REGISTRY, e.g. REGISTRY=europe-west1-docker.pkg.dev/webkit-servers/agent-orange}"

# The default tag is the commit you built from, so an image in the registry can
# always be traced back to a tree. `-dirty` when it would be a lie: the bytes in
# the image are not the bytes at that commit.
default_tag() {
  local sha dirty=''
  sha="$(git rev-parse --short HEAD 2>/dev/null || echo 'nogit')"
  [ -n "$(git status --porcelain 2>/dev/null || true)" ] && dirty='-dirty'
  printf '%s%s' "$sha" "$dirty"
}
TAG="${1:-$(default_tag)}"

BASE_REPO="$REGISTRY/session-base"
CORE_REPO="$REGISTRY/session-core"

# ── Preflight: the credential helper ─────────────────────────────────────────
# Being logged into gcloud is NOT enough. Docker resolves credentials per
# registry HOST, and an Artifact Registry host is a separate entry from the old
# gcr.io ones — a machine with `gcr.io` configured and not
# `europe-west1-docker.pkg.dev` fails the push with "no basic auth credentials",
# which reads like a network problem and is not. Checked here rather than
# discovered after a multi-minute build.
preflight_auth() {
  local host="${REGISTRY%%/*}"
  case "$host" in
    *-docker.pkg.dev|gcr.io|*.gcr.io) ;;
    *) return 0 ;;   # not a Google registry; whatever you use, you know how
  esac
  if ! grep -q "\"$host\"" "${DOCKER_CONFIG:-$HOME/.docker}/config.json" 2>/dev/null; then
    echo "!! Docker has no credential helper for $host." >&2
    echo "!! Run this once, then try again:" >&2
    echo "!!   gcloud auth configure-docker $host" >&2
    exit 1
  fi
}
preflight_auth

echo "── building session-base (sandbox harness) ──"
docker build -t "$BASE_REPO:$TAG" sandbox

echo "── building session-core (harness + neutral CLI tools) ──"
docker build -f installations/core/Dockerfile \
  --build-arg BASE_IMAGE="$BASE_REPO:$TAG" \
  -t "$CORE_REPO:$TAG" installations/core

echo "── pushing ──"
for repo in "$BASE_REPO" "$CORE_REPO"; do
  docker push "$repo:$TAG"
  if [ "${PUSH_LATEST:-true}" = "true" ]; then
    docker tag "$repo:$TAG" "$repo:latest"
    docker push "$repo:latest"
  else
    echo "   (PUSH_LATEST=false — :latest left pointing where it was)"
  fi
done

# The digest is the point: it is what a project should pin when it wants "these
# exact bytes", and what Agent Orange records on every session launched from it.
core_digest="$(docker inspect --format '{{index .RepoDigests 0}}' "$CORE_REPO:$TAG" 2>/dev/null || true)"

cat <<EOF

── published ──────────────────────────────────────────────────────────────────
  $BASE_REPO:$TAG$([ "${PUSH_LATEST:-true}" = "true" ] && echo "   (also :latest)")
  $CORE_REPO:$TAG$([ "${PUSH_LATEST:-true}" = "true" ] && echo "   (also :latest)")
${core_digest:+
  session-core digest: $core_digest}

Use it in a project's Dockerfile:

  ARG BASE_IMAGE=$CORE_REPO:$TAG
  FROM \${BASE_IMAGE}
  RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \\
      && rm -rf /var/lib/apt/lists/*

Do NOT set CMD / ENTRYPOINT / EXPOSE / HEALTHCHECK / WORKDIR — the base owns those.

To run the stack against what you just pushed (production's path, locally):
  ./stack start        # or: ./stack start mock   (free)
See README-stack.md → "Registry mode", and installations/README.md for layering.
EOF
