// Package embedding is the embedding-provider seam for the memory system
// (spec §7.5). Core never speaks HTTP to an embedding model itself: the host
// supplies a Provider (Anthropic, OpenAI, a local endpoint, …) and core calls
// it synchronously on memory_create and on the query side of memory_search.
//
// The seam has one deliberate property: **a nil Provider is a supported
// deployment, not an error**. With no provider configured, memories are stored
// with a NULL content_embedding and search degrades to keyword+recency — the
// result shape never changes (§7.6.5). Everything in this package exists to
// make that degradation explicit and testable rather than accidental:
//
//	emb, err := embedding.Embed(ctx, provider, content)  // provider may be nil
//	if err != nil { return err }
//	store.CreateMemory(ctx, mem, emb)                    // nil ⇒ NULL column
//
// Mock (mock.go) is the deterministic offline embedder used by tests and by
// mock mode, so e2e stays offline-green.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
)

// Dim is the width every Provider must emit. It is fixed by the database
// column (`content_embedding vector(1536)`, migration 022) and mirrors
// agentdb.MemoryEmbeddingDim — a test pins the two together. It is not a
// per-provider choice: a host whose model emits a different width must project
// or pad to Dim before returning, or migration 022 must change.
const Dim = 1536

// ErrEmptyText is returned by a Provider asked to embed blank text. The helpers
// in this package never let that happen (they short-circuit first); it exists
// so a directly-called provider fails loudly instead of returning a zero
// vector, which cosine cannot rank.
var ErrEmptyText = errors.New("embedding: cannot embed empty text")

// Provider turns text into a vector. Hosts implement it; core only calls it.
//
// Contract:
//   - the returned slice has exactly Dim finite float32s (see Validate);
//   - the same text yields the same vector for the lifetime of a deployment
//     (rows are embedded once, at create; a provider that drifts silently
//     invalidates every stored row);
//   - implementations are safe for concurrent use — one Provider is shared by
//     every session in the process;
//   - ctx cancellation is honoured: memory_create blocks on this call.
//
// Normalisation is not required — the semantic leg uses pgvector's cosine
// operator (`<=>`), which normalises internally.
type Provider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Validate checks a vector against the Provider contract: exactly Dim finite
// float32s. Exported so a host can unit-test its own provider against the same
// rule agentdb enforces at the INSERT.
func Validate(v []float32) error {
	if len(v) != Dim {
		return fmt.Errorf("embedding: provider must return %d dimensions, got %d", Dim, len(v))
	}
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return fmt.Errorf("embedding: provider returned a non-finite value at index %d", i)
		}
	}
	return nil
}

// Embed is the call site for the WRITE path (memory_create, §7.5). It returns
// the embedding of text, or nothing at all.
//
// A nil provider is not an error: it is the documented "no embedding endpoint
// configured" deployment, and the caller passes the nil vector straight to
// CreateMemory, which stores a NULL column. Blank text is likewise nothing to
// embed.
//
// A provider that IS configured but fails IS an error, and the caller must
// surface it. Writing a row with a silently-NULL embedding because a token
// expired would leave that memory permanently invisible to the semantic leg
// long after the outage ended — memories are append-only and are never
// re-embedded, so a write-path failure is not recoverable later.
func Embed(ctx context.Context, p Provider, text string) ([]float32, error) {
	if p == nil || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	v, err := p.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embedding: provider failed: %w", err)
	}
	if err := Validate(v); err != nil {
		return nil, err
	}
	return v, nil
}

// EmbedOrDegrade is Embed for the READ path (the query side of memory_search).
// It never returns an error: a provider failure degrades this one query to the
// keyword leg (§7.6.5) instead of failing the agent's search outright.
//
// The asymmetry with Embed is deliberate and is the whole degradation story:
// on the read path a keyword-only answer is a worse answer to one question,
// while on the write path a missing embedding is a permanently worse row.
// Failures are logged, never swallowed silently.
func EmbedOrDegrade(ctx context.Context, p Provider, text string) []float32 {
	v, err := Embed(ctx, p, text)
	if err != nil {
		log.Printf("[embedding] query embedding failed, degrading to keyword-only: %v", err)
		return nil
	}
	return v
}

// NewFromEnv selects a provider the way agentd selects its other backends
// (cmd/agentd/backends.go), from AGENTKIT_EMBEDDING_BACKEND:
//
//	none (default) — no provider; memory search is keyword+recency (§7.6.5)
//	mock           — the deterministic offline embedder (mock mode, e2e)
//	openai         — OpenAI /v1/embeddings (openai.go); reads OPENAI_API_KEY,
//	                 and optionally AGENTKIT_EMBEDDING_MODEL and
//	                 AGENTKIT_EMBEDDING_BASE_URL
//
// "none" returns (nil, nil): a nil Provider is a success, and callers hand it
// straight to Embed. An unrecognised value is an error rather than a silent
// fall back to "none" — a typo must not quietly cost a deployment its semantic
// leg. A host with its own provider still constructs it and passes it in;
// this function is the convenience path, not the only one.
func NewFromEnv(env func(string) string) (Provider, error) {
	backend := strings.TrimSpace(env("AGENTKIT_EMBEDDING_BACKEND"))
	switch backend {
	case "", "none":
		return nil, nil
	case "mock":
		return NewMock(), nil
	case "openai":
		// A missing key is a boot error, not a degrade-to-keyword: asking for
		// "openai" and silently getting no semantic leg is the failure this
		// whole package exists to prevent.
		return NewOpenAI(OpenAIConfig{
			APIKey:  env("OPENAI_API_KEY"),
			Model:   env("AGENTKIT_EMBEDDING_MODEL"),
			BaseURL: env("AGENTKIT_EMBEDDING_BASE_URL"),
		})
	default:
		return nil, fmt.Errorf("unknown AGENTKIT_EMBEDDING_BACKEND %q (want none|mock|openai)", backend)
	}
}
