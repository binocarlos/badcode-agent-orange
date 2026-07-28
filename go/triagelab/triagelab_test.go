package triagelab

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// allKinds is the whole taxonomy; property tests iterate it so a fourth kind
// added later is covered the moment it exists.
var allKinds = []Kind{Plain, Misdirect, Ambiguous}

// specFor builds a valid spec of each kind, so a property test can iterate
// kinds without restating the queue/decoy rules three times.
func specFor(kind Kind) Spec {
	switch kind {
	case Plain:
		return Spec{Kind: Plain, Queue: Outage}
	case Misdirect:
		return Spec{Kind: Misdirect, Queue: Outage, Decoy: Billing}
	default:
		return Spec{Kind: Ambiguous, Decoy: Billing}
	}
}

// The determinism contract: same seed and spec → the same tickets and the same
// bytes; a different seed → different bytes. Both halves matter — a generator
// that ignored its seed would pass the first check forever.
func TestDeterminism(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(string(kind), func(t *testing.T) {
			spec := specFor(kind)
			spec.N = 3
			first, t1, err := Generate(42, spec)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			again, t2, err := Generate(42, spec)
			if err != nil {
				t.Fatalf("generate again: %v", err)
			}
			if !reflect.DeepEqual(first.Tickets, again.Tickets) {
				t.Fatal("same seed+spec produced different tickets")
			}
			if !bytes.Equal(first.Text(), again.Text()) {
				t.Fatal("same seed+spec produced different text bytes")
			}
			if !bytes.Equal(first.JSON(), again.JSON()) {
				t.Fatal("same seed+spec produced different JSON bytes")
			}
			if !reflect.DeepEqual(t1, t2) {
				t.Fatalf("same seed+spec produced different truths: %+v vs %+v", t1, t2)
			}
			other, _, err := Generate(43, spec)
			if err != nil {
				t.Fatalf("generate other seed: %v", err)
			}
			if bytes.Equal(first.Text(), other.Text()) {
				t.Fatal("different seeds produced identical text — the seed is not reaching the stream")
			}
		})
	}
}

// goldenText pins the exact bytes of seed 13, misdirect outage-behind-billing.
// If this test ever fails, the generator's output changed for existing seeds —
// which breaks every recorded experiment and the e2e fixtures. That is a
// contract violation, not a refactoring detail.
const goldenText = `Subject: Invoice for this month looks wrong
Reported by: Owen Castellanos (Kelvin and Roe)

Raising this against the renewal invoice rather than anything technical.

Calls to /v2/webhooks have returned HTTP 500 for the last 28 minutes.

The billing contact is unchanged and the payment method is the same card.

Nothing about the subscription has changed since the last renewal.

We can send screenshots if that helps.
`

const goldenJSON = `{"tickets":[{"subject":"Invoice for this month looks wrong","reporter":"Owen Castellanos","company":"Kelvin and Roe","lines":["Raising this against the renewal invoice rather than anything technical.","Calls to /v2/webhooks have returned HTTP 500 for the last 28 minutes.","The billing contact is unchanged and the payment method is the same card.","Nothing about the subscription has changed since the last renewal.","We can send screenshots if that helps."]}]}`

func TestGoldenBytes(t *testing.T) {
	d, tr, err := Generate(13, Spec{Kind: Misdirect, Queue: Outage, Decoy: Billing})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := string(d.Text()); got != goldenText {
		t.Errorf("text drifted from the golden bytes:\n got:\n%s\nwant:\n%s", got, goldenText)
	}
	if got := string(d.JSON()); got != goldenJSON {
		t.Errorf("JSON drifted from the golden bytes:\n got: %s\nwant: %s", got, goldenJSON)
	}
	if tr.Route != string(Outage) {
		t.Errorf("held-out route: want outage, got %s", tr.Route)
	}
	if tr.Decoy != string(Billing) {
		t.Errorf("held-out decoy: want billing, got %s", tr.Decoy)
	}
}

