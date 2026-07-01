package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
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
	t.Status = StatusNeedsHuman
	return ts.Update(ctx, t)
}

// ApprovalService is the SOLE holder of a Connector in the orchestrator. The
// un-bypassable publish gate (contracts §7 floor #3): Connector.Publish is
// reachable only through Approve, which runs only on an explicit human action.
type ApprovalService struct {
	tickets   TicketStore
	connector Connector // unexported: nothing outside this type can reach Publish
	tel       Telemetry // §10b E-1: ctx+error interface (not *Telemetry)
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
	t.Status = StatusDone
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
// /api/feedback — Reject itself does not edit guidance; Contract gap G4). The
// ticket returns to Todo for a re-draft on the next tick. Reject never touches
// a.connector.
func (a *ApprovalService) Reject(ctx context.Context, ticketID, note string) (HumanFeedback, error) {
	t, err := a.tickets.Get(ctx, ticketID)
	if err != nil {
		return HumanFeedback{}, fmt.Errorf("reject %s: %w", ticketID, err)
	}
	if t.Status != StatusNeedsHuman || len(t.PendingPost) == 0 {
		return HumanFeedback{}, fmt.Errorf("reject %s: no pending post", ticketID)
	}
	t.PendingPost = nil
	t.Status = StatusTodo
	if err := a.tickets.Update(ctx, t); err != nil {
		return HumanFeedback{}, fmt.Errorf("reject %s: persist: %w", ticketID, err)
	}
	return HumanFeedback{TargetRef: "ticket:" + ticketID, Note: note}, nil
}
