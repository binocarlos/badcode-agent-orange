package agentkit

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// buildSkillTar builds an in-memory tar of relPath->content for the skill bundle.
func buildSkillTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOnSkillHoisted_CapturesSkillDirArtifact(t *testing.T) {
	ctx := context.Background()
	r, env, arts, store := newArtifactTestRunner(t)

	const sessionID = "skill-sess-1"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	env.ExecStdoutByCmd = map[string][]byte{
		"tar -cf - -C /workspace/.hoisted-skills/graph-gen .": buildSkillTar(t, map[string]string{
			"./SKILL.md": "---\nname: graph-gen\n---\nbody", "./skill.manifest.json": "{}",
		}),
	}

	q := events.QueryContext{SessionID: sessionID, QueryID: "q-skill-1"}
	ev := events.Envelope{
		Type: events.SkillHoisted,
		Data: map[string]any{
			"artifactPath":  ".hoisted-skills/graph-gen",
			"name":          "graph-gen",
			"visibility":    "organizational",
			"requiresBuild": false,
		},
	}
	r.onSkillHoisted(ctx, q, ev)

	list, err := arts.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 artifact, got %d: %+v", len(list), list)
	}
	if !list[0].IsDir {
		t.Fatalf("expected IsDir skill artifact, got %+v", list[0])
	}
	if list[0].ArtifactType != "skill" {
		t.Fatalf("expected artifactType 'skill', got %q", list[0].ArtifactType)
	}
	if list[0].FilePath != ".hoisted-skills/graph-gen" {
		t.Fatalf("expected filePath '.hoisted-skills/graph-gen', got %q", list[0].FilePath)
	}
}

func TestOnSkillHoisted_MissingArtifactPathIsDefensive(t *testing.T) {
	ctx := context.Background()
	r, _, arts, store := newArtifactTestRunner(t)
	const sessionID = "skill-sess-2"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r.onSkillHoisted(ctx, events.QueryContext{SessionID: sessionID}, events.Envelope{
		Type: events.SkillHoisted, Data: map[string]any{"name": "x"},
	})
	if n := arts.Count("Save"); n != 0 {
		t.Fatalf("expected 0 Save calls for missing artifactPath, got %d", n)
	}
}

type fakeSkillCatalog struct {
	calls []SkillPromotion
	err   error
}

func (f *fakeSkillCatalog) Promote(_ context.Context, p SkillPromotion) error {
	f.calls = append(f.calls, p)
	return f.err
}

func newSkillCatalogRunner(t *testing.T, cat SkillCatalog) (*runnerImpl, *execenv.MockExecutionEnvironment, *agentkittest.MemStore) {
	t.Helper()
	env := execenv.NewMock()
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:          env,
		Registry:     imageregistry.NewMock(),
		Store:        store,
		Artifacts:    artifacts.NewMock(),
		Claims:       agentkittest.StaticClaims{Token: "t"},
		SkillCatalog: cat,
		Policy:       Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), env, store
}

func TestOnSkillHoisted_PromotesToCatalog(t *testing.T) {
	ctx := context.Background()
	cat := &fakeSkillCatalog{}
	r, env, store := newSkillCatalogRunner(t, cat)

	const sessionID = "skill-promote-1"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1", UserEmail: "u@acme.com"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	env.ExecStdoutByCmd = map[string][]byte{
		"tar -cf - -C /workspace/.hoisted-skills/graph-gen .": buildSkillTar(t, map[string]string{"./SKILL.md": "x"}),
	}

	r.onSkillHoisted(ctx, events.QueryContext{SessionID: sessionID}, events.Envelope{
		Type: events.SkillHoisted,
		Data: map[string]any{
			"artifactPath":  ".hoisted-skills/graph-gen",
			"name":          "graph-gen",
			"visibility":    "organizational",
			"requiresBuild": true,
			"manifest":      map[string]any{"name": "graph-gen", "description": "Generate graphs"},
		},
	})

	if len(cat.calls) != 1 {
		t.Fatalf("expected 1 Promote call, got %d", len(cat.calls))
	}
	p := cat.calls[0]
	if p.Customer != "acme" || p.OwnerEmail != "u@acme.com" {
		t.Fatalf("expected customer/owner from session, got %+v", p)
	}
	if p.Name != "graph-gen" || p.Visibility != "organizational" || !p.RequiresBuild {
		t.Fatalf("unexpected promotion fields: %+v", p)
	}
	if p.Description != "Generate graphs" {
		t.Fatalf("expected description from manifest, got %q", p.Description)
	}
}

func TestOnSkillHoisted_PublicVisibilityCappedToOrg(t *testing.T) {
	ctx := context.Background()
	cat := &fakeSkillCatalog{}
	r, env, store := newSkillCatalogRunner(t, cat)
	const sessionID = "skill-promote-2"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1", UserEmail: "u@acme.com"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	env.ExecStdoutByCmd = map[string][]byte{"tar -cf - -C /workspace/.hoisted-skills/x .": buildSkillTar(t, map[string]string{"./SKILL.md": "y"})}
	r.onSkillHoisted(ctx, events.QueryContext{SessionID: sessionID}, events.Envelope{
		Type: events.SkillHoisted,
		Data: map[string]any{"artifactPath": ".hoisted-skills/x", "name": "x", "visibility": "public"},
	})
	if len(cat.calls) != 1 || cat.calls[0].Visibility != "organizational" {
		t.Fatalf("expected visibility capped to organizational, got %+v", cat.calls)
	}
}
