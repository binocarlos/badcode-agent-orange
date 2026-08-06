package agentkit

// RD19, briefing half: a selector that matches nothing is legal (a fresh worker
// has no rolling summary), but it must not be silent — a typo and "nothing
// written yet" are otherwise indistinguishable, and the job runs with a quietly
// thinner prompt than its author believes.

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func TestBriefingSelectorThatMatchesNothingIsLogged(t *testing.T) {
	// The worker's own selector is a typo; the default rolling summary exists.
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=email-answerer": "You have answered 40 emails.",
	})
	worker := briefingWorker("email-answerer", "kind=hosue-style")

	var got []BriefingSection
	out := captureLog(t, func() {
		got = BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	})

	// The job still runs, and the sections that DID match are unaffected.
	if len(got) != 1 || !strings.Contains(got[0].Content, "40 emails") {
		t.Fatalf("a missing selector must cost only its own section: %+v", got)
	}
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "matched no memory") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("a briefing selector that matched nothing must log — otherwise a typo and "+
			"'nothing written yet' are the same silence. got %q", out)
	}
	for _, want := range []string{"kind=hosue-style", "email-answerer", "acme", "thinner prompt"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the line must name %q so an operator can find the selector: %q", want, line)
		}
	}
	// The selector that DID match must not be reported as missing.
	if strings.Contains(line, "rolling-summary") {
		t.Fatalf("a selector that matched must not be logged as missing: %q", line)
	}
}

func TestBriefingSelectorMatchingEmptyContentIsLogged(t *testing.T) {
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=email-answerer": "   \n  ",
	})
	worker := briefingWorker("email-answerer")

	var got []BriefingSection
	out := captureLog(t, func() {
		got = BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	})
	if len(got) != 0 {
		t.Fatalf("a whitespace-only memory contributes no section: %+v", got)
	}
	if !strings.Contains(out, "empty content") {
		t.Fatalf("a memory that exists but is blank thins the prompt just as silently: %q", out)
	}
}

// A fully-satisfied briefing logs nothing: the signal is only worth anything if
// the healthy case is quiet.
func TestBriefingThatMatchesEverythingLogsNothing(t *testing.T) {
	src := briefingSource(map[string]string{
		"kind=rolling-summary,worker=email-answerer": "Summary.",
		"kind=house-style":                           "Plain English.",
	})
	worker := briefingWorker("email-answerer", "kind=house-style")

	out := captureLog(t, func() {
		if got := BuildBriefingSections(context.Background(), src, "acme", worker, nil); len(got) != 2 {
			t.Fatalf("both selectors must produce a section: %+v", got)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a healthy briefing must be quiet, or the RD19 line is just noise: %q", out)
	}
}

// The seam's real error path is unchanged: a fault is still reported as a fault,
// not as "matched no memory".
func TestBriefingSelectorErrorIsStillReportedAsAFault(t *testing.T) {
	src := briefingSource(map[string]string{})
	src.errs["kind=house-style"] = errBriefingBoom
	worker := briefingWorker("email-answerer", "kind=house-style")

	out := captureLog(t, func() {
		BuildBriefingSections(context.Background(), src, "acme", worker, nil)
	})
	faulty := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "kind=house-style") {
			faulty = l
		}
	}
	if !strings.Contains(faulty, "boom") || strings.Contains(faulty, "matched no memory") {
		t.Fatalf("a real lookup fault must not be reported as an empty selector: %q", out)
	}
}

var errBriefingBoom = errors.New("boom")
