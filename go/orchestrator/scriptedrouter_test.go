package orchestrator

import (
	"context"
	"testing"
)

// ScriptedRouter is a deterministic offline ModelRouter test double (Slice C): a
// tier→Model map. An unmapped tier falls back to a shared empty ScriptedModel (never
// nil) so a missing tier is usable and never panics. It lives in a *_test.go file so
// every test in the package can use it while production code keeps the Slice-B
// TierRouter (router.go); the ModelRouter interface is frozen in contracts.go.
type ScriptedRouter map[ModelTier]Model

func (r ScriptedRouter) For(tier ModelTier) Model {
	if m, ok := r[tier]; ok && m != nil {
		return m
	}
	return &ScriptedModel{} // usable, deterministic ("" output) — never nil
}

var _ ModelRouter = ScriptedRouter(nil)

func TestScriptedRouterResolvesTiers(t *testing.T) {
	full := &ScriptedModel{Default: "opus"}
	mid := &ScriptedModel{Default: "sonnet"}
	r := ScriptedRouter{TierFull: full, TierMid: mid}

	if got, _, _ := r.For(TierFull).Run(context.Background(), "x"); got != "opus" {
		t.Fatalf("full tier = %q", got)
	}
	if got, _, _ := r.For(TierMid).Run(context.Background(), "x"); got != "sonnet" {
		t.Fatalf("mid tier = %q", got)
	}
	// An unmapped tier must not panic — it returns a usable Model.
	if m := r.For(TierCheap); m == nil {
		t.Fatalf("unmapped tier returned nil Model")
	}
}
