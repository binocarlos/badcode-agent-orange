package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunBuildOrder(t *testing.T) {
	in := strings.NewReader(`[{"name":"a"},{"name":"b","parent":"a"}]`)
	var out bytes.Buffer
	if err := Run(nil, in, &out); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if got := out.String(); got != "a\nb\n" {
		t.Fatalf("out = %q, want %q", got, "a\nb\n")
	}
}

func TestRunChain(t *testing.T) {
	in := strings.NewReader(`[{"name":"a"},{"name":"b","parent":"a"},{"name":"c","parent":"b"}]`)
	var out bytes.Buffer
	if err := Run([]string{"-target", "c"}, in, &out); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if got := out.String(); got != "a\nb\nc\n" {
		t.Fatalf("out = %q, want %q", got, "a\nb\nc\n")
	}
}

func TestRunBadJSON(t *testing.T) {
	if err := Run(nil, strings.NewReader("not json"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error on bad JSON")
	}
}
