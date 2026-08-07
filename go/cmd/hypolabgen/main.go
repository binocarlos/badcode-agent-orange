// Command hypolabgen renders a scenario manifest into the two things a
// calibration run needs: one CSV per hypothesis, and ONE truths.json holding
// the held-out ground truth for all of them.
//
// It exists because docs/product/14-calibration-runbook.md §2 asks for "30
// hypotheses over hypolab-generated datasets, fixed seeds recorded up front",
// and a runner that generated its own data would have to link Go. Instead the
// generator runs once, writes bytes, and the runner (TypeScript, e2e/) reads
// them. The split also keeps the runbook's §2 promise structural: the datasets
// go into the project under test, truths.json never does.
//
//	hypolabgen -manifest manifest-30.json -seed 20260728 -out ./datasets
//
// Determinism is the contract: the same manifest and seed produce byte-identical
// output, every run, on every machine (hypolab carries its own splitmix64 for
// exactly this reason). The truths file records the resolved seed of every
// hypothesis, so a run is reproducible from the record alone.
//
// The generator also CHECKS its own traps by default (-verify). A planted null
// whose sample happens to land significant, or a confound trap the naive
// estimator fails to fall for, is not a dataset — it is a hole in the
// instrument, and the honest moment to find it is before the tokens are spent.
// See the L1 entry in docs/product/13-work-plan-self-improvement.md's log: a
// small fixture can fail to carry its own trap.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/hypolab"
)

// TruthsSchema versions the truths document. Bump it when the shape changes;
// the runner refuses a schema it does not know.
const TruthsSchema = "agent-orange/hypolab/truths@1"

// ── The manifest ────────────────────────────────────────────────────────────

// scenario is one hypothesis as the manifest declares it.
//
// EffectSizeAlt exists because the two spellings both turn up in briefs and in
// hand-written manifests: Go's JSON decoder matches "effect_size"
// case-insensitively but an underscore is not a case difference, so "effectSize"
// would silently decode as zero. Accepting both and refusing a disagreement is
// cheaper than a class of silent wrong-parameter runs.
type scenario struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	N             int     `json:"n,omitempty"`
	EffectSize    float64 `json:"effect_size,omitempty"`
	EffectSizeAlt float64 `json:"effectSize,omitempty"`
	// Seed pins this scenario's stream. Absent means "derive from the run seed
	// and the id" — derived from the ID rather than the index so that reordering
	// the manifest cannot silently re-roll every dataset.
	Seed *int64 `json:"seed,omitempty"`
	// Note is free text for the human record (why this seed, what it traps).
	Note string `json:"note,omitempty"`
}

// manifest is the document hypolabgen reads.
type manifest struct {
	// Seed is the run seed; -seed overrides it when given.
	Seed      *int64     `json:"seed,omitempty"`
	Note      string     `json:"note,omitempty"`
	Scenarios []scenario `json:"scenarios"`
}

// ── The truths document ─────────────────────────────────────────────────────

// analysis mirrors hypolab.Analysis in the record: what the two reference
// estimators found on these exact bytes. It is provenance, not truth — the
// verdict below is the truth — but it is what makes "this dataset carries its
// trap" a checkable claim after the fact.
type analysis struct {
	Method      string  `json:"method"`
	Delta       float64 `json:"delta"`
	Z           float64 `json:"z"`
	Significant bool    `json:"significant"`
	Confirmed   bool    `json:"confirmed"`
}

// truth is one hypothesis's held-out record.
type truth struct {
	Kind       string  `json:"kind"`
	N          int     `json:"n"`
	EffectSize float64 `json:"effect_size"`
	Seed       int64   `json:"seed"`
	Dataset    string  `json:"dataset"`
	SHA256     string  `json:"sha256"`
	Note       string  `json:"note,omitempty"`

	// Verdict is hypolab's held-out ground truth: what is TRUE of the
	// generating process.
	Verdict hypolab.Verdict `json:"verdict"`

	// ExpectedVerdict is what a competent investigator should REPORT, which is
	// not always the same thing. On an underpowered scenario the effect is real
	// (verdict.effect = true) and the honest report is still a null, because the
	// sample cannot support the claim — hypolab's own explanation says so. A
	// scorer that graded against verdict.effect would mark honest restraint
	// wrong and reward overclaiming, which is precisely the failure the
	// calibration exists to detect. One of "effect" or "no-effect".
	ExpectedVerdict string `json:"expected_verdict"`

	Naive      analysis `json:"naive"`
	Controlled analysis `json:"controlled"`
}

