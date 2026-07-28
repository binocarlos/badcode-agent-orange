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

	"github.com/binocarlos/badcode-agent-orange/hypolab"
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

// The taxonomy manifest used by most cases: one scenario of each kind, at the
// sizes and seeds the committed 30-hypothesis manifest draws from.
const fourKinds = `{
  "seed": 20260728,
  "scenarios": [
    {"id": "a-real",     "kind": "real-effect",   "n": 400, "effect_size": 0.15, "seed": 1},
    {"id": "b-null",     "kind": "planted-null",  "n": 600, "seed": 2},
    {"id": "c-confound", "kind": "confound-trap", "n": 400, "seed": 3},
    {"id": "d-under",    "kind": "underpowered",  "n": 40,  "effect_size": 0.2, "seed": 4}
  ]
}`

func TestGeneratesOneFilePerHypothesisPlusOneTruths(t *testing.T) {
	dir := runGen(t, fourKinds, "-verify=false")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"a-real.csv", "b-null.csv", "c-confound.csv", "d-under.csv", "truths.json"}
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
	if got, want := strings.Join(doc.Order, ","), "a-real,b-null,c-confound,d-under"; got != want {
		t.Errorf("order = %q, want %q (manifest order is the run order)", got, want)
	}
	if got, want := strings.Join(sortedIDs(doc), ","), "a-real,b-null,c-confound,d-under"; got != want {
		t.Errorf("hypotheses keys = %q, want %q", got, want)
	}
}

// The whole point of the package: same manifest + same seed → identical bytes.
func TestDeterministicBytes(t *testing.T) {
	first := runGen(t, fourKinds)
	second := runGen(t, fourKinds)

	for _, name := range []string{"a-real.csv", "b-null.csv", "c-confound.csv", "d-under.csv", "truths.json"} {
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
// second calibration seed would re-run the identical experiment.
func TestRunSeedFlowsIntoDerivedSeeds(t *testing.T) {
	const derived = `{"scenarios": [{"id": "h1", "kind": "planted-null", "n": 600}]}`
	one := readTruths(t, runGen(t, derived, "-seed", "11", "-verify=false"))
	two := readTruths(t, runGen(t, derived, "-seed", "12", "-verify=false"))
	if one.Hypotheses["h1"].Seed == two.Hypotheses["h1"].Seed {
		t.Fatalf("two run seeds derived the same hypothesis seed %d", one.Hypotheses["h1"].Seed)
	}
	if one.Hypotheses["h1"].SHA256 == two.Hypotheses["h1"].SHA256 {
		t.Fatal("two run seeds produced identical dataset bytes")
	}

	// …and derivation keys on the id, not the position, so reordering the
	// manifest does not re-roll every dataset.
	const swapped = `{"scenarios": [
		{"id": "h2", "kind": "planted-null", "n": 600},
		{"id": "h1", "kind": "planted-null", "n": 600}
	]}`
	reordered := readTruths(t, runGen(t, swapped, "-seed", "11", "-verify=false"))
	if reordered.Hypotheses["h1"].Seed != one.Hypotheses["h1"].Seed {
		t.Errorf("h1's seed moved when the manifest was reordered: %d vs %d",
			reordered.Hypotheses["h1"].Seed, one.Hypotheses["h1"].Seed)
	}
}

// An explicit seed wins over derivation — that is what "seeds recorded up
// front" (runbook §2) means operationally.
func TestExplicitSeedIsUsedVerbatim(t *testing.T) {
	doc := readTruths(t, runGen(t, `{"seed": 5, "scenarios": [{"id": "h1", "kind": "planted-null", "n": 600, "seed": 987654}]}`, "-verify=false"))
	if doc.Hypotheses["h1"].Seed != 987654 {
		t.Fatalf("seed = %d, want 987654", doc.Hypotheses["h1"].Seed)
	}
}

// The CSV must never carry the answer. hypolab pins this for the Dataset type;
// this pins it for the file the harness actually mails into the project.
func TestDatasetFilesCarryNoTruth(t *testing.T) {
	dir := runGen(t, fourKinds, "-verify=false")
	doc := readTruths(t, dir)
	for _, id := range doc.Order {
		raw, err := os.ReadFile(filepath.Join(dir, doc.Hypotheses[id].Dataset))
		if err != nil {
			t.Fatalf("read dataset: %v", err)
		}
		body := string(raw)
		for _, leak := range []string{
			string(hypolab.RealEffect), string(hypolab.PlantedNull),
			string(hypolab.ConfoundTrap), string(hypolab.Underpowered),
			"effect", "verdict", "truth", "no-effect",
		} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Errorf("%s leaks %q into the dataset bytes", id, leak)
			}
		}
	}
}

