# core-v1 — Vite webapp flavor. Thin variant FROM platinum-base.
# Adds the Vite toolchain + Vite-based webapp skills/CLAUDE.md/app template.
# Do NOT set CMD/ENTRYPOINT/EXPOSE/HEALTHCHECK (owned by the agentkit core image).
ARG BASE_IMAGE=registry:5000/platinum-sandbox-platinum-base:dev
FROM ${BASE_IMAGE}
COPY installations/core-v1/overlay/ /workspace/
# Materialize the shared component library (owned by platinum-base at
# /workspace/lib/webviz) into the Vite source tree. The overlay ships only the
# flavor-specific data-loader.js (build-time import.meta.glob); the components +
# platinum-data.js come from the single source of truth in the base image.
RUN cp -r /workspace/lib/webviz/. /workspace/src/lib/
RUN npm install -g vite && npm cache clean --force