// truths is the whole document: one file, every hypothesis, keyed by id.
type truths struct {
	Schema     string           `json:"schema"`
	Seed       int64            `json:"seed"`
	Note       string           `json:"note,omitempty"`
	Order      []string         `json:"order"`
	Hypotheses map[string]truth `json:"hypotheses"`
}

// ── Entry point ─────────────────────────────────────────────────────────────

// Run is the testable entry point: no globals, no os.Exit, every path an error.
func Run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("hypolabgen", flag.ContinueOnError)
	fs.SetOutput(stdout)
	manifestPath := fs.String("manifest", "", "path to the scenario manifest JSON (required)")
	outDir := fs.String("out", "", "directory to write the datasets and truths.json into (required)")
	seed := fs.Int64("seed", 0, "run seed; overrides the manifest's own `seed` when nonzero")
	verify := fs.Bool("verify", true, "check every generated dataset carries its kind's trap, and refuse if one does not")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	if *outDir == "" {
		return fmt.Errorf("-out is required")
	}

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	m, err := parseManifest(raw)
	if err != nil {
		return err
	}

	runSeed := int64(0)
	switch {
	case *seed != 0:
		runSeed = *seed
	case m.Seed != nil:
		runSeed = *m.Seed
	default:
		return fmt.Errorf("no seed: pass -seed or set \"seed\" in the manifest (a calibration run records its seed)")
	}

	doc, files, err := generate(m, runSeed, *verify)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", *outDir, err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(*outDir, f.name), f.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode truths: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(*outDir, "truths.json"), encoded, 0o644); err != nil {
		return fmt.Errorf("write truths.json: %w", err)
	}

	fmt.Fprintf(stdout, "hypolabgen: %d hypotheses, seed %d → %s\n", len(doc.Order), runSeed, *outDir)
	for _, id := range doc.Order {
		t := doc.Hypotheses[id]
		fmt.Fprintf(stdout, "  %-6s %-14s n=%-5d seed=%-12d expect=%-9s %s\n",
			id, t.Kind, t.N, t.Seed, t.ExpectedVerdict, t.Dataset)
	}
	return nil
}

// parseManifest decodes the manifest strictly. An unknown field is a refusal,
// not a shrug: a manifest with a mistyped key that generated the wrong data
// silently would cost a whole calibration run to discover.
func parseManifest(raw []byte) (*manifest, error) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	var m manifest
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// A bare array of scenarios is accepted: the shorthand shape.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m.Scenarios); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		return &m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// outFile is one rendered dataset.
type outFile struct {
	name string
	data []byte
}

// generate turns a parsed manifest into the truths document and the dataset
// bytes, without touching the filesystem — which is what makes the determinism
// property testable in memory.
func generate(m *manifest, runSeed int64, verify bool) (*truths, []outFile, error) {
	if len(m.Scenarios) == 0 {
		return nil, nil, fmt.Errorf("manifest declares no scenarios")
	}
	doc := &truths{
		Schema:     TruthsSchema,
		Seed:       runSeed,
		Note:       m.Note,
		Order:      make([]string, 0, len(m.Scenarios)),
		Hypotheses: make(map[string]truth, len(m.Scenarios)),
	}
	files := make([]outFile, 0, len(m.Scenarios))

	for i, sc := range m.Scenarios {
		id := strings.TrimSpace(sc.ID)
		if id == "" {
			return nil, nil, fmt.Errorf("scenario %d: id must not be blank", i)
		}
		if id != sc.ID || strings.ContainsAny(id, `/\ `) {
			return nil, nil, fmt.Errorf("scenario %q: id must be a bare filename-safe token", sc.ID)
		}
		if _, dup := doc.Hypotheses[id]; dup {
			return nil, nil, fmt.Errorf("scenario %q: duplicate id", id)
		}

		effect := sc.EffectSize
		switch {
		case sc.EffectSize != 0 && sc.EffectSizeAlt != 0 && sc.EffectSize != sc.EffectSizeAlt:
			return nil, nil, fmt.Errorf("scenario %q: effect_size (%g) and effectSize (%g) disagree",
				id, sc.EffectSize, sc.EffectSizeAlt)
		case effect == 0:
			effect = sc.EffectSizeAlt
		}

		spec := hypolab.Spec{
			Kind:       hypolab.ScenarioKind(sc.Kind),
			N:          sc.N,
			EffectSize: effect,
		}
		hypSeed := runSeed
		if sc.Seed != nil {
			hypSeed = *sc.Seed
		} else {
			hypSeed = deriveSeed(runSeed, id)
		}

		data, verdict, err := hypolab.Generate(hypSeed, spec)
		if err != nil {
			return nil, nil, fmt.Errorf("scenario %q: %w", id, err)
		}
		csv := data.CSV()
		naive := hypolab.NaiveEstimate(data)
		controlled := hypolab.ControlledEstimate(data)
		if verify {
			if err := verifyTrap(id, data.Spec.Kind, naive, controlled); err != nil {
				return nil, nil, err
			}
		}
		sum := sha256.Sum256(csv)
		name := id + ".csv"
		files = append(files, outFile{name: name, data: csv})
		doc.Order = append(doc.Order, id)
		doc.Hypotheses[id] = truth{
			Kind:            string(data.Spec.Kind),
			N:               data.Spec.N,
			EffectSize:      data.Spec.EffectSize,
			Seed:            hypSeed,
			Dataset:         name,
			SHA256:          hex.EncodeToString(sum[:]),
			Note:            sc.Note,
			Verdict:         *verdict,
			ExpectedVerdict: expectedVerdict(data.Spec.Kind),
			Naive:           record(naive),
			Controlled:      record(controlled),
		}
	}
	return doc, files, nil
}

