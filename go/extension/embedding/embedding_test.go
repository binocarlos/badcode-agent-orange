package embedding

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// stubProvider is a Provider whose behaviour each test dictates.
type stubProvider struct {
	vec  []float32
	err  error
	call int
}

func (s *stubProvider) Embed(context.Context, string) ([]float32, error) {
	s.call++
	return s.vec, s.err
}

func goodVector() []float32 {
	v := make([]float32, Dim)
	v[7] = 1
	return v
}

// Dim is not an independent choice: it is the width of the database column
// migration 022 creates, and the width CreateMemory rejects anything else at.
func TestEmbeddingDimMatchesStore(t *testing.T) {
	if Dim != agentdb.MemoryEmbeddingDim {
		t.Fatalf("Dim = %d but agentdb.MemoryEmbeddingDim = %d — a provider's output would be rejected at the INSERT",
			Dim, agentdb.MemoryEmbeddingDim)
	}
	// A mock vector must render as a pgvector literal of exactly Dim components.
	v, err := NewMock().Embed(context.Background(), "the refund policy is thirty days")
	if err != nil {
		t.Fatalf("mock embed: %v", err)
	}
	lit := agentdb.FormatVector(v)
	if n := strings.Count(lit, ",") + 1; n != Dim {
		t.Fatalf("pgvector literal has %d components, want %d", n, Dim)
	}
}

func TestEmbeddingValidate(t *testing.T) {
	nan := goodVector()
	nan[3] = float32(math.NaN())
	inf := goodVector()
	inf[3] = float32(math.Inf(-1))

	tests := []struct {
		name    string
		vec     []float32
		wantErr string
	}{
		{"correct width", goodVector(), ""},
		{"nil", nil, "got 0"},
		{"empty", []float32{}, "got 0"},
		{"too short", make([]float32, Dim-1), "got 1535"},
		{"too long", make([]float32, Dim+1), "got 1537"},
		{"NaN", nan, "non-finite"},
		{"Inf", inf, "non-finite"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.vec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// The degradation path: no provider configured is a supported deployment, not
// an error. Both helpers must hand back a nil vector, which CreateMemory
// stores as a NULL column and SearchMemories reads as keyword-only (§7.6.5).
func TestEmbeddingNilProviderDegrades(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		p    Provider
		text string
	}{
		{"nil provider", nil, "remember this"},
		{"nil provider, blank text", nil, ""},
		{"provider present, empty text", &stubProvider{vec: goodVector()}, ""},
		{"provider present, whitespace text", &stubProvider{vec: goodVector()}, " \n\t "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Embed(ctx, tc.p, tc.text)
			if err != nil {
				t.Fatalf("Embed: want nil error, got %v", err)
			}
			if v != nil {
				t.Fatalf("Embed: want nil vector, got %d dims", len(v))
			}
			if v := EmbedOrDegrade(ctx, tc.p, tc.text); v != nil {
				t.Fatalf("EmbedOrDegrade: want nil vector, got %d dims", len(v))
			}
			// Blank text must never reach the provider at all.
			if sp, ok := tc.p.(*stubProvider); ok && sp.call != 0 {
				t.Fatalf("provider called %d times for blank text", sp.call)
			}
		})
	}
}

// A CONFIGURED provider that misbehaves is asymmetric by design: loud on the
// write path (the row would be permanently unsearchable by meaning), silent
// keyword-only degradation on the read path (one worse answer, now).
func TestEmbeddingProviderFailureIsAsymmetric(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("upstream 503")

	tests := []struct {
		name    string
		stub    *stubProvider
		wantErr string
	}{
		{"provider error", &stubProvider{err: boom}, "upstream 503"},
		{"wrong width", &stubProvider{vec: make([]float32, 8)}, "got 8"},
		{"nil vector, nil error", &stubProvider{}, "got 0"},
		{"healthy", &stubProvider{vec: goodVector()}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Embed(ctx, tc.stub, "some content")
			if tc.wantErr == "" {
				if err != nil || len(v) != Dim {
					t.Fatalf("healthy provider: vec=%d err=%v", len(v), err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("write path: want error containing %q, got %v", tc.wantErr, err)
				}
				if v != nil {
					t.Fatalf("write path: a failed embed must return no vector, got %d dims", len(v))
				}
				if err != nil && errors.Is(err, boom) != (tc.stub.err == boom) {
					t.Fatalf("write path must wrap the provider's error: %v", err)
				}
			}

			// Read path: same provider, never an error.
			got := EmbedOrDegrade(ctx, tc.stub, "some content")
			if tc.wantErr == "" && len(got) != Dim {
				t.Fatalf("read path: healthy provider must yield a vector, got %d dims", len(got))
			}
			if tc.wantErr != "" && got != nil {
				t.Fatalf("read path: failure must degrade to nil, got %d dims", len(got))
			}
		})
	}
}

// Embed passes the context through, so a cancelled memory_create does not wait
// on an embedding endpoint.
func TestEmbeddingContextIsPassedThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Embed(ctx, NewMock(), "anything"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if v := EmbedOrDegrade(ctx, NewMock(), "anything"); v != nil {
		t.Fatalf("cancelled read path must degrade to nil, got %d dims", len(v))
	}
}

func TestEmbeddingNewFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantMock bool
		wantErr  string
	}{
		{"unset defaults to none", "", false, ""},
		{"explicit none", "none", false, ""},
		{"mock", "mock", true, ""},
		{"whitespace tolerated", "  mock  ", true, ""},
		{"typo is loud", "moc", false, "unknown AGENTKIT_EMBEDDING_BACKEND"},
		// "openai" used to belong on this list. It is now a real backend
		// (openai.go), covered by TestNewFromEnvOpenAI — which also pins the
		// rule this row was protecting: a named-but-unbuildable backend is a
		// BOOT error, never a silent fall back to keyword-only. A provider we
		// have not implemented still is: the point is that unrecognised names
		// never degrade quietly.
		{"unimplemented backend names are not silently accepted", "voyage", false, "want none|mock|openai"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string {
				if k != "AGENTKIT_EMBEDDING_BACKEND" {
					t.Fatalf("unexpected env lookup %q", k)
				}
				return tc.value
			}
			p, err := NewFromEnv(env)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				if p != nil {
					t.Fatalf("failed selection must yield no provider")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantMock {
				if _, ok := p.(*Mock); !ok {
					t.Fatalf("want *Mock, got %T", p)
				}
				return
			}
			// "none" must be a usable nil — the degradation path, not a panic.
			if p != nil {
				t.Fatalf("want nil provider, got %T", p)
			}
			if v, err := Embed(context.Background(), p, "text"); v != nil || err != nil {
				t.Fatalf("nil provider from env must degrade cleanly: vec=%v err=%v", v, err)
			}
		})
	}
}
