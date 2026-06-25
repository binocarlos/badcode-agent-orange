#!/usr/bin/env bash
# Build + push the Platinum agentkit sandbox image to the platinumimages ACR.
#
# The v2 (agentkit) agent stack pulls this image from the registry per session.
# Requires `az` CLI logged in (az login) and Docker running.
#
# Usage: scripts/build-sandbox-agentkit.sh [tag]   (default tag: dev)
set -euo pipefail

TAG="${1:-dev}"
REGISTRY="platinumimages.azurecr.io"
IMAGE="${REGISTRY}/platinum-sandbox-agentkit:${TAG}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> Logging in to ${REGISTRY}"
az acr login --name platinumimages

echo "==> Building ${IMAGE}"
docker build -f "${ROOT}/dind/SandboxAgentkit.Dockerfile" -t "${IMAGE}" "${ROOT}"

echo "==> Pushing ${IMAGE}"
docker push "${IMAGE}"

echo "==> Done. Image: ${IMAGE}"
echo "    Set AGENT_V2_BASE_IMAGE=${IMAGE} and AGENT_IMAGE_REGISTRY=registry to use it."
