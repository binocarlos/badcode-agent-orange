package topology

import (
	"strings"
	"testing"
)

// okTopology returns a minimal valid definition for registry tests.
func okTopology(name, version string) *Topology {
	return &Topology{
		Name: name, Version: version, Description: "a test topology",
		Render: func(Answers) (*Bundle, error) { return &Bundle{}, nil },
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	r := NewRegistry()
	r.Register(okTopology("alpha", "v1"))

	got, ok := r.Get("alpha", "v1")
	if !ok {
		t.Fatal("registered topology not found")
	}
	if got.Ref() != "alpha@v1" {
		t.Fatalf("ref: want alpha@v1, got %s", got.Ref())
	}
	if _, ok := r.Get("alpha", "v2"); ok {
		t.Fatal("unregistered version resolved")
	}
	if _, ok := r.Get("beta", "v1"); ok {
		t.Fatal("unregistered name resolved")
	}
}

// Same name, new version is how topologies evolve (D1: versioned so
// authorability/evolution needs no rework); both stay resolvable.
func TestRegistryVersionsCoexist(t *testing.T) {
	r := NewRegistry()
	r.Register(okTopology("alpha", "v1"))
	r.Register(okTopology("alpha", "v2"))
	for _, v := range []string{"v1", "v2"} {
		if _, ok := r.Get("alpha", v); !ok {
			t.Fatalf("alpha@%s not resolvable", v)
		}
	}
}

// List orders by name then NUMERIC version — v10 sorts after v2, not before.
func TestRegistryListOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(okTopology("beta", "v1"))
	r.Register(okTopology("alpha", "v10"))
	r.Register(okTopology("alpha", "v2"))
	want := []string{"alpha@v2", "alpha@v10", "beta@v1"}
	got := r.List()
	if len(got) != len(want) {
		t.Fatalf("list length: want %d, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].Ref() != w {
			t.Fatalf("list[%d]: want %s, got %s", i, w, got[i].Ref())
		}
	}
}

// mustPanic runs fn and asserts it panics with a message containing want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("want panic containing %q, got none", want)
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("panic %q does not contain %q", msg, want)
		}
	}()
	fn()
}

// Registration panics mirror mcpserver.register: definition bugs die at boot,
// loudly, rather than shadowing each other at runtime.
func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		top  *Topology
		want string
	}{
		{"nil topology", nil, "topology is nil"},
		{"empty name", okTopology("", "v1"), "not kebab-case"},
		{"non-kebab name", okTopology("Solo_One", "v1"), "not kebab-case"},
		{"empty version", okTopology("a", ""), "not of the form v1"},
		{"bare number version", okTopology("a", "1"), "not of the form v1"},
		{"v-zero version", okTopology("a", "v0"), "not of the form v1"},
		{"no description", &Topology{Name: "a", Version: "v1", Render: func(Answers) (*Bundle, error) { return &Bundle{}, nil }}, "no description"},
		{"no renderer", &Topology{Name: "a", Version: "v1", Description: "d"}, "no renderer"},
		{
			"duplicate question id",
			withQuestions(okTopology("a", "v1"),
				Question{ID: "q", Prompt: "p", Type: QuestionString},
				Question{ID: "q", Prompt: "p", Type: QuestionBool}),
			`duplicate question id "q"`,
		},
		{
			"non-kebab question id",
			withQuestions(okTopology("a", "v1"), Question{ID: "Q One", Prompt: "p", Type: QuestionString}),
			"not kebab-case",
		},
		{
			"question without prompt",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Type: QuestionString}),
			"no prompt",
		},
		{
			"unknown question type",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Prompt: "p", Type: "number"}),
			`unknown type "number"`,
		},
		{
			"choice without choices",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Prompt: "p", Type: QuestionChoice}),
			"no choices",
		},
		{
			"choices on a string question",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Prompt: "p", Type: QuestionString, Choices: []string{"x"}}),
			"choices on a string question",
		},
		{
			"default of the wrong type",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Prompt: "p", Type: QuestionBool, Default: "yes"}),
			"default does not satisfy",
		},
		{
			"choice default outside the list",
			withQuestions(okTopology("a", "v1"), Question{ID: "q", Prompt: "p", Type: QuestionChoice, Choices: []string{"x", "y"}, Default: "z"}),
			"default does not satisfy",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			mustPanic(t, tc.want, func() { r.Register(tc.top) })
		})
	}
}

func withQuestions(t *Topology, qs ...Question) *Topology {
	t.Questions = qs
	return t
}

func TestRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(okTopology("alpha", "v1"))
	mustPanic(t, "duplicate registration of alpha@v1", func() {
		r.Register(okTopology("alpha", "v1"))
	})
}

// The built-in catalogue holds the seed catalogue's entry 1, and everything in
// it validates (List reaching here at all means every init() Register call
// survived its checks).
func TestBuiltinsContainSolo(t *testing.T) {
	top, ok := Get("solo", "v1")
	if !ok {
		t.Fatal("solo@v1 is not registered")
	}
	if top.Description == "" || len(top.Questions) == 0 {
		t.Fatal("solo@v1 is missing its description or questions")
	}
	found := false
	for _, lt := range List() {
		if lt.Ref() == "solo@v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("solo@v1 absent from List()")
	}
}
