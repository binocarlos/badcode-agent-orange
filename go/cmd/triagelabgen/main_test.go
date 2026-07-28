package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/triagelab"
)

// writeManifest drops a manifest into a temp dir and returns its path.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// runGen runs the CLI into a fresh output dir and returns that dir.
func runGen(t *testing.T, manifestBody string, extra ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "datasets")
	args := append([]string{"-manifest", writeManifest(t, manifestBody), "-out", out}, extra...)
	var stdout bytes.Buffer
	if err := Run(args, &stdout); err != nil {
		t.Fatalf("Run: %v (stdout: %s)", err, stdout.String())
	}
	return out
}

// readTruths decodes the truths document a run wrote.
func readTruths(t *testing.T, dir string) *truths {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "truths.json"))
	if err != nil {
		t.Fatalf("read truths.json: %v", err)
	}
	var doc truths
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse truths.json: %v", err)
	}
	return &doc
}

// The taxonomy manifest used by most cases: one scenario of each kind, at seeds
// the committed manifests also draw from.
const threeKinds = `{
  "seed": 20260728,
  "scenarios": [
    {"id": "a-plain", "kind": "plain",     "queue": "billing", "seed": 1},
    {"id": "b-mis",   "kind": "misdirect", "queue": "outage", "decoy": "billing", "seed": 2},
    {"id": "c-amb",   "kind": "ambiguous", "decoy": "access", "seed": 3}
  ]
}`

func TestGeneratesOneFilePerTicketPlusOneTruths(t *testing.T) {
	dir := runGen(t, threeKinds, "-verify=false")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"a-plain.txt", "b-mis.txt", "c-amb.txt", "truths.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("output files = %v, want %v", names, want)
	}

	doc := readTruths(t, dir)
	if doc.Schema != TruthsSchema {
		t.Errorf("schema = %q, want %q", doc.Schema, TruthsSchema)
	}
	if doc.Seed != 20260728 {
		t.Errorf("seed = %d, want 20260728", doc.Seed)
	}
	if got, want := strings.Join(doc.Order, ","), "a-plain,b-mis,c-amb"; got != want {
		t.Errorf("order = %q, want %q (manifest order is the run order)", got, want)
	}
	if got, want := strings.Join(sortedIDs(doc), ","), "a-plain,b-mis,c-amb"; got != want {
		t.Errorf("ticket keys = %q, want %q", got, want)
	}
}

// The whole point of the package: same manifest + same seed → identical bytes.
func TestDeterministicBytes(t *testing.T) {
	first := runGen(t, threeKinds)
	second := runGen(t, threeKinds)

	for _, name := range []string{"a-plain.txt", "b-mis.txt", "c-amb.txt", "truths.json"} {
		a, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs between two runs of the same manifest", name)
		}
	}
}

// A run seed change must reach the data. Otherwise "-seed" is decoration and a
// second scenario seed would re-run the identical experiment.
func TestRunSeedFlowsIntoDerivedSeeds(t *testing.T) {
	const derived = `{"scenarios": [{"id": "t1", "kind": "plain", "queue": "outage"}]}`
	one := readTruths(t, runGen(t, derived, "-seed", "11"))
	two := readTruths(t, runGen(t, derived, "-seed", "12"))
	if one.Ticket["t1"].Seed == two.Ticket["t1"].Seed {
		t.Fatalf("two run seeds derived the same ticket seed %d", one.Ticket["t1"].Seed)
	}
	if one.Ticket["t1"].SHA256 == two.Ticket["t1"].SHA256 {
		t.Fatal("two run seeds produced identical ticket bytes")
	}

	// …and derivation keys on the id, not the position, so reordering the
	// manifest does not re-roll every ticket.
	const swapped = `{"scenarios": [
		{"id": "t2", "kind": "plain", "queue": "outage"},
		{"id": "t1", "kind": "plain", "queue": "outage"}
	]}`
	reordered := readTruths(t, runGen(t, swapped, "-seed", "11"))
	if reordered.Ticket["t1"].Seed != one.Ticket["t1"].Seed {
		t.Errorf("t1's seed moved when the manifest was reordered: %d vs %d",
			reordered.Ticket["t1"].Seed, one.Ticket["t1"].Seed)
	}
}

