# core-v2 — no-build (static HTML + CDN import map) webapp flavor.
# Thin variant FROM platinum-base, which has no Vite, so nothing to remove.
# Do NOT set CMD/ENTRYPOINT/EXPOSE/HEALTHCHECK (owned by the agentkit core image).
ARG BASE_IMAGE=registry:5000/platinum-sandbox-platinum-base:dev
FROM ${BASE_IMAGE}
COPY installations/core-v2/overlay/ /workspace/
# Materialize the shared component library (owned by platinum-base at
# /workspace/lib/webviz) into the no-build webapp tree, alongside its served
# root so the browser can fetch ./lib/components/* relative to webapp/index.html.
# The overlay ships only the flavor-specific data-loader.js (runtime fetch).
RUN cp -r /workspace/lib/webviz/. /workspace/webapp/lib/
