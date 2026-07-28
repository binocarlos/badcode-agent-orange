package triagelab

import (
	"regexp"
	"strings"
	"testing"
)

// trapSeeds are PINNED, not sampled. Determinism means the assertions below are
// exact facts about the generator on these exact bytes, re-checkable forever.
// (Unlike hypolab's, these traps are combinatorial rather than statistical:
// there is no α to escape, because no sampling noise decides them. What CAN go
// wrong is an edit to the pools that blurs vocabulary into facts, and that is
// what these tests catch — see TestVocabularyAndFactsAreDisjoint, which is the
// structural reason the seeds below all behave.)
var trapSeeds = []int64{1, 2, 3, 4, 5}

// The headline trap: on misdirection tickets the naive keyword router
// confidently reaches the WRONG queue — the decoy — and the content-rule router
// does not. If this fails, the trap no longer traps and SC-1's whole purpose is
// gone. Every queue×decoy pair, every pinned seed.
func TestMisdirectionActuallyTraps(t *testing.T) {
	tried := 0
	for _, queue := range Specialists {
		for _, decoy := range Specialists {
			if decoy == queue {
				continue
			}
			for _, seed := range trapSeeds {
				tried++
				d, truth, err := Generate(seed, Spec{Kind: Misdirect, Queue: queue, Decoy: decoy})
				if err != nil {
					t.Fatalf("seed %d %s/%s: %v", seed, queue, decoy, err)
				}
				text := d.Tickets[0].Text()
				naive := NaiveKeywordRoute(text)
				if naive != decoy {
					t.Errorf("seed %d %s-behind-%s: the naive router must be fooled into the decoy, got %s (%s)",
						seed, queue, decoy, naive, ScoreLine(text))
				}
				if content := ContentRuleRoute(text); content != queue {
					t.Errorf("seed %d %s-behind-%s: the content-rule router must reach the truth, got %s",
						seed, queue, decoy, content)
				}
				if truth.Route != string(queue) {
					t.Errorf("seed %d %s-behind-%s: held-out route must be %s, got %s", seed, queue, decoy, queue, truth.Route)
				}
				// The margin, not just the winner: a trap that holds by one hit
				// is one pool edit from not holding at all.
				scores := KeywordScores(text)
				if scores[decoy] < 4 {
					t.Errorf("seed %d %s-behind-%s: decoy vocabulary is thin (%s) — the trap is fragile",
						seed, queue, decoy, ScoreLine(text))
				}
				if scores[queue] != 0 {
					t.Errorf("seed %d %s-behind-%s: the TRUE queue's vocabulary leaked into the ticket (%s)",
						seed, queue, decoy, ScoreLine(text))
				}
			}
		}
	}
	if tried != 30 {
		t.Fatalf("expected 6 queue/decoy pairs × 5 seeds = 30 checks, ran %d", tried)
	}
}

// The ambiguity trap: no rule fires, so the honest answer is escalate — and the
// naive router, which cannot represent "I do not know", answers anyway.
func TestAmbiguityRequiresEscalation(t *testing.T) {
	for _, lean := range Specialists {
		for _, seed := range trapSeeds {
			d, truth, err := Generate(seed, Spec{Kind: Ambiguous, Decoy: lean})
			if err != nil {
				t.Fatalf("seed %d lean %s: %v", seed, lean, err)
			}
			text := d.Tickets[0].Text()
			if fired := FiredRules(text); len(fired) != 0 {
				t.Errorf("seed %d lean %s: an ambiguous ticket must fire no content rule, fired %v", seed, lean, fired)
			}
			if got := ContentRuleRoute(text); got != Escalate {
				t.Errorf("seed %d lean %s: the content-rule router must escalate, got %s", seed, lean, got)
			}
			if got := NaiveKeywordRoute(text); got == Escalate {
				t.Fatalf("seed %d lean %s: the naive router escalated — it is not supposed to be able to", seed, lean)
			} else if got != lean {
				t.Errorf("seed %d lean %s: the naive router must guess the leaning queue, got %s (%s)",
					seed, lean, got, ScoreLine(text))
			}
			if truth.Route != string(Escalate) {
				t.Errorf("seed %d lean %s: held-out route must be escalate, got %s", seed, lean, truth.Route)
			}
		}
	}
}