// Defaults and the stream shape.
func TestSpecDefaults(t *testing.T) {
	for _, kind := range allKinds {
		d, tr, err := Generate(1, specFor(kind))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(d.Tickets) != 1 {
			t.Errorf("%s: default N: want 1 ticket, got %d", kind, len(d.Tickets))
		}
		if tr.Tickets != 1 {
			t.Errorf("%s: truth must record the stream length, got %d", kind, tr.Tickets)
		}
		if d.Spec.N != 1 {
			t.Errorf("%s: the resolved spec must carry N=1, got %d", kind, d.Spec.N)
		}
	}
	spec := specFor(Plain)
	spec.N = 4
	d, tr, err := Generate(11, spec)
	if err != nil {
		t.Fatalf("N=4: %v", err)
	}
	if len(d.Tickets) != 4 || tr.Tickets != 4 {
		t.Fatalf("N=4: want 4 tickets and truth.tickets=4, got %d and %d", len(d.Tickets), tr.Tickets)
	}
	// A stream is a stream: two tickets from one dataset must not be the same
	// bytes, or "N tickets" would mean "one ticket, N times".
	if d.Tickets[0].Text() == d.Tickets[1].Text() {
		t.Error("consecutive tickets in one dataset are byte-identical — the stream is not advancing")
	}
	// ...but they DO share one route, because they share one generating process.
	for _, ticket := range d.Tickets {
		if got := ContentRuleRoute(ticket.Text()); string(got) != tr.Route {
			t.Errorf("ticket in an %s dataset routes to %s, truth says %s", d.Spec.Kind, got, tr.Route)
		}
	}
}

// The separation contract: nothing a Dataset renders contains the route.
//
// The structural half is compile-time — Dataset has no truth field, and neither
// rendering touches Spec. This pins the bytes too, over every kind, queue and
// decoy combination the generator can produce.
func TestDatasetBytesCarryNoTruth(t *testing.T) {
	// Vocabulary a truth channel would use and a ticket never may. Queue NAMES
	// are deliberately absent from this list: "billing" is legitimate ticket
	// vocabulary, and banning it would ban the trap. What must never appear is
	// the language of the ANSWER.
	leaks := []string{
		"ROUTE-TO", "route", "Route", "escalate", "Escalate",
		"decoy", "misdirect", "ambiguous", "correct queue", "ground truth",
	}
	for _, spec := range everySpec() {
		spec.N = 2
		d, tr, err := Generate(5, spec)
		if err != nil {
			t.Fatalf("%+v: %v", spec, err)
		}
		for name, rendered := range map[string][]byte{"text": d.Text(), "JSON": d.JSON()} {
			for _, leak := range leaks {
				if bytes.Contains(rendered, []byte(leak)) {
					t.Errorf("%s %s: %s bytes contain %q — the answer is leaking into the dataset",
						spec.Kind, spec.Queue, name, leak)
				}
			}
			if bytes.Contains(rendered, []byte(tr.Explanation)) {
				t.Errorf("%s %s: %s bytes contain the truth explanation", spec.Kind, spec.Queue, name)
			}
			// The kind is a property of the generating process and no business
			// of the reader's.
			if bytes.Contains(rendered, []byte(string(spec.Kind))) {
				t.Errorf("%s %s: %s bytes name the ticket kind", spec.Kind, spec.Queue, name)
			}
		}
	}
}

// everySpec enumerates every legal spec: three plain, six misdirect, three
// ambiguous. Property tests iterate it so no combination is quietly untested.
func everySpec() []Spec {
	var out []Spec
	for _, q := range Specialists {
		out = append(out, Spec{Kind: Plain, Queue: q})
		out = append(out, Spec{Kind: Ambiguous, Decoy: q})
		for _, d := range Specialists {
			if d != q {
				out = append(out, Spec{Kind: Misdirect, Queue: q, Decoy: d})
			}
		}
	}
	return out
}

// The rendered pools must survive this package's own hand-rolled JSON quoting
// (which, like hypolab's, assumes clean strings) and its plain-text layout.
func TestPoolsAreRenderSafe(t *testing.T) {
	for _, s := range poolStrings() {
		if strings.ContainsAny(s, "\"\\\n\r\t") {
			t.Errorf("pool string %q contains a character the renderers do not escape", s)
		}
		if strings.TrimSpace(s) == "" {
			t.Errorf("pool holds a blank string")
		}
	}
}

