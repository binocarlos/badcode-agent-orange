package orchestrator

import "testing"

// ModelTier + TierFull/TierMid/TierCheap are declared in contracts.go (§3). This
// test just pins the frozen string values so a rename fails loudly.
func TestModelTierFrozenStrings(t *testing.T) {
	cases := map[ModelTier]string{
		TierFull:  "full",
		TierMid:   "mid",
		TierCheap: "cheap",
	}
	for tier, want := range cases {
		if string(tier) != want {
			t.Fatalf("tier %v = %q, want %q (frozen by contracts §3)", tier, string(tier), want)
		}
	}
}