// An explicit seed wins over derivation — that is what "seeds recorded up
// front" (doc 19 §2) means operationally.
func TestExplicitSeedIsUsedVerbatim(t *testing.T) {
	doc := readTruths(t, runGen(t, `{"seed": 5, "scenarios": [{"id": "t1", "kind": "plain", "queue": "access", "seed": 987654}]}`))
	if doc.Ticket["t1"].Seed != 987654 {
		t.Fatalf("seed = %d, want 987654", doc.Ticket["t1"].Seed)
	}
}

// The ticket file must never carry the answer. triagelab pins this for the
// Dataset type; this pins it for the file the harness actually mails into the
// project.
func TestTicketFilesCarryNoTruth(t *testing.T) {
	dir := runGen(t, threeKinds, "-verify=false")
	doc := readTruths(t, dir)
	for _, id := range doc.Order {
		raw, err := os.ReadFile(filepath.Join(dir, doc.Ticket[id].Dataset))
		if err != nil {
			t.Fatalf("read ticket: %v", err)
		}
		body := strings.ToLower(string(raw))
		for _, leak := range []string{
			string(triagelab.Plain), string(triagelab.Misdirect), string(triagelab.Ambiguous),
			string(triagelab.Escalate), "route", "decoy", "truth",
		} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaks %q into the ticket bytes", id, leak)
			}
		}
		if strings.Contains(body, strings.ToLower(doc.Ticket[id].Explanation)) {
			t.Errorf("%s leaks its own explanation into the ticket bytes", id)
		}
	}
}

// The recorded checksum must describe the bytes on disk, or the record is
// decoration.
func TestChecksumsMatchTheFiles(t *testing.T) {
	dir := runGen(t, threeKinds, "-verify=false")
	doc := readTruths(t, dir)
	for _, id := range doc.Order {
		entry := doc.Ticket[id]
		raw, err := os.ReadFile(filepath.Join(dir, entry.Dataset))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Dataset, err)
		}
		if got := sha256Hex(raw); got != entry.SHA256 {
			t.Errorf("%s: recorded sha256 %s, file hashes to %s", id, entry.SHA256, got)
		}
		if !strings.HasPrefix(string(raw), "Subject: ") {
			t.Errorf("%s: a ticket file must open with its subject line", id)
		}
	}
}

// The record's `routing` block is provenance: it must describe what the two
// reference routers actually did on these exact bytes, or "this ticket carries
// its trap" becomes unfalsifiable after the fact.
func TestRoutingRecordMatchesTheBytes(t *testing.T) {
	dir := runGen(t, threeKinds)
	doc := readTruths(t, dir)
	for _, id := range doc.Order {
		entry := doc.Ticket[id]
		raw, err := os.ReadFile(filepath.Join(dir, entry.Dataset))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Dataset, err)
		}
		text := string(raw)
		if got := string(triagelab.NaiveKeywordRoute(text)); got != entry.Routing.Naive {
			t.Errorf("%s: recorded naive route %q, the bytes give %q", id, entry.Routing.Naive, got)
		}
		if got := string(triagelab.ContentRuleRoute(text)); got != entry.Routing.Content {
			t.Errorf("%s: recorded content route %q, the bytes give %q", id, entry.Routing.Content, got)
		}
		if got := triagelab.ScoreLine(text); got != entry.Routing.Scores {
			t.Errorf("%s: recorded scores %q, the bytes give %q", id, entry.Routing.Scores, got)
		}
		// The content-rule router is the charter, so the record's route and its
		// content route must agree — on every kind, escalation included.
		if entry.Routing.Content != entry.Route {
			t.Errorf("%s: held-out route %q but the charter's own router says %q", id, entry.Route, entry.Routing.Content)
		}
	}
	// …and the traps are visible in the record, not merely asserted in a test.
	if doc.Ticket["b-mis"].Routing.Naive != "billing" || doc.Ticket["b-mis"].Route != "outage" {
		t.Errorf("the misdirection record should read naive=billing route=outage, got naive=%q route=%q",
			doc.Ticket["b-mis"].Routing.Naive, doc.Ticket["b-mis"].Route)
	}
	if doc.Ticket["c-amb"].Routing.Naive == "escalate" {
		t.Error("the ambiguity record should show the naive router guessing a queue, not escalating")
	}
}

