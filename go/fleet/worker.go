package fleet

import "github.com/bayes-price/agentkit/execenv"

// Worker maps a stable ID to a unit of compute that runs agent sessions.
// The Env is the per-worker placement primitive: the Fleet composes above it.
//
//   - DinD:       one daemon host = one worker.
//   - Kubernetes: the whole cluster/namespace = one worker (the k8s scheduler
//                 places pods internally).
//   - Managed:    the provider (Daytona, E2B, …) = one worker.
type Worker struct {
	// ID is stable and is what the SessionStore binding records.
	ID string
	// Env is the ExecutionEnvironment for this worker.
	Env execenv.ExecutionEnvironment
	// Caps is a snapshot of Env.Capabilities() cached at registration so the
	// fleet can make placement decisions without calling into the environment.
	Caps execenv.Capabilities
	// Labels are arbitrary key/value annotations used by affinity policies
	// (zone, gpu, image-affinity, …).
	Labels map[string]string
}
