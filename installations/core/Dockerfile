# core — the minimal, product-neutral root installation (EXAMPLE).
#
# The sandbox harness image (built from ../sandbox; tagged `agentkit-sandbox:dev`
# by the standalone stack) already provides node:20-slim + bash, git, ripgrep,
# ca-certificates, the in-image control server, /workspace, port 3010, healthcheck
# and CMD. "core" adds only a few universally-useful, project-neutral CLI tools so
# every downstream image can rely on them. It contains NOTHING project-specific.
#
# Do NOT set CMD / ENTRYPOINT / EXPOSE / HEALTHCHECK — owned by the base image.
#
# Build (from the repo root):
#   docker build -t agentkit-sandbox:dev sandbox
#   docker build -f installations/core/Dockerfile \
#     --build-arg BASE_IMAGE=agentkit-sandbox:dev \
#     -t agent-orange-core:dev installations/core
ARG BASE_IMAGE=agentkit-sandbox:dev
FROM ${BASE_IMAGE}

RUN apt-get update && apt-get install -y --no-install-recommends \
      coreutils findutils grep sed gawk \
      curl jq file less unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
