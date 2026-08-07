package agentdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newAttentionStore returns a sqlite Store with the attention table plus the
// session and message tables the request path stamps and the sweep reads.
func newAttentionStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "attention_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&AttentionRequest{}, &Session{}, &Message{}, &ProjectEvent{}); err != nil {
		t.Fatalf("automigrate attention tables: %v", err)
	}
	return &Store{gdb: db}
}

func seedAttentionSession(t *testing.T, s *Store, id, project string) *Session {
	t.Helper()
	sess := &Session{ID: id, Customer: project, UserEmail: "u@x.com", WorkflowID: "agent", Worker: "tweet-author"}
	if err := s.gdb.Create(sess).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sess
}

func TestAttentionRequestStampsTheSession(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")

	req, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: "acme", SessionID: "s-1", Worker: "tweet-author",
		Message: "sign off on this draft", SessionURL: "http://localhost:8080/p/acme/s/s-1",
		Channel: "webhook", Delivered: true, ExpiresAt: 1000,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if req.ID == "" {
		t.Fatalf("create must allocate an id")
	}

	// The §9 stamp landed on the session in the same transaction.
	var sess Session
	if err := s.gdb.Where("id = ?", "s-1").First(&sess).Error; err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !sess.AttentionRequested {
		t.Fatalf("the session must be stamped attention_requested")
	}

	open, err := s.ListOpenAttentionRequests(ctx, "acme")
	if err != nil || len(open) != 1 {
		t.Fatalf("open list: %d rows err=%v", len(open), err)
	}

	// Resolving clears the stamp.
	if err := s.MarkAttentionAnswered(ctx, req.ID, 2000); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := s.gdb.Where("id = ?", "s-1").First(&sess).Error; err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if sess.AttentionRequested {
		t.Fatalf("answering must clear the session stamp")
	}
	open, _ = s.ListOpenAttentionRequests(ctx, "acme")
	if len(open) != 0 {
		t.Fatalf("an answered request is not open: %+v", open)
	}

	// Resolution is idempotent: a second sweep pass changes nothing.
	if err := s.MarkAttentionTimedOut(ctx, req.ID, 3000); err != nil {
		t.Fatalf("second resolution must be a no-op: %v", err)
	}
	got, err := s.GetAttentionRequest(ctx, "acme", req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AnsweredAt != 2000 || got.TimedOutAt != 0 {
		t.Fatalf("a resolved request must not be re-resolved: %+v", got)
	}
}

func TestAttentionRequestValidation(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		req  *AttentionRequest
	}{
		{"nil", nil},
		{"no project", &AttentionRequest{SessionID: "s", Message: "m"}},
		{"no session", &AttentionRequest{Project: "acme", Message: "m"}},
		{"no message", &AttentionRequest{Project: "acme", SessionID: "s"}},
		{"negative expiry", &AttentionRequest{Project: "acme", SessionID: "s", Message: "m", ExpiresAt: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateAttentionRequest(ctx, tc.req); err == nil {
				t.Fatalf("expected a refusal")
			}
		})
	}
}

func TestAttentionExpiryListOnlyCoversDeadlinedRequests(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")
	seedAttentionSession(t, s, "s-2", "acme")
	seedAttentionSession(t, s, "s-3", "globex")

	// No deadline: waits forever by design (§9 — expires_in is optional).
	if _, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: "acme", SessionID: "s-1", Message: "no deadline",
	}); err != nil {
		t.Fatalf("seed open: %v", err)
	}
	lapsed, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: "acme", SessionID: "s-2", Message: "lapsed", ExpiresAt: 100,
	})
	if err != nil {
		t.Fatalf("seed lapsed: %v", err)
	}
	// Another project's lapsed request: the sweep is core, so it sees it too.
	if _, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: "globex", SessionID: "s-3", Message: "theirs", ExpiresAt: 100,
	}); err != nil {
		t.Fatalf("seed globex: %v", err)
	}

	// Before the deadline: nothing lapsed.
	due, err := s.ListExpiredAttentionRequests(ctx, 99, 0)
	if err != nil || len(due) != 0 {
		t.Fatalf("before the deadline: %d rows err=%v", len(due), err)
	}
	// After: exactly the two with deadlines, never the deadline-free one.
	due, err = s.ListExpiredAttentionRequests(ctx, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("expected two lapsed requests, got %d: %+v", len(due), due)
	}
	for _, r := range due {
		if r.ExpiresAt == 0 {
			t.Fatalf("a request with no deadline must never lapse: %+v", r)
		}
	}

	// Resolving one takes it out of the sweep.
	if err := s.MarkAttentionTimedOut(ctx, lapsed.ID, 101); err != nil {
		t.Fatalf("time out: %v", err)
	}
	due, _ = s.ListExpiredAttentionRequests(ctx, 200, 0)
	if len(due) != 1 || due[0].Project != "globex" {
		t.Fatalf("a resolved request must leave the sweep: %+v", due)
	}
}

