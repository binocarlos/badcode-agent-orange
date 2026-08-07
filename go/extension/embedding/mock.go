package embedding

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Mock is a deterministic, offline, dependency-free Provider: a hashed
// bag-of-words ("hashing trick") embedder. It exists so tests and mock mode
// exercise the real embedding path — a non-NULL column, a live semantic leg,
// RRF actually fusing two legs — with no network and no API key (§7.5).
//
// Properties it guarantees, and which its tests pin:
//   - deterministic: the same text always yields the same vector, in this
//     process and in the next one (no seeds, no map iteration, no time);
//   - case- and whitespace-insensitive: "Refund  Policy" == "refund policy";
//   - distinct: different texts yield different vectors, including reorderings
//     of the same words (adjacent word pairs are hashed as features too);
//   - unit length: ‖v‖ = 1, so cosine similarity is a plain dot product and
//     distances are comparable across texts.
//
// What it is NOT: semantic. Similarity here tracks *word overlap*, so it
// roughly duplicates the keyword leg and cannot find a paraphrase that shares
// no words — the one thing a real embedder is for. It is a stand-in for the
// plumbing, not for the model. Tests that need genuine "paraphrase with zero
// overlap" behaviour must supply vectors directly (as agentdb's live memory
// tests do with orthogonal one-hot vectors), and no product decision about
// retrieval quality may be based on mock-mode results.
type Mock struct{}

// compile-time interface check
var _ Provider = (*Mock)(nil)

// NewMock returns the deterministic offline embedder. It is stateless, so one
// instance can be shared by every session (as the Provider contract requires).
func NewMock() *Mock { return &Mock{} }

// mockProbes is how many dimensions each feature contributes to. More than one
// makes a single hash collision cost proportionally less similarity.
const mockProbes = 2

// bigramWeight is the weight of an adjacent word pair relative to a single
// word. It is what makes word ORDER visible at all; kept below 1 so shared
// vocabulary still dominates similarity.
const bigramWeight = 0.5

// Embed returns the unit-normalised hashed bag-of-words vector for text.
func (m *Mock) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, ErrEmptyText
	}

	v := make([]float32, Dim)
	toks := mockTokenize(text)
	for i, tok := range toks {
		mockAddFeature(v, "w:"+tok, 1)
		if i > 0 {
			mockAddFeature(v, "b:"+toks[i-1]+" "+tok, bigramWeight)
		}
	}
	// Text with no word characters at all ("!!!", "→") tokenises to nothing;
	// hash the raw text so it still gets a stable, non-zero vector.
	if mockIsZero(v) {
		mockAddFeature(v, "raw:"+strings.ToLower(strings.TrimSpace(text)), 1)
	}
	// Only reachable if every probe cancelled out — astronomically unlikely,
	// but a zero vector cannot be normalised and cosine cannot rank it.
	if mockIsZero(v) {
		v[0] = 1
	}
	mockNormalise(v)
	return v, nil
}

// mockTokenize lowercases and splits on anything that is not a letter or
// digit, which is why case and punctuation do not change the vector.
func mockTokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// mockAddFeature scatters one feature across mockProbes dimensions with a
// hash-derived sign, so unrelated features cancel rather than accumulate.
func mockAddFeature(v []float32, feature string, weight float32) {
	for probe := 0; probe < mockProbes; probe++ {
		h := fnv.New64a()
		_, _ = h.Write([]byte{byte(probe)})
		_, _ = h.Write([]byte(feature))
		sum := h.Sum64()
		idx := int(sum % uint64(len(v)))
		if sum&(1<<63) != 0 {
			v[idx] -= weight
		} else {
			v[idx] += weight
		}
	}
}

func mockIsZero(v []float32) bool {
	for _, f := range v {
		if f != 0 {
			return false
		}
	}
	return true
}

func mockNormalise(v []float32) {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return
	}
	for i, f := range v {
		v[i] = float32(float64(f) / norm)
	}
}
