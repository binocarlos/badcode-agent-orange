// Package imagetree computes build order over a forest of images that inherit via
// a single parent pointer (e.g. Docker FROM chains). It is dependency-free and
// knows nothing about Docker, registries, or any file format — callers supply
// []Node and receive ordered names. Parent == "" means the node roots at an
// external base (not represented as a Node).
package imagetree

import (
	"fmt"
	"sort"
)

// Node is one image in the inheritance forest.
type Node struct {
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
}

// index validates the node set and returns name -> parent.
func index(nodes []Node) (map[string]string, error) {
	m := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Name == "" {
			return nil, fmt.Errorf("imagetree: node with empty name")
		}
		if _, dup := m[n.Name]; dup {
			return nil, fmt.Errorf("imagetree: duplicate node name %q", n.Name)
		}
		m[n.Name] = n.Parent
	}
	for name, parent := range m {
		if parent == "" {
			continue
		}
		if _, ok := m[parent]; !ok {
			return nil, fmt.Errorf("imagetree: node %q has unknown parent %q", name, parent)
		}
	}
	return m, nil
}

// Chain returns the ancestor chain for target, root-first and INCLUDING target as
// the last element.
func Chain(nodes []Node, target string) ([]string, error) {
	m, err := index(nodes)
	if err != nil {
		return nil, err
	}
	if _, ok := m[target]; !ok {
		return nil, fmt.Errorf("imagetree: unknown target %q", target)
	}
	var rev []string
	seen := map[string]bool{}
	for cur := target; cur != ""; cur = m[cur] {
		if seen[cur] {
			return nil, fmt.Errorf("imagetree: cycle detected at %q", cur)
		}
		seen[cur] = true
		rev = append(rev, cur)
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// BuildOrder returns every node name with each node after its parent
// (topological). Deterministic: ties broken by name.
func BuildOrder(nodes []Node) ([]string, error) {
	m, err := index(nodes)
	if err != nil {
		return nil, err
	}
	children := map[string][]string{}
	indeg := map[string]int{}
	for name := range m {
		indeg[name] = 0
	}
	for name, parent := range m {
		if parent != "" {
			children[parent] = append(children[parent], name)
			indeg[name]++
		}
	}
	var ready []string
	for name, d := range indeg {
		if d == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	var out []string
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		out = append(out, n)
		kids := children[n]
		sort.Strings(kids)
		for _, c := range kids {
			indeg[c]--
			if indeg[c] == 0 {
				ready = append(ready, c)
			}
		}
		sort.Strings(ready)
	}
	if len(out) != len(m) {
		return nil, fmt.Errorf("imagetree: cycle detected (ordered %d of %d nodes)", len(out), len(m))
	}
	return out, nil
}
