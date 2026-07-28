package triagelab

// routers.go — the two REFERENCE ROUTERS, this package's answer to hypolab's
// two estimators.
//
// Both read rendered ticket TEXT — the same bytes a dispatcher receives — and
// neither can see the Spec or the Truth. That is what makes them evidence: a
// router that peeked at a typed field would prove nothing about the trap.
//
//	NaiveKeywordRoute  counts each queue's surface vocabulary and takes the
//	                   argmax. It is a plausible bad procedure, not a straw man:
//	                   it is what "route on what the ticket is about" means if
//	                   you never write down what the queues are FOR. Its defining
//	                   weakness is that it has no way to say "I do not know" —
//	                   an argmax always names a winner.
//
//	ContentRuleRoute   applies the charter's content rules, which are stated
//	                   facts rather than words, and escalates when the number of
//	                   rules that fire is not exactly one.
//
// The dispatcher's charter in go/topology/triagelab.go states the same rules in
// prose. These functions are the harness's reference implementation of that
// prose — used to VERIFY that a generated ticket carries its trap before the
// tokens are spent (cmd/triagelabgen -verify), never to score a worker. Workers
// are scored against the held-out Truth, which is a property of the generating
// process, not of any router.

import (
	"regexp"
	"strconv"
	"strings"
)

// ── The surface vocabulary (what a keyword router indexes) ───────────────────

// vocabulary is each queue's surface lexicon. Every term here is checked by
// test to be absent from every stated-fact template — see content.go's header
// for why that disjointness is the whole instrument.
var vocabulary = map[Queue][]string{
	Billing: {"invoice", "billing", "payment", "refund", "subscription", "receipt", "pricing", "renewal"},
	Outage:  {"outage", "downtime", "unavailable", "degraded", "incident", "maintenance", "slowdown"},
	Access:  {"login", "password", "credentials", "sso", "mfa", "locked out"},
}

// vocabularyRe holds one word-boundary matcher per term, built once. Word
// boundaries rather than plain substrings: "sso" must not be found inside a
// longer word, and a lexicon that matched fragments would flatter the naive
// router by accident.
var vocabularyRe = func() map[Queue][]*regexp.Regexp {
	out := make(map[Queue][]*regexp.Regexp, len(vocabulary))
	for q, terms := range vocabulary {
		res := make([]*regexp.Regexp, 0, len(terms))
		for _, t := range terms {
			res = append(res, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(t)+`\b`))
		}
		out[q] = res
	}
	return out
}()

// KeywordScores counts each queue's vocabulary hits in a ticket. Exported
// because the MARGIN is the honest number to record beside a trap claim: "the
// naive router chose billing" says little; "billing 5, outage 0" says how
// firmly the trap holds on these exact bytes.
func KeywordScores(text string) map[Queue]int {
	scores := make(map[Queue]int, len(Specialists))
	for _, q := range Specialists {
		n := 0
		for _, re := range vocabularyRe[q] {
			n += len(re.FindAllStringIndex(text, -1))
		}
		scores[q] = n
	}
	return scores
}

// NaiveKeywordRoute returns the queue with the most vocabulary hits, breaking
// ties (including the all-zero tie) by the fixed Specialists order.
//
// It NEVER returns escalate. That is the point, not an oversight: a keyword
// count has no representation for "none of these", so an org that routes this
// way answers every ambiguous ticket with a confident guess. The ambiguity trap
// exists to measure exactly that.
func NaiveKeywordRoute(text string) Queue {
	scores := KeywordScores(text)
	best := Specialists[0]
	for _, q := range Specialists[1:] {
		if scores[q] > scores[best] {
			best = q
		}
	}
	return best
}

// ── The content rules (what the charter states) ──────────────────────────────

var (
	// outage: a request that FAILED at the service.
	httpFailureRe = regexp.MustCompile(`(?i)\bHTTP\s+5\d\d\b`)
	timeoutRe     = regexp.MustCompile(`(?i)\btimed out after \d+\s*s\b`)

	// billing: a monetary amount that is WRONG. Both halves are required — an
	// amount on its own is a fact about a bill, not a fault in one.
	moneyRe       = regexp.MustCompile(`(?i)\bGBP\s+\d[\d,]*\.\d{2}\b`)
	discrepancyRe = regexp.MustCompile(`(?i)\b(?:instead of|rather than the agreed|does not match)\b`)

	// access: a person who could not get in.
	signInRe     = regexp.MustCompile(`(?i)\b(?:cannot|could not|can not|unable to) sign in\b`)
	permissionRe = regexp.MustCompile(`(?i)\b(?:denied|refused) permission\b`)
)

// ContentRule reports whether one queue's rule fires on this text.
func ContentRule(q Queue, text string) bool {
	switch q {
	case Outage:
		return httpFailureRe.MatchString(text) || timeoutRe.MatchString(text)
	case Billing:
		return moneyRe.MatchString(text) && discrepancyRe.MatchString(text)
	case Access:
		return signInRe.MatchString(text) || permissionRe.MatchString(text)
	}
	return false
}

// FiredRules lists the queues whose rules the text satisfies, in Specialists
// order.
func FiredRules(text string) []Queue {
	var fired []Queue
	for _, q := range Specialists {
		if ContentRule(q, text) {
			fired = append(fired, q)
		}
	}
	return fired
}

// ContentRuleRoute applies the charter: exactly one rule fired names the queue;
// zero or several mean escalate.
//
// Escalating on SEVERAL is not defensive padding — a ticket that states both an
// outage and a wrong charge is a real thing, and the honest answer is that a
// person should split it. The generator never produces one, which is why the
// case is documented rather than exercised: a rule with no test is a claim, and
// this one is a definition.
func ContentRuleRoute(text string) Queue {
	fired := FiredRules(text)
	if len(fired) == 1 {
		return fired[0]
	}
	return Escalate
}

// ── Convenience over a whole dataset ────────────────────────────────────────

// RouteAll runs one router over every ticket of a dataset, in order.
func RouteAll(d *Dataset, route func(string) Queue) []Queue {
	out := make([]Queue, 0, len(d.Tickets))
	for _, t := range d.Tickets {
		out = append(out, route(t.Text()))
	}
	return out
}

// Agree reports whether every route in the list equals want.
func Agree(routes []Queue, want Queue) bool {
	for _, r := range routes {
		if r != want {
			return false
		}
	}
	return len(routes) > 0
}

// ScoreLine renders a ticket's keyword margin as a stable one-liner, for the
// generator's record and its refusal messages: "billing=5 outage=0 access=0".
// Specialists order, always — a map iteration here would make the record's
// bytes wobble between runs.
func ScoreLine(text string) string {
	scores := KeywordScores(text)
	parts := make([]string, 0, len(Specialists))
	for _, q := range Specialists {
		parts = append(parts, string(q)+"="+strconv.Itoa(scores[q]))
	}
	return strings.Join(parts, " ")
}
