package orchestrator

import (
	"context"
	"fmt"
	"strings"
)

// Verdict is frozen in contracts.go (§10b S-1). This file provides the verify-scope
// that produces one.

const verifyTemplate = `You are a verifier. Judge ONLY whether the work below meets the acceptance
criteria. Do not improve it. Reply with exactly "PASS: <one-line reason>" or
"FAIL: <one-line reason>" as the FIRST line of your reply.
ACCEPTANCE CRITERIA:
%s
WORK PRODUCED:
%s`

// Verify runs a SEPARATE scope (ARCHITECTURE §11) that checks a Result against the
// ticket's acceptance criteria — criteria set at plan time by a different scope than
// executed the work. It never edits the work; it only judges it. §10c §H: the
// protocol is structured — the first non-empty reply line decides, by uppercase
// prefix ("PASS"/"FAIL"); anything else is a conservative fail ("unparseable
// verdict"), which never advances work: it burns an attempt and its Reason
// surfaces via AttemptNotes.
func Verify(ctx context.Context, router ModelRouter, tier ModelTier, t Ticket, r Result) (Verdict, error) {
	prompt := fmt.Sprintf(verifyTemplate, t.Acceptance, r.Output)
	out, _, err := router.For(tier).Run(ctx, prompt)
	if err != nil {
		return Verdict{}, fmt.Errorf("verify %s: %w", t.ID, err)
	}
	return parseVerdict(out), nil
}

// parseVerdict reads the first non-empty line: HasPrefix "PASS" → pass,
// "FAIL" → fail, anything else → unparseable (fail). The verdict line itself is
// the Reason, so a FAIL's feedback travels verbatim into AttemptNotes.
func parseVerdict(out string) Verdict {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "PASS"):
			return Verdict{Pass: true, Reason: line}
		case strings.HasPrefix(up, "FAIL"):
			return Verdict{Pass: false, Reason: line}
		}
		return Verdict{Pass: false, Reason: "unparseable verdict: " + line}
	}
	return Verdict{Pass: false, Reason: "unparseable verdict: empty reply"}
}
