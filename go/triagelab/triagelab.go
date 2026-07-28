// Package triagelab generates synthetic support tickets whose correct ROUTE is
// held out — the coordination scenario of docs/product/19-scenario-library.md
// §3 (SC-1), built to hypolab's shape verbatim (work plan 13, item SC1).
//
// The point of the package is the thing it refuses to do: it never puts the
// route inside the ticket. Generate returns the dataset and the ground-truth
// route as SEPARATE values, and the Dataset type has no truth field at all, so
// nothing that serialises a dataset (text for an event, JSON for a fixture) can
// leak the answer by accident. Truth travels only where the harness
// deliberately sends it — outside the project under test, per
// docs/AGENTS_RESEARCH.md §4.
//
// # The domain, and why it splits vocabulary from facts
//
// A ticket belongs to one of three specialist queues, or to `escalate`. Which
// one is decided by CONTENT RULES — stated facts the dispatcher's charter
// carries, and which the reference router in routers.go applies:
//
//	outage   the ticket states a request that FAILED at the service: an HTTP
//	         status in the 500s, or a stated timeout.
//	billing  the ticket states a monetary amount that is WRONG: a figure
//	         alongside a discrepancy ("instead of", "does not match", ...).
//	access   the ticket states a person who could not SIGN IN, or who was
//	         DENIED PERMISSION.
//	escalate no rule fires (or more than one does): the honest answer is to
//	         escalate, never to guess.
//
// Separately, each queue has a SURFACE VOCABULARY — the nouns a keyword router
// would index (invoice, outage, sso, ...). The two are deliberately disjoint:
// no rule phrase is a vocabulary term and no vocabulary term appears in a rule
// phrase. That disjointness is what makes the traps below real rather than
// rhetorical, and routers_test.go pins it.
//
// # The trap taxonomy
//
//   - Plain: vocabulary and stated facts agree. Both the naive keyword router
//     and the content-rule router reach the correct queue.
//   - Misdirect: the vocabulary points at a DECOY queue while the stated facts
//     belong to another — a message full of billing words that is actually an
//     outage report. A naive keyword router misroutes it; a content-rule router
//     does not. This is the headline trap, the exact analogue of hypolab's
//     confound. Pinned by test on documented seeds.
//   - Ambiguous: nothing the charter names is stated at all. The correct answer
//     is `escalate`; any confident route is wrong. A keyword router, which has
//     no way to say "I don't know", falls for every one of these.
//
// Determinism is a hard contract: the same seed and spec yield byte-identical
// output. The generator draws from its own splitmix64 stream (no math/rand,
// whose algorithms are not pinned across Go versions; no clocks, no global
// state), and the tests pin golden bytes. Stdlib only, engine-liftable.
package triagelab

import (
	"errors"
	"fmt"
	"strings"
)

// Kind names one entry of the trap taxonomy.
type Kind string

const (
	// Plain: surface vocabulary and stated facts agree.
	Plain Kind = "plain"
	// Misdirect: vocabulary points at Decoy, the facts belong to Queue.
	Misdirect Kind = "misdirect"
	// Ambiguous: no content rule fires; the correct route is escalate.
	Ambiguous Kind = "ambiguous"
)

// Queue is a routing destination: one of three specialist queues, or escalate.
//
// These are QUEUE IDs, not worker names. A topology's specialists are named by
// its operator; the harness maps a queue id onto whichever worker holds it, so
// the generated truth never depends on one org chart's naming.
type Queue string

const (
	Billing  Queue = "billing"
	Outage   Queue = "outage"
	Access   Queue = "access"
	Escalate Queue = "escalate"
)

// Specialists is the closed set of specialist queues, in the fixed order the
// naive router breaks ties by and the report prints.
var Specialists = []Queue{Billing, Outage, Access}

// ErrBadSpec wraps every spec-validation failure.
var ErrBadSpec = errors.New("triagelab: bad ticket spec")

// Spec describes one dataset: a stream of N tickets drawn from one generating
// process. Zero values mean "use the kind's default": N=0 takes 1.
//
//   - Plain     needs Queue (a specialist queue); Decoy must be empty.
//   - Misdirect needs Queue and Decoy, and they must differ.
//   - Ambiguous needs Decoy (whose vocabulary the ticket leans on) and must not
//     name a Queue — escalate is not something the generator is told, it is
//     what having no stated fact MEANS.
type Spec struct {
	Kind  Kind
	Queue Queue
	Decoy Queue
	N     int
}

