package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// C4 — briefing-section selection (§6.2 step 2.4, §7.4).
//
// The contract under test, in one sentence: core looks up the newest match of
// the built-in rolling-summary selector plus each of the worker's own
// selectors, injects each as its own headed section capped independently at
// briefing_max_bytes, and never lets any of that fail a job.
// ---------------------------------------------------------------------------

// fakeBriefingSource is the BriefingMemorySource seam backed by a map, plus a
// call log — the "only memory reads core ever performs" claim is only
// meaningful if a test can count them.
type fakeBriefingSource struct {
	bySelector map[string]*agentdb.Memory
	errs       map[string]error
	calls      []string
}

func (f *fakeBriefingSource) NewestMemory(_ context.Context, project, selector string) (*agentdb.Memory, error) {
	f.calls = append(f.calls, selector)
	if err, ok := f.errs[selector]; ok {
		return nil, err
	}
	if m, ok := f.bySelector[selector]; ok {
		// Guard the project argument here rather than in a separate test: the
		// seam is the only place C4 could leak across projects.
		if project == "" {
			return nil, fmt.Errorf("briefing lookup with no project")
		}
		return m, nil
	}
	return nil, agentdb.ErrMemoryNotFound
}

func briefingSource(entries map[string]string) *fakeBriefingSource {
	src := &fakeBriefingSource{bySelector: map[string]*agentdb.Memory{}, errs: map[string]error{}}
	for selector, content := range entries {
		src.bySelector[selector] = &agentdb.Memory{ID: "mem-" + selector, Content: content}
	}
	return src
}

func briefingWorker(name string, briefing ...string) *agentdb.Worker {
	w := &agentdb.Worker{Project: "acme", Name: name, MaxInstances: 1, Enabled: true}
	if len(briefing) > 0 {
		w.Briefing = agentdb.SelectorList(briefing)
	}
	return w
}

// TestBriefingSectionsDefaultSelectorOnly is the compatibility case the work
// plan demands: with `briefing` unset the result must be byte-identical to the
// old single rolling-summary injection — one section, the default heading, the
// summary verbatim.
func TestBriefingSectionsDefaultSelectorOnly(t *testing.T) {
	const summary = "You have answered 40 emails, mostly about refunds."
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=email-answerer": summary,
	})

	got := BuildBriefingSections(context.Background(), src, "acme",
		briefingWorker("email-answerer"), nil)

	want := []BriefingSection{{Heading: DefaultBriefingHeading, Content: summary}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("sections = %#v, want %#v", got, want)
	}

	// And the composed prompt is byte-identical to what C2 produced when the
	// caller hand-built that one section.
	in := baseInput()
	in.Worker = briefingWorker("email-answerer")
	in.Worker.SystemPrompt = "Answer support email."
	in.Briefing = got
	composed, err := ComposeJob(context.Background(), in)
	if err != nil {
		t.Fatalf("ComposeJob: %v", err)
	}
	legacy := CorePreamble("email-answerer", "acme") +
		"\n\n--- worker prompt ---\nAnswer support email." +
		"\n\n--- " + DefaultBriefingHeading + " ---\n" + summary
	if composed.SystemPrompt != legacy {
		t.Fatalf("composed prompt not byte-identical to the legacy injection:\n got %q\nwant %q",
			composed.SystemPrompt, legacy)
	}

	// Exactly one memory read, and it is the built-in selector.
	if len(src.calls) != 1 || src.calls[0] != "kind=rolling-summary,worker=email-answerer" {
		t.Fatalf("memory reads = %v, want exactly the default selector", src.calls)
	}
}

// TestBriefingSectionsMultipleSelectors: the default section plus one per
// configured selector, each under its own heading, in order.
func TestBriefingSectionsMultipleSelectors(t *testing.T) {
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=email-answerer": "40 emails answered.",
		"kind=lesson,worker=email-answerer":          "Never promise a refund date.",
		"name=house-style":                           "British English, no exclamation marks.",
	})
	worker := briefingWorker("email-answerer",
		"kind=lesson,worker=email-answerer", "name=house-style")

	got := BuildBriefingSections(context.Background(), src, "acme", worker, nil)

	want := []BriefingSection{
		{Heading: DefaultBriefingHeading, Content: "40 emails answered."},
		{Heading: DefaultBriefingHeading + ": kind=lesson,worker=email-answerer", Content: "Never promise a refund date."},
		{Heading: DefaultBriefingHeading + ": name=house-style", Content: "British English, no exclamation marks."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("section %d = %#v, want %#v", i, got[i], want[i])
		}
	}

	// One query per selector — no extra reads, ever (§7.4).
	if len(src.calls) != 3 {
		t.Fatalf("memory reads = %v, want one per selector", src.calls)
	}
}

