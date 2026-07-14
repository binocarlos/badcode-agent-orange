package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestFileConnectorPublishesAndDedupes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "published.jsonl")
	c := &fileConnector{path: path}

	ref, err := c.Publish(ctx, orchestrator.Post{Channel: "drafts", Text: "hello", DedupeKey: "t1"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ref != "file://published/t1" {
		t.Fatalf("ref = %q", ref)
	}

	// A redelivered approval (same DedupeKey) returns the same ref and does
	// NOT append again — the §10b E-5 idempotency contract.
	ref2, err := c.Publish(ctx, orchestrator.Post{Channel: "drafts", Text: "hello again", DedupeKey: "t1"})
	if err != nil || ref2 != ref {
		t.Fatalf("dedupe: ref2=%q err=%v", ref2, err)
	}
	// A different key appends.
	if _, err := c.Publish(ctx, orchestrator.Post{Channel: "drafts", Text: "bye", DedupeKey: "t2"}); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 published lines, got %d:\n%s", len(lines), data)
	}
	if !strings.Contains(lines[0], `"text":"hello"`) || !strings.Contains(lines[1], `"dedupe_key":"t2"`) {
		t.Fatalf("published content wrong:\n%s", data)
	}
}
