# Slice D — Connector + the Publish-Approval Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the single most important v1 safety property — *publishing is un-bypassable*. A worker
scope drafts content; that draft becomes a `PendingPost` on a **Needs-Human** ticket; a human
**approve** action is the ONLY code path that ever calls `Connector.Publish`; **reject** carries an
optional note that becomes `HumanFeedback` and never publishes. The `Connector` seam ships with a
deterministic **fake** for offline tests and ONE real, thin channel adapter left **parameterized**
(the social channel is deferred — deployment-plan Open Decision #1). It is made **structurally
impossible** for any worker scope to reach `Connector.Publish`, and a test asserts that boundary.

**Architecture:** New code in the existing `go/orchestrator` package (contracts §0), on top of
Slice 0's `MemBoard`/`Telemetry` and Slice A's `Ticket`/`TicketStore`. Slice D **produces** the
`Post` type, the `Connector` seam, a `FakeConnector`, a parameterized `ChannelConnector` (network
code isolated to one file, with a `TODO(channel)` for the chosen SDK/HTTP), the closed **worker tool
surface** (`WorkerToolset`, which excludes publishing), and an `ApprovalService` that is the **sole
holder** of a `Connector`. The gate is enforced in mechanism: the worker path (`Runner`) has no
`Connector` field; only `NewApprovalService` accepts one; only `Approve` calls `Publish`.

**Tech Stack:** Go 1.25, standard library only (`context`, `encoding/json`, `errors`, `fmt`,
`net/http`, `reflect`, `sync`). No external deps in this slice — the chosen channel's SDK arrives with
the real adapter's `TODO(channel)`, not before. Consumes contract types verbatim (contracts §3–§5);
tests are fully offline via the `FakeConnector`.

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; all new code under `go/orchestrator/`. One line.
- `go build ./...` and `go vet ./...` must stay green. One line.
- **Consume contract types verbatim** (contracts §3 `TicketStatus`, §4 `Ticket`/`Post`/`HumanFeedback`, §5 `Connector`/`TicketStore`) — never redefine or renegotiate; a needed change is stop-and-escalate. One line.
- **No external dependencies** in Slice D — stdlib only; the chosen channel SDK lands behind the `ChannelConnector` `TODO(channel)`, isolated to one file. One line.
- **All tests are offline and deterministic** via the `FakeConnector` — nothing touches the network in tests; the real `ChannelConnector` is never exercised against a live channel here. One line.
- **The publish-approval floor (contracts §7 #3) is enforced in mechanism, non-editable:** `Connector.Publish` is reachable ONLY from the approval action; a worker scope cannot publish. This slice's keystone. One line.
- **Secrets never touch the board/fragments:** the channel endpoint + token are injected into `ChannelConnector` (Secret Manager in Slice F), never read from versioned content. One line.
- TDD: failing test first, minimal impl, run to green, frequent commits. One line.
- Deterministic ids in tests/doubles (fake refs `fake://post/<n>`, ticket ids `t1`,`t2`,…) — no uuid/time/random. One line.

**Prerequisite (see Contract gaps G1):** Slice A's frozen shared types must exist in package
`orchestrator` before Slice D compiles: `TicketStatus` + its consts (contracts §3), `Ticket`
(contracts §4), and the `TicketStore` interface (contracts §5). If executing Slice D standalone
before Slice A, first land those declarations copied **verbatim** from the contracts; this plan
consumes them and does not redeclare them.

---

## File Structure

```
go/orchestrator/
  connector.go              # Post type + Connector seam + FakeConnector           (NEW — Task 1)
  connector_test.go         #                                                       (NEW — Task 1)
  channel_connector.go      # ChannelConnector — one real channel, parameterized    (NEW — Task 2)
  channel_connector_test.go #                                                       (NEW — Task 2)
  workertools.go            # WorkerToolset — the closed worker syscall surface     (NEW — Task 3)
  workertools_test.go       #                                                       (NEW — Task 3)
  approval.go               # FilePendingPost + ApprovalService (Approve/Reject)    (NEW — Task 4)
  approval_test.go          # + fakeTicketStore test double                         (NEW — Task 4)
  boundary_test.go          # the C2 structural floor: worker cannot reach Publish  (NEW — Task 5)
go/examples/approvalgate/
  main.go                   # human-watchable draft→pending→approve/reject demo     (NEW — Task 6)
```

---

### Task 1: The `Post` type, the `Connector` seam, and a `FakeConnector`

**Files:**
- Create: `go/orchestrator/connector.go`
- Test: `go/orchestrator/connector_test.go`

**Interfaces:**
- Consumes: nothing new (stdlib `context`).
- Produces: `type Post struct{ Channel string; Text string; Media []string }` (contracts §4, verbatim);
  `type Connector interface { Publish(ctx, Post) (ref string, err error) }` (contracts §5, verbatim);
  `type FakeConnector struct { Published []Post; Ref string; Err error; Calls int }` implementing
  `Connector` — records "would publish", returns `Ref` (default `fake://post/<n>`), and fails with
  `Err` when set. The offline test/dev double.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestFakeConnectorRecordsAndCanFail(t *testing.T) {
	ctx := context.Background()

	// Happy path: records the post and returns a deterministic ref.
	f := &FakeConnector{}
	ref, err := f.Publish(ctx, Post{Channel: "demo", Text: "hello world"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ref != "fake://post/1" {
		t.Fatalf("ref = %q, want fake://post/1", ref)
	}
	if len(f.Published) != 1 || f.Published[0].Text != "hello world" || f.Calls != 1 {
		t.Fatalf("did not record would-publish: %+v calls=%d", f.Published, f.Calls)
	}

	// Failure path: configured error surfaces, nothing recorded, call still counted.
	boom := errors.New("channel down")
	fail := &FakeConnector{Err: boom}
	if _, err := fail.Publish(ctx, Post{Channel: "demo", Text: "x"}); !errors.Is(err, boom) {
		t.Fatalf("expected configured error, got %v", err)
	}
	if len(fail.Published) != 0 || fail.Calls != 1 {
		t.Fatalf("failed publish must not record: %+v calls=%d", fail.Published, fail.Calls)
	}

	// It satisfies the Connector seam.
	var _ Connector = &FakeConnector{}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestFakeConnectorRecordsAndCanFail -v`
Expected: FAIL — `undefined: FakeConnector` / `undefined: Connector` / `undefined: Post`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
)

// Post — a unit of content a Connector publishes (contracts §4, verbatim).
// Media is empty for v1 (text-only); it exists for forward-compatibility.
type Post struct {
	Channel string
	Text    string
	Media   []string // paths/urls; empty for v1 text-only
}

// Connector — publishes a Post to a real channel (contracts §5, verbatim). The
// ONLY network-to-the-world seam. Invoked EXCLUSIVELY by the approval flow
// (Task 4) — never by a worker scope directly (contracts §7 floor #3).
type Connector interface {
	Publish(ctx context.Context, p Post) (ref string, err error)
}

// FakeConnector is the deterministic, offline Connector double: it records what
// it "would publish", can be made to fail, and never touches the network.
type FakeConnector struct {
	Published []Post // recorded would-publish posts (successful calls only)
	Ref       string // ref to return; default "fake://post/<n>"
	Err       error  // when set, Publish fails and records nothing
	Calls     int    // total Publish invocations (success + failure)
}

func (f *FakeConnector) Publish(_ context.Context, p Post) (string, error) {
	f.Calls++
	if f.Err != nil {
		return "", f.Err
	}
	f.Published = append(f.Published, p)
	ref := f.Ref
	if ref == "" {
		ref = fmt.Sprintf("fake://post/%d", f.Calls)
	}
	return ref, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestFakeConnectorRecordsAndCanFail -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/connector.go go/orchestrator/connector_test.go
git commit -m "feat(orchestrator): Connector seam + Post + deterministic FakeConnector"
```

---

### Task 2: The one real channel adapter (parameterized, channel deferred)

**Files:**
- Create: `go/orchestrator/channel_connector.go`
- Test: `go/orchestrator/channel_connector_test.go`

**Interfaces:**
- Consumes: `Post`, `Connector` (Task 1); stdlib `net/http`, `errors`.
- Produces: `type ChannelConnector struct { Endpoint, Token string; HTTP *http.Client }` implementing
  `Connector`; `func NewChannelConnector(endpoint, token string) *ChannelConnector`;
  `var ErrChannelTODO error`. This is the ONLY network-touching type in the slice; the specific
  channel's request/auth is a single `TODO(channel)`. Unconfigured (empty endpoint/token) → config
  error; configured → `ErrChannelTODO` until the channel is chosen. No network in tests.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestChannelConnectorParameterizedAndDeferred(t *testing.T) {
	ctx := context.Background()

	// Unconfigured → a clear config error (never a silent no-op, never a nil-panic).
	empty := NewChannelConnector("", "")
	if _, err := empty.Publish(ctx, Post{Channel: "x", Text: "hi"}); err == nil {
		t.Fatalf("unconfigured connector must error")
	}

	// Configured (endpoint+token injected — NOT from the board) → the channel is
	// deferred, so it fails loud with ErrChannelTODO rather than half-publishing.
	c := NewChannelConnector("https://example.invalid/api", "secret-token")
	if c.HTTP == nil {
		t.Fatalf("expected a default HTTP client")
	}
	if _, err := c.Publish(ctx, Post{Channel: "x", Text: "hi"}); !errors.Is(err, ErrChannelTODO) {
		t.Fatalf("expected ErrChannelTODO, got %v", err)
	}

	// It satisfies the Connector seam and is a *http.Client host (isolation check).
	var _ Connector = &ChannelConnector{}
	var _ *http.Client = c.HTTP
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestChannelConnectorParameterizedAndDeferred -v`
Expected: FAIL — `undefined: NewChannelConnector` / `undefined: ErrChannelTODO`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrChannelTODO fails loud until the v1 social channel is chosen
// (deployment-plan Open Decision #1: Bluesky / Mastodon are the low-friction
// candidates). Choosing it is the only work left in this file.
var ErrChannelTODO = errors.New("channel connector: target channel not yet chosen (deployment-plan Open Decision #1)")

// ChannelConnector is the ONE real Connector: a thin, isolated adapter to a
// single social channel. ALL network code in the slice lives here and nowhere
// else. It is parameterized by Endpoint + Token (injected from Secret Manager in
// Slice F — NEVER read from the board/fragments, which are versioned content).
// The specific platform is deferred: no channel is hardcoded.
type ChannelConnector struct {
	Endpoint string       // channel API base
	Token    string       // channel credential (secret; injected)
	HTTP     *http.Client // reused; overridable in tests
}

// NewChannelConnector builds the adapter with a sane default HTTP client.
func NewChannelConnector(endpoint, token string) *ChannelConnector {
	return &ChannelConnector{
		Endpoint: endpoint,
		Token:    token,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Publish sends one Post to the configured channel and returns the channel's
// post id/url as ref.
func (c *ChannelConnector) Publish(ctx context.Context, p Post) (string, error) {
	if c.Endpoint == "" || c.Token == "" {
		return "", fmt.Errorf("channel connector not configured (endpoint/token must be injected)")
	}
	// TODO(channel): once Open Decision #1 is made, implement the chosen channel's
	// create-post call here and ONLY here:
	//   1. marshal p.Text (v1 is text-only; p.Media deferred — Contract gap G6)
	//      into the channel's create-post request body;
	//   2. authenticate with c.Token (bearer / app-password / OAuth per channel);
	//   3. POST to c.Endpoint via c.HTTP with the ctx;
	//   4. parse the response and return the new post's id/url as ref;
	//   5. treat a non-2xx as an error (the approval flow keeps the ticket
	//      Needs-Human for retry — see Task 4).
	// Idempotency: the channel has no dedup key for us (Contract gap G5) — rely on
	// the ticket-state guard in ApprovalService and, if the channel offers one,
	// pass an idempotency key here.
	_ = ctx
	return "", ErrChannelTODO
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestChannelConnectorParameterizedAndDeferred -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/channel_connector.go go/orchestrator/channel_connector_test.go
git commit -m "feat(orchestrator): parameterized ChannelConnector (real channel deferred, isolated)"
```

---

### Task 3: The closed worker tool surface (publishing is not a worker tool)

**Files:**
- Create: `go/orchestrator/workertools.go`
- Test: `go/orchestrator/workertools_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type WorkerSyscall string` + consts `SyscallJobFinished`, `SyscallEscalateToHuman`
  (contracts §6, the reduced worker surface); `func WorkerToolset() map[WorkerSyscall]bool` — the
  **complete, closed** set of tools a worker scope may invoke; `func IsWorkerTool(name string) bool`.
  Publishing is deliberately absent — this is the first half of the C2 floor: the worker surface has
  no publish tool to grant.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import "testing"

func TestWorkerToolsetExcludesPublishing(t *testing.T) {
	set := WorkerToolset()

	// The worker surface is exactly the two thread syscalls (contracts §6).
	if len(set) != 2 || !set[SyscallJobFinished] || !set[SyscallEscalateToHuman] {
		t.Fatalf("worker surface is not the frozen §6 set: %+v", set)
	}

	// Publishing is NOT a worker tool, under any plausible name (contracts §7 #3).
	for _, name := range []string{"publish", "Publish", "connector", "connector.publish", "post"} {
		if IsWorkerTool(name) {
			t.Fatalf("%q is reachable as a worker tool — the publish gate is bypassable", name)
		}
	}

	// The map is a copy: mutating it cannot smuggle a tool into the real surface.
	set["publish"] = true
	if IsWorkerTool("publish") {
		t.Fatalf("WorkerToolset must return a fresh copy each call")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestWorkerToolsetExcludesPublishing -v`
Expected: FAIL — `undefined: WorkerToolset`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

// WorkerSyscall names a tool a worker scope may invoke. The worker surface is
// deliberately reduced (contracts §6): only the thread syscalls. Publishing is
// NOT here — a worker has no publish tool to call (contracts §7 floor #3).
type WorkerSyscall string

const (
	SyscallJobFinished     WorkerSyscall = "job_finished"       // deliver a Result
	SyscallEscalateToHuman WorkerSyscall = "escalate_to_human"  // raise a Needs-Human ticket
)

// WorkerToolset returns the complete, closed set of syscalls a worker scope may
// invoke. Returned fresh each call so callers cannot mutate the canonical surface.
// There is no publish/Connector entry: publishing is unreachable from here.
func WorkerToolset() map[WorkerSyscall]bool {
	return map[WorkerSyscall]bool{
		SyscallJobFinished:     true,
		SyscallEscalateToHuman: true,
	}
}

// IsWorkerTool reports whether name is an allowed worker syscall.
func IsWorkerTool(name string) bool {
	return WorkerToolset()[WorkerSyscall(name)]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestWorkerToolsetExcludesPublishing -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/workertools.go go/orchestrator/workertools_test.go
git commit -m "feat(orchestrator): closed worker tool surface — no publish tool (C2 floor, part 1)"
```

---

### Task 4: PendingPost lifecycle + `ApprovalService` (Approve is the sole publisher)

**Files:**
- Create: `go/orchestrator/approval.go`
- Test: `go/orchestrator/approval_test.go`

**Interfaces:**
- Consumes: `Connector`, `Post` (Task 1); `TicketStore`, `Ticket`, `TicketStatus` +
  `StatusNeedsHuman`/`StatusDone`/`StatusTodo` (contracts §3–§5, Slice A); `HumanFeedback`
  (contracts §4); `*Telemetry` (Slice 0). stdlib `context`, `encoding/json`, `fmt`.
- Produces:
  - `func FilePendingPost(ctx, ts TicketStore, ticketID string, p Post) error` — a worker's draft
    becomes a `PendingPost` on a Needs-Human ticket. The caller (manager / `ResultSink`) does **not**
    hold a `Connector`; filing a pending post cannot publish.
  - `type ApprovalService struct{ … }` with an **unexported** `connector Connector` field — the sole
    holder of a `Connector` in the whole orchestrator;
    `func NewApprovalService(ts TicketStore, c Connector, tel *Telemetry) *ApprovalService`;
    `func (a *ApprovalService) Approve(ctx, ticketID string) (ref string, err error)` — publishes the
    ticket's `PendingPost` **exactly once**, then → Done (clears PendingPost); errors if no pending
    post (guards double-publish); on Connector failure leaves the ticket Needs-Human (retryable) and
    does not clear;
    `func (a *ApprovalService) Reject(ctx, ticketID, note string) (HumanFeedback, error)` — clears
    the PendingPost **without** publishing, returns `HumanFeedback{TargetRef:"ticket:<id>", Note:note}`,
    and sends the ticket back to Todo for a re-draft. `Reject` never references `a.connector`.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// fakeTicketStore is an in-memory TicketStore double for offline tests. (If Slice
// A ships an in-memory TicketStore, reuse it and delete this helper.)
type fakeTicketStore struct {
	items map[string]Ticket
	seq   int
}

func newFakeTickets() *fakeTicketStore { return &fakeTicketStore{items: map[string]Ticket{}} }

func (s *fakeTicketStore) Create(_ context.Context, t Ticket) (string, error) {
	s.seq++
	t.ID = fmt.Sprintf("t%d", s.seq)
	s.items[t.ID] = t
	return t.ID, nil
}
func (s *fakeTicketStore) Update(_ context.Context, t Ticket) error {
	if _, ok := s.items[t.ID]; !ok {
		return fmt.Errorf("no ticket %s", t.ID)
	}
	s.items[t.ID] = t
	return nil
}
func (s *fakeTicketStore) Get(_ context.Context, id string) (Ticket, error) {
	t, ok := s.items[id]
	if !ok {
		return Ticket{}, fmt.Errorf("no ticket %s", id)
	}
	return t, nil
}
func (s *fakeTicketStore) List(_ context.Context, status TicketStatus) ([]Ticket, error) {
	var out []Ticket
	for _, t := range s.items {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func seedDraftTicket(t *testing.T, ts *fakeTicketStore) string {
	t.Helper()
	id, err := ts.Create(context.Background(), Ticket{Title: "draft a post", Status: StatusInProgress})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return id
}

func TestApproveIsTheSolePublisher(t *testing.T) {
	ctx := context.Background()
	ts := newFakeTickets()
	conn := &FakeConnector{}
	svc := NewApprovalService(ts, conn, NewTelemetry())

	id := seedDraftTicket(t, ts)

	// A worker draft becomes a PendingPost on a Needs-Human ticket. No publish yet.
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "ship it"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}
	pending, _ := ts.Get(ctx, id)
	if pending.Status != StatusNeedsHuman || len(pending.PendingPost) == 0 {
		t.Fatalf("draft did not become a pending post: %+v", pending)
	}
	if conn.Calls != 0 {
		t.Fatalf("filing a pending post must NOT publish (calls=%d)", conn.Calls)
	}

	// Approve publishes exactly once and moves the ticket to Done.
	ref, err := svc.Approve(ctx, id)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 || len(conn.Published) != 1 || conn.Published[0].Text != "ship it" {
		t.Fatalf("approve must publish exactly once: calls=%d published=%+v", conn.Calls, conn.Published)
	}
	if ref == "" {
		t.Fatalf("approve must return the channel ref")
	}
	done, _ := ts.Get(ctx, id)
	if done.Status != StatusDone || len(done.PendingPost) != 0 {
		t.Fatalf("approved ticket must be Done with no pending post: %+v", done)
	}

	// A second approve is a no-op publish (guards double-post).
	if _, err := svc.Approve(ctx, id); err == nil {
		t.Fatalf("re-approving a published ticket must error")
	}
	if conn.Calls != 1 {
		t.Fatalf("double-approve must NOT publish again (calls=%d)", conn.Calls)
	}
}

func TestRejectEmitsFeedbackAndDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	ts := newFakeTickets()
	conn := &FakeConnector{}
	svc := NewApprovalService(ts, conn, NewTelemetry())

	id := seedDraftTicket(t, ts)
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "meh draft"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}

	fb, err := svc.Reject(ctx, id, "too salesy — be wittier")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("reject must NEVER publish (calls=%d)", conn.Calls)
	}
	if fb.TargetRef != "ticket:"+id || fb.Note != "too salesy — be wittier" {
		t.Fatalf("reject must emit HumanFeedback targeting the ticket: %+v", fb)
	}
	after, _ := ts.Get(ctx, id)
	if after.Status != StatusTodo || len(after.PendingPost) != 0 {
		t.Fatalf("rejected ticket must clear the pending post and return to Todo: %+v", after)
	}

	// An empty note is allowed (contracts §8 note is optional).
	id2 := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id2, Post{Channel: "demo", Text: "x"})
	if fb2, err := svc.Reject(ctx, id2, ""); err != nil || fb2.Note != "" {
		t.Fatalf("empty-note reject: fb=%+v err=%v", fb2, err)
	}
}

func TestApproveKeepsTicketPendingOnConnectorFailure(t *testing.T) {
	ctx := context.Background()
	ts := newFakeTickets()
	boom := errors.New("channel 503")
	svc := NewApprovalService(ts, &FakeConnector{Err: boom}, NewTelemetry())

	id := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "retry me"})

	if _, err := svc.Approve(ctx, id); !errors.Is(err, boom) {
		t.Fatalf("expected connector error, got %v", err)
	}
	stuck, _ := ts.Get(ctx, id)
	if stuck.Status != StatusNeedsHuman || len(stuck.PendingPost) == 0 {
		t.Fatalf("failed publish must leave the ticket Needs-Human for retry: %+v", stuck)
	}

	// Sanity: the pending post round-trips (json.RawMessage decode).
	var p Post
	if err := json.Unmarshal(stuck.PendingPost, &p); err != nil || p.Text != "retry me" {
		t.Fatalf("pending post did not round-trip: %+v err=%v", p, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestApprove|TestReject' -v`
Expected: FAIL — `undefined: NewApprovalService` / `undefined: FilePendingPost`.

- [ ] **Step 3: Write minimal implementation**

```go
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
	tel       *Telemetry
}

// NewApprovalService is the ONLY constructor that accepts a Connector.
func NewApprovalService(ts TicketStore, c Connector, tel *Telemetry) *ApprovalService {
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

	ref, err := a.connector.Publish(ctx, p) // the ONE call site of Connector.Publish
	if err != nil {
		return "", fmt.Errorf("approve %s: publish: %w", ticketID, err) // ticket unchanged → retryable
	}

	t.PendingPost = nil
	t.Status = StatusDone
	if err := a.tickets.Update(ctx, t); err != nil {
		return "", fmt.Errorf("approve %s: persist done: %w", ticketID, err)
	}
	if a.tel != nil {
		a.tel.Record(Run{Scope: "approve", BoardRevision: t.BoardRev, Output: "published " + ref})
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestApprove|TestReject' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/approval.go go/orchestrator/approval_test.go
git commit -m "feat(orchestrator): PendingPost lifecycle + ApprovalService (approve = sole publisher)"
```

---

### Task 5: The C2 structural boundary test (a worker cannot reach Publish)

**Files:**
- Create: `go/orchestrator/boundary_test.go`

**Interfaces:**
- Consumes: everything above + `Runner` (Slice 0, the worker path); stdlib `reflect`. No new
  production code — this task asserts the keystone safety property three ways.

**Rationale (the three structural guarantees this locks in):**
1. **The worker surface has no publish tool** — `WorkerToolset` (Task 3) is closed and excludes it.
2. **The worker path holds no Connector** — `Runner` (the type that composes+runs a worker scope) has
   no `Connector`-typed field, asserted by reflection. Publishing is not reachable from the code that
   runs a worker.
3. **Publish happens only on approval** — driving a full draft→pending cycle with a counting connector
   shows `Publish` is never called until `ApprovalService.Approve`, and reject never calls it.

- [ ] **Step 1: Write the failing/asserting test**

```go
package orchestrator

import (
	"context"
	"reflect"
	"testing"
)

// TestWorkerPathHasNoConnectorField asserts the worker executor (Runner) cannot
// hold a Connector — publishing is structurally out of the worker path. If a
// future change adds a Connector-typed field to Runner, this fails loudly.
func TestWorkerPathHasNoConnectorField(t *testing.T) {
	connIface := reflect.TypeOf((*Connector)(nil)).Elem()
	rt := reflect.TypeOf(Runner{})
	for i := 0; i < rt.NumField(); i++ {
		ft := rt.Field(i).Type
		if ft == connIface || ft.Implements(connIface) {
			t.Fatalf("Runner.%s is/holds a Connector — the worker path must never reach Publish", rt.Field(i).Name)
		}
	}
}

// TestPublishOnlyReachableFromApproval drives the full worker→approval cycle with
// a counting connector and asserts Publish fires ONLY on approve, never on the
// worker/draft/reject paths (contracts §7 floor #3, the keystone v1 property).
func TestPublishOnlyReachableFromApproval(t *testing.T) {
	ctx := context.Background()
	ts := newFakeTickets()
	conn := &FakeConnector{} // Calls is our publish counter
	svc := NewApprovalService(ts, conn, NewTelemetry())

	// Simulate the worker path: a worker drafted content; the manager/ResultSink
	// (which holds NO connector) files it as a pending post. Zero publishes here.
	id, _ := ts.Create(ctx, Ticket{Title: "post", Status: StatusInProgress})
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "draft"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("the worker/draft path published (%d) — gate is bypassable", conn.Calls)
	}

	// Rejecting also never publishes.
	rejID, _ := ts.Create(ctx, Ticket{Title: "post2", Status: StatusInProgress})
	_ = FilePendingPost(ctx, ts, rejID, Post{Channel: "demo", Text: "nope"})
	if _, err := svc.Reject(ctx, rejID, "no"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("reject published (%d) — must never publish", conn.Calls)
	}

	// Only the explicit approval action publishes — exactly once.
	if _, err := svc.Approve(ctx, id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 {
		t.Fatalf("expected exactly one publish via approve, got %d", conn.Calls)
	}

	// And the worker tool surface still offers no way in.
	if IsWorkerTool("publish") || IsWorkerTool("connector") {
		t.Fatalf("a worker tool exposes publishing — gate is bypassable")
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestWorkerPathHasNoConnectorField|TestPublishOnlyReachableFromApproval' -v`
Expected: PASS (all dependencies implemented in Tasks 1–4). If `TestWorkerPathHasNoConnectorField`
fails, a `Connector` leaked into the worker path — fix the wiring, not the test.

- [ ] **Step 3: Commit**

```bash
git add go/orchestrator/boundary_test.go
git commit -m "test(orchestrator): C2 floor — publishing unreachable from the worker path"
```

---

### Task 6: Human-watchable approval-gate demo command

**Files:**
- Create: `go/examples/approvalgate/main.go`

**Interfaces:**
- Consumes: the `orchestrator` package + the `fakeTicketStore` pattern (re-implemented in `main`,
  since test doubles are package-private). Produces a `main` that walks a draft → PendingPost →
  approve (publishes via the FakeConnector) and a second draft → reject (emits a note, no publish),
  printing each step so a human can *see* the gate hold.

- [ ] **Step 1: Write the implementation**

```go
// Command approvalgate demonstrates the v1 publish-approval floor: a draft becomes
// a Needs-Human pending post; approving it publishes exactly once via the fake
// connector; rejecting it emits a note and never publishes. Offline, deterministic.
package main

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// memTickets is a tiny in-process TicketStore for the demo (mirrors the test double).
type memTickets struct {
	items map[string]orchestrator.Ticket
	seq   int
}

func newMemTickets() *memTickets {
	return &memTickets{items: map[string]orchestrator.Ticket{}}
}
func (s *memTickets) Create(_ context.Context, t orchestrator.Ticket) (string, error) {
	s.seq++
	t.ID = fmt.Sprintf("t%d", s.seq)
	s.items[t.ID] = t
	return t.ID, nil
}
func (s *memTickets) Update(_ context.Context, t orchestrator.Ticket) error {
	s.items[t.ID] = t
	return nil
}
func (s *memTickets) Get(_ context.Context, id string) (orchestrator.Ticket, error) {
	t, ok := s.items[id]
	if !ok {
		return orchestrator.Ticket{}, fmt.Errorf("no ticket %s", id)
	}
	return t, nil
}
func (s *memTickets) List(_ context.Context, status orchestrator.TicketStatus) ([]orchestrator.Ticket, error) {
	var out []orchestrator.Ticket
	for _, t := range s.items {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func main() {
	ctx := context.Background()
	ts := newMemTickets()
	conn := &orchestrator.FakeConnector{}
	svc := orchestrator.NewApprovalService(ts, conn, orchestrator.NewTelemetry())

	// 1) Approve path.
	good, _ := ts.Create(ctx, orchestrator.Ticket{Title: "launch tweet", Status: orchestrator.StatusInProgress})
	must(orchestrator.FilePendingPost(ctx, ts, good, orchestrator.Post{Channel: "demo", Text: "We shipped v1 🚀"}))
	fmt.Printf("filed pending post on %s (publishes so far: %d)\n", good, conn.Calls)
	ref, err := svc.Approve(ctx, good)
	if err != nil {
		panic(err)
	}
	fmt.Printf("APPROVED %s → published ref=%s (publishes so far: %d)\n", good, ref, conn.Calls)

	// 2) Reject path.
	bad, _ := ts.Create(ctx, orchestrator.Ticket{Title: "salesy post", Status: orchestrator.StatusInProgress})
	must(orchestrator.FilePendingPost(ctx, ts, bad, orchestrator.Post{Channel: "demo", Text: "BUY NOW!!!"}))
	fb, err := svc.Reject(ctx, bad, "too salesy — be witty, not shouty")
	if err != nil {
		panic(err)
	}
	fmt.Printf("REJECTED %s → feedback{%s: %q} (publishes so far: %d)\n", bad, fb.TargetRef, fb.Note, conn.Calls)

	fmt.Printf("\ngate held: %d post(s) published, only via approve.\n", conn.Calls)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `cd go && go run ./examples/approvalgate`
Expected output:
```
filed pending post on t1 (publishes so far: 0)
APPROVED t1 → published ref=fake://post/1 (publishes so far: 1)
REJECTED t2 → feedback{ticket:t2: "too salesy — be witty, not shouty"} (publishes so far: 1)

gate held: 1 post(s) published, only via approve.
```

- [ ] **Step 3: Verify the whole package + commit**

Run: `cd go && go build ./... && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS.

```bash
git add go/examples/approvalgate/main.go
git commit -m "feat(examples): approvalgate demo — the publish gate holding (approve vs reject)"
```

---

## Contract gaps found

Genuine gaps/ambiguities in the frozen contracts surfaced while planning Slice D. None were resolved
by editing a contract (that is stop-and-escalate); each is either flagged for the author or given a
documented local default that does not contradict the contracts.

- **G1 — shared-type declaration ownership / slice ordering.** Slice D **consumes** `TicketStatus`
  (§3), `Ticket` (§4), and `TicketStore` (§5), which the ownership map (§9) assigns to Slice A. If
  Slice D is executed before Slice A, those frozen declarations must be landed first (copy §3–§5
  verbatim into `orchestrator`). The contracts don't state which file holds the shared types or that
  a slice may depend on a later-numbered slice's declarations. **Assumption:** Slice A's
  `TicketStatus`/`Ticket`/`TicketStore` exist; Slice D adds none of them.
- **G2 — no home for the published ref on the Ticket.** After `Approve` publishes, the channel
  returns a `ref` (post id/url), but `Ticket` (§4) has no `PublishedRef`/`ExternalRef` field. The
  plan returns the ref and records it in Telemetry, but there is no canonical persisted attribution
  on the ticket. **Suggest:** add `Ticket.PublishedRef` or define that the ref is written into
  `Ticket.Result`.
- **G3 — post-reject lane unspecified.** §3 has no "rejected" status; §8 says reject`{note}` →
  `HumanFeedback` but not where the ticket goes. **Default chosen:** clear `PendingPost`, set status
  → `StatusTodo` (re-draft on the next tick). Needs confirmation (alternative: `StatusBacklog`, or a
  new terminal `rejected` lane).
- **G4 — reject-note vs write_fragment wiring.** §8 lists both `POST /reject {note?}` and
  `POST /feedback {target_ref, note}` (the latter → `write_fragment`). It's unstated whether a
  reject note is *auto-applied* as a fragment edit or merely surfaced. **Default chosen:** `Reject`
  returns `HumanFeedback` and does **not** edit guidance; applying it via `write_fragment` is Slice
  E's `/api/feedback`. Confirm this is the intended split.
- **G5 — no publish idempotency key.** `Post` (§4) has no idempotency/dedup key, and `Connector`
  (§5) returns only a ref. A retry after a partially-failed publish could double-post on the real
  channel. The plan guards with ticket state (PendingPost cleared only on success), but the channel
  itself is undeduped. **Flag** for the real `ChannelConnector`.
- **G6 — Media deferred but present.** `Post.Media` exists yet §4 says "empty for v1 text-only." The
  `ChannelConnector` `TODO(channel)` defers Media. Not a blocker; noted so it isn't silently dropped
  when the channel is chosen.
- **G7 — draft→Post channel source.** Contracts say a worker "produces a draft → a Needs-Human
  ticket," but not how `Result.Output` (text) acquires a `Post.Channel` — the worker has no channel
  tool (correctly). **Assumption:** the channel string comes from ticket/project config at
  `FilePendingPost` time (single-channel v1), not from the worker. Confirm the channel's source.
