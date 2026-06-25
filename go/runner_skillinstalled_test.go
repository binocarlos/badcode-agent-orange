package agentkit

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
)

func TestOnSkillInstalled_RecordsSessionMetadata(t *testing.T) {
	r, _, _, store, _, _ := newTestRunner(t)
	ctx := context.Background()

	store.Seed(&agentdb.Session{ID: "sess-1", Customer: "acme", UserEmail: "u@x"})

	fire := func(id, name string) {
		r.onSkillInstalled(ctx, events.QueryContext{SessionID: "sess-1"}, events.Envelope{
			Type: events.SkillInstalled, Data: map[string]any{"id": id, "name": name},
		})
	}
	fire("s1", "hello")
	fire("s2", "hello") // dedupe by name, latest wins
	fire("s3", "other")

	sess, _ := store.GetSession(ctx, "sess-1")
	raw, _ := sess.Metadata["installed_skills"].([]any)
	if len(raw) != 2 {
		t.Fatalf("want 2 installed skills, got %d: %+v", len(raw), raw)
	}
	byName := map[string]string{}
	for _, e := range raw {
		m := e.(map[string]any)
		byName[m["name"].(string)] = m["id"].(string)
	}
	if byName["hello"] != "s2" || byName["other"] != "s3" {
		t.Fatalf("dedupe wrong: %+v", byName)
	}
}