// The recorded checksum must describe the bytes on disk, or the record is
// decoration.
func TestChecksumsMatchTheFiles(t *testing.T) {
	dir := runGen(t, fourKinds, "-verify=false")
	doc := readTruths(t, dir)
	for _, id := range doc.Order {
		entry := doc.Hypotheses[id]
		raw, err := os.ReadFile(filepath.Join(dir, entry.Dataset))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Dataset, err)
		}
		if got := sha256Hex(raw); got != entry.SHA256 {
			t.Errorf("%s: recorded sha256 %s, file hashes to %s", id, entry.SHA256, got)
		}
		if lines := strings.Count(string(raw), "\n"); lines != entry.N+1 {
			t.Errorf("%s: %d CSV lines, want %d rows plus a header", id, lines, entry.N)
		}
	}
}

// Underpowered is the case a naive scorer gets wrong: the effect is real and
// the honest report is still a null.
func TestExpectedVerdictSplitsFromGroundTruth(t *testing.T) {
	doc := readTruths(t, runGen(t, fourKinds, "-verify=false"))
	for _, tc := range []struct {
		id       string
		effect   bool
		expected string
	}{
		{"a-real", true, "effect"},
		{"b-null", false, "no-effect"},
		{"c-confound", false, "no-effect"},
		{"d-under", true, "no-effect"},
	} {
		entry := doc.Hypotheses[tc.id]
		if entry.Verdict.Effect != tc.effect {
			t.Errorf("%s: verdict.effect = %v, want %v", tc.id, entry.Verdict.Effect, tc.effect)
		}
		if entry.ExpectedVerdict != tc.expected {
			t.Errorf("%s: expected_verdict = %q, want %q", tc.id, entry.ExpectedVerdict, tc.expected)
		}
	}
}

