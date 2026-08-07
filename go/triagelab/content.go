package triagelab

// content.go — the text a ticket is built from, and the one discipline that
// governs it: SURFACE VOCABULARY and STATED FACTS come from different pools and
// never overlap.
//
//   - The `surface` pools (subject, opener, context) carry a queue's
//     vocabulary — the nouns a keyword router indexes. They state no fact the
//     charter's rules name.
//   - The `situations` pools carry the facts — an HTTP failure, a wrong amount,
//     a sign-in refusal — and are written to contain NO vocabulary term at all.
//
// That split is what makes the misdirection trap real: a misdirect ticket takes
// its surface from the decoy and its situation from the true queue, so a
// keyword router sees only the decoy and a rule-following router sees only the
// truth. routers_test.go pins the disjointness directly (every vocabulary term
// is absent from every situation template, and no situation-free surface fires
// a content rule), so a future edit that blurs the two fails a test instead of
// quietly softening the instrument.

import "fmt"

// reporters and companies are neutral filler: they carry no vocabulary term and
// state no fact, so they change the bytes without changing either router.
var reporters = []string{
	"Dana Whitlock", "Ivo Brennan", "Marta Sorensen", "Femi Adeyemi",
	"Priya Raghunathan", "Tomas Lindqvist", "Nadia Farouk", "Owen Castellanos",
}

var companies = []string{
	"Harrowgate Foods", "Kelvin and Roe", "Northline Logistics", "Bramble Retail",
	"Quayside Analytics", "Verity Care", "Ostrom Manufacturing", "Ashgrove Media",
}

// surface is one queue's vocabulary-bearing text: what the ticket SOUNDS like.
type surface struct {
	subjects []string
	openers  []string
	contexts []string
}

// surfaces maps a specialist queue to its vocabulary-bearing pools.
var surfaces = map[Queue]surface{
	Billing: {
		subjects: []string{
			"Invoice for this month looks wrong",
			"Question about our subscription renewal",
			"Payment taken twice, refund not received?",
			"Receipt does not match what we expected",
		},
		openers: []string{
			"We went through the billing statement this morning and something is off.",
			"Our finance lead asked me to raise this about the latest invoice.",
			"This is about our subscription: the payment went through, but the numbers surprised us.",
			"Raising this against the renewal invoice rather than anything technical.",
		},
		contexts: []string{
			"Nothing about the subscription has changed since the last renewal.",
			"The billing contact is unchanged and the payment method is the same card.",
			"We have the receipt and the invoice PDF if either helps.",
			"Our pricing tier has been the same all year, as far as we can tell.",
		},
	},
	Outage: {
		subjects: []string{
			"Reporting has been unavailable since this morning",
			"Intermittent outage on the export job",
			"Service unavailable for our whole team",
			"Degraded performance since the maintenance window",
		},
		openers: []string{
			"Half the team is reporting an outage and we are getting nowhere.",
			"We are seeing what looks like downtime on one part of the product.",
			"There is some kind of incident on our side of the product and it started early.",
			"Flagging a slowdown that has turned into something worse over the morning.",
		},
		contexts: []string{
			"Was there a maintenance window overnight? We were not told about one.",
			"The slowdown is not constant: it comes and goes, which makes it hard to describe.",
			"No configuration changed on our side before the degraded behaviour started.",
			"Your status page shows no incident, which is why we are asking rather than waiting.",
		},
	},
	Access: {
		subjects: []string{
			"Login trouble for two of our staff",
			"SSO stopped working for the new starters",
			"Locked out after the password reset",
			"MFA prompt never arrives",
		},
		openers: []string{
			"Two people were locked out this morning and the password reset did not help.",
			"Our SSO connection is behaving oddly for part of the team.",
			"The new starters have credentials issued, but something is not right.",
			"Raising this as a login problem rather than anything to do with the work itself.",
		},
		contexts: []string{
			"The owner has not changed any of the login settings recently.",
			"Everyone affected uses SSO; the ones with a password set directly are fine.",
			"We tried a fresh credentials issue and a new MFA enrolment already.",
			"The password policy was tightened last quarter, but nothing since.",
		},
	},
}

