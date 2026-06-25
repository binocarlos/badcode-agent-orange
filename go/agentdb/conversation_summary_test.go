package agentdb

import "testing"

func TestSourceHash_StableAndOrderInsensitiveToInputSlice(t *testing.T) {
	a := []*QueryEvents{
		{ID: "q1", CreatedAt: 100},
		{ID: "q2", CreatedAt: 200},
	}
	h1 := SourceHash(a)
	// Same content, different slice order -> same hash (we sort by id).
	b := []*QueryEvents{
		{ID: "q2", CreatedAt: 200},
		{ID: "q1", CreatedAt: 100},
	}
	if SourceHash(b) != h1 {
		t.Fatalf("hash should be order-insensitive")
	}
	// A changed event -> different hash.
	c := []*QueryEvents{
		{ID: "q1", CreatedAt: 100},
		{ID: "q2", CreatedAt: 201},
	}
	if SourceHash(c) == h1 {
		t.Fatalf("hash should change when an event changes")
	}
	if SourceHash(nil) != "" {
		t.Fatalf("empty input -> empty hash")
	}
}

func TestComposeTranscriptText_JoinsSearchText(t *testing.T) {
	qes := []*QueryEvents{
		{ID: "q1", SearchText: "what is brand awareness"},
		{ID: "q2", SearchText: "awareness is 42 percent"},
	}
	got := ComposeTranscriptText(qes)
	want := "what is brand awareness\nawareness is 42 percent"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
