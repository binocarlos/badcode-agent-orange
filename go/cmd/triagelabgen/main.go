// Command triagelabgen renders a ticket manifest into the two things a triage
// run needs: one text file per ticket, and ONE truths.json holding the held-out
// correct route for all of them.
//
// It is hypolabgen's sibling and deliberately its twin, down to the flags:
// docs/product/19-scenario-library.md §2 makes "deterministic seeded generator,
// truth as a separate return, verified traps" the admissibility contract for
// every scenario, and the cheapest way to keep a contract is to keep one shape.
//
//	triagelabgen -manifest manifest-24.json -seed 20260728 -out ./datasets
//
// Determinism is the contract: the same manifest and seed produce byte-identical
// output, every run, on every machine (triagelab carries its own splitmix64 for
// exactly this reason). The truths file records the resolved seed of every
// ticket, so a run is reproducible from the record alone.
//
// The generator also CHECKS its own traps by default (-verify). A misdirection
// ticket the naive keyword router happens to get RIGHT is not a trap — it is a
// hole in the instrument, and the honest moment to find it is before the tokens
// are spent. See the L1 entry in docs/product/13-work-plan-self-improvement.md's
// log: a small fixture can fail to carry its own trap.
//
// # One difference from hypolabgen, worth knowing
//
// hypolab records both `verdict` (what is true) and `expected_verdict` (what a
// competent investigator should REPORT), because an underpowered sample has a
// real effect and a null is still the honest answer. Triage has no such gap:
// the correct route IS the answer, including `escalate`, which is a route and
// not a refusal to give one. So there is one truth field here, and metrics
// score against it directly.
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

	"github.com/binocarlos/badcode-agent-orange/triagelab"
)

// TruthsSchema versions the truths document. Bump it when the shape changes;
// the runner refuses a schema it does not know.
const TruthsSchema = "agent-orange/triagelab/truths@1"

// ── The manifest ────────────────────────────────────────────────────────────

// scenario is one ticket as the manifest declares it.
type scenario struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Queue string `json:"queue,omitempty"`
	Decoy string `json:"decoy,omitempty"`
	N     int    `json:"n,omitempty"`
	// Seed pins this ticket's stream. Absent means "derive from the run seed and
	// the id" — derived from the ID rather than the index so that reordering the
	// manifest cannot silently re-roll every ticket.
	Seed *int64 `json:"seed,omitempty"`
	// Note is free text for the human record (why this seed, what it traps).
	Note string `json:"note,omitempty"`
}

// manifest is the document triagelabgen reads.
type manifest struct {
	// Seed is the run seed; -seed overrides it when given.
	Seed      *int64     `json:"seed,omitempty"`
	Note      string     `json:"note,omitempty"`
	Scenarios []scenario `json:"scenarios"`
}

// ── The truths document ─────────────────────────────────────────────────────

// routing mirrors what the two reference routers found on these exact bytes. It
// is provenance, not truth — `truth.Route` is the truth — but it is what makes
// "this ticket carries its trap" a checkable claim after the fact.
type routing struct {
	Naive   string `json:"naive"`
	Content string `json:"content"`
	// Scores is the keyword margin, e.g. "billing=7 outage=0 access=0". The
	// winner alone says the trap held; the margin says how firmly.
	Scores string `json:"scores"`
}

// truth is one ticket's held-out record.
type truth struct {
	Kind    string `json:"kind"`
	Queue   string `json:"queue,omitempty"`
	Decoy   string `json:"decoy,omitempty"`
	N       int    `json:"n"`
	Seed    int64  `json:"seed"`
	Dataset string `json:"dataset"`
	SHA256  string `json:"sha256"`
	Note    string `json:"note,omitempty"`

	// Route is triagelab's held-out ground truth: where this ticket belongs.
	// One of the specialist queue ids, or "escalate".
	Route       string `json:"route"`
	Explanation string `json:"explanation"`

	Routing routing `json:"routing"`
}

// truths is the whole document: one file, every ticket, keyed by id.
type truths struct {
	Schema string           `json:"schema"`
	Seed   int64            `json:"seed"`
	Note   string           `json:"note,omitempty"`
	Order  []string         `json:"order"`
	Ticket map[string]truth `json:"tickets"`
}

// ── Entry point ─────────────────────────────────────────────────────────────

// Run is the testable entry point: no globals, no os.Exit, every path an error.
func Run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("triagelabgen", flag.ContinueOnError)
	fs.SetOutput(stdout)
	manifestPath := fs.String("manifest", "", "path to the ticket manifest JSON (required)")
	outDir := fs.String("out", "", "directory to write the tickets and truths.json into (required)")
	seed := fs.Int64("seed", 0, "run seed; overrides the manifest's own `seed` when nonzero")
	verify := fs.Bool("verify", true, "check every generated ticket carries its kind's trap, and refuse if one does not")
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
		return fmt.Errorf("no seed: pass -seed or set \"seed\" in the manifest (a scenario run records its seed)")
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

	fmt.Fprintf(stdout, "triagelabgen: %d tickets, seed %d → %s\n", len(doc.Order), runSeed, *outDir)
	for _, id := range doc.Order {
		tr := doc.Ticket[id]
		fmt.Fprintf(stdout, "  %-6s %-10s route=%-9s naive=%-9s seed=%-12d %s\n",
			id, tr.Kind, tr.Route, tr.Routing.Naive, tr.Seed, tr.Dataset)
	}
	return nil
}

