# core — the minimal, product-neutral root installation (EXAMPLE).
#
# The sandbox harness image (built from ../sandbox; tagged `agentkit-sandbox:dev`
# by the standalone stack) already provides node:20-slim + bash, git, ripgrep,
# ca-certificates, the in-image control server, /workspace, port 3010, healthcheck
# and CMD. "core" adds only a few universally-useful, project-neutral CLI tools so
# every downstream image can rely on them. It contains NOTHING project-specific.
#
# Do NOT set CMD / ENTRYPOINT / EXPOSE / HEALTHCHECK / WORKDIR — owned by the
# base image. WORKDIR is on that list as of 2026-08-13: this file used to end
# with `WORKDIR /workspace`, and every session launched from it failed to start
# because the base's CMD was relative to /app. The agent's working directory is
# /workspace regardless — the harness sets it — so there is nothing to gain here.
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
