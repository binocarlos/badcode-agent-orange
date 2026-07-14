# Dockerfile — builds the @agentkit/sandbox in-image agent.
#
# This image contains the control server that the agentkit Runner talks to over
# HTTP/SSE.  The Runner provisions a container from this image and reaches the
# agent at http://localhost:<hostPort> via a leased port mapping.
#
# The agent implements the sandbox HTTP contract (docs/07-in-image-agent.md):
#   GET  /health
#   POST /sessions
#   POST /sessions/:id/query-stream
#   GET  /sessions/:id/stream/:queryId
#   POST /sessions/:id/cancel
#   POST /sessions/:id/load-conversation
#   ...
#
# # Build
#
#   docker build -t agentkit-sandbox:dev agent-library/sandbox
#
# # Run (standalone, without the Go Runner)
#
#   docker run --rm -p 3010:3010 \
#     -e ANTHROPIC_BASE_URL=http://host-gateway:4000 \
#     agentkit-sandbox:dev
#
# # Environment variables
#
#   PORT                  Control server HTTP port (default: 3010)
#   HOST                  Bind address (default: 0.0.0.0)
#   ANTHROPIC_BASE_URL    Model proxy URL; the SDK sends calls here.
#                         Set to the mockproxy address for keyless testing.
#                         Leave unset to use the Anthropic API directly
#                         (requires CLAUDE_CODE_OAUTH_TOKEN or
#                         ANTHROPIC_API_KEY in env or image).
#   CLAUDE_CODE_OAUTH_TOKEN  Claude Code subscription token (`claude setup-token`)
#                         for direct mode; ignored when ANTHROPIC_BASE_URL is set.
#   SESSION_ID            (optional) Legacy single-session mode session ID.
#                         Omit for multi-session mode (recommended).
#   SESSION_TOKEN         (optional) Legacy single-session scoped token.
#   HOST_API_URL          Host API base URL for tool callbacks (default: "")
#   DEFAULT_MODEL         Model ID passed to the SDK (default: claude-opus-4-5)
#   LOG_LEVEL             Pino log level: trace|debug|info|warn|error (default: info)
#   NODE_ENV              development|production (default: production)

# ──────────────────────────────────────────────────────────────────────────────
# Stage 1: dependency installation
# Install only production dependencies to keep the final image lean.
# ──────────────────────────────────────────────────────────────────────────────
FROM node:20-slim AS deps

WORKDIR /app

# Copy manifest files first for better layer caching: the expensive
# yarn install layer only rebuilds when package.json or yarn.lock changes.
COPY package.json yarn.lock ./

# Install ALL dependencies (including devDeps that include tsx) so we can run
# TypeScript directly without a separate build step.
RUN yarn install --frozen-lockfile

# ──────────────────────────────────────────────────────────────────────────────
# Stage 2: final image
# Copy source on top of the installed node_modules.
# ──────────────────────────────────────────────────────────────────────────────
FROM node:20-slim

WORKDIR /app

# The claude_code preset's built-in tools shell out to real binaries:
#   Bash -> bash, Grep/Glob -> ripgrep (rg), and git history tools -> git.
# node:20-slim ships none of these, so a "full agentic turn" silently degrades
# (Grep returns nothing, Bash fails) without them. Install and clean apt lists.
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ripgrep git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Bake the Claude Code CLI into the base image. The claude-agent-sdk harness
# spawns the `claude` binary once per turn, so it MUST be on PATH — without it
# every turn fails with "Claude Code process exited with code 1". This is the
# "all harness binaries baked into the base image" model (no engine×harness
# matrix): a session picks its harness at runtime and the binary is already here.
RUN npm install -g @anthropic-ai/claude-code@2.1.162 \
    && npm cache clean --force

# IS_SANDBOX=1 lets Claude Code run with --dangerously-skip-permissions as root
# (it refuses that flag for root otherwise). This image is a single-purpose
# sandbox — the container itself is the isolation boundary — so the escape hatch
# is correct here.
ENV IS_SANDBOX=1

# Copy the installed node_modules from the deps stage.
COPY --from=deps /app/node_modules ./node_modules

# Copy the TypeScript source.  We run via tsx (the TypeScript executor bundled
# in devDependencies) so no separate compilation step is needed.  tsx is a
# zero-config TypeScript runner that uses esbuild under the hood and handles
# ESM imports natively — matching the `dev` script in package.json.
COPY package.json yarn.lock tsconfig.json ./
COPY src/ ./src/

# The agent's working directory. The claude-agent-sdk harness runs each turn with
# cwd=/workspace; this directory MUST exist or the SDK's subprocess spawn fails
# with a misleading "spawn node ENOENT" (Node reports a missing cwd as ENOENT on
# the command). The host populates/mounts it per session.
RUN mkdir -p /workspace

# In-image agent port.  The Runner maps a host port to this port when
# provisioning the container so it can call POST /sessions/:id/query-stream.
EXPOSE 3010

# Health check: the Runner's waitForHealthy polls GET /health every second.
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=10 \
    CMD node -e "require('http').get('http://localhost:3010/health', r => process.exit(r.statusCode === 200 ? 0 : 1)).on('error', () => process.exit(1))"

# Run with tsx so the TypeScript source executes without a build step.
# In production you can replace tsx with node + a compiled dist/ (see docs).
CMD ["./node_modules/.bin/tsx", "src/index.ts"]