// situationsFor renders the stated fact for one queue — the ONLY part of a
// ticket the charter's content rules can see. Every template here is checked by
// test to contain no vocabulary term of any queue.
func situationsFor(r *rng, q Queue) string {
	switch q {
	case Billing:
		agreed := r.span(40, 160)
		taken := agreed + r.span(10, 90)
		when := r.choose(billingDates)
		switch r.pick(3) {
		case 0:
			return fmt.Sprintf("The amount taken on %s was %s instead of the agreed %s.", when, money(taken), money(agreed))
		case 1:
			return fmt.Sprintf("What was collected on %s came to %s rather than the agreed %s.", when, money(taken), money(agreed))
		default:
			return fmt.Sprintf("The figure debited on %s is %s and does not match the %s we signed for.", when, money(taken), money(agreed))
		}
	case Outage:
		endpoint := r.choose(endpoints)
		code := httpCodes[r.pick(len(httpCodes))]
		at := clock(r)
		switch r.pick(3) {
		case 0:
			return fmt.Sprintf("Every request to %s has come back with HTTP %d since %s.", endpoint, code, at)
		case 1:
			return fmt.Sprintf("Calls to %s have returned HTTP %d for the last %d minutes.", endpoint, code, r.span(15, 90))
		default:
			return fmt.Sprintf("The %s call timed out after %ds on every attempt since %s.", endpoint, r.span(10, 50), at)
		}
	case Access:
		switch r.pick(3) {
		case 0:
			return fmt.Sprintf("%d of their staff cannot sign in at all this morning.", r.span(2, 5))
		case 1:
			return fmt.Sprintf("%d named users could not sign in after the change, and one more was denied permission to open the shared reports.", r.span(2, 4))
		default:
			return "The team lead was refused permission to open the shared workspace, and one colleague cannot sign in."
		}
	}
	// Unreachable: resolveSpec refused every other queue.
	return ""
}

// ambiguousSituation states nothing the charter names. It is not vague for the
// sake of it: every sentence here describes a real complaint that a competent
// desk cannot route without asking, which is exactly the case doctrine WD-3
// says `escalate` exists for.
func ambiguousSituation(r *rng) string {
	switch r.pick(4) {
	case 0:
		return fmt.Sprintf("Since %s something has felt off, but nobody can point at a single thing that broke.", r.choose(weekdays))
	case 1:
		return "Three people mentioned it independently, and none of them can reproduce it on demand."
	case 2:
		return fmt.Sprintf("It has been like this since %s. We have nothing to show you, only the sense that it is worse than last week.", r.choose(weekdays))
	default:
		return "It is intermittent, we have no message to quote back at you, and the people affected keep changing."
	}
}

var closings = []string{
	"Let us know what you need from us.",
	"Happy to jump on a call if that is faster.",
	"We can send screenshots if that helps.",
	"Please come back to us today if you can.",
}

// ambiguousSubjects are deliberately non-committal: a subject line is not a
// fact, and the charter says so.
var ambiguousSubjects = []string{
	"Something is not right since yesterday",
	"Not sure who to send this to",
	"Odd behaviour, hard to pin down",
	"Can someone take a look?",
}

var (
	billingDates = []string{"the 3rd", "the 11th", "Tuesday", "last Friday"}
	weekdays     = []string{"Monday", "Tuesday", "Thursday", "last Wednesday"}
	endpoints    = []string{"/v2/reports", "/v2/export", "/v1/search", "/v2/webhooks"}
	httpCodes    = []int{500, 502, 503, 504}
)

// money formats a whole-pound figure. Integers only: a printed float is a
// portability question nobody needs to answer twice.
func money(v int) string { return fmt.Sprintf("GBP %d.00", v) }

// clock renders a morning time-of-day.
func clock(r *rng) string { return fmt.Sprintf("%02d:%02d", r.span(6, 6), r.pick(60)) }

// generateTicket composes one ticket. The draw order — reporter, company,
// subject, opener, situation, context(s), closing — is part of the determinism
// contract: reordering it re-rolls every recorded dataset.
func generateTicket(r *rng, spec Spec) Ticket {
	reporter := r.choose(reporters)
	company := r.choose(companies)

	// The surface (what it SOUNDS like) comes from the decoy when there is one,
	// and from the true queue otherwise. That single line is the whole trap.
	voice := spec.Queue
	if spec.Decoy != "" {
		voice = spec.Decoy
	}
	s := surfaces[voice]

	subject := r.choose(s.subjects)
	if spec.Kind == Ambiguous {
		subject = r.choose(ambiguousSubjects)
	}
	opener := r.choose(s.openers)

	var situation string
	if spec.Kind == Ambiguous {
		situation = ambiguousSituation(r)
	} else {
		situation = situationsFor(r, spec.Queue)
	}

	// A misdirect or ambiguous ticket carries two context lines rather than one:
	// the decoy needs enough vocabulary to actually win a keyword count, which is
	// the difference between a trap and a story about a trap.
	contexts := []string{r.choose(s.contexts)}
	if spec.Kind != Plain {
		contexts = append(contexts, pickAnother(r, s.contexts, contexts[0]))
	}

	lines := append([]string{opener, situation}, contexts...)
	lines = append(lines, r.choose(closings))
	return Ticket{Subject: subject, Reporter: reporter, Company: company, Lines: lines}
}

// pickAnother draws from pool, avoiding `taken`. It always consumes exactly one
// draw so the stream stays predictable; the walk that follows is deterministic
// arithmetic, not a re-roll.
func pickAnother(r *rng, pool []string, taken string) string {
	i := r.pick(len(pool))
	for n := 0; n < len(pool); n++ {
		candidate := pool[(i+n)%len(pool)]
		if candidate != taken {
			return candidate
		}
	}
	return pool[i]
}

