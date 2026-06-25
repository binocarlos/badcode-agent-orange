package agentkit

import (
	"context"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestWriteWorkspaceFile_ExecsCatIntoInstance(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "sess-1", Customer: "acme", Job: "j1", UserEmail: "u@x"})

	// Provision a running instance so WriteWorkspaceFile has an instance to write into.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "sess-1", Customer: "acme", Job: "j1", UserEmail: "u@x",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Enable stdin capture so we can assert on what was written.
	env.ExecStdinCapture = true

	err := r.WriteWorkspaceFile(ctx, SessionRef{SessionID: "sess-1"}, "CLAUDE.md", []byte("focus here"))
	if err != nil {
		t.Fatalf("WriteWorkspaceFile: %v", err)
	}

	// At least one Exec must have targeted /workspace/CLAUDE.md.
	found := false
	for _, entry := range env.ExecStdinLog {
		for _, arg := range entry.Cmd {
			if strings.Contains(arg, "/workspace/CLAUDE.md") {
				found = true
				break
			}
		}
	}
	// Also check via Exec records (cmd args).
	if !found {
		execCalls := env.CallsTo("Exec")
		for _, call := range execCalls {
			for _, a := range call.Args {
				if s, ok := a.(string); ok && strings.Contains(s, "/workspace/CLAUDE.md") {
					found = true
					break
				}
				if ss, ok := a.([]string); ok {
					for _, s := range ss {
						if strings.Contains(s, "/workspace/CLAUDE.md") {
							found = true
							break
						}
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected an Exec writing /workspace/CLAUDE.md; stdin log: %+v, exec calls: %+v",
			env.ExecStdinLog, env.CallsTo("Exec"))
	}
}

func TestWriteWorkspaceFile_RejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "sess-2", Customer: "acme", Job: "j1", UserEmail: "u@x"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "sess-2", Customer: "acme", Job: "j1", UserEmail: "u@x",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	err := r.WriteWorkspaceFile(ctx, SessionRef{SessionID: "sess-2"}, "../../etc/passwd", []byte("bad"))
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}