// Truth is the held-out ground truth for one generated dataset. It never
// travels with the dataset: the harness decides who sees it, and when.
//
// Every ticket in one dataset comes from one generating process and therefore
// shares one route — the same relationship hypolab's rows have to their spec.
type Truth struct {
	Route       string `json:"route"`
	Kind        string `json:"kind"`
	Decoy       string `json:"decoy,omitempty"`
	Tickets     int    `json:"tickets"`
	Explanation string `json:"explanation"`
}

// Ticket is one generated support ticket. Structured so the estimators in
// routers.go read rendered TEXT (which is what a dispatcher receives) rather
// than a typed field a router could cheat off.
type Ticket struct {
	Subject  string
	Reporter string
	Company  string
	Lines    []string
}

// Dataset is the generated stream plus its resolved spec. Deliberately no truth
// field — see the package comment.
type Dataset struct {
	Spec    Spec
	Tickets []Ticket
}

const (
	defaultN = 1
	// maxN keeps a single dataset from becoming a corpus; a stream of tickets
	// belongs in a manifest, one dataset per ticket, so the record names every
	// seed (the runbook §2 discipline hypolabgen established).
	maxN = 50
)

// Generate renders the dataset for seed+spec, plus the held-out truth. Same
// seed and spec always yield byte-identical tickets (and therefore text and
// JSON); the per-ticket draw order is part of that contract.
func Generate(seed int64, spec Spec) (*Dataset, *Truth, error) {
	resolved, err := resolveSpec(spec)
	if err != nil {
		return nil, nil, err
	}
	r := newRNG(seed)
	tickets := make([]Ticket, resolved.N)
	for i := range tickets {
		tickets[i] = generateTicket(r, resolved)
	}
	return &Dataset{Spec: resolved, Tickets: tickets}, truthFor(resolved), nil
}

// resolveSpec validates a spec and fills defaults.
func resolveSpec(spec Spec) (Spec, error) {
	if spec.N < 0 {
		return Spec{}, fmt.Errorf("%w: N must not be negative, got %d", ErrBadSpec, spec.N)
	}
	if spec.N == 0 {
		spec.N = defaultN
	}
	if spec.N > maxN {
		return Spec{}, fmt.Errorf("%w: N=%d exceeds the maximum %d (one dataset per ticket keeps every seed in the record)",
			ErrBadSpec, spec.N, maxN)
	}
	switch spec.Kind {
	case Plain:
		if err := mustBeSpecialist("queue", spec.Queue); err != nil {
			return Spec{}, err
		}
		if spec.Decoy != "" {
			return Spec{}, fmt.Errorf("%w: a %s ticket has no decoy by definition; decoy %q is a contradiction",
				ErrBadSpec, spec.Kind, spec.Decoy)
		}
	case Misdirect:
		if err := mustBeSpecialist("queue", spec.Queue); err != nil {
			return Spec{}, err
		}
		if err := mustBeSpecialist("decoy", spec.Decoy); err != nil {
			return Spec{}, err
		}
		if spec.Queue == spec.Decoy {
			return Spec{}, fmt.Errorf("%w: a misdirection ticket needs queue and decoy to differ; both are %q",
				ErrBadSpec, spec.Queue)
		}
	case Ambiguous:
		if spec.Queue != "" {
			return Spec{}, fmt.Errorf("%w: an %s ticket states no routable fact, so it has no queue; %q is a contradiction",
				ErrBadSpec, spec.Kind, spec.Queue)
		}
		if err := mustBeSpecialist("decoy", spec.Decoy); err != nil {
			return Spec{}, err
		}
	default:
		return Spec{}, fmt.Errorf("%w: unknown ticket kind %q", ErrBadSpec, spec.Kind)
	}
	return spec, nil
}

// mustBeSpecialist refuses anything but one of the three specialist queues —
// escalate included, which is an ANSWER and never an input.
func mustBeSpecialist(field string, q Queue) error {
	for _, s := range Specialists {
		if q == s {
			return nil
		}
	}
	if q == Escalate {
		return fmt.Errorf("%w: %s must name a specialist queue; %q is an answer, not an input",
			ErrBadSpec, field, Escalate)
	}
	return fmt.Errorf("%w: %s must be one of %v, got %q", ErrBadSpec, field, Specialists, q)
}

