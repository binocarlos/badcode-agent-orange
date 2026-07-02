package orchestrator

import (
	"context"
	"fmt"
	"strings"
)

// Verdict is frozen in contracts.go (§10b S-1). This file provides the verify-scope
// that produces one.

const verifyTemplate = `You are a verifier. Judge ONLY whether the work below meets the acceptance
criteria. Do not improve it. Reply with PASS or FAIL and one short reason.
ACCEPTANCE CRITERIA:
%s
WORK PRODUCED:
%s`

// Verify runs a SEPARATE scope (ARCHITECTURE §11) that checks a Result against the
// ticket's acceptance criteria — criteria set at plan time by a different scope than
// executed the work. It never edits the work; it only judges it. Pass iff the
// (upper-cased) output contains PASS and not FAIL; the raw text is the Reason.
func Verify(ctx context.Context, router ModelRouter, tier ModelTier, t Ticket, r Result) (Verdict, error) {
	prompt := fmt.Sprintf(verifyTemplate, t.Acceptance, r.Output)
	out, _, err := router.For(tier).Run(ctx, prompt)
	if err != nil {
		return Verdict{}, fmt.Errorf("verify %s: %w", t.ID, err)
	}
	up := strings.ToUpper(out)
	pass := strings.Contains(up, "PASS") && !strings.Contains(up, "FAIL")
	return Verdict{Pass: pass, Reason: out}, nil
}