// TestBriefingSectionsCapEachIndependently: the byte cap applies per section,
// so one runaway memory cannot eat another section's budget.
func TestBriefingSectionsCapEachIndependently(t *testing.T) {
	long := strings.Repeat("a", 100)
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=w": long,
		"kind=lesson":                   long,
		"kind=short":                    "brief",
	})
	worker := briefingWorker("w", "kind=lesson", "kind=short")
	settings := &agentdb.ProjectSettings{Project: "acme", BriefingMaxBytes: 20}

	got := BuildBriefingSections(context.Background(), src, "acme", worker, settings)
	if len(got) != 3 {
		t.Fatalf("got %d sections, want 3: %#v", len(got), got)
	}
	marker := BriefingTruncationMarker(20)
	for i := 0; i < 2; i++ {
		want := strings.Repeat("a", 20) + marker
		if got[i].Content != want {
			t.Fatalf("section %d content = %q, want %q", i, got[i].Content, want)
		}
	}
	// The section that fits is untouched — no marker on content under the cap.
	if got[2].Content != "brief" {
		t.Fatalf("short section = %q, want it untouched", got[2].Content)
	}
	if strings.Contains(got[2].Content, "truncated") {
		t.Fatalf("short section carries a truncation marker: %q", got[2].Content)
	}
}

// TestBriefingSectionsCapDefaultsAndBoundaries pins the cap's edges: 0 means
// "unset ⇒ 2048" (the B1 convention), a section exactly at the cap is not
// marked, and the cut never splits a multi-byte rune.
func TestBriefingSectionsCapDefaultsAndBoundaries(t *testing.T) {
	t.Run("zero means the spec default", func(t *testing.T) {
		content := strings.Repeat("x", agentdb.DefaultBriefingMaxBytes+10)
		src := briefingSource(map[string]string{"kind=rolling-summary,worker=w": content})
		got := BuildBriefingSections(context.Background(), src, "acme", briefingWorker("w"),
			&agentdb.ProjectSettings{Project: "acme", BriefingMaxBytes: 0})
		want := strings.Repeat("x", agentdb.DefaultBriefingMaxBytes) +
			BriefingTruncationMarker(agentdb.DefaultBriefingMaxBytes)
		if len(got) != 1 || got[0].Content != want {
			t.Fatalf("content = %q…, want a 2048-byte cut plus the marker", got[0].Content)
		}
	})

	t.Run("exactly at the cap is not truncated", func(t *testing.T) {
		content := strings.Repeat("y", 32)
		src := briefingSource(map[string]string{"kind=rolling-summary,worker=w": content})
		got := BuildBriefingSections(context.Background(), src, "acme", briefingWorker("w"),
			&agentdb.ProjectSettings{Project: "acme", BriefingMaxBytes: 32})
		if len(got) != 1 || got[0].Content != content {
			t.Fatalf("content = %q, want it untouched at exactly the cap", got[0].Content)
		}
	})

	t.Run("the cut lands on a rune boundary", func(t *testing.T) {
		// Each "é" is two bytes: a cap of 5 falls inside the third one.
		content := strings.Repeat("é", 10)
		src := briefingSource(map[string]string{"kind=rolling-summary,worker=w": content})
		got := BuildBriefingSections(context.Background(), src, "acme", briefingWorker("w"),
			&agentdb.ProjectSettings{Project: "acme", BriefingMaxBytes: 5})
		want := strings.Repeat("é", 2) + BriefingTruncationMarker(5)
		if len(got) != 1 || got[0].Content != want {
			t.Fatalf("content = %q, want %q (no split rune)", got[0].Content, want)
		}
	})
}

