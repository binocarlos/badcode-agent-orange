// Package ociregistry implements ImageRegistry by committing session containers
// as image layers and pushing/pulling from an OCI-compatible registry
// (Docker Hub, Azure Container Registry, GHCR, self-hosted).
//
// Persist: docker tag <image-ref> → docker push → Handle{Kind:"registry", Ref:<full-ref>}.
// (The ref passed to Persist is already a committed image — execenv.Snapshot does the
// commit upstream; the registry dedups shared base layers so only the diff layer uploads.)
// Materialize / EnsurePresent: docker pull <ref>.
// Resolve: docker inspect <tag> (local cache hit, no pull needed).
// PortableHandles = TRUE — a pushed ref is reachable from any worker with registry credentials.
// SupportsDiff = false — layer dedup is handled by the registry itself.
//
// See agent-library/docs/03-image-registry.md (registry row).
package ociregistry

import (
	"context"
	"fmt"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

const (
	HandleKind       = "registry"
	metaKeySessionID = "session_id"
	metaKeyImageRef  = "image_ref"
)

// Config holds the registry connection details.
type Config struct {
	DockerHost string
	Registry   string
	Username   string
	Password   string
	AlwaysPull bool // force a pull on EnsurePresent (local dev :dev-tag drift)
}

// Registry is the ociregistry ImageRegistry adapter.
type Registry struct {
	docker     dockerAPI
	registry   string
	authStr    string
	alwaysPull bool
}

// New creates an ociregistry Registry.
func New(cfg Config) (*Registry, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	} else {
		opts = append(opts, client.FromEnv)
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("ociregistry: docker client: %w", err)
	}
	return &Registry{
		docker:     &realDockerClient{c: c},
		registry:   cfg.Registry,
		authStr:    encodeRegistryAuth(cfg.Username, cfg.Password),
		alwaysPull: cfg.AlwaysPull,
	}, nil
}

func newWithAPI(d dockerAPI, registry, authStr string) *Registry {
	return &Registry{docker: d, registry: registry, authStr: authStr}
}

// Capabilities reports ociregistry's abilities.
func (r *Registry) Capabilities() imageregistry.Capabilities {
	return imageregistry.Capabilities{
		SupportsDiff:    false,
		SupportsBuild:   false,
		SupportsRemote:  true,
		PortableHandles: true,
	}
}

var _ imageregistry.ImageRegistry = (*Registry)(nil)