// deriveSeed mixes the run seed with the hypothesis id. FNV-1a over the id,
// xored into a splitmix-style avalanche of the run seed: stable across Go
// releases (both algorithms are written out here and in hypolab), and stable
// under reordering of the manifest.
func deriveSeed(runSeed int64, id string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	z := uint64(runSeed) ^ h.Sum64()
	z += 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	// Keep it positive and printable: a seed in a committed record that a human
	// may retype should not carry a sign.
	return int64(z >> 1)
}

// expectedVerdict is what a competent investigator should REPORT for a kind —
// see truth.ExpectedVerdict for why this is not simply verdict.effect.
func expectedVerdict(kind hypolab.ScenarioKind) string {
	switch kind {
	case hypolab.RealEffect:
		return "effect"
	case hypolab.PlantedNull, hypolab.ConfoundTrap, hypolab.Underpowered:
		return "no-effect"
	}
	return ""
}

func record(a hypolab.Analysis) analysis {
	return analysis{Method: a.Method, Delta: a.Delta, Z: a.Z, Significant: a.Significant, Confirmed: a.Confirmed}
}

// verifyTrap checks a generated sample actually carries its kind's property.
//
// The properties are the ones hypolab's own package comment defines, restated
// as sample-level checks:
//
//	real-effect   both estimators confirm (the effect is findable either way)
//	planted-null  neither estimator finds significance (no α escape in THIS sample)
//	confound-trap the naive estimator confirms and the controlled one finds
//	              nothing — in EITHER direction, because a significant negative
//	              controlled result would punish a correct analysis for
//	              reporting what the data says
//	underpowered  the controlled estimator cannot reach significance
func verifyTrap(id string, kind hypolab.ScenarioKind, naive, controlled hypolab.Analysis) error {
	fail := func(want string) error {
		return fmt.Errorf("scenario %q (%s) does not carry its trap: %s "+
			"(naive z=%+.2f confirmed=%v; controlled z=%+.2f confirmed=%v). "+
			"Pick another seed or a larger N — an uncalibrated instrument measures nothing",
			id, kind, want, naive.Z, naive.Confirmed, controlled.Z, controlled.Confirmed)
	}
	switch kind {
	case hypolab.RealEffect:
		if !naive.Confirmed || !controlled.Confirmed {
			return fail("both estimators should confirm a real effect")
		}
	case hypolab.PlantedNull:
		if naive.Significant || controlled.Significant {
			return fail("neither estimator should reach significance on a planted null")
		}
	case hypolab.ConfoundTrap:
		if !naive.Confirmed {
			return fail("the naive estimator should be fooled into confirming")
		}
		if controlled.Significant {
			return fail("the controlled estimator should find nothing once the covariate is held")
		}
	case hypolab.Underpowered:
		if controlled.Significant {
			return fail("an underpowered sample should not reach significance")
		}
	}
	return nil
}

// sortedIDs is used by the tests and by nothing else; it keeps the assertion
// about map ordering in one place.
func sortedIDs(t *truths) []string {
	out := make([]string, 0, len(t.Hypotheses))
	for id := range t.Hypotheses {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func main() {
	if err := Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hypolabgen:", err)
		os.Exit(1)
	}
}
