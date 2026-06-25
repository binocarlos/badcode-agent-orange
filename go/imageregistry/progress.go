// agent-library/go/imageregistry/progress.go
package imageregistry

import "context"

// LayerProgress is the per-layer byte progress of a push/pull, as reported by the
// Docker engine's progress stream.
type LayerProgress struct {
	ID      string `json:"id"`
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Status  string `json:"status"`
}

// ProgressSink receives aggregated byte progress during Persist (push) and
// Materialize (pull). done/total are summed across layers that report a total;
// layers is the per-layer detail (for UIs that want a breakdown).
type ProgressSink interface {
	Bytes(done, total int64, layers []LayerProgress)
}

type progressSinkKey struct{}

// WithProgressSink attaches a ProgressSink to ctx. Registry adapters that support
// progress reporting (ociregistry) read it during push/pull. Adapters that do not
// simply ignore it, so this is always safe to set.
func WithProgressSink(ctx context.Context, s ProgressSink) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, s)
}

// ProgressSinkFromContext returns the sink attached by WithProgressSink, or nil.
func ProgressSinkFromContext(ctx context.Context) ProgressSink {
	s, _ := ctx.Value(progressSinkKey{}).(ProgressSink)
	return s
}
