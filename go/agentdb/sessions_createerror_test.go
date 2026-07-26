package agentdb

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestLivePG_SessionCreateErrorRoundTrips pins migration 032's column and the
// one property the whole fix rests on: the reason must be CLEARABLE.
//
// A gorm `default:` tag on CreateError would make GORM substitute the default
// for the zero value on write, so a session that failed once and then started
// cleanly would carry its old reason for ever — a diagnostic that outlived its
// cause, which is worse than the silence it replaced. The DEFAULT lives in the
// migration SQL instead; this test is what fails if anyone adds the tag back.
func TestLivePG_SessionCreateErrorRoundTrips(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "a@b.c")

	// A fresh row starts empty (the migration's DEFAULT '').
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreateError != "" {
		t.Fatalf("a fresh session must have no create_error, got %q", got.CreateError)
	}

	const reason = `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") ` +
		"names no image in the §13 catalogue, so it was used as a literal registry reference " +
		"and that reference failed: pull access denied"
	got.CreateError = reason
	if _, err := s.UpdateSession(ctx, got); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	got, err = s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreateError != reason {
		t.Fatalf("create_error did not round-trip: %q", got.CreateError)
	}

	// …and clears again when the session finally starts.
	got.CreateError = ""
	if _, err := s.UpdateSession(ctx, got); err != nil {
		t.Fatalf("UpdateSession (clear): %v", err)
	}
	got, err = s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreateError != "" {
		t.Fatalf("a successful create must be able to clear the reason, got %q", got.CreateError)
	}
}
