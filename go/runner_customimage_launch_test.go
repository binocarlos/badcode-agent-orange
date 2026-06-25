package agentkit

import (
	"context"
	"testing"

	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

type fakeCustomImages struct {
	handle imageregistry.Handle
	ok     bool
	err    error
}

func (f *fakeCustomImages) Resolve(ctx context.Context, id, email, customer string) (imageregistry.Handle, bool, error) {
	return f.handle, f.ok, f.err
}

func TestResolveLaunchImage_CustomImageMaterialized(t *testing.T) {
	r, _, reg, _, _, _ := newTestRunner(t)
	// Persist a handle so Materialize can round-trip it.
	h, err := reg.Persist(context.Background(), execenv.ImageRef("mock-image:custom"), imageregistry.PersistOptions{SessionID: "x"})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	r.deps.CustomImages = &fakeCustomImages{handle: h, ok: true}

	ref, err := r.resolveLaunchImage(context.Background(), "", "img-1", "a@acme.com", "acme")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ref == "" || ref == execenv.ImageRef(r.deps.Policy.BaseImage) {
		t.Fatalf("expected materialized custom image ref, got %q", ref)
	}
}

func TestResolveLaunchImage_CustomImageNotVisible_FallsBackToBase(t *testing.T) {
	r, _, _, _, _, _ := newTestRunner(t)
	r.deps.Policy.BaseImage = "base:dev"
	r.deps.CustomImages = &fakeCustomImages{ok: false} // not found / not visible

	ref, err := r.resolveLaunchImage(context.Background(), "", "img-missing", "a@acme.com", "acme")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ref != execenv.ImageRef("base:dev") {
		t.Fatalf("expected base fallback, got %q", ref)
	}
}

func TestResolveLaunchImage_ExplicitImageStillWinsOverCustom(t *testing.T) {
	r, _, _, _, _, _ := newTestRunner(t)
	r.deps.CustomImages = &fakeCustomImages{ok: true}
	ref, err := r.resolveLaunchImage(context.Background(), "explicit:tag", "img-1", "a@acme.com", "acme")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ref != execenv.ImageRef("explicit:tag") {
		t.Fatalf("explicit image must win, got %q", ref)
	}
}
