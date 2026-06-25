# example — EXAMPLE installation extending "core".
#
# Demonstrates the layered build and is the starting template for a per-project
# base image. To create a real one, copy this folder into your PROJECT's repo
# (per-project installations live with the project, not here), set "parent" in
# installation.json, and replace the layers below with what the project needs.
#
# Do NOT set CMD / ENTRYPOINT / EXPOSE / HEALTHCHECK — owned by the base image.
#
# Build (after core exists):
#   docker build -f installations/example/Dockerfile \
#     --build-arg BASE_IMAGE=agent-orange-core:dev \
#     -t agent-orange-example:dev installations/example
ARG BASE_IMAGE=agent-orange-core:dev
FROM ${BASE_IMAGE}

# --- Example project runtime layer (replace per project) -------------------
# RUN apt-get update && apt-get install -y --no-install-recommends \
#       python3 python3-pip nodejs \
#     && rm -rf /var/lib/apt/lists/*

# --- Seed the workspace from this installation's overlay/ -------------------
# Files under overlay/ are copied into /workspace (project memory, skills,
# templates). Here it ships only a CLAUDE.md as an example.
COPY overlay/ /workspace/

WORKDIR /workspace
