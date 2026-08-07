package hypolab

import (
	"testing"
)

// The hypothesis-lab e2e fixture, pinned byte-for-byte.
//
// e2e/features/topologies.stack.spec.ts (T13, hypothesis-lab@v1) embeds this
// exact dataset — Generate(13, {ConfoundTrap, N:120}) — as the event payload
// its investigator receives. The e2e cannot call this package, so the two
// copies are kept honest here: this test pins the generator to these bytes,
// and the spec file's comment points back at this test. If the fixture must
// ever change, regenerate BOTH copies together.
//
// N=120 (not the 2000-row default) keeps the event text small while still
// carrying the trap: the property test below proves the naive estimator
// confirms the false effect on these very bytes and the controlled one
// refuses to — i.e. the story the scripted investigator tells in the e2e is
// the story the data actually supports.
const e2eFixtureCSV = `jumper,age_group,late
other,old,no
other,young,yes
other,old,no
other,old,no
other,young,no
other,old,yes
other,young,yes
other,old,no
red,young,yes
red,young,no
red,young,yes
other,young,yes
other,old,no
red,young,yes
red,old,no
other,old,no
red,young,no
other,old,yes
red,young,yes
red,young,yes
red,old,no
red,young,no
red,young,yes
other,old,no
other,young,yes
other,old,no
red,young,yes
red,old,no
other,old,no
red,young,yes
red,young,no
other,young,no
other,old,yes
other,old,yes
other,old,no
red,old,no
other,old,no
other,old,yes
other,young,yes
other,young,yes
red,young,yes
red,young,yes
other,old,no
other,old,no
red,old,no
red,young,no
red,young,yes
red,young,no
red,young,yes
red,old,no
red,young,yes
red,young,no
red,old,no
other,old,no
red,young,no
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,no
red,old,yes
red,old,no
red,young,yes
other,old,no
red,young,yes
red,young,yes
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,yes
other,old,yes
other,young,yes
red,young,yes
other,old,no
red,young,no
other,old,no
other,young,yes
red,young,yes
red,old,no
other,old,no
other,old,no
red,young,yes
other,old,no
red,old,no
red,young,yes
other,old,no
other,old,no
other,old,yes
other,old,no
red,young,yes
red,young,yes
other,old,no
other,old,yes
other,old,no
other,young,yes
other,old,no
other,old,no
other,young,no
red,young,yes
red,young,yes
red,old,no
other,old,no
red,young,yes
other,young,yes
other,old,no
other,old,no
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
red,young,yes
other,old,no
other,young,no
red,young,yes
other,old,no
`

func TestE2EFixtureBytes(t *testing.T) {
	d, v, err := Generate(13, Spec{Kind: ConfoundTrap, N: 120})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := string(d.CSV()); got != e2eFixtureCSV {
		t.Fatalf("the e2e fixture drifted from the generator; regenerate the copy in e2e/features/topologies.stack.spec.ts too:\n%s", got)
	}

	// The fixture must itself carry the trap, or the e2e's scripted story
	// would be about data that does not support it.
	naive := NaiveEstimate(d)
	if !naive.Confirmed {
		t.Errorf("naive estimator must confirm the false effect on the fixture (delta=%.3f z=%.2f)", naive.Delta, naive.Z)
	}
	controlled := ControlledEstimate(d)
	if controlled.Significant {
		t.Errorf("controlled estimator must find no effect on the fixture (delta=%.3f z=%.2f)", controlled.Delta, controlled.Z)
	}
	if v.Effect {
		t.Error("fixture truth must be effect=false")
	}
}
