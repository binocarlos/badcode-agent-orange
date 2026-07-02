package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FilePendingPost turns a worker's draft into a PendingPost on a Needs-Human
// ticket. This is the ONLY way a Post enters the approval queue. The caller (the
// manager exchange / ResultSink) does not hold a Connector, so filing a draft
// cannot publish — publishing happens later, only via ApprovalService.Approve.
func FilePendingPost(ctx context.Context, ts TicketStore, ticketID string, p Post) error {
	t, err := ts.Get(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("file pending post %s: %w", ticketID, err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("file pending post %s: marshal: %w", ticketID, err)
	}
	t.PendingPost = body
	// PendingPost bookkeeping owns this lane set (the §10c §D exception): a filed
	// post IS the needs_human state, whatever lane the draft came from.
	t.Status = StatusNeedsHuman
	t.UpdatedAt = time.Now().Unix()
	return ts.Update(ctx, t)
}

// ApprovalService is the SOLE holder of a Connector in the orchestrator. The
// un-bypassable publish gate (contracts §7 floor #3): Connector.Publish is
// reachable only through Approve, which runs only on an explicit human action.
type ApprovalService struct {
	// mu serializes Approve/Reject/Answer within this process. Each is a
	// get-check-act sequence over the ticket, so two concurrent Approves for the
	// same ticket would BOTH see the PendingPost and BOTH publish — the
	// exactly-once gate must not depend on caller timing. Process-local
	// serialization is the single-box v1 answer; cross-process idempotency rides
	// on Post.DedupeKey (= the ticket id, §10b E-5) at the Connector, which must
	// treat a repeated key as already-published.
	mu sync.Mutex

	tickets   TicketStore
	connector Connector // unexported: nothing outside this type can reach Publish
	tel       Telemetry // §10b E-1: ctx+error interface (not *Telemetry)

	// MaxAttempts is the retry cap Reject counts against (§10c §D uniform
	// attempts accounting). 0 means DefaultMaxAttempts; set it to match
	// ManagerExchange.MaxAttempts when that is configured.
	MaxAttempts int
}

// NewApprovalService is the ONLY constructor that accepts a Connector.
func NewApprovalService(ts TicketStore, c Connector, tel Telemetry) *ApprovalService {
	return &ApprovalService{tickets: ts, connector: c, tel: tel}
}

// Approve publishes the ticket's PendingPost via the Connector EXACTLY ONCE, then
// moves the ticket to Done and clears the pending post. It errors if the ticket
// has no pending post (guards double-publish). If the Connector fails, the ticket
// is left Needs-Human with its pending post intact so the operator can retry.
func (a *ApprovalService) Approve(ctx context.Context, ticketID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.tickets.Get(ctx, ticketID)
	if err != nil {
		return "", fmt.Errorf("approve %s: %w", ticketID, err)
	}
	if t.Status != StatusNeedsHuman || len(t.PendingPost) == 0 {
		return "", fmt.Errorf("approve %s: no pending post to publish", ticketID)
	}
	var p Post
	if err := json.Unmarshal(t.PendingPost, &p); err != nil {
		return "", fmt.Errorf("approve %s: decode pending post: %w", ticketID, err)
	}
	// §10b E-5: the ticket id is the idempotency key so a redelivered approval /
	// publish retry can never double-post on the real channel.
	p.DedupeKey = ticketID

	ref, err := a.connector.Publish(ctx, p) // the ONE call site of Connector.Publish
	if err != nil {
		return "", fmt.Errorf("approve %s: publish: %w", ticketID, err) // ticket unchanged → retryable
	}

	// §10b E-4: persist the channel's returned ref as attribution before → Done.
	t.PublishedRef = ref
	t.PendingPost = nil
	Transition(&t, EvApproved, "", a.MaxAttempts) // §10c §D: needs_human → done
	if err := a.tickets.Update(ctx, t); err != nil {
		return "", fmt.Errorf("approve %s: persist done: %w", ticketID, err)
	}
	if a.tel != nil {
		if _, err := a.tel.Record(ctx, Run{Scope: "approve", BoardRevision: t.BoardRev, Output: "published " + ref}); err != nil {
			return "", fmt.Errorf("approve %s: telemetry: %w", ticketID, err)
		}
	}
	return ref, nil
}

// Reject clears the PendingPost WITHOUT publishing and returns the human's note
// as HumanFeedback targeting the ticket (fed to write_fragment by Slice E's
// /api/feedback — Reject itself does not edit guidance; Contract gap G4). §10c
// §D: the lane move routes through Transition (EvRejected), which increments
// Attempts, appends the note to AttemptNotes (so the next draft sees it), and at
// the cap keeps the ticket needs_human instead of re-queuing — the same uniform
// accounting as a verify failure. Reject never touches a.connector.
func (a *ApprovalService) Reject(ctx context.Context, ticketID, note string) (HumanFeedback, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.tickets.Get(ctx, ticketID)
	if err != nil {
		return HumanFeedback{}, fmt.Errorf("reject %s: %w", ticketID, err)
	}
	if t.Status != StatusNeedsHuman || len(t.PendingPost) == 0 {
		return HumanFeedback{}, fmt.Errorf("reject %s: no pending post", ticketID)
	}
	t.PendingPost = nil
	Transition(&t, EvRejected, note, a.MaxAttempts)
	if err := a.tickets.Update(ctx, t); err != nil {
		return HumanFeedback{}, fmt.Errorf("reject %s: persist: %w", ticketID, err)
	}
	return HumanFeedback{TargetRef: "ticket:" + ticketID, Note: note}, nil
}

// Answer resumes an ESCALATED ticket (§10c §E — the escalation is answerable,
// not a roach motel): valid only on a needs_human ticket WITHOUT a PendingPost.
// If a PendingPost exists the operator must approve or reject instead — answering
// a drafted post is not reject-with-guidance. Applies EvAnswered: the text lands
// in AttemptNotes (the retry prompt feed), the stale escalation Result clears,
// and the ticket returns to todo with NO attempt increment (an answer is new
// information, not a failure).
func (a *ApprovalService) Answer(ctx context.Context, ticketID, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("answer %s: empty text", ticketID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.tickets.Get(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("answer %s: %w", ticketID, err)
	}
	if t.Status != StatusNeedsHuman {
		return fmt.Errorf("answer %s: ticket is %q, not needs_human", ticketID, t.Status)
	}
	if len(t.PendingPost) > 0 {
		return fmt.Errorf("answer %s: ticket has a pending post — approve or reject it instead", ticketID)
	}
	Transition(&t, EvAnswered, text, a.MaxAttempts)
	t.Result = nil // the escalation question is consumed; the next run starts clean
	if err := a.tickets.Update(ctx, t); err != nil {
		return fmt.Errorf("answer %s: persist: %w", ticketID, err)
	}
	return nil
}
