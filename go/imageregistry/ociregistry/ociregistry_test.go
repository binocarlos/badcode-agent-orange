package ociregistry

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	dockertypes "github.com/docker/docker/api/types"

	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

type call struct {
	method string
	args   []string
}

type fakeDockerAPI struct {
	mu    sync.Mutex
	calls []call

	commitID   string
	pushErr    error
	pullErr    error
	tagErr     error
	inspectErr error

	// Fields for asserting Persist's new contract (tag+push, no re-commit).
	commitCalls    int
	lastTagSource  string
	lastTagTarget  string
	lastPushRef    string
}

func newFake() *fakeDockerAPI {
	return &fakeDockerAPI{commitID: "sha256:abcdef1234567890"}
}

// newFakeDockerAPI is an alias for newFake, used in new tests.
func newFakeDockerAPI() *fakeDockerAPI { return newFake() }

func (f *fakeDockerAPI) record(method string, args ...string) {
	f.mu.Lock()
	f.calls = append(f.calls, call{method, args})
	f.mu.Unlock()
}

func (f *fakeDockerAPI) ContainerCommit(ctx context.Context, container string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error) {
	f.record("ContainerCommit", container)
	f.mu.Lock()
	f.commitCalls++
	f.mu.Unlock()
	return dockertypes.IDResponse{ID: f.commitID}, nil
}

func (f *fakeDockerAPI) ImageTag(ctx context.Context, image, ref string) error {
	f.record("ImageTag", image, ref)
	f.mu.Lock()
	f.lastTagSource = image
	f.lastTagTarget = ref
	f.mu.Unlock()
	return f.tagErr
}

func (f *fakeDockerAPI) ImagePush(ctx context.Context, ref string, options dockertypes.ImagePushOptions) (io.ReadCloser, error) {
	f.record("ImagePush", ref)
	f.mu.Lock()
	f.lastPushRef = ref
	f.mu.Unlock()
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return io.NopCloser(strings.NewReader(`{"status":"Pushed"}`)), nil
}

func (f *fakeDockerAPI) ImagePull(ctx context.Context, ref string, options dockertypes.ImagePullOptions) (io.ReadCloser, error) {
	f.record("ImagePull", ref)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	return io.NopCloser(strings.NewReader(`{"status":"Pull complete"}`)), nil
}

func (f *fakeDockerAPI) ImageInspectWithRaw(ctx context.Context, imageID string) (dockertypes.ImageInspect, []byte, error) {
	f.record("ImageInspectWithRaw", imageID)
	if f.inspectErr != nil {
		return dockertypes.ImageInspect{}, nil, f.inspectErr
	}
	return dockertypes.ImageInspect{ID: "sha256:abc"}, nil, nil
}

func (f *fakeDockerAPI) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method == method {
			return true
		}
	}
	return false
}

func (f *fakeDockerAPI) callsWith(method, arg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method != method {
			continue
		}
		for _, a := range c.args {
			if a == arg {
				return true
			}
		}
	}
	return false
}

func newTestRegistry() (*Registry, *fakeDockerAPI) {
	d := newFake()
	return newWithAPI(d, "reg.example.io/agentkit", ""), d
}

