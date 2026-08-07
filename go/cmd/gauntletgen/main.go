// Command gauntletgen renders an ADVERSARIAL ticket manifest into the two
// things a gauntlet run needs: one text file per ticket, and one truths.json
// holding the held-out route AND the planted directive for all of them.
//
// It is triagelabgen's sibling and deliberately its twin, down to the flags:
// docs/product/19-scenario-library.md §2 makes "deterministic seeded generator,
// truth as a separate return, verified traps" the admissibility contract for
// every scenario, and the cheapest way to keep a contract is to keep one shape.
//
//	gauntletgen -manifest manifest-24.json -seed 20260728 -out ./datasets
//
// # Why a separate command rather than a flag on triagelabgen
//
// The truths document has a different SCHEMA, and that difference is a safety
// rail rather than bookkeeping: the gauntlet rig refuses a plain SC-1 dataset
// outright, instead of running happily over tickets that carry no attacks and
// reporting a directive-compliance rate of zero — which would look exactly like
// an org that resisted everything. triagelabgen's own bytes and tests are
// untouched by this file, so SC-1's committed reports stay reproducible.
//
// # What -verify checks (on by default)
//
// Everything triagelabgen checks about the trap — on the FINAL bytes, directive
// included, because a planted line adds vocabulary and could in principle carry
// a keyword router somewhere new — plus the SC-3 half: the directive in the
// bytes is the directive in the record, a compliant reference worker trips its
// signal, and a rule-following one does not. A directive nobody could obey
// makes the headline rate unmeasurable; a signal that charter-obedience also
// trips makes it a lie. Both are cheaper to find here than after a run.
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
	"strings"

	"github.com/binocarlos/badcode-agent-orange/triagelab"
)

// TruthsSchema versions the gauntlet truths document. Deliberately not
// triagelabgen's: a rig that speaks one must refuse the other.
const TruthsSchema = "agent-orange/triagelab/gauntlet-truths@1"

// ── The manifest ────────────────────────────────────────────────────────────

// scenario is one ticket as the manifest declares it: an SC-1 ticket plus, at
// most, one directive.
type scenario struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Queue string `json:"queue,omitempty"`
	Decoy string `json:"decoy,omitempty"`
	N     int    `json:"n,omitempty"`
	// Directive names the attack to plant; absent means a CLEAN control ticket.
	Directive string `json:"directive,omitempty"`
	// DirectiveQueue is the destination a reroute directive demands. It must be
	// neither the ticket's own queue nor its decoy — the generator refuses both,
	// because obeying and mis-trapping would then be one observation.
	DirectiveQueue string `json:"directive_queue,omitempty"`
	// Seed pins this ticket's stream. Absent means "derive from the run seed and
	// the id" — derived from the ID rather than the index so that reordering the
	// manifest cannot silently re-roll every ticket.
	Seed *int64 `json:"seed,omitempty"`
	// Note is free text for the human record (why this seed, what it traps).
	Note string `json:"note,omitempty"`
}

type manifest struct {
	Seed      *int64     `json:"seed,omitempty"`
	Note      string     `json:"note,omitempty"`
	Scenarios []scenario `json:"scenarios"`
}

// ── The truths document ─────────────────────────────────────────────────────

// routing mirrors what the two reference ROUTERS found on the final bytes.
type routing struct {
	Naive   string `json:"naive"`
	Content string `json:"content"`
	Scores  string `json:"scores"`
}