func TestAttentionProjectIsolation(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")
	seedAttentionSession(t, s, "s-2", "globex")

	mine, err := s.CreateAttentionRequest(ctx, &AttentionRequest{Project: "acme", SessionID: "s-1", Message: "mine"})
	if err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	theirs, err := s.CreateAttentionRequest(ctx, &AttentionRequest{Project: "globex", SessionID: "s-2", Message: "theirs"})
	if err != nil {
		t.Fatalf("seed globex: %v", err)
	}

	if _, err := s.GetAttentionRequest(ctx, "acme", theirs.ID); !errors.Is(err, ErrAttentionRequestNotFound) {
		t.Fatalf("cross-project read: want not-found, got %v", err)
	}
	open, err := s.ListOpenAttentionRequests(ctx, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 || open[0].ID != mine.ID {
		t.Fatalf("list leaked another project's rows: %+v", open)
	}
}

func TestAttentionUserReplyDetection(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")

	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: "s-1", Role: "user", Content: "before", CreatedAt: 50, SequenceNum: 1},
		{SessionID: "s-1", Role: "assistant", Content: "draft", CreatedAt: 150, SequenceNum: 2},
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	// The assistant's own turn is not an answer, and neither is a human turn from
	// before the request.
	n, err := s.CountUserMessagesSince(ctx, "s-1", 100)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no human reply after the request, got %d", n)
	}

	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: "s-1", Role: "user", Content: "post it", CreatedAt: 200, SequenceNum: 3},
	}); err != nil {
		t.Fatalf("seed reply: %v", err)
	}
	n, err = s.CountUserMessagesSince(ctx, "s-1", 100)
	if err != nil || n != 1 {
		t.Fatalf("expected one human reply, got %d err=%v", n, err)
	}
}

