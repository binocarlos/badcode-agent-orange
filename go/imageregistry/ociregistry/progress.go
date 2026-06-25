package ociregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// dockerProgressMsg is the subset of the Docker push/pull JSON stream we consume.
// The engine emits newline-delimited objects; json.Decoder reads them in sequence.
type dockerProgressMsg struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	ProgressDetail *struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error string `json:"error"`
}

// reportProgress decodes a Docker push/pull progress stream and forwards aggregated
// byte progress to the context sink (if any). rc is always drained and closed. An
// object carrying an "error" field aborts with that error (the push/pull genuinely
// failed) — this check occurs whether or not a sink is present.
func (r *Registry) reportProgress(ctx context.Context, rc io.ReadCloser) error {
	if rc == nil {
		return nil
	}
	defer rc.Close()
	sink := imageregistry.ProgressSinkFromContext(ctx)
	layers := map[string]*imageregistry.LayerProgress{}
	var order []string
	dec := json.NewDecoder(rc)
	for {
		var m dockerProgressMsg
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("ociregistry: decode progress stream: %w", err)
		}
		if m.Error != "" {
			return fmt.Errorf("ociregistry: registry stream error: %s", m.Error)
		}
		if sink == nil || m.ID == "" {
			continue // no sink or status-only line (e.g. "Pushing repository ...")
		}
		lp, ok := layers[m.ID]
		if !ok {
			lp = &imageregistry.LayerProgress{ID: m.ID}
			layers[m.ID] = lp
			order = append(order, m.ID)
		}
		lp.Status = m.Status
		if m.ProgressDetail != nil {
			lp.Current = m.ProgressDetail.Current
			lp.Total = m.ProgressDetail.Total
		}
		var done, total int64
		out := make([]imageregistry.LayerProgress, 0, len(order))
		for _, id := range order {
			l := layers[id]
			if l.Total > 0 { // skip deduped/metadata layers that never transfer
				done += l.Current
				total += l.Total
			}
			out = append(out, *l)
		}
		sink.Bytes(done, total, out)
	}
}