// TestBriefingSectionsMissingAndEmpty: a selector with no match, or one whose
// newest memory is blank, contributes nothing — a bare heading over nothing is
// noise in a prompt. No archivist wired ⇒ no summary ⇒ no briefing (§7.4).
func TestBriefingSectionsMissingAndEmpty(t *testing.T) {
	src := briefingSource(map[string]string{
		"kind=lesson": "   \n\t ", // present but blank
		"kind=kept":   "keep me",
	})
	worker := briefingWorker("w", "kind=lesson", "kind=missing", "kind=kept")

	got := BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	if len(got) != 1 {
		t.Fatalf("got %d sections, want 1: %#v", len(got), got)
	}
	if got[0].Heading != DefaultBriefingHeading+": kind=kept" || got[0].Content != "keep me" {
		t.Fatalf("section = %#v", got[0])
	}

	t.Run("nothing at all is nil, not an empty section", func(t *testing.T) {
		empty := briefingSource(nil)
		if got := BuildBriefingSections(context.Background(), empty, "acme", briefingWorker("w"), nil); got != nil {
			t.Fatalf("got %#v, want nil", got)
		}
	})
}

// TestBriefingSectionsDegradeOnError is the §7.4 promise that a broken briefing
// costs one section and never the job: a store error (Postgres down, the SQLite
// fallback, a selector the parser rejects) is swallowed, the other sections
// still land, and ComposeJob succeeds.
func TestBriefingSectionsDegradeOnError(t *testing.T) {
	src := briefingSource(map[string]string{"kind=good": "still here"})
	src.errs["kind=rolling-summary,worker=w"] = agentdb.ErrMemoryRequiresPostgres
	src.errs["kind=bad"] = errors.New("selector: unexpected token")
	worker := briefingWorker("w", "kind=bad", "kind=good")

	got := BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	if len(got) != 1 || got[0].Content != "still here" {
		t.Fatalf("sections = %#v, want just the healthy one", got)
	}

	in := baseInput()
	in.Worker = worker
	in.Briefing = got
	if _, err := ComposeJob(context.Background(), in); err != nil {
		t.Fatalf("ComposeJob must survive a degraded briefing: %v", err)
	}
}

// TestBriefingSectionsNoSource: no memory store (the SQLite fallback, where
// memory is unavailable by decision D4) composes without a briefing rather than
// refusing to compose.
func TestBriefingSectionsNoSource(t *testing.T) {
	if got := BuildBriefingSections(context.Background(), nil, "acme", briefingWorker("w"), nil); got != nil {
		t.Fatalf("nil source: got %#v, want nil", got)
	}
	src := briefingSource(map[string]string{"kind=rolling-summary,worker=w": "x"})
	if got := BuildBriefingSections(context.Background(), src, "", briefingWorker("w"), nil); got != nil {
		t.Fatalf("empty project: got %#v, want nil", got)
	}
	if got := BuildBriefingSections(context.Background(), src, "acme", nil, nil); got != nil {
		t.Fatalf("nil worker: got %#v, want nil", got)
	}
	if len(src.calls) != 0 {
		t.Fatalf("a refused build must read no memories, got %v", src.calls)
	}
}

// TestBriefingSectionsDedupesTheDefaultSelector: a worker that lists the
// rolling-summary selector explicitly gets one section, not the same paragraph
// twice under two headings.
func TestBriefingSectionsDedupesTheDefaultSelector(t *testing.T) {
	src := briefingSource(map[string]string{"kind=rolling-summary,worker=w": "once"})
	worker := briefingWorker("w", "kind=rolling-summary,worker=w", "  ")

	got := BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	if len(got) != 1 || got[0].Heading != DefaultBriefingHeading {
		t.Fatalf("sections = %#v, want exactly the default one", got)
	}
	if len(src.calls) != 1 {
		t.Fatalf("memory reads = %v, want 1", src.calls)
	}
}

// TestBriefingSectionsSelectorFormat pins the built-in selector text itself:
// it is the §7.4 contract between core and every archivist prompt in every
// project, so changing it silently orphans every rolling summary ever written.
func TestBriefingSectionsSelectorFormat(t *testing.T) {
	if got, want := RollingSummarySelector("email-answerer"), "kind=rolling-summary,worker=email-answerer"; got != want {
		t.Fatalf("RollingSummarySelector = %q, want %q", got, want)
	}
	if _, err := agentdb.ParseLabelSelector(RollingSummarySelector("email-answerer")); err != nil {
		t.Fatalf("the built-in selector must parse: %v", err)
	}
}