func TestSpecRefusals(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"unknown kind", Spec{Kind: "poetry", Queue: Billing}, `unknown ticket kind "poetry"`},
		{"empty kind", Spec{Queue: Billing}, "unknown ticket kind"},
		{"negative N", Spec{Kind: Plain, Queue: Billing, N: -1}, "must not be negative"},
		{"N above maximum", Spec{Kind: Plain, Queue: Billing, N: 51}, "exceeds the maximum"},
		{"plain with no queue", Spec{Kind: Plain}, "queue must be one of"},
		{"plain with a decoy", Spec{Kind: Plain, Queue: Billing, Decoy: Outage}, "no decoy by definition"},
		{"queue is escalate", Spec{Kind: Plain, Queue: Escalate}, "an answer, not an input"},
		{"misdirect with no decoy", Spec{Kind: Misdirect, Queue: Billing}, "decoy must be one of"},
		{"misdirect decoy equals queue", Spec{Kind: Misdirect, Queue: Billing, Decoy: Billing}, "needs queue and decoy to differ"},
		{"ambiguous with a queue", Spec{Kind: Ambiguous, Queue: Billing, Decoy: Outage}, "states no routable fact"},
		{"ambiguous with no decoy", Spec{Kind: Ambiguous}, "decoy must be one of"},
		{"unknown queue", Spec{Kind: Plain, Queue: "shipping"}, `got "shipping"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Generate(1, tc.spec)
			if err == nil {
				t.Fatalf("want error containing %q, got a dataset", tc.wantErr)
			}
			if !errors.Is(err, ErrBadSpec) {
				t.Fatalf("error does not wrap ErrBadSpec: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// The truth's explanation must speak about the trap it planted — the record is
// read by humans after a run and "misdirect" alone explains nothing.
func TestTruthExplainsItself(t *testing.T) {
	for _, spec := range everySpec() {
		_, tr, err := Generate(3, spec)
		if err != nil {
			t.Fatalf("%+v: %v", spec, err)
		}
		if tr.Explanation == "" {
			t.Errorf("%+v: truth carries no explanation", spec)
		}
		if spec.Kind == Ambiguous {
			if tr.Route != string(Escalate) {
				t.Errorf("an ambiguous ticket's route must be escalate, got %s", tr.Route)
			}
			if !strings.Contains(tr.Explanation, "escalate") {
				t.Errorf("the ambiguous explanation must say what the right answer is: %q", tr.Explanation)
			}
		}
		if spec.Kind == Misdirect && !strings.Contains(tr.Explanation, string(spec.Decoy)) {
			t.Errorf("a misdirect explanation must name the decoy: %q", tr.Explanation)
		}
	}
}

// poolStrings is every literal string content.go can put into a ticket. Test
// scaffolding — nothing at runtime reads it.
func poolStrings() []string {
	out := append([]string{}, reporters...)
	out = append(out, companies...)
	out = append(out, closings...)
	out = append(out, ambiguousSubjects...)
	out = append(out, billingDates...)
	out = append(out, weekdays...)
	out = append(out, endpoints...)
	for _, q := range Specialists {
		s := surfaces[q]
		out = append(out, s.subjects...)
		out = append(out, s.openers...)
		out = append(out, s.contexts...)
	}
	return out
}

// surfaceStrings is only the vocabulary-bearing pools — the text that must
// never fire a content rule.
func surfaceStrings() []string {
	out := append([]string{}, closings...)
	out = append(out, ambiguousSubjects...)
	for _, q := range Specialists {
		s := surfaces[q]
		out = append(out, s.subjects...)
		out = append(out, s.openers...)
		out = append(out, s.contexts...)
	}
	return out
}

// situationSamples walks a seeded stream to render many stated-fact lines per
// queue, so the disjointness test does not have to restate the templates.
func situationSamples(q Queue, draws int) []string {
	out := make([]string, 0, draws)
	r := newRNG(20260728)
	for i := 0; i < draws; i++ {
		out = append(out, situationsFor(r, q))
	}
	return out
}

// ambiguousSamples is situationSamples for the kind that has no queue.
func ambiguousSamples(draws int) []string {
	out := make([]string, 0, draws)
	r := newRNG(20260728)
	for i := 0; i < draws; i++ {
		out = append(out, ambiguousSituation(r))
	}
	return out
}
