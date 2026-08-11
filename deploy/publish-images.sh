#!/usr/bin/env bash
#
# publish-images.sh — build and push the two SERVICE images (agentd, web).
#
# Distinct from publish-base.sh, and the difference matters:
#
#   publish-base.sh    the SESSION base — what a session container runs, and
#                      what a project's custom image is built FROM.
#   publish-images.sh  the SERVICES — agentd and the web UI, i.e. Agent Orange
#                      itself.
#
# docker-compose.yml BUILDS both services locally, which is why running the
# stack needs no registry. Kubernetes cannot build, so a cluster deployment
# needs them pushed somewhere the cluster can pull from.
#
#   REGISTRY=<host>/<project>/<repo> ./deploy/publish-images.sh [tag]
#
# The web image is a BUILT bundle of examples/web — a UI change is invisible
# until this runs again.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

: "${REGISTRY:?set REGISTRY, e.g. REGISTRY=europe-west1-docker.pkg.dev/webkit-servers/agent-orange}"

default_tag() {
  local sha dirty=''
  sha="$(git rev-parse --short HEAD 2>/dev/null || echo 'nogit')"
  [ -n "$(git status --porcelain 2>/dev/null || true)" ] && dirty='-dirty'
  printf '%s%s' "$sha" "$dirty"
}
TAG="${1:-$(default_tag)}"

# Same trap as publish-base.sh: being logged into gcloud is not the same as
# Docker having a credential helper for THIS registry host.
host="${REGISTRY%%/*}"
case "$host" in
  *-docker.pkg.dev|gcr.io|*.gcr.io)
    if ! grep -q "\"$host\"" "${DOCKER_CONFIG:-$HOME/.docker}/config.json" 2>/dev/null; then
      echo "!! Docker has no credential helper for $host. Run:" >&2
      echo "!!   gcloud auth configure-docker $host" >&2
      exit 1
    fi ;;
esac

echo "── building agentd ──"
docker build -t "$REGISTRY/agentd:$TAG" -f deploy/agentd.Dockerfile go

echo "── building web (bundles examples/web) ──"
docker build -t "$REGISTRY/web:$TAG" -f deploy/web.Dockerfile .

for repo in agentd web; do
  docker push "$REGISTRY/$repo:$TAG"
  docker tag "$REGISTRY/$repo:$TAG" "$REGISTRY/$repo:latest"
  docker push "$REGISTRY/$repo:latest"
done

cat <<EOF

── published ──────────────────────────────────────────────────────────────────
  $REGISTRY/agentd:$TAG   (also :latest)
  $REGISTRY/web:$TAG      (also :latest)

Point the cluster at this tag:
  cd deploy/k8s && IMAGE_TAG=$TAG ./apply.sh
EOF