func (r *Registry) EnsurePresent(ctx context.Context, ref execenv.ImageRef) error {
	// Force-pull mode (local dev): the :dev tag is stable but its contents drift
	// on rebuild, so we must always pull to pick up the latest pushed image.
	if !r.alwaysPull {
		// Skip pull if image already present locally.
		if _, _, err := r.docker.ImageInspectWithRaw(ctx, string(ref)); err == nil {
			return nil
		}
	}
	rc, err := r.docker.ImagePull(ctx, string(ref), dockertypes.ImagePullOptions{
		RegistryAuth: r.authStr,
	})
	if err != nil {
		return fmt.Errorf("ociregistry: pull image %s: %w", ref, err)
	}
	// Stream pull progress to the context sink (set by the runner during session
	// create) so the frontend can render a download bar. reportProgress always
	// drains + closes rc, and surfaces a mid-stream registry error.
	return r.reportProgress(ctx, rc)
}
func (r *Registry) Build(ctx context.Context, spec imageregistry.BuildSpec) (execenv.ImageRef, error) {
	return "", fmt.Errorf("ociregistry: Build not supported — use a pre-built image pushed to the registry")
}
// Resolve checks whether the tagged ref (spec.Tag) is already present locally.
// Returns (ref, true, nil) on hit; ("", false, nil) on miss. No remote call.
func (r *Registry) Resolve(ctx context.Context, spec imageregistry.BuildSpec) (execenv.ImageRef, bool, error) {
	if spec.Tag == "" {
		return "", false, nil
	}
	if _, _, err := r.docker.ImageInspectWithRaw(ctx, spec.Tag); err != nil {
		return "", false, nil
	}
	return execenv.ImageRef(spec.Tag), true, nil
}
func (r *Registry) Persist(ctx context.Context, ref execenv.ImageRef, opts imageregistry.PersistOptions) (imageregistry.Handle, error) {
	// ref is an already-committed in-engine image (execenv.Snapshot produced it).
	// We do NOT re-commit here — Persist's contract is "store an in-engine image ref
	// durably". Tag it to the remote ref and push; the registry dedups shared base
	// layers, so only the new top layer (the session diff) is uploaded.
	remoteRef := r.remoteRef(opts.SessionID, "latest")
	if err := r.docker.ImageTag(ctx, string(ref), remoteRef); err != nil {
		return imageregistry.Handle{}, fmt.Errorf("ociregistry: tag image: %w", err)
	}

	// Push — parse the progress stream into the context sink (if any), then drain.
	rc, err := r.docker.ImagePush(ctx, remoteRef, dockertypes.ImagePushOptions{
		RegistryAuth: r.authStr,
	})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("ociregistry: push image: %w", err)
	}
	if err := r.reportProgress(ctx, rc); err != nil {
		return imageregistry.Handle{}, fmt.Errorf("ociregistry: push image: %w", err)
	}

	return imageregistry.Handle{
		Kind: HandleKind,
		Ref:  remoteRef,
		Meta: map[string]string{
			metaKeySessionID: opts.SessionID,
			metaKeyImageRef:  string(ref),
		},
	}, nil
}
func (r *Registry) Materialize(ctx context.Context, h imageregistry.Handle) (execenv.ImageRef, error) {
	if h.Kind != HandleKind {
		return "", fmt.Errorf("ociregistry: expected handle kind %q, got %q", HandleKind, h.Kind)
	}
	// Use the local image if it's already present. `docker commit` materialises
	// the snapshot in the daemon's local store (persisted in the DinD volume), so
	// after a stack recreate that wipes the (ephemeral) registry the image can
	// still be restored from the local copy — no pull needed. On a fresh worker
	// that lacks it, fall through and pull from the registry as before.
	if _, _, err := r.docker.ImageInspectWithRaw(ctx, h.Ref); err == nil {
		return execenv.ImageRef(h.Ref), nil
	}
	rc, err := r.docker.ImagePull(ctx, h.Ref, dockertypes.ImagePullOptions{
		RegistryAuth: r.authStr,
	})
	if err != nil {
		return "", fmt.Errorf("ociregistry: pull for materialize %s: %w", h.Ref, err)
	}
	if err := r.reportProgress(ctx, rc); err != nil {
		return "", fmt.Errorf("ociregistry: pull for materialize %s: %w", h.Ref, err)
	}
	return execenv.ImageRef(h.Ref), nil
}
// Remove is a no-op for wrong-kind handles and returns nil for correct-kind handles.
// Actual deletion from the remote registry is out of band (registry GC or management API).
func (r *Registry) Remove(_ context.Context, h imageregistry.Handle) error {
	if h.Kind != HandleKind {
		return nil
	}
	return nil
}

func (r *Registry) remoteRef(sessionID, tag string) string {
	return fmt.Sprintf("%s/%s:%s", r.registry, sanitizeName(sessionID), tag)
}

func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			out = append(out, c)
		} else if c >= 'A' && c <= 'Z' {
			out = append(out, c+32)
		} else {
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	// Docker image repository names must not start or end with a dash.
	// Trim leading/trailing dashes introduced by the substitution above.
	trimmed := strings.TrimFunc(string(out), func(r rune) bool { return r == '-' || r == '.' })
	if trimmed == "" {
		// Fall back to a safe placeholder if nothing valid remains.
		return "image"
	}
	return trimmed
}

