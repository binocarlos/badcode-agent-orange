# 11 — Deployment recipes

The same orchestration core runs in three very different shapes by composing one
`ExecutionEnvironment` with one `ImageRegistry`. This page is the practical "which adapters, which
config" guide.

## Recipe A — Dev / tests: single container + local build

The cheapest setup. One shared container (or none, with the mock), images built locally, no isolation.

```go
runner := agentkit.NewRunner(agentkit.Deps{
	Env:      dockerlocal.New(dockerlocal.Config{ // single shared container
		Socket:        "/var/run/docker.sock",
		SharedName:    "agentkit-dev",
		Network:       "agentkit-dev",
	}),
	Registry: localbuild.New(),                    // docker build + save/load
	Store:    devStore{}, Artifacts: artifacts.New(devBlobs{}),
	Claims:   devClaims{}, // ...
	Policy:   agentkit.Policy{BaseImage: "agentkit-sandbox:dev", SuspendTimeout: 0 /* no reaper */},
})
```

- `Capabilities.IsolatedPerSession=false` → sessions multiplex by `SESSION_ID` in the shared
  container (the in-image agent already scopes by it).
- `SupportsSuspend=false` → the idle reaper is a no-op; sessions just stay up.
- Snapshots, if used, are `docker commit` of the workspace dir → local tar (rare in dev).
- **For unit/integration tests:** swap to `execenv.NewMock()` + `imageregistry.NewMock()` — no Docker
  at all, runs in milliseconds. This is the default for the host's hermetic agent tests.

This mirrors today's non-DinD development mode and the hot-reload bind mount
(`/sandbox-src/src:/app/src`) becomes a `ProvisionSpec.Mount`.

## Recipe B — Staging/prod today: Docker-in-Docker + blob archive

The current Platinum production shape, now driven from Go instead of the TS orchestrator.

```go
runner := agentkit.NewRunner(agentkit.Deps{
	Env: dockerdind.New(dockerdind.Config{
		DockerHost: "tcp://localhost:2375",
		PortRange:  [2]int{30001, 30100},   // PortAllocator pool
		GatewayCallbackEnv: "ANTHROPIC_BASE_URL",
		Labels:     map[string]string{"agentkit.managed": "true"}, // for Recover()
	}),
	Registry: blobarchive.New(hostBlobs, blobarchive.Config{
		Account:    "platinumsessions",
		Container:  "session-archives",
		PreferDiff: true,    // docker diff + getArchive fast path
	}),
	Store:     platinumStore{db},
	Artifacts: artifacts.NewBlobStore(hostBlobs, ...),
	OrgContext: platinumOrgContext{...}, Claims: platinumJWT{secret},
	TokenLogger: platinumTokenLog{db}, Metrics: promMetrics{},
	Policy: agentkit.Policy{
		BaseImage:      "platinum-sandbox:prod",
		SuspendTimeout: 5 * time.Minute,
		ArchiveTimeout: 24 * time.Hour,
		MaxConcurrent:  20,
	},
})
```

- One container per session via the DinD daemon; host reaches the agent on `localhost:<leasedPort>`.
- Idle reaper suspends after 5m; archive loop snapshots+persists+destroys after 24h cold — both
  flush-guarded.
- `blobarchive.Persist` does the commit→diff→tar→gzip→blob dance; `Materialize` reverses it. The
  "restored container → force full save" heuristic lives in the adapter.
- `Recover()` re-adopts labelled containers on host restart.
- The model-proxy (key injection, model-id rewrite) runs as a small host-side service the
  `ANTHROPIC_BASE_URL` points at — the successor to `orchestrator/src/proxy.ts`, now a host concern.

Functionally identical to today; the difference is *where the orchestration logic lives* (Go host, not
a TS process).

## Recipe C — Scaled production: Kubernetes + remote registry

The shape the library is designed to make *possible*, even though Platinum doesn't run it yet.

```go
runner := agentkit.NewRunner(agentkit.Deps{
	Env: k8senv.New(k8senv.Config{
		Namespace:   "agent-sessions",
		PodTemplate: "...",            // image, resources, readiness probe on /health
		ServicePer:  "session",        // ephemeral Service per pod, or pod-IP direct
	}),
	Registry: remote.New(remote.Config{Registry: "myreg.azurecr.io/agentkit"}),
	// host services as in B
	Policy: agentkit.Policy{ BaseImage: "myreg.azurecr.io/agentkit/sandbox:1.4.2", ArchiveTimeout: 6*time.Hour },
})
```

- Each session is a Pod; `Provision` creates it, `Suspend`/`Resume` scale it, `Destroy` deletes it.
- Images come from a real registry (`EnsurePresent` = pull; `Persist` = push).
- **Snapshotting on K8s is the open question** ([02](02-execution-environment.md#open-design-questions)):
  the adapter declares `SupportsSnapshot` and the archive loop honours it. Options: workspace-only CSI
  volume snapshots, a buildkit sidecar, or stateless sessions. Pick per product.
- No port pool (Services/pod-IPs handle addressing); the `PortAllocator` is DinD-only and simply
  absent here.

## What changes between recipes — and what doesn't

| | A: single+local | B: DinD+blob | C: K8s+remote |
|---|---|---|---|
| `ExecutionEnvironment` | `dockerlocal` | `dockerdind` | `k8senv` |
| `ImageRegistry` | `localbuild` | `blobarchive` | `remote` |
| Orchestration core (lifecycle, reaper, archive loop, flush guard, recovery) | **same** | **same** | **same** |
| `Runner` / `EventPipeline` / `ArtifactStore` | **same** | **same** | **same** |
| in-image agent (`sandbox/`) | **same** | **same** | **same** |
| frontend (`web/`) | **same** | **same** | **same** |
| Isolation | shared | per-container | per-pod |
| Suspend | off | stop/start | scale |
| Snapshot | local tar | diff archive → blob | registry push (or n/a) |

The bottom rows are the only differences, and they're all behind the two engine interfaces. Everything
above them — the genuinely valuable, hard-won logic — is written once.

## Migration note

Platinum starts at **Recipe B** when it adopts the library, because that's its current production
shape and the `blobarchive` adapter is a faithful port of today's suspend/restore. Recipe A is for the
library's own tests and a host's dev loop. Recipe C is the future the architecture unlocks. See
[91-migration-plan.md](91-migration-plan.md).
