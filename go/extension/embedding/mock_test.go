package embedding

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
)

func mustEmbed(t *testing.T, text string) []float32 {
	t.Helper()
	v, err := NewMock().Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed %q: %v", text, err)
	}
	return v
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // both operands are unit vectors
}

// Sample texts used across the mock's property tests.
var mockTexts = []string{
	"The customer asked about refund windows; policy is 30 days.",
	"rolling summary of the email-answerer worker for 2026-07-25",
	"a",
	"!!!",                      // no word characters at all
	"BadCode ticket AO-4711",   // jargon and ids, exactly what the keyword leg is for
	"héllo wörld — üñïcodé ok", // non-ASCII letters are word characters
}

// Determinism is the whole point: a memory is embedded once, at create, and
// compared against query embeddings computed weeks later.
func TestEmbeddingMockIsDeterministic(t *testing.T) {
	for _, text := range mockTexts {
		t.Run(text, func(t *testing.T) {
			first := mustEmbed(t, text)
			for i := 0; i < 3; i++ {
				if got := mustEmbed(t, text); !reflect.DeepEqual(got, first) {
					t.Fatalf("embedding of %q changed between calls", text)
				}
			}
			// A second instance must agree with the first: no per-instance state.
			other, err := NewMock().Embed(context.Background(), text)
			if err != nil || !reflect.DeepEqual(other, first) {
				t.Fatalf("two Mock instances disagree on %q (err=%v)", text, err)
			}
		})
	}
}

// Case and punctuation are noise; the tokenizer drops them, so these pairs are
// the SAME text as far as the mock is concerned.
func TestEmbeddingMockNormalisesInput(t *testing.T) {
	tests := []struct{ name, a, b string }{
		{"case", "Refund Policy", "refund policy"},
		{"whitespace", "  refund   policy \n", "refund policy"},
		{"punctuation", "refund, policy!", "refund policy"},
		{"separators", "refund-policy", "refund policy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(mustEmbed(t, tc.a), mustEmbed(t, tc.b)) {
				t.Fatalf("%q and %q must embed identically", tc.a, tc.b)
			}
		})
	}
}

// Different text ⇒ different vector, including reorderings of the same words
// (that is what the bigram features buy).
func TestEmbeddingMockDistinguishesTexts(t *testing.T) {
	tests := []struct{ name, a, b string }{
		{"different words", "refund policy", "shipping address"},
		{"one word differs", "refund policy", "refund window"},
		{"word order", "alpha beta gamma", "gamma beta alpha"},
		{"repetition", "refund refund", "refund"},
		{"substring", "refund", "refunds"},
		{"digits matter", "AO-4711", "AO-4712"},
		{"non-word texts", "!!!", "???"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if reflect.DeepEqual(mustEmbed(t, tc.a), mustEmbed(t, tc.b)) {
				t.Fatalf("%q and %q must not embed identically", tc.a, tc.b)
			}
		})
	}
}

// Unit length + exactly Dim finite values: the contract agentdb's INSERT and
// pgvector's cosine operator both depend on.
func TestEmbeddingMockVectorsAreUnitLength(t *testing.T) {
	for _, text := range mockTexts {
		t.Run(text, func(t *testing.T) {
			v := mustEmbed(t, text)
			if err := Validate(v); err != nil {
				t.Fatalf("mock violates the provider contract: %v", err)
			}
			var sum float64
			for _, f := range v {
				sum += float64(f) * float64(f)
			}
			if norm := math.Sqrt(sum); math.Abs(norm-1) > 1e-5 {
				t.Fatalf("‖v‖ = %v, want 1", norm)
			}
		})
	}
}

// Similarity tracks WORD OVERLAP — enough for the semantic leg to be non-
// degenerate offline, and honestly not more (see the type comment).
func TestEmbeddingMockSimilarityTracksOverlap(t *testing.T) {
	tests := []struct{ name, query, closer, farther string }{
		{
			"shared topic words win",
			"refund policy questions",
			"our refund policy allows returns",
			"the deployment pipeline uses docker",
		},
		{
			"more overlap beats less",
			"rolling summary email answerer",
			"rolling summary for the email answerer worker",
			"rolling summary of the sales pipeline",
		},
		{
			"jargon token carries",
			"ticket AO-4711 status",
			"AO-4711 was closed yesterday",
			"all tickets were closed yesterday",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := mustEmbed(t, tc.query)
			near := cosine(q, mustEmbed(t, tc.closer))
			far := cosine(q, mustEmbed(t, tc.farther))
			if near <= far {
				t.Fatalf("cosine(query, closer)=%.4f must exceed cosine(query, farther)=%.4f", near, far)
			}
			if self := cosine(q, mustEmbed(t, tc.query)); math.Abs(self-1) > 1e-5 {
				t.Fatalf("cosine with itself = %.6f, want 1", self)
			}
		})
	}

	// Unrelated texts are near-orthogonal, so RRF's semantic leg does not
	// promote noise just because a vector exists.
	unrelated := cosine(
		mustEmbed(t, "the customer asked about refund windows"),
		mustEmbed(t, "kubernetes label selectors parse into jsonb containment"),
	)
	if math.Abs(unrelated) > 0.3 {
		t.Fatalf("unrelated texts should be near-orthogonal, cosine = %.4f", unrelated)
	}
}

func TestEmbeddingMockRejectsEmptyText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"empty", "", true},
		{"spaces", "   ", true},
		{"newlines and tabs", "\n\t\r", true},
		{"punctuation only is embeddable", "!!!", false},
		{"single letter is embeddable", "a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := NewMock().Embed(context.Background(), tc.text)
			if tc.wantErr {
				if !errors.Is(err, ErrEmptyText) {
					t.Fatalf("want ErrEmptyText, got %v", err)
				}
				if v != nil {
					t.Fatalf("failed embed must return no vector")
				}
				return
			}
			if err != nil || Validate(v) != nil {
				t.Fatalf("embed %q: err=%v validate=%v", tc.text, err, Validate(v))
			}
		})
	}
}

// One Provider is shared by every session in the process, so it must be safe
// for concurrent use (run under -race to mean anything).
func TestEmbeddingMockIsConcurrencySafe(t *testing.T) {
	m := NewMock()
	want := mustEmbed(t, "concurrent access must be safe")

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := m.Embed(context.Background(), "concurrent access must be safe")
			if err != nil {
				errs <- err
				return
			}
			if !reflect.DeepEqual(got, want) {
				errs <- errors.New("concurrent embedding differs from the sequential one")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