// parseManifest decodes the manifest strictly. An unknown field is a refusal,
// not a shrug: a manifest with a mistyped key that generated the wrong tickets
// silently would cost a whole run to discover.
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

// outFile is one rendered ticket.
type outFile struct {
	name string
	data []byte
}

// generate turns a parsed manifest into the truths document and the ticket
// bytes, without touching the filesystem — which is what makes the determinism
// property testable in memory.
func generate(m *manifest, runSeed int64, verify bool) (*truths, []outFile, error) {
	if len(m.Scenarios) == 0 {
		return nil, nil, fmt.Errorf("manifest declares no scenarios")
	}
	doc := &truths{
		Schema: TruthsSchema,
		Seed:   runSeed,
		Note:   m.Note,
		Order:  make([]string, 0, len(m.Scenarios)),
		Ticket: make(map[string]truth, len(m.Scenarios)),
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
		if _, dup := doc.Ticket[id]; dup {
			return nil, nil, fmt.Errorf("scenario %q: duplicate id", id)
		}

		spec := triagelab.Spec{
			Kind:  triagelab.Kind(sc.Kind),
			Queue: triagelab.Queue(sc.Queue),
			Decoy: triagelab.Queue(sc.Decoy),
			N:     sc.N,
		}
		ticketSeed := runSeed
		if sc.Seed != nil {
			ticketSeed = *sc.Seed
		} else {
			ticketSeed = deriveSeed(runSeed, id)
		}

		data, held, err := triagelab.Generate(ticketSeed, spec)
		if err != nil {
			return nil, nil, fmt.Errorf("scenario %q: %w", id, err)
		}
		text := data.Text()
		naive := triagelab.RouteAll(data, triagelab.NaiveKeywordRoute)
		content := triagelab.RouteAll(data, triagelab.ContentRuleRoute)
		if verify {
			if err := verifyTrap(id, data.Spec, held, naive, content, data); err != nil {
				return nil, nil, err
			}
		}
		sum := sha256.Sum256(text)
		name := id + ".txt"
		files = append(files, outFile{name: name, data: text})
		doc.Order = append(doc.Order, id)
		doc.Ticket[id] = truth{
			Kind:        string(data.Spec.Kind),
			Queue:       string(data.Spec.Queue),
			Decoy:       string(data.Spec.Decoy),
			N:           data.Spec.N,
			Seed:        ticketSeed,
			Dataset:     name,
			SHA256:      hex.EncodeToString(sum[:]),
			Note:        sc.Note,
			Route:       held.Route,
			Explanation: held.Explanation,
			Routing: routing{
				Naive:   joinRoutes(naive),
				Content: joinRoutes(content),
				Scores:  triagelab.ScoreLine(string(text)),
			},
		}
	}
	return doc, files, nil
}

// joinRoutes renders a stream's routes for the record. With the usual N=1 it is
// just the route; a longer stream shows every ticket, so a record can never
// claim more agreement than it measured.
func joinRoutes(routes []triagelab.Queue) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, ",")
}

// deriveSeed mixes the run seed with the ticket id. FNV-1a over the id, xored
// into a splitmix-style avalanche of the run seed: stable across Go releases
// (both algorithms are written out here and in triagelab), and stable under
// reordering of the manifest.
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

// verifyTrap checks a generated ticket actually carries its kind's property.
//
// The properties are the ones triagelab's package comment defines, restated as
// sample-level checks over the two reference routers:
//
//	plain      both routers reach the held-out route (the floor: a router that
//	           is wrong everywhere is broken, not fooled)
//	misdirect  the naive keyword router lands on the DECOY and the content-rule
//	           router reaches the truth. Landing merely "somewhere wrong" is not
//	           enough — the trap's claim is that surface vocabulary carries a
//	           keyword router off, and only the decoy proves that
//	ambiguous  the content-rule router escalates, and the naive one does not:
//	           the whole point is that a confident router cannot decline
func verifyTrap(id string, spec triagelab.Spec, held *triagelab.Truth, naive, content []triagelab.Queue, d *triagelab.Dataset) error {
	fail := func(want string) error {
		return fmt.Errorf("ticket %q (%s) does not carry its trap: %s "+
			"(naive=%s content=%s truth=%s; keywords %s). "+
			"Pick another seed — an uncalibrated instrument measures nothing",
			id, spec.Kind, want, joinRoutes(naive), joinRoutes(content), held.Route,
			triagelab.ScoreLine(string(d.Text())))
	}
	route := triagelab.Queue(held.Route)
	switch spec.Kind {
	case triagelab.Plain:
		if !triagelab.Agree(naive, route) || !triagelab.Agree(content, route) {
			return fail("both routers should reach a plain ticket's queue")
		}
	case triagelab.Misdirect:
		if !triagelab.Agree(naive, spec.Decoy) {
			return fail("the naive keyword router should be carried off to the decoy")
		}
		if !triagelab.Agree(content, route) {
			return fail("the content-rule router should reach the stated-fact queue")
		}
	case triagelab.Ambiguous:
		if !triagelab.Agree(content, triagelab.Escalate) {
			return fail("the content-rule router should escalate when no rule fires")
		}
		for _, r := range naive {
			if r == triagelab.Escalate {
				return fail("the naive keyword router should be unable to escalate")
			}
		}
	}
	return nil
}

// sortedIDs is used by the tests and by nothing else; it keeps the assertion
// about map ordering in one place.
func sortedIDs(t *truths) []string {
	out := make([]string, 0, len(t.Ticket))
	for id := range t.Ticket {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func main() {
	if err := Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "triagelabgen:", err)
		os.Exit(1)
	}
}
