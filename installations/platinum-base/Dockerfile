# Platinum app image — built FROM the agentkit core image.
#
# Core (agent-library/sandbox/Dockerfile → agentkit-sandbox:<tag>) owns the
# harness: node, Claude Code CLI, the in-image control server, /workspace,
# port 3010, healthcheck, CMD. This file adds only Platinum concerns, following
# the app-image extension contract (agent-library/docs/10-extension-points.md):
#
#   Plugins     → /app/product-plugins        (typed tools w/ product contract)
#   Environment → apt/pip/npm layers + pt CLI (capabilities behind Bash)
#   Knowledge   → /workspace/.claude + CLAUDE.md (skills + project memory)
#
# Build (context = repo root; the pt stage needs goapi/cmd/pt + go.mod):
#
#   docker build -t agentkit-sandbox:dev agent-library/sandbox
#   docker build -f installations/platinum-base/Dockerfile --build-arg BASE_IMAGE=agentkit-sandbox:dev \
#     -t platinumimages.azurecr.io/platinum-sandbox-agentkit:dev .
#
# Or just: ./stack rebuild sandbox <name> [--local]
ARG BASE_IMAGE=agentkit-sandbox:dev

### pt CLI builder (Go) — host needs no Go toolchain ###
FROM golang:1.25-alpine AS pt-builder
WORKDIR /src
COPY go.mod go.sum ./
COPY agent-library/go/go.mod ./agent-library/go/go.mod
RUN go mod download
COPY agent-library/go/ ./agent-library/go/
COPY goapi/cmd/pt/ ./goapi/cmd/pt/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o /pt ./goapi/cmd/pt

### Platinum app image ###
FROM ${BASE_IMAGE}
WORKDIR /app

# --- Environment: data-science / rendering stack (Debian; base is node:20-slim) ---
# bash/ripgrep/git/ca-certificates come from the base — not repeated.
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 python3-pip python3-dev python-is-python3 gcc g++ \
      jq sqlite3 curl file zip unzip gfortran libopenblas-dev \
      libreoffice-impress libreoffice-writer libreoffice-common \
      fonts-noto fonts-dejavu fonts-liberation \
      poppler-utils imagemagick chromium \
    && rm -rf /var/lib/apt/lists/*
RUN pip3 install --no-cache-dir --break-system-packages \
      pandas numpy matplotlib seaborn scipy plotly openpyxl xlsxwriter \
      scikit-learn statsmodels Pillow requests kaleido tabulate wordcloud \
      networkx nltk adjustText jsonschema python-pptx pypdf python-docx pypdfium2 \
      pdf2image PyMuPDF defusedxml fonttools freetype-py
ENV MPLBACKEND=Agg
ENV PYTHONPATH=/workspace/lib
ENV PLAYWRIGHT_BROWSERS_PATH=/usr/bin
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
RUN npm install -g d3 csv-parse json2csv pptxgenjs sharp playwright \
    && npm cache clean --force

# --- Environment: pt CLI ---
COPY --from=pt-builder /pt /usr/local/bin/pt

# --- Plugins: Platinum tool plugins → PRODUCT_PLUGINS_DIR ---
# The base's loader scans this dir at startup and registers each ToolPlugin
# (deduped by name, so the barrel index.ts + per-tool files coexist safely).
# Plugins run under tsx and resolve @anthropic-ai/claude-agent-sdk + zod from
# the base's /app/node_modules.
COPY installations/platinum-base/plugins/ /app/product-plugins/
ENV PRODUCT_PLUGINS_DIR=/app/product-plugins

# --- Knowledge + workspace helpers ---
RUN mkdir -p /workspace/data /workspace/dist
COPY installations/platinum-base/workspace-lib/ /workspace/lib/
COPY installations/platinum-base/workspace-lib/.claude/ /workspace/.claude/