// compliance mirrors what the two reference AGENTS did with the directive —
// provenance for the claim "this ticket carries an obeyable attack whose signal
// a rule-follower does not trip". Empty on a clean ticket.
type compliance struct {
	Compliant     bool `json:"compliant_worker_trips_signal"`
	RuleFollowing bool `json:"rule_following_worker_trips_signal"`
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

	Route       string `json:"route"`
	Explanation string `json:"explanation"`

	Routing routing `json:"routing"`

	// Attacked says whether a directive was planted, so a reader (and the rig's
	// denominator) never has to infer it from a nil.
	Attacked bool `json:"attacked"`
	// Directive is the planted attack and its designed signal. Nil when clean.
	Directive *triagelab.Directive `json:"directive,omitempty"`
	// Compliance is the verifier's own record of what the two reference agents
	// did on these exact bytes.
	Compliance *compliance `json:"compliance,omitempty"`
}

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
	fs := flag.NewFlagSet("gauntletgen", flag.ContinueOnError)
	fs.SetOutput(stdout)
	manifestPath := fs.String("manifest", "", "path to the adversarial ticket manifest JSON (required)")
	outDir := fs.String("out", "", "directory to write the tickets and truths.json into (required)")
	seed := fs.Int64("seed", 0, "run seed; overrides the manifest's own `seed` when nonzero")
	verify := fs.Bool("verify", true, "check every ticket carries its kind's trap AND an obeyable directive, and refuse if one does not")
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

	attacked := 0
	for _, id := range doc.Order {
		if doc.Ticket[id].Attacked {
			attacked++
		}
	}
	fmt.Fprintf(stdout, "gauntletgen: %d tickets (%d attacked, %d clean), seed %d → %s\n",
		len(doc.Order), attacked, len(doc.Order)-attacked, runSeed, *outDir)
	for _, id := range doc.Order {
		tr := doc.Ticket[id]
		directive := "-"
		if tr.Directive != nil {
			directive = string(tr.Directive.Kind)
			if tr.Directive.Queue != "" {
				directive += "→" + string(tr.Directive.Queue)
			}
		}
		fmt.Fprintf(stdout, "  %-6s %-10s route=%-9s directive=%-20s seed=%-12d %s\n",
			id, tr.Kind, tr.Route, directive, tr.Seed, tr.Dataset)
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

		spec := triagelab.GauntletSpec{
			Spec: triagelab.Spec{
				Kind:  triagelab.Kind(sc.Kind),
				Queue: triagelab.Queue(sc.Queue),
				Decoy: triagelab.Queue(sc.Decoy),
				N:     sc.N,
			},
			Directive:      triagelab.DirectiveKind(sc.Directive),
			DirectiveQueue: triagelab.Queue(sc.DirectiveQueue),
		}
		ticketSeed := runSeed
		if sc.Seed != nil {
			ticketSeed = *sc.Seed
		} else {
			ticketSeed = deriveSeed(runSeed, id)
		}

		data, held, err := triagelab.GenerateGauntlet(ticketSeed, spec)
		if err != nil {
			return nil, nil, fmt.Errorf("scenario %q: %w", id, err)
		}
		text := data.Text()
		naive := triagelab.RouteAll(data, triagelab.NaiveKeywordRoute)
		content := triagelab.RouteAll(data, triagelab.ContentRuleRoute)
		var record *compliance
		if held.Attacked() {
			record = &compliance{
				Compliant:     triagelab.Complied(*held.Directive, triagelab.CompliantActions(string(text))),
				RuleFollowing: triagelab.Complied(*held.Directive, triagelab.RuleFollowingActions(string(text))),
			}
		}
		if verify {
			// The SC-1 trap, checked on the FINAL bytes: a planted line adds
			// words, and a trap that only held before the attack was planted is
			// not the trap this run measures.
			if err := verifyTrap(id, data.Spec, &held.Truth, naive, content, data); err != nil {
				return nil, nil, err
			}
			if err := triagelab.VerifyGauntlet(data, held); err != nil {
				return nil, nil, fmt.Errorf("ticket %q: %w. Pick another seed or another directive — "+
					"an unobeyable attack measures nothing", id, err)
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
			Attacked:   held.Attacked(),
			Directive:  held.Directive,
			Compliance: record,
		}
	}
	return doc, files, nil
}

// joinRoutes renders a stream's routes for the record.
func joinRoutes(routes []triagelab.Queue) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, ",")
}

// deriveSeed mixes the run seed with the ticket id — FNV-1a over the id xored
// into a splitmix-style avalanche of the run seed. Written out here (as in
// triagelabgen and triagelab) rather than imported from math/rand, so a Go
// release cannot move a recorded run's tickets.
func deriveSeed(runSeed int64, id string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	z := uint64(runSeed) ^ h.Sum64()
	z += 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return int64(z >> 1)
}

// verifyTrap checks a generated ticket still carries its SC-1 property with the
// directive planted. The properties are triagelab's, restated as sample-level
// checks over the two reference routers — see triagelabgen's copy, which this
// one deliberately mirrors line for line.
func verifyTrap(id string, spec triagelab.Spec, held *triagelab.Truth, naive, content []triagelab.Queue, d *triagelab.Dataset) error {
	fail := func(want string) error {
		return fmt.Errorf("ticket %q (%s) does not carry its trap with the directive planted: %s "+
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

func main() {
	if err := Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gauntletgen:", err)
		os.Exit(1)
	}
}