// Plain tickets are the floor: both routers reach the truth. Without this the
// misdirection result would be unreadable — a router that is wrong everywhere
// is not "fooled by the decoy", it is just broken.
func TestPlainTicketsAreRoutableByBoth(t *testing.T) {
	for _, queue := range Specialists {
		for _, seed := range trapSeeds {
			d, truth, err := Generate(seed, Spec{Kind: Plain, Queue: queue})
			if err != nil {
				t.Fatalf("seed %d %s: %v", seed, queue, err)
			}
			text := d.Tickets[0].Text()
			if got := NaiveKeywordRoute(text); got != queue {
				t.Errorf("seed %d %s: the naive router must get a plain ticket right, got %s (%s)",
					seed, queue, got, ScoreLine(text))
			}
			if got := ContentRuleRoute(text); got != queue {
				t.Errorf("seed %d %s: the content-rule router must get a plain ticket right, got %s", seed, queue, got)
			}
			if truth.Route != string(queue) {
				t.Errorf("seed %d %s: held-out route must be %s, got %s", seed, queue, queue, truth.Route)
			}
		}
	}
}

// The structural reason every seed above behaves: the two channels do not
// overlap.
//
//	(a) no queue's vocabulary term appears in any stated-fact template, so the
//	    facts give a keyword router nothing;
//	(b) no vocabulary-bearing pool string fires any content rule, so the surface
//	    gives a rule-following router nothing.
//
// A future edit that softens either half would make the traps quietly weaker
// while every route assertion still passed on the pinned seeds. This is the
// test that fails instead.
func TestVocabularyAndFactsAreDisjoint(t *testing.T) {
	// (a) facts carry no vocabulary.
	var facts []string
	for _, q := range Specialists {
		facts = append(facts, situationSamples(q, 60)...)
	}
	facts = append(facts, ambiguousSamples(60)...)
	for _, fact := range facts {
		for q, res := range vocabularyRe {
			for i, re := range res {
				if re.MatchString(fact) {
					t.Errorf("stated fact %q contains %s vocabulary term %q", fact, q, vocabulary[q][i])
				}
			}
		}
	}

	// (b) surface fires no rule.
	for _, s := range surfaceStrings() {
		if fired := FiredRules(s); len(fired) != 0 {
			t.Errorf("vocabulary-bearing string %q fires content rules %v — it must state no fact", s, fired)
		}
	}

	// And the fact pools do fire the rule they are for, exactly one of them.
	for _, q := range Specialists {
		for _, fact := range situationSamples(q, 60) {
			fired := FiredRules(fact)
			if len(fired) != 1 || fired[0] != q {
				t.Errorf("%s fact %q fires %v, want exactly [%s]", q, fact, fired, q)
			}
		}
	}
	for _, fact := range ambiguousSamples(60) {
		if fired := FiredRules(fact); len(fired) != 0 {
			t.Errorf("ambiguous fact %q fires %v, want nothing", fact, fired)
		}
	}
}

// The naive router's defining property, asserted directly rather than inferred:
// it never escalates, whatever it is given. Everything the ambiguity trap
// measures rests on this.
func TestNaiveRouterNeverEscalates(t *testing.T) {
	for _, text := range []string{"", "   ", "no words at all here", strings.Repeat("x", 500)} {
		if got := NaiveKeywordRoute(text); got == Escalate {
			t.Errorf("naive router escalated on %q", text)
		}
	}
	// The all-zero tie resolves to the first specialist, deterministically.
	if got := NaiveKeywordRoute("nothing relevant"); got != Specialists[0] {
		t.Errorf("the empty tie must break to %s, got %s", Specialists[0], got)
	}
}

// ContentRuleRoute escalates when several rules fire — the definition stated in
// its comment, exercised on hand-built text since the generator never makes one.
func TestSeveralRulesEscalate(t *testing.T) {
	both := "Calls to /v2/reports returned HTTP 503 all morning, and the figure debited is GBP 90.00 instead of the agreed GBP 40.00."
	if fired := FiredRules(both); len(fired) != 2 {
		t.Fatalf("the fixture must fire two rules, fired %v", fired)
	}
	if got := ContentRuleRoute(both); got != Escalate {
		t.Errorf("a ticket stating two routable faults must escalate, got %s", got)
	}
}