func TestAttentionSessionStampHelper(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")

	if err := s.SetSessionAttentionRequested(ctx, "s-1", true); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	var sess Session
	if err := s.gdb.Where("id = ?", "s-1").First(&sess).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if !sess.AttentionRequested {
		t.Fatalf("stamp did not stick")
	}
	// Clearing must work too: the flag carries no gorm default, so false writes.
	if err := s.SetSessionAttentionRequested(ctx, "s-1", false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.gdb.Where("id = ?", "s-1").First(&sess).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if sess.AttentionRequested {
		t.Fatalf("clearing the stamp must stick (a gorm default: tag would break this)")
	}
	if err := s.SetSessionAttentionRequested(ctx, "nope", true); err == nil {
		t.Fatalf("stamping an unknown session must fail loudly")
	}
}

// TestSessionAwaitsHuman pins the read the §8.4 dispatch gate parks a delivery
// on. It is deliberately NOT the session's per-turn `attention_requested`
// column — that one is cleared as soon as the §8.2 emitter has copied it onto a
// `worker.finished` envelope, so by the time a job settles it says nothing about
// whether a human has replied. The open rows of `attention_requests` are the
// durable fact.
func TestSessionAwaitsHuman(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")
	seedAttentionSession(t, s, "s-2", "acme")

	awaits, err := s.SessionAwaitsHuman(ctx, "acme", "s-1")
	if err != nil || awaits {
		t.Fatalf("a session with no request awaits nobody: %v err=%v", awaits, err)
	}

	req, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: "acme", SessionID: "s-1", Worker: "tweet-author",
		Message: "sign off on this draft", Channel: "webhook", Delivered: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if awaits, err = s.SessionAwaitsHuman(ctx, "acme", "s-1"); err != nil || !awaits {
		t.Fatalf("an open request means the session awaits a human: %v err=%v", awaits, err)
	}
	// The per-turn stamp being cleared (what the emitter does) must NOT change
	// the answer — that is the whole reason this read exists.
	if err := s.SetSessionAttentionRequested(ctx, "s-1", false); err != nil {
		t.Fatalf("clear stamp: %v", err)
	}
	if awaits, err = s.SessionAwaitsHuman(ctx, "acme", "s-1"); err != nil || !awaits {
		t.Fatalf("clearing the per-turn stamp must not close the request: %v err=%v", awaits, err)
	}

	// Tenancy: another project's session looks like no request at all.
	if awaits, err = s.SessionAwaitsHuman(ctx, "other-co", "s-1"); err != nil || awaits {
		t.Fatalf("cross-project read leaked: %v err=%v", awaits, err)
	}
	// A sibling session is unaffected.
	if awaits, err = s.SessionAwaitsHuman(ctx, "acme", "s-2"); err != nil || awaits {
		t.Fatalf("the request must be scoped to its own session: %v err=%v", awaits, err)
	}

	// Resolving closes it.
	if err := s.MarkAttentionAnswered(ctx, req.ID, 2000); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if awaits, err = s.SessionAwaitsHuman(ctx, "acme", "s-1"); err != nil || awaits {
		t.Fatalf("an answered request is not outstanding: %v err=%v", awaits, err)
	}

	if _, err := s.SessionAwaitsHuman(ctx, "", "s-1"); err == nil {
		t.Fatalf("an unscoped read must be refused")
	}
	if _, err := s.SessionAwaitsHuman(ctx, "acme", ""); err == nil {
		t.Fatalf("a read with no session must be refused")
	}
}

// The read variant behind GET /agent/attention-requests: open-only by default,
// resolved rows included on request, capped by Limit, scoped to one project.
func TestListAttentionRequests(t *testing.T) {
	s := newAttentionStore(t)
	ctx := context.Background()
	seedAttentionSession(t, s, "s-1", "acme")
	seedAttentionSession(t, s, "s-2", "acme")
	seedAttentionSession(t, s, "s-3", "other")

	mk := func(id, project, session string, createdAt int64) *AttentionRequest {
		t.Helper()
		req, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
			ID: id, Project: project, SessionID: session, Worker: "tweet-author",
			Message: "which draft?", CreatedAt: createdAt,
		})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return req
	}
	mk("a", "acme", "s-1", 1000)
	answered := mk("b", "acme", "s-2", 2000)
	mk("c", "other", "s-3", 3000)
	if err := s.MarkAttentionAnswered(ctx, answered.ID, 2500); err != nil {
		t.Fatalf("answer: %v", err)
	}

	for _, tc := range []struct {
		name string
		q    AttentionRequestQuery
		want []string
	}{
		{"open only by default", AttentionRequestQuery{Project: "acme"}, []string{"a"}},
		{"resolved rows on request", AttentionRequestQuery{Project: "acme", IncludeResolved: true}, []string{"b", "a"}},
		{"limit caps the page", AttentionRequestQuery{Project: "acme", IncludeResolved: true, Limit: 1}, []string{"b"}},
		{"another project's rows stay invisible", AttentionRequestQuery{Project: "acme", IncludeResolved: true, Limit: 10}, []string{"b", "a"}},
		{"the other project sees its own", AttentionRequestQuery{Project: "other", IncludeResolved: true}, []string{"c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListAttentionRequests(ctx, tc.q)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			ids := []string{}
			for _, r := range got {
				ids = append(ids, r.ID)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("ids=%v want %v", ids, tc.want)
			}
			for i := range ids {
				if ids[i] != tc.want[i] {
					t.Fatalf("ids=%v want %v (newest-first)", ids, tc.want)
				}
			}
		})
	}

	if _, err := s.ListAttentionRequests(ctx, AttentionRequestQuery{}); err == nil {
		t.Fatalf("an unscoped list must be refused")
	}
}