// truthFor writes the held-out truth for a resolved spec. Deterministic: the
// truth is a property of the generating process, never of one sample.
func truthFor(spec Spec) *Truth {
	switch spec.Kind {
	case Plain:
		return &Truth{
			Route: string(spec.Queue), Kind: string(spec.Kind), Tickets: spec.N,
			Explanation: fmt.Sprintf("the ticket states a fact the %s rule names, and its vocabulary points the same way; "+
				"a keyword router and a rule-following router agree here.", spec.Queue),
		}
	case Misdirect:
		return &Truth{
			Route: string(spec.Queue), Kind: string(spec.Kind), Decoy: string(spec.Decoy), Tickets: spec.N,
			Explanation: fmt.Sprintf("the ticket reads like %s work and is %s work: its vocabulary points at %s while the "+
				"only fact the charter names is the %s one. Routing on words gives %s; routing on the stated facts gives %s.",
				spec.Decoy, spec.Queue, spec.Decoy, spec.Queue, spec.Decoy, spec.Queue),
		}
	case Ambiguous:
		return &Truth{
			Route: string(Escalate), Kind: string(spec.Kind), Decoy: string(spec.Decoy), Tickets: spec.N,
			Explanation: fmt.Sprintf("the ticket states none of the facts the charter names — it only sounds like %s work. "+
				"The honest answer is to escalate; any confident queue is a guess dressed as a decision.", spec.Decoy),
		}
	}
	// Unreachable: resolveSpec refused every other kind.
	return nil
}

// ── Rendering ────────────────────────────────────────────────────────────────

// Text renders the whole stream: one ticket per block, separated by a rule
// line. Deterministic by construction (ticket order is generation order).
func (d *Dataset) Text() []byte {
	var sb strings.Builder
	for i, t := range d.Tickets {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		sb.WriteString(t.Text())
	}
	return []byte(sb.String())
}

// Text renders one ticket as a support desk would show it.
func (t Ticket) Text() string {
	var sb strings.Builder
	sb.WriteString("Subject: " + t.Subject + "\n")
	sb.WriteString("Reported by: " + t.Reporter + " (" + t.Company + ")\n")
	for _, line := range t.Lines {
		sb.WriteString("\n" + line + "\n")
	}
	return sb.String()
}

// JSON renders the dataset as {"tickets":[{...},...]} — arrays and a fixed key
// order, so the byte output is deterministic. No spec, no truth.
func (d *Dataset) JSON() []byte {
	var sb strings.Builder
	sb.WriteString(`{"tickets":[`)
	for i, t := range d.Tickets {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"subject":` + jsonString(t.Subject))
		sb.WriteString(`,"reporter":` + jsonString(t.Reporter))
		sb.WriteString(`,"company":` + jsonString(t.Company))
		sb.WriteString(`,"lines":[`)
		for j, line := range t.Lines {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(jsonString(line))
		}
		sb.WriteString("]}")
	}
	sb.WriteString("]}")
	return []byte(sb.String())
}

// jsonString quotes s as a JSON string. The generated pools hold no quotes,
// backslashes or control characters (pinned by test), so plain quoting is exact.
func jsonString(s string) string { return `"` + s + `"` }

// ── The deterministic stream ─────────────────────────────────────────────────

// rng is a splitmix64 stream: tiny, well-mixed, and — unlike math/rand — its
// output is pinned by this file rather than by the Go release notes. Copied
// deliberately from hypolab rather than shared: two generators that drift apart
// are a smaller problem than one that cannot be changed without invalidating
// every recorded run of the other.
type rng struct{ state uint64 }

func newRNG(seed int64) *rng { return &rng{state: uint64(seed)} }

func (r *rng) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// pick returns an index in [0, n). n must be positive (every pool is a non-empty
// literal in this package, checked by test).
func (r *rng) pick(n int) int { return int(r.next() % uint64(n)) }

// choose returns one element of a non-empty pool.
func (r *rng) choose(pool []string) string { return pool[r.pick(len(pool))] }

// span returns lo + pick(width).
func (r *rng) span(lo, width int) int { return lo + r.pick(width) }
