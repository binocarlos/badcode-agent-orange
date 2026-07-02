package agentdb

import (
	"encoding/json"
	"strings"
	"testing"
)

func foldFragOp(kind OpKind, id, body string) Op {
	b, _ := json.Marshal(BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return Op{Op: kind, EntityType: "prompt_fragment", EntityID: id, Body: b}
}

func foldRev(id string, seq int64, ops ...Op) BoardRevision {
	return BoardRevision{ID: id, Seq: seq, Status: "applied", Ops: OpsToJSON(ops)}
}

// TestFoldFragmentsFoldsAndPins is the one shared fold (§10c I-4): add/update/
// remove fold in seq order, stop at the target revision, stamp LastChangedIn.
func TestFoldFragmentsFoldsAndPins(t *testing.T) {
	revs := []BoardRevision{
		foldRev("r1", 1, foldFragOp(OpAdd, "routing", "Be basic.")),
		foldRev("r2", 2, foldFragOp(OpAdd, "role", "Writer.")),
		foldRev("r3", 3, foldFragOp(OpUpdate, "routing", "Be clever.")),
		foldRev("r4", 4, Op{Op: OpRemove, EntityType: "prompt_fragment", EntityID: "role"}),
	}

	// As of r3: both fragments, routing updated in r3.
	b, err := FoldFragments(revs, "r3")
	if err != nil {
		t.Fatalf("fold r3: %v", err)
	}
	if b.Revision != "r3" || len(b.Fragments) != 2 {
		t.Fatalf("fold r3 wrong: %+v", b)
	}
	// Fragments sorted by ID: role, routing.
	if b.Fragments[0].ID != "role" || b.Fragments[1].Body != "Be clever." {
		t.Fatalf("fold r3 fragments wrong: %+v", b.Fragments)
	}
	if b.Fragments[1].LastChangedIn != "r3" || b.Fragments[0].LastChangedIn != "r2" {
		t.Fatalf("LastChangedIn wrong: %+v", b.Fragments)
	}

	// As of r4: role removed.
	b4, err := FoldFragments(revs, "r4")
	if err != nil {
		t.Fatalf("fold r4: %v", err)
	}
	if len(b4.Fragments) != 1 || b4.Fragments[0].ID != "routing" {
		t.Fatalf("remove not folded: %+v", b4.Fragments)
	}

	// Out-of-order input still folds by ascending Seq.
	shuffled := []BoardRevision{revs[2], revs[0], revs[3], revs[1]}
	bs, err := FoldFragments(shuffled, "r3")
	if err != nil || len(bs.Fragments) != 2 || bs.Fragments[1].Body != "Be clever." {
		t.Fatalf("out-of-order fold wrong: %+v err=%v", bs, err)
	}
}

func TestFoldFragmentsUnknownRevision(t *testing.T) {
	revs := []BoardRevision{foldRev("r1", 1, foldFragOp(OpAdd, "g", "x"))}
	if _, err := FoldFragments(revs, "r99"); err == nil || !strings.Contains(err.Error(), "r99") {
		t.Fatalf("expected unknown-revision error, got %v", err)
	}
}

// Ops with other entity types remain SKIPPED by the fold (historical logs stay
// readable) — Append rejects them at write time (RequireFragmentOps).
func TestFoldFragmentsSkipsOtherEntityTypes(t *testing.T) {
	revs := []BoardRevision{
		foldRev("r1", 1,
			foldFragOp(OpAdd, "g", "keep"),
			Op{Op: OpAdd, EntityType: "staff", EntityID: "s1", Body: json.RawMessage(`{"id":"s1"}`)},
		),
	}
	b, err := FoldFragments(revs, "r1")
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if len(b.Fragments) != 1 || b.Fragments[0].ID != "g" {
		t.Fatalf("non-fragment op leaked into fold: %+v", b.Fragments)
	}
}

// RequireFragmentOps is the fail-loud Append guard (§10c I-4): a changeset with
// any non-prompt_fragment op is rejected, naming the entity type.
func TestRequireFragmentOps(t *testing.T) {
	ok := []Op{foldFragOp(OpAdd, "g", "x")}
	if err := RequireFragmentOps(ok); err != nil {
		t.Fatalf("fragment-only ops rejected: %v", err)
	}
	bad := []Op{foldFragOp(OpAdd, "g", "x"), {Op: OpAdd, EntityType: "staff", EntityID: "s1"}}
	err := RequireFragmentOps(bad)
	if err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("expected fail-loud error naming entity type, got %v", err)
	}
}

func TestJSONHelpers(t *testing.T) {
	if got := string(JSONBytes(nil)); got != "[]" {
		t.Fatalf("JSONBytes(nil) = %q, want []", got)
	}
	if got := string(JSONBytes(JSONArray(`[1]`))); got != "[1]" {
		t.Fatalf("JSONBytes = %q, want [1]", got)
	}
	var ops []Op
	if err := json.Unmarshal(JSONBytes(OpsToJSON([]Op{foldFragOp(OpAdd, "g", "x")})), &ops); err != nil || len(ops) != 1 {
		t.Fatalf("OpsToJSON round-trip wrong: %v %+v", err, ops)
	}
}
