#!/usr/bin/env bash
#
# apply.sh — deploy Agent Orange to the current kubectl context.
#
#   REGISTRY=… IMAGE_TAG=… ./apply.sh          apply
#   REGISTRY=… IMAGE_TAG=… ./apply.sh --dry-run  run full admission, persist NOTHING
#
# --dry-run is a SERVER-side dry run: the API server runs every admission
# controller and writes no object. It is the honest way to find out whether a
# cluster will accept the privileged daemon before committing to anything.
#
# Secrets are NOT created here — see README.md. A secret in a repo is a secret
# in a repo, whatever the .gitignore says.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"

: "${REGISTRY:?set REGISTRY, e.g. REGISTRY=europe-west1-docker.pkg.dev/webkit-servers/agent-orange}"
: "${IMAGE_TAG:?set IMAGE_TAG — the tag deploy/publish-images.sh printed}"

DRY=()
[ "${1:-}" = "--dry-run" ] && DRY=(--dry-run=server)

echo "── context: $(kubectl config current-context) ──"
[ ${#DRY[@]} -gt 0 ] && echo "── SERVER DRY RUN — nothing will be created ──"

for f in 00-namespace-and-config.yaml 10-postgres.yaml 20-agent-orange.yaml 30-web.yaml 40-ingress.yaml; do
  echo "── $f"
  sed -e "s#IMAGE_AGENTD#$REGISTRY/agentd:$IMAGE_TAG#" \
      -e "s#IMAGE_WEB#$REGISTRY/web:$IMAGE_TAG#" "$f" \
    | kubectl apply "${DRY[@]}" -f -
done

if [ ${#DRY[@]} -eq 0 ]; then
  cat <<'EOF'

── applied ────────────────────────────────────────────────────────────────────
Watch it come up:
  kubectl -n agent-orange get pods -w

The first boot pulls the session base into the Docker daemon on the first
session, not at startup — so the first conversation is slow and later ones are
not. That is the pull, not a hang.

If the agentd pod will not start, the log line that matters is the model one:
  kubectl -n agent-orange logs deploy/agent-orange -c agentd | grep -i "model proxy"
EOF
fi