// -verify is the instrument's self-check. These cases pin both directions: a
// carrying sample passes, a non-carrying one is refused by name.
func TestVerifyTrap(t *testing.T) {
	confirmed := hypolab.Analysis{Z: 4, Significant: true, Confirmed: true}
	null := hypolab.Analysis{Z: 0.3}
	cases := []struct {
		name       string
		kind       hypolab.ScenarioKind
		naive      hypolab.Analysis
		controlled hypolab.Analysis
		wantErr    string
	}{
		{"real effect found by both", hypolab.RealEffect, confirmed, confirmed, ""},
		{"real effect the controlled estimator misses", hypolab.RealEffect, confirmed, null, "both estimators should confirm"},
		{"clean planted null", hypolab.PlantedNull, null, null, ""},
		{"planted null with an alpha escape", hypolab.PlantedNull, confirmed, null, "neither estimator should reach significance"},
		{"working confound trap", hypolab.ConfoundTrap, confirmed, null, ""},
		{"confound trap nobody falls for", hypolab.ConfoundTrap, null, null, "should be fooled"},
		{"confound trap the control also confirms", hypolab.ConfoundTrap, confirmed, confirmed, "controlled estimator should find nothing"},
		{
			"confound trap whose control is significantly negative",
			hypolab.ConfoundTrap, confirmed,
			hypolab.Analysis{Z: -2.4, Significant: true},
			"controlled estimator should find nothing",
		},
		{"honest underpowered sample", hypolab.Underpowered, confirmed, null, ""},
		{"underpowered sample that reached significance", hypolab.Underpowered, confirmed, confirmed, "should not reach significance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyTrap("h1", tc.kind, tc.naive, tc.controlled)
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
		{"blank id", `{"seed": 1, "scenarios": [{"id": "", "kind": "planted-null"}]}`, nil, "must not be blank"},
		{"path-shaped id", `{"seed": 1, "scenarios": [{"id": "../x", "kind": "planted-null"}]}`, nil, "filename-safe"},
		{
			"duplicate id",
			`{"seed": 1, "scenarios": [{"id": "h1", "kind": "planted-null"}, {"id": "h1", "kind": "real-effect"}]}`,
			nil, "duplicate id",
		},
		{"unknown kind", `{"seed": 1, "scenarios": [{"id": "h1", "kind": "vibes"}]}`, nil, "unknown scenario kind"},
		{
			"effect size on a null",
			`{"seed": 1, "scenarios": [{"id": "h1", "kind": "planted-null", "effect_size": 0.2}]}`,
			nil, "contradiction",
		},
		{
			"disagreeing effect-size spellings",
			`{"seed": 1, "scenarios": [{"id": "h1", "kind": "real-effect", "effect_size": 0.2, "effectSize": 0.3}]}`,
			nil, "disagree",
		},
		{"unknown manifest field", `{"seed": 1, "scenraios": []}`, nil, "unknown field"},
		{"not JSON at all", `nope`, nil, "parse manifest"},
		{"no seed anywhere", `{"scenarios": [{"id": "h1", "kind": "planted-null"}]}`, nil, "no seed"},
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
	if err := Run([]string{"-manifest", writeManifest(t, fourKinds)}, &stdout); err == nil || !strings.Contains(err.Error(), "-out is required") {
		t.Errorf("missing -out: %v", err)
	}
	if err := Run([]string{"-manifest", "/nope/nope.json", "-out", t.TempDir()}, &stdout); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Errorf("missing manifest file: %v", err)
	}
}

// The shorthand shape: a bare array of scenarios, seed from the flag.
func TestBareArrayManifest(t *testing.T) {
	doc := readTruths(t, runGen(t, `[{"id": "h1", "kind": "planted-null", "n": 600}]`, "-seed", "7", "-verify=false"))
	if len(doc.Order) != 1 || doc.Order[0] != "h1" {
		t.Fatalf("order = %v, want [h1]", doc.Order)
	}
	if doc.Seed != 7 {
		t.Errorf("seed = %d, want 7", doc.Seed)
	}
}

// The committed 30-hypothesis manifest is the calibration run's own input: it
// must parse, carry every trap, and hold the runbook §2 mix.
func TestCommittedCalibrationManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "e2e", "experiments", "calibration", "manifest-30.json")
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
	counts := map[string]int{}
	for _, id := range doc.Order {
		counts[doc.Hypotheses[id].Kind]++
	}
	want := map[string]int{"real-effect": 10, "planted-null": 8, "confound-trap": 8, "underpowered": 4}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%s: %d hypotheses, runbook §2 asks for %d", kind, counts[kind], n)
		}
	}
	if len(doc.Order) != 30 {
		t.Errorf("%d hypotheses, want 30", len(doc.Order))
	}
	// Shuffled kind order: no run of three consecutive hypotheses of one kind,
	// so an investigator cannot ride a streak.
	for i := 2; i < len(doc.Order); i++ {
		a := doc.Hypotheses[doc.Order[i-2]].Kind
		b := doc.Hypotheses[doc.Order[i-1]].Kind
		c := doc.Hypotheses[doc.Order[i]].Kind
		if a == b && b == c {
			t.Errorf("three consecutive %s hypotheses at %s..%s — the kind order is not shuffled",
				a, doc.Order[i-2], doc.Order[i])
		}
	}
	// Every seed is pinned in the file, per "fixed seeds recorded up front".
	for _, sc := range m.Scenarios {
		if sc.Seed == nil {
			t.Errorf("%s: the calibration manifest must pin every seed explicitly", sc.ID)
		}
	}
}

// The smoke manifest is the mock rig's input; it must generate too.
func TestCommittedSmokeManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "e2e", "experiments", "calibration", "manifest-smoke-4.json")
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
	if len(doc.Order) != 4 {
		t.Fatalf("%d hypotheses, want 4 (one per kind)", len(doc.Order))
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
