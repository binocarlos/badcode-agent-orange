package imagetree

import (
	"reflect"
	"strings"
	"testing"
)

func TestChainLinear(t *testing.T) {
	nodes := []Node{{Name: "a"}, {Name: "b", Parent: "a"}, {Name: "c", Parent: "b"}}
	got, err := Chain(nodes, "c")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Chain = %v, want %v", got, want)
	}
}

func TestChainRootIsSelf(t *testing.T) {
	nodes := []Node{{Name: "a"}, {Name: "b", Parent: "a"}}
	got, err := Chain(nodes, "a")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Chain = %v, want %v", got, want)
	}
}

func TestBuildOrderLinear(t *testing.T) {
	nodes := []Node{{Name: "c", Parent: "b"}, {Name: "a"}, {Name: "b", Parent: "a"}}
	got, err := BuildOrder(nodes)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildOrder = %v, want %v", got, want)
	}
}

func TestBuildOrderForestParentBeforeChild(t *testing.T) {
	nodes := []Node{{Name: "a"}, {Name: "b", Parent: "a"}, {Name: "x"}, {Name: "y", Parent: "x"}}
	got, err := BuildOrder(nodes)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if len(got) != 4 {
		t.Fatalf("BuildOrder len = %d, want 4 (%v)", len(got), got)
	}
	if pos["a"] > pos["b"] || pos["x"] > pos["y"] {
		t.Fatalf("parent must precede child: %v", got)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
		frag  string
	}{
		{"missing parent", []Node{{Name: "b", Parent: "a"}}, "unknown parent"},
		{"duplicate", []Node{{Name: "a"}, {Name: "a"}}, "duplicate"},
		{"cycle", []Node{{Name: "a", Parent: "b"}, {Name: "b", Parent: "a"}}, "cycle"},
		{"empty name", []Node{{Name: ""}}, "empty name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := BuildOrder(c.nodes); err == nil || !strings.Contains(err.Error(), c.frag) {
				t.Fatalf("BuildOrder err = %v, want contains %q", err, c.frag)
			}
		})
	}
}

func TestChainUnknownTarget(t *testing.T) {
	if _, err := Chain([]Node{{Name: "a"}}, "z"); err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("err = %v, want unknown target", err)
	}
}
