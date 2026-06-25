# agentkit — AG-8 milestone: live two-session run on DinD

This directory documents the exact steps to run the **usable-standalone milestone**:
two concurrent agent sessions provisioned against a real Docker-in-Docker (DinD)
daemon, each completing a turn through the claude-agent-sdk harness, with
session-isolated SSE streams — using the mock model proxy so no real API key is required.

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.22+ | `go run ./examples/standalone` |
| Docker | 24+ | build the sandbox image and run DinD |
| Node.js | 20+ | included in the sandbox image; not required on the host |

All commands below are run from the **repository root** (`Platinum/`).

---

## Step 1 — Build the sandbox image

The sandbox image is the in-image agent: a Fastify server that drives the
claude-agent-sdk inside the container and exposes the sandbox HTTP contract
(see `agent-library/docs/07-in-image-agent.md`).

```bash
docker build -t agentkit-sandbox:dev agent-library/sandbox
```

This copies `agent-library/sandbox/src/` into the image and installs npm
dependencies.  The image runs with `tsx src/index.ts` — no separate compile step.

---

## Step 2 — Start a DinD daemon

Docker-in-Docker (DinD) is the execution environment: the Runner provisions
one container **per session** against this daemon.

### Option A — dedicated DinD container (recommended for isolation)

```bash
docker run -d --name agentkit-dind \
  --privileged \
  -p 2375:2375 \
  docker:dind \
  dockerd --host=tcp://0.0.0.0:2375 --tls=false
```

Wait ~3 seconds for the daemon to start, then verify:

```bash
docker -H tcp://localhost:2375 info
```

### Option B — use the host Docker socket directly

If you are already running Docker on the host and don't need isolation:

```bash
export DOCKER_HOST=unix:///var/run/docker.sock
```

Skip loading the image in Step 3 (it is already in the host daemon).

---

## Step 3 — Load the sandbox image into the DinD daemon

The DinD daemon starts with an empty image store.  Load the image you built in
Step 1:

```bash
docker save agentkit-sandbox:dev | docker -H tcp://localhost:2375 load
```

Verify the image is present in the DinD daemon:

```bash
docker -H tcp://localhost:2375 images agentkit-sandbox:dev
```

---

## Step 4 — Start the mock model proxy

The mock proxy emulates just enough of the Anthropic Messages streaming API for
the claude-agent-sdk to complete a turn — no real API key needed.

```bash
# In a separate terminal:
cd agent-library/go
go run ./examples/mockproxy
# Listens on :4000 by default.  Use MOCK_ADDR=:5001 to change the port.
```

Expected output:
```
[mockproxy] listening on :4000 — set ANTHROPIC_BASE_URL=http://localhost:4000
[mockproxy] sessions confirmed: watch x-session-id in logs above to verify isolation
```

### Using a real Anthropic API key (alternative)

Skip the mock proxy entirely and export:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
unset ANTHROPIC_BASE_URL
```

The SDK will connect to `https://api.anthropic.com` directly.

---

## Step 5 — Run the reference host

The reference host (`agent-library/go/examples/standalone/main.go`) creates two
concurrent sessions, sends a message to each, and streams the SSE output.

```bash
cd agent-library/go

DOCKER_HOST=tcp://localhost:2375 \
BASE_IMAGE=agentkit-sandbox:dev \
ANTHROPIC_BASE_URL=http://172.17.0.1:4000 \
go run ./examples/standalone
```

`172.17.0.1` is the default Docker bridge gateway — the address INSIDE a
container that reaches a service listening on the host.  Adjust if your bridge
network uses a different subnet.

### Flags (override env vars)

```
-docker-host string   Docker daemon address (default: $DOCKER_HOST or tcp://localhost:2375)
-base-image string    Sandbox image tag    (default: $BASE_IMAGE or agentkit-sandbox:dev)
-agent-port int       In-image agent port  (default: 3010)
```

---

## Expected output

```
[standalone] docker-host=tcp://localhost:2375  base-image=agentkit-sandbox:dev  agent-port=3010
[standalone] ANTHROPIC_BASE_URL=http://172.17.0.1:4000
[standalone] session s1 created  addr=http://localhost:30001  state=running
[standalone] session s2 created  addr=http://localhost:30002  state=running
[standalone] session s1 COMPLETE  SSE bytes=412
[standalone] session s2 COMPLETE  SSE bytes=412

--- SSE from s1 ---
event: content_delta
data: {"delta":"Hello from the mock proxy"}
...

--- SSE from s2 ---
event: content_delta
data: {"delta":"Hello from the mock proxy"}
...

[standalone] session isolation OK: s1 and s2 streams differ as expected
BOTH sessions completed — check mockproxy logs for x-session-id headers to confirm no cross-session contamination
```

In the mock proxy terminal you should see:

```
[mockproxy] POST /v1/messages  x-session-id="s1"  remote=...
[mockproxy] turn complete  x-session-id="s1"
[mockproxy] POST /v1/messages  x-session-id="s2"  remote=...
[mockproxy] turn complete  x-session-id="s2"
```

The `x-session-id` headers confirm per-session isolation: the sandbox's
`AsyncLocalStorage` fetch patch correctly tagged each outbound model call with
the session that triggered it.

---

## Hermetic test (no Docker required)

The autonomous proof of the wiring runs as a standard Go test with no daemon:

```bash
cd agent-library/go
go test ./examples/standalone/... -v
```

This substitutes:
- `execenv.NewMock()` for the DinD environment (no containers)
- An `httptest.Server` for the sandbox control server (scripted SSE turns)
- `agentkittest` in-memory helpers for the store, claims, and artifacts

All three test cases must be green:

```
--- PASS: TestHermeticTwoSessionIsolation
--- PASS: TestHermeticRunnerWiring
--- PASS: TestHermeticHarnessSelection
```

---

## Cleanup

```bash
# Stop the DinD container (destroys all provisioned session containers too):
docker stop agentkit-dind && docker rm agentkit-dind

# Remove the sandbox image:
docker rmi agentkit-sandbox:dev
```

---

## Architecture summary

```
host process (go run ./examples/standalone)
  └── Runner (agentkit.NewRunner)
        └── Fleet (fleet.NewMemory)
              └── Worker dind-1
                    └── DinD (execenv/docker.NewDinD → tcp://localhost:2375)
                          ├── Container s1 (agentkit-sandbox:dev)  :30001
                          │     └── Fastify + ClaudeAgentSdkHarness
                          │           └── fetch → ANTHROPIC_BASE_URL → mockproxy :4000
                          └── Container s2 (agentkit-sandbox:dev)  :30002
                                └── Fastify + ClaudeAgentSdkHarness
                                      └── fetch → ANTHROPIC_BASE_URL → mockproxy :4000
```

The mock proxy logs `x-session-id` for every inbound request, confirming that
each container's `AsyncLocalStorage` fetch patch correctly stamps the header
with the session ID that triggered the turn.