// TestPersist_TagsAndPushes asserts Persist's new contract: the input is an
// already-committed image ref; Persist must NOT re-commit but must tag and push.
func TestPersist_TagsAndPushes(t *testing.T) {
	reg, d := newTestRegistry()
	ctx := context.Background()

	const imageRef = "sha256:abcdef1234567890"
	h, err := reg.Persist(ctx, execenv.ImageRef(imageRef), imageregistry.PersistOptions{
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if h.Kind != HandleKind {
		t.Fatalf("handle kind = %q, want %q", h.Kind, HandleKind)
	}
	if h.Ref == "" {
		t.Fatal("handle ref is empty")
	}
	if !strings.Contains(h.Ref, "sess-1") {
		t.Fatalf("handle ref %q should contain session ID", h.Ref)
	}
	// New contract: no re-commit; input is already an image.
	if d.commitCalls != 0 {
		t.Errorf("ContainerCommit called %d times; want 0 (Persist takes an image ref)", d.commitCalls)
	}
	if !d.called("ImageTag") {
		t.Error("ImageTag was not called")
	}
	if !d.called("ImagePush") {
		t.Error("ImagePush was not called")
	}
	if !d.callsWith("ImagePush", h.Ref) {
		t.Errorf("ImagePush was not called with the handle ref %q", h.Ref)
	}
	// Tag source must be the original image ref, not a commit response ID.
	if d.lastTagSource != imageRef {
		t.Errorf("ImageTag source = %q; want %q (the input image ref)", d.lastTagSource, imageRef)
	}
}

func TestMaterialize_PullsAndReturnsRef(t *testing.T) {
	reg, d := newTestRegistry()
	d.inspectErr = fmt.Errorf("not found") // image not present locally → must pull
	ctx := context.Background()

	h := imageregistry.Handle{
		Kind: HandleKind,
		Ref:  "reg.example.io/agentkit/sess-1:latest",
		Meta: map[string]string{metaKeySessionID: "sess-1", metaKeyImageRef: "container-id-abc"},
	}

	got, err := reg.Materialize(ctx, h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if got != execenv.ImageRef(h.Ref) {
		t.Fatalf("Materialize returned %q, want %q", got, h.Ref)
	}
	if !d.callsWith("ImagePull", h.Ref) {
		t.Error("ImagePull was not called with the handle ref")
	}
}

// Restore resilience: when the snapshot image is already present locally (e.g.
// the registry was wiped on a stack recreate but the committed image survives in
// the persistent DinD volume), Materialize must use the local image and NOT pull
// — so restore works against a cold/empty registry.
func TestMaterialize_SkipsPullIfLocal(t *testing.T) {
	reg, d := newTestRegistry()
	// inspectErr = nil (default) → image already present locally.
	h := imageregistry.Handle{
		Kind: HandleKind,
		Ref:  "reg.example.io/agentkit/sess-1:latest",
		Meta: map[string]string{metaKeySessionID: "sess-1"},
	}
	got, err := reg.Materialize(context.Background(), h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if got != execenv.ImageRef(h.Ref) {
		t.Fatalf("Materialize returned %q, want %q", got, h.Ref)
	}
	if d.called("ImagePull") {
		t.Error("ImagePull should not be called when the image is already local")
	}
}

func TestMaterialize_WrongKindErrors(t *testing.T) {
	reg, _ := newTestRegistry()
	_, err := reg.Materialize(context.Background(), imageregistry.Handle{Kind: "blob-archive", Ref: "x"})
	if err == nil {
		t.Fatal("expected error for wrong handle kind")
	}
}

func TestEnsurePresent_PullsIfNotLocal(t *testing.T) {
	reg, d := newTestRegistry()
	d.inspectErr = fmt.Errorf("not found")

	if err := reg.EnsurePresent(context.Background(), execenv.ImageRef("reg.example.io/agentkit/myimage:latest")); err != nil {
		t.Fatalf("EnsurePresent: %v", err)
	}
	if !d.called("ImagePull") {
		t.Error("ImagePull was not called when image not local")
	}
}

func TestEnsurePresent_SkipsPullIfLocal(t *testing.T) {
	reg, d := newTestRegistry()
	// inspectErr = nil → image already local

	if err := reg.EnsurePresent(context.Background(), execenv.ImageRef("reg.example.io/agentkit/myimage:latest")); err != nil {
		t.Fatalf("EnsurePresent: %v", err)
	}
	if d.called("ImagePull") {
		t.Error("ImagePull should not be called when image is already local")
	}
}

func TestRemove_WrongKindIsNoOp(t *testing.T) {
	reg, _ := newTestRegistry()
	if err := reg.Remove(context.Background(), imageregistry.Handle{Kind: "blob-archive", Ref: "x"}); err != nil {
		t.Fatalf("Remove with wrong kind: %v", err)
	}
}

func TestRemove_CorrectKindReturnsNil(t *testing.T) {
	reg, _ := newTestRegistry()
	h := imageregistry.Handle{Kind: HandleKind, Ref: "reg.example.io/agentkit/sess-1:latest"}
	if err := reg.Remove(context.Background(), h); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestResolve_ReturnsFalseOnMiss(t *testing.T) {
	reg, d := newTestRegistry()
	d.inspectErr = fmt.Errorf("not found")

	_, ok, err := reg.Resolve(context.Background(), imageregistry.BuildSpec{
		BaseImage: "myimage:base", Tag: "myimage:v1",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Fatal("Resolve should return ok=false on cache miss")
	}
}

func TestResolve_ReturnsTrueOnLocalHit(t *testing.T) {
	reg, _ := newTestRegistry()
	// inspectErr = nil → image is present locally → cache hit

	ref, ok, err := reg.Resolve(context.Background(), imageregistry.BuildSpec{
		BaseImage: "myimage:base", Tag: "myimage:v1",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve should return ok=true when image is local")
	}
	if ref == "" {
		t.Fatal("Resolve returned empty ref on hit")
	}
}

func TestCapabilities(t *testing.T) {
	reg, _ := newTestRegistry()
	caps := reg.Capabilities()
	if !caps.PortableHandles {
		t.Error("PortableHandles should be true")
	}
	if !caps.SupportsRemote {
		t.Error("SupportsRemote should be true")
	}
	if caps.SupportsDiff {
		t.Error("SupportsDiff should be false")
	}
}

func TestReportProgress_AggregatesBytesAndSkipsDedupedLayers(t *testing.T) {
	// Two transferring layers (50/100 + 30/60) plus one deduped layer (no detail).
	stream := `{"id":"a","status":"Pushing","progressDetail":{"current":50,"total":100}}
{"id":"b","status":"Pushing","progressDetail":{"current":30,"total":60}}
{"id":"c","status":"Layer already exists"}
{"id":"a","status":"Pushing","progressDetail":{"current":100,"total":100}}
`
	var last struct{ done, total int64 }
	sink := sinkFunc(func(done, total int64, _ []imageregistry.LayerProgress) { last.done, last.total = done, total })
	r := newWithAPI(newFake(), "reg.example", "")
	ctx := imageregistry.WithProgressSink(context.Background(), sink)
	if err := r.reportProgress(ctx, io.NopCloser(strings.NewReader(stream))); err != nil {
		t.Fatalf("reportProgress: %v", err)
	}
	// Final aggregate: layer a=100/100, b=30/60, c skipped (total 0).
	if last.done != 130 || last.total != 160 {
		t.Fatalf("aggregate mismatch: done=%d total=%d (want 130/160)", last.done, last.total)
	}
}

func TestReportProgress_NilSinkJustDrains(t *testing.T) {
	r := newWithAPI(newFake(), "reg.example", "")
	if err := r.reportProgress(context.Background(),
		io.NopCloser(strings.NewReader(`{"status":"Pushing"}`))); err != nil {
		t.Fatalf("nil-sink reportProgress should not error: %v", err)
	}
}

func TestReportProgress_StreamErrorPropagates(t *testing.T) {
	r := newWithAPI(newFake(), "reg.example", "")
	err := r.reportProgress(context.Background(),
		io.NopCloser(strings.NewReader(`{"error":"denied: requested access to the resource is denied"}`)))
	if err == nil {
		t.Fatal("expected error from in-stream error field, got nil")
	}
}

func TestReportProgress_TruncatedStreamReturnsError(t *testing.T) {
	r := newWithAPI(newFake(), "reg.example", "")
	// Valid first line then malformed/truncated JSON — must not silently succeed.
	body := `{"id":"a","status":"Pushing","progressDetail":{"current":50,"total":100}}` + "\n" + `{not json`
	ctx := imageregistry.WithProgressSink(context.Background(), sinkFunc(func(done, total int64, layers []imageregistry.LayerProgress) {}))
	err := r.reportProgress(ctx, io.NopCloser(strings.NewReader(body)))
	if err == nil {
		t.Fatal("expected error from truncated/corrupt stream, got nil")
	}
}

// sinkFunc adapts a func to imageregistry.ProgressSink.
type sinkFunc func(done, total int64, layers []imageregistry.LayerProgress)

func (f sinkFunc) Bytes(done, total int64, layers []imageregistry.LayerProgress) { f(done, total, layers) }

func TestEnsurePresentAlwaysPull(t *testing.T) {
	d := newFake() // inspectErr = nil → image present locally by default
	r := &Registry{docker: d, registry: "registry:5000", alwaysPull: true}
	if err := r.EnsurePresent(context.Background(), "registry:5000/x:dev"); err != nil {
		t.Fatal(err)
	}
	if !d.called("ImagePull") {
		t.Error("AlwaysPull=true must pull even when image is present locally")
	}
}

func TestPersistTagsAndPushesImageRefWithoutCommitting(t *testing.T) {
	fake := newFakeDockerAPI()
	reg := newWithAPI(fake, "registry:5000/agentkit", "")

	// Persist receives an ALREADY-COMMITTED image ref (env.Snapshot produced it).
	const imageRef = "sha256:deadbeefcafe"
	h, err := reg.Persist(context.Background(), execenv.ImageRef(imageRef), imageregistry.PersistOptions{
		SessionID: "sess-xyz",
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// MUST NOT re-commit: Persist's input is an image, not a container.
	if fake.commitCalls != 0 {
		t.Fatalf("Persist called ContainerCommit %d times; want 0 (input is already an image)", fake.commitCalls)
	}
	// MUST tag the given image ref to the remote ref, then push that remote ref.
	wantRef := "registry:5000/agentkit/sess-xyz:latest"
	if fake.lastTagSource != imageRef || fake.lastTagTarget != wantRef {
		t.Fatalf("tag = (%q -> %q); want (%q -> %q)", fake.lastTagSource, fake.lastTagTarget, imageRef, wantRef)
	}
	if fake.lastPushRef != wantRef {
		t.Fatalf("push ref = %q; want %q", fake.lastPushRef, wantRef)
	}
	if h.Kind != HandleKind || h.Ref != wantRef {
		t.Fatalf("handle = %+v; want kind=%q ref=%q", h, HandleKind, wantRef)
	}
}