// -verify is the instrument's self-check. These cases pin both directions: a
// carrying sample passes, a non-carrying one is refused by name.
func TestVerifyTrap(t *testing.T) {
	q := func(s string) []triagelab.Queue { return []triagelab.Queue{triagelab.Queue(s)} }
	empty := &triagelab.Dataset{Tickets: []triagelab.Ticket{{Subject: "s"}}}
	cases := []struct {
		name    string
		spec    triagelab.Spec
		route   string
		naive   []triagelab.Queue
		content []triagelab.Queue
		wantErr string
	}{
		{"plain both agree", triagelab.Spec{Kind: triagelab.Plain, Queue: triagelab.Billing}, "billing", q("billing"), q("billing"), ""},
		{"plain the naive router misses", triagelab.Spec{Kind: triagelab.Plain, Queue: triagelab.Billing}, "billing", q("outage"), q("billing"), "both routers should reach"},
		{"plain the charter misses", triagelab.Spec{Kind: triagelab.Plain, Queue: triagelab.Billing}, "billing", q("billing"), q("escalate"), "both routers should reach"},
		{
			"working misdirection",
			triagelab.Spec{Kind: triagelab.Misdirect, Queue: triagelab.Outage, Decoy: triagelab.Billing},
			"outage", q("billing"), q("outage"), "",
		},
		{
			"misdirection nobody falls for",
			triagelab.Spec{Kind: triagelab.Misdirect, Queue: triagelab.Outage, Decoy: triagelab.Billing},
			"outage", q("outage"), q("outage"), "carried off to the decoy",
		},
		{
			"misdirection wrong in the wrong way",
			triagelab.Spec{Kind: triagelab.Misdirect, Queue: triagelab.Outage, Decoy: triagelab.Billing},
			"outage", q("access"), q("outage"), "carried off to the decoy",
		},
		{
			"misdirection the charter also misses",
			triagelab.Spec{Kind: triagelab.Misdirect, Queue: triagelab.Outage, Decoy: triagelab.Billing},
			"outage", q("billing"), q("billing"), "should reach the stated-fact queue",
		},
		{"working ambiguity", triagelab.Spec{Kind: triagelab.Ambiguous, Decoy: triagelab.Access}, "escalate", q("access"), q("escalate"), ""},
		{
			"ambiguity the charter routes anyway",
			triagelab.Spec{Kind: triagelab.Ambiguous, Decoy: triagelab.Access}, "escalate", q("access"), q("access"),
			"should escalate when no rule fires",
		},
		{
			"ambiguity the naive router declines",
			triagelab.Spec{Kind: triagelab.Ambiguous, Decoy: triagelab.Access}, "escalate", q("escalate"), q("escalate"),
			"should be unable to escalate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyTrap("t1", tc.spec, &triagelab.Truth{Route: tc.route}, tc.naive, tc.content, empty)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected refusal: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatal("expected a refusal, got none")
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("refusal %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// Every refusal the CLI can produce, by the shape of the mistake that causes it.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		args     []string
		want     string
	}{
		{"no scenarios", `{"seed": 1, "scenarios": []}`, nil, "no scenarios"},
		{"blank id", `{"seed": 1, "scenarios": [{"id": "", "kind": "plain", "queue": "billing"}]}`, nil, "must not be blank"},
		{"path-shaped id", `{"seed": 1, "scenarios": [{"id": "../x", "kind": "plain", "queue": "billing"}]}`, nil, "filename-safe"},
		{
			"duplicate id",
			`{"seed": 1, "scenarios": [{"id": "t1", "kind": "plain", "queue": "billing"}, {"id": "t1", "kind": "plain", "queue": "outage"}]}`,
			nil, "duplicate id",
		},
		{"unknown kind", `{"seed": 1, "scenarios": [{"id": "t1", "kind": "vibes"}]}`, nil, "unknown ticket kind"},
		{
			"plain with a decoy",
			`{"seed": 1, "scenarios": [{"id": "t1", "kind": "plain", "queue": "billing", "decoy": "outage"}]}`,
			nil, "no decoy by definition",
		},
		{
			"ambiguous with a queue",
			`{"seed": 1, "scenarios": [{"id": "t1", "kind": "ambiguous", "queue": "billing", "decoy": "outage"}]}`,
			nil, "states no routable fact",
		},
		{
			"escalate asked for as an input",
			`{"seed": 1, "scenarios": [{"id": "t1", "kind": "plain", "queue": "escalate"}]}`,
			nil, "an answer, not an input",
		},
		{"unknown manifest field", `{"seed": 1, "scenraios": []}`, nil, "unknown field"},
		{"not JSON at all", `nope`, nil, "parse manifest"},
		{"no seed anywhere", `{"scenarios": [{"id": "t1", "kind": "plain", "queue": "billing"}]}`, nil, "no seed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{
				"-manifest", writeManifest(t, tc.manifest),
				"-out", filepath.Join(t.TempDir(), "out"),
			}, tc.args...)
			var stdout bytes.Buffer
			err := Run(args, &stdout)
			if err == nil {
				t.Fatalf("expected a refusal, got none (stdout: %s)", stdout.String())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Missing flags and missing files fail before anything is written.
func TestFlagRefusals(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run([]string{"-out", t.TempDir()}, &stdout); err == nil || !strings.Contains(err.Error(), "-manifest is required") {
		t.Errorf("missing -manifest: %v", err)
	}
	if err := Run([]string{"-manifest", writeManifest(t, threeKinds)}, &stdout); err == nil || !strings.Contains(err.Error(), "-out is required") {
		t.Errorf("missing -out: %v", err)
	}
	if err := Run([]string{"-manifest", "/nope/nope.json", "-out", t.TempDir()}, &stdout); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Errorf("missing manifest file: %v", err)
	}
}

// The shorthand shape: a bare array of scenarios, seed from the flag.
func TestBareArrayManifest(t *testing.T) {
	doc := readTruths(t, runGen(t, `[{"id": "t1", "kind": "plain", "queue": "outage"}]`, "-seed", "7"))
	if len(doc.Order) != 1 || doc.Order[0] != "t1" {
		t.Fatalf("order = %v, want [t1]", doc.Order)
	}
	if doc.Seed != 7 {
		t.Errorf("seed = %d, want 7", doc.Seed)
	}
}

// A stream of several tickets shares one route and is still a stream.
func TestMultiTicketDataset(t *testing.T) {
	dir := runGen(t, `{"seed": 42, "scenarios": [{"id": "t1", "kind": "misdirect", "queue": "access", "decoy": "billing", "n": 3}]}`)
	doc := readTruths(t, dir)
	entry := doc.Ticket["t1"]
	if entry.N != 3 {
		t.Fatalf("n = %d, want 3", entry.N)
	}
	if entry.Routing.Naive != "billing,billing,billing" {
		t.Errorf("the record must show every ticket's route, got %q", entry.Routing.Naive)
	}
	if entry.Routing.Content != "access,access,access" {
		t.Errorf("the record must show every ticket's charter route, got %q", entry.Routing.Content)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entry.Dataset))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(raw), "Subject: "); got != 3 {
		t.Errorf("%d subject lines in the file, want 3", got)
	}
}

// The committed 24-ticket manifest is the SC-1 run's own input: it must parse,
// carry every trap, and hold the doc 19 §3 mix.
func TestCommittedTriageManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "e2e", "experiments", "triage", "manifest-24.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no committed manifest at %s: %v", path, err)
	}
	m, err := parseManifest(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if m.Seed == nil {
		t.Fatal("the committed manifest must record its own seed")
	}
	doc, _, err := generate(m, *m.Seed, true)
	if err != nil {
		t.Fatalf("the committed manifest does not carry its traps: %v", err)
	}
	if len(doc.Order) != 24 {
		t.Errorf("%d tickets, want 24", len(doc.Order))
	}
	counts := map[string]int{}
	pairs := map[string]bool{}
	leans := map[string]bool{}
	for _, id := range doc.Order {
		entry := doc.Ticket[id]
		counts[entry.Kind]++
		switch entry.Kind {
		case string(triagelab.Misdirect):
			pairs[entry.Queue+"/"+entry.Decoy] = true
		case string(triagelab.Ambiguous):
			leans[entry.Decoy] = true
		}
	}
	want := map[string]int{"plain": 8, "misdirect": 10, "ambiguous": 6}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%s: %d tickets, the manifest's own note claims %d", kind, counts[kind], n)
		}
	}
	// All six queue/decoy pairs, or the headline rate is an average over an
	// arbitrary subset of the misdirection space.
	if len(pairs) != 6 {
		t.Errorf("misdirection covers %d of the 6 queue/decoy pairs: %v", len(pairs), pairs)
	}
	if len(leans) != 3 {
		t.Errorf("ambiguity covers %d of the 3 leans: %v", len(leans), leans)
	}
	// Shuffled kind order: no run of three consecutive tickets of one kind, so
	// a dispatcher cannot ride a streak.
	for i := 2; i < len(doc.Order); i++ {
		a := doc.Ticket[doc.Order[i-2]].Kind
		b := doc.Ticket[doc.Order[i-1]].Kind
		c := doc.Ticket[doc.Order[i]].Kind
		if a == b && b == c {
			t.Errorf("three consecutive %s tickets at %s..%s — the kind order is not shuffled",
				a, doc.Order[i-2], doc.Order[i])
		}
	}
	// Every seed is pinned in the file, per "fixed seeds recorded up front".
	for _, sc := range m.Scenarios {
		if sc.Seed == nil {
			t.Errorf("%s: the triage manifest must pin every seed explicitly", sc.ID)
		}
	}
	// And the headline claim, computed from the committed record rather than
	// asserted: a keyword router gets every trap wrong and every plain ticket
	// right.
	naiveRight, trapTotal := 0, 0
	for _, id := range doc.Order {
		entry := doc.Ticket[id]
		if entry.Routing.Naive == entry.Route {
			naiveRight++
		}
		if entry.Kind != string(triagelab.Plain) {
			trapTotal++
			if entry.Routing.Naive == entry.Route {
				t.Errorf("%s (%s) is a trap the naive router did not fall for", id, entry.Kind)
			}
		}
	}
	if naiveRight != counts["plain"] {
		t.Errorf("the naive router got %d of 24 right; it should get exactly the %d plain tickets", naiveRight, counts["plain"])
	}
	if trapTotal != 16 {
		t.Errorf("%d trap tickets, want 16", trapTotal)
	}
}

// The smoke manifest is the mock rig's input; it must generate too, and its
// alternating kind order is what the early/late window depends on.
func TestCommittedSmokeManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "e2e", "experiments", "triage", "manifest-smoke-6.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no committed smoke manifest at %s: %v", path, err)
	}
	m, err := parseManifest(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if m.Seed == nil {
		t.Fatal("the smoke manifest must record its own seed")
	}
	doc, _, err := generate(m, *m.Seed, true)
	if err != nil {
		t.Fatalf("the smoke manifest does not carry its traps: %v", err)
	}
	if len(doc.Order) != 6 {
		t.Fatalf("%d tickets, want 6 (two per kind)", len(doc.Order))
	}
	// A TRAP first, deliberately: the critic's rewrite lands at the end of
	// ticket 1, so a stream opening with something a keyword router already gets
	// right would produce a flat accuracy curve and prove nothing about delivery.
	wantKinds := []string{"misdirect", "plain", "ambiguous", "misdirect", "plain", "ambiguous"}
	for i, id := range doc.Order {
		if got := doc.Ticket[id].Kind; got != wantKinds[i] {
			t.Errorf("%s is %s, the smoke's window arithmetic needs %s at position %d", id, got, wantKinds[i], i)
		}
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
