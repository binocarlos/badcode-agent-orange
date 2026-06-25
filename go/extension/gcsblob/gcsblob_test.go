package gcsblob

import "testing"

func TestJoinKey(t *testing.T) {
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{}, ""},
		{[]string{"", ""}, ""},
		{[]string{"a", "b", "c"}, "a/b/c"},
		{[]string{"a", "", "c"}, "a/c"},
		{[]string{"/a/", "/b/"}, "a/b"},
		{[]string{"session", "s-123"}, "session/s-123"},
		{[]string{"", "global-ns"}, "global-ns"},
	}
	for _, c := range cases {
		if got := joinKey(c.parts...); got != c.want {
			t.Errorf("joinKey(%v) = %q, want %q", c.parts, got, c.want)
		}
	}
}

func TestCleanPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"/":            "",
		"a":            "a",
		"/a/b/":        "a/b",
		"agent-orange": "agent-orange",
	} {
		if got := cleanPrefix(in); got != want {
			t.Errorf("cleanPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObjAndRelKey(t *testing.T) {
	// With a non-empty store prefix, obj prepends and relKey strips, so a key
	// round-trips: relKey(obj(k)) == k.
	b := &BlobStore{prefix: "root/session/s1"}
	key := "a/b.txt"
	full := b.obj(key)
	if full != "root/session/s1/a/b.txt" {
		t.Fatalf("obj = %q", full)
	}
	if got := b.relKey(full); got != key {
		t.Fatalf("relKey(obj(%q)) = %q, want round-trip", key, got)
	}

	// objPrefix preserves a caller's trailing slash (List semantics).
	if got := b.objPrefix("dir/"); got != "root/session/s1/dir/" {
		t.Fatalf("objPrefix = %q", got)
	}
}

func TestObjAndRelKeyEmptyPrefix(t *testing.T) {
	b := &BlobStore{prefix: ""}
	if got := b.obj("a/b.txt"); got != "a/b.txt" {
		t.Fatalf("obj = %q", got)
	}
	if got := b.relKey("a/b.txt"); got != "a/b.txt" {
		t.Fatalf("relKey = %q", got)
	}
	if got := b.objPrefix("dir/"); got != "dir/" {
		t.Fatalf("objPrefix = %q", got)
	}
}