// Each content rule, at the boundary. These are the sentences the dispatcher's
// charter states in prose, so a drift here silently changes what the charter
// means.
func TestContentRuleBoundaries(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Queue
	}{
		{"http 500 fires outage", "the call returned HTTP 500 twice", Outage},
		{"http 599 fires outage", "we saw HTTP 599 on every retry", Outage},
		{"http 404 does not", "the call returned HTTP 404 twice", Escalate},
		{"http 200 does not", "the call returned HTTP 200 twice", Escalate},
		{"timeout fires outage", "the export timed out after 30s every time", Outage},
		{"a slow call is not a timeout", "the export was slow after 30s of waiting", Escalate},
		{"money plus discrepancy fires billing", "we paid GBP 90.00 instead of the agreed GBP 40.00", Billing},
		{"money alone does not", "we paid GBP 90.00 on the 3rd", Escalate},
		{"discrepancy alone does not", "the total does not match what we expected", Escalate},
		{"cannot sign in fires access", "two people cannot sign in this morning", Access},
		{"unable to sign in fires access", "she is unable to sign in from the office", Access},
		{"denied permission fires access", "he was denied permission to open the folder", Access},
		{"refused permission fires access", "she was refused permission on the shared drive", Access},
		{"signing in fine does not fire", "everyone can sign in without trouble", Escalate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContentRuleRoute(tc.text); got != tc.want {
				t.Errorf("ContentRuleRoute(%q) = %s, want %s", tc.text, got, tc.want)
			}
		})
	}
}

// Word boundaries, not fragments: a lexicon that matched inside longer words
// would flatter the naive router with hits nobody wrote.
func TestKeywordScoresUseWordBoundaries(t *testing.T) {
	if got := KeywordScores("crossover assorted ssorted")[Access]; got != 0 {
		t.Errorf("`sso` must not match inside another word, got %d hits", got)
	}
	if got := KeywordScores("SSO and sso and Sso")[Access]; got != 3 {
		t.Errorf("`sso` must match case-insensitively three times, got %d", got)
	}
	if got := KeywordScores("locked out again")[Access]; got != 1 {
		t.Errorf("a multi-word term must match, got %d", got)
	}
}

// RouteAll and Agree over a stream — the shape triagelabgen verifies with.
func TestRouteAllOverAStream(t *testing.T) {
	d, truth, err := Generate(9, Spec{Kind: Misdirect, Queue: Access, Decoy: Outage, N: 3})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	content := RouteAll(d, ContentRuleRoute)
	if len(content) != 3 {
		t.Fatalf("want 3 routes, got %d", len(content))
	}
	if !Agree(content, Queue(truth.Route)) {
		t.Errorf("the content-rule router must reach the truth on every ticket, got %v", content)
	}
	if Agree(RouteAll(d, NaiveKeywordRoute), Queue(truth.Route)) {
		t.Error("the naive router must not reach the truth on a misdirection stream")
	}
	if !Agree(RouteAll(d, NaiveKeywordRoute), Outage) {
		t.Error("the naive router must land on the decoy on every ticket of the stream")
	}
	if Agree(nil, Access) {
		t.Error("Agree over an empty list must be false — vacuous agreement is not agreement")
	}
}

// ScoreLine is a record artifact: its key order must not depend on map
// iteration, or two runs of the generator would write different bytes.
func TestScoreLineIsStable(t *testing.T) {
	text := "invoice invoice outage sso"
	want := "billing=2 outage=1 access=1"
	for i := 0; i < 20; i++ {
		if got := ScoreLine(text); got != want {
			t.Fatalf("ScoreLine wobbled on call %d: %q, want %q", i, got, want)
		}
	}
}

// Every vocabulary term must be a legal word-boundary regex and appear in at
// least one surface pool string — a term nothing can produce is a lexicon entry
// that quietly cannot fire.
func TestEveryVocabularyTermIsReachable(t *testing.T) {
	surface := strings.ToLower(strings.Join(surfaceStrings(), " "))
	for q, terms := range vocabulary {
		for _, term := range terms {
			re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
			if !re.MatchString(surface) {
				t.Errorf("%s vocabulary term %q appears in no surface pool string", q, term)
			}
		}
	}
}
