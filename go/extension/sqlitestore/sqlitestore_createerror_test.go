package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// TestSessionCreateErrorRoundTripsAndClears: the fallback store must carry the
// reason a session failed to start, or the standalone stack on sqlite is back to
// "no running instance and no snapshot — session must be re-created" for every
// failure — the defect the column exists to close.
//
// Clearing is the half that is easy to get wrong here: every other text column
// in this upsert is CASE-guarded so an empty value does not overwrite a stored
// one. create_error must NOT be, because a create that finally succeeds has to
// wipe the reason it failed before — a diagnostic that outlives its cause is
// worse than none.
func TestSessionCreateErrorRoundTripsAndClears(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	const reason = `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") ` +
		"names no image in the §13 catalogue, so it was used as a literal registry reference " +
		"and that reference failed: pull access denied"

	if _, err := st.UpdateSession(ctx, &agentdb.Session{
		ID: "s1", Customer: "acme", Status: "error", CreateError: reason,
	}); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	got, err := st.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreateError != reason {
		t.Fatalf("create_error did not round-trip: %q", got.CreateError)
	}

	if _, err := st.UpdateSession(ctx, &agentdb.Session{
		ID: "s1", Customer: "acme", Status: "running", CreateError: "",
	}); err != nil {
		t.Fatalf("UpdateSession (clear): %v", err)
	}
	got, err = st.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreateError != "" {
		t.Fatalf("a successful create must clear the reason, got %q", got.CreateError)
	}
}
