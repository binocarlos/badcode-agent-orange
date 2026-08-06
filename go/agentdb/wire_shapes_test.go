package agentdb

// The wire-shape guard (doc 22 RD29, work-plan item B3).
//
// The four structs below are the ones the operator console mirrors field by
// field in the browser, and the console writes them back WHOLE:
// `PutProjectSettings` / `PutWorker` assign every column, and the browser
// builds its body from `coerceProjectSettings` / `coerceWorker`, which
// construct a fresh object from an EXPLICIT field list. An unknown key is
// therefore dropped on read and written back as its zero value on the next
// save — so adding a field to one of these structs silently arms a data-loss
// bug that fires the next time a human presses Save, and no test on either
// side used to enumerate keys.
//
// The guard is the `token_usage.go` discipline: ONE captured artefact, two
// readers. `web/src/wire-shapes.json` holds the sorted JSON key list of each
// struct, CAPTURED from the writer (encoding/json, through the struct tags —
// never hand-authored). This test is the engine-side reader; the browser-side
// reader is `web/src/wireShapes.test.ts`, which asserts the key set of
// `Object.keys(coerceX(...))`. Adding a field on either side fails the other
// side's test.
//
// Regenerate after a deliberate field change:
//
//	cd go && AGENTKIT_UPDATE_WIRE_SHAPES=1 go test -count=1 ./agentdb -run TestWireShapesMatchCapturedFile
//
// then run the web suite and make `coerceX` agree. A regeneration that is not
// accompanied by a browser change is exactly the bug this file exists to catch,
// so treat a red vitest after regenerating as the guard working.
//
// `-count=1` IS LOAD-BEARING IN THAT RECIPE, and the reason is a trap worth
// stating: the artefact lives OUTSIDE this Go module, and `go test`'s cache does
// not track reads of it (measured — editing only `wire-shapes.json` still gets
// `ok (cached)`). So a *hand-edit of the JSON alone* can go unnoticed here, and
// a regeneration run can be served from cache and never write the file at all.
// Neither weakens the direction the guard exists for — a change to a struct in
// this package changes this package's inputs, so the cache is invalidated and
// the test really runs — and the browser reader (vitest, which does not cache
// test results) catches the hand-edit case. But run this test with `-count=1`
// whenever you are reasoning about the file's contents.
//
// Why a fully-POPULATED struct is marshalled rather than a zero-valued one:
// `Worker.Briefing` and `Schedule.TargetSession` carry `omitempty`, so a zero
// value omits precisely the two keys most likely to be missed by a mirror.
// Every field is reflectively set to a non-zero value first, so the captured
// list is "every key this struct can put on the wire".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// wireShapePath is the captured artefact, relative to this package directory.
const wireShapePath = "../../web/src/wire-shapes.json"

// wireShapeStructs is the closed set of guarded structs. Adding an entry here
// (and regenerating) is how a new mirrored struct joins the guard; the test
// fails if the file and this map disagree about which structs exist.
func wireShapeStructs() map[string]any {
	return map[string]any{
		"ProjectSettings": ProjectSettings{},
		"Subscription":    Subscription{},
		"Schedule":        Schedule{},
		"Worker":          Worker{},
	}
}

// wireShapeFile is the on-disk shape. `README` rides in the file because JSON
// has no comments and the regeneration recipe has to travel with the artefact.
type wireShapeFile struct {
	README []string            `json:"_README"`
	Shapes map[string][]string `json:"shapes"`
}

var wireShapeREADME = []string{
	"GENERATED — do not hand-edit. Sorted JSON key list of each Go struct the",
	"operator console mirrors, captured from encoding/json via the struct tags.",
	"Regenerate: cd go && AGENTKIT_UPDATE_WIRE_SHAPES=1 go test -count=1 ./agentdb -run TestWireShapesMatchCapturedFile",
	"(-count=1 matters: this file is outside the Go module, so go test's cache does not track it.)",
	"Readers: go/agentdb/wire_shapes_test.go (engine) and web/src/wireShapes.test.ts (browser).",
	"Why it exists: the console PUTs these objects whole and rebuilds them from an",
	"explicit field list, so a field present on one side and absent on the other is",
	"written back as its zero value the next time a human saves. See doc 22 RD29.",
	"Keys are captured from a FULLY POPULATED struct, so `omitempty` fields",
	"(Worker.briefing, Schedule.target_session) are included.",
}

// fillNonZero sets every settable field of v to a non-zero value, recursively,
// so that marshalling it cannot omit an `omitempty` key.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillNonZero(p.Elem())
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillNonZero(key)
		elem := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(elem)
		m.SetMapIndex(key, elem)
		v.Set(m)
	case reflect.Interface:
		// `any` fields/values: any non-nil concrete value will do.
		v.Set(reflect.ValueOf("x"))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}
	}
}

// wireKeys marshals a fully populated copy of v and returns its sorted JSON
// key set — the exact set of keys the engine can put on the wire for it.
func wireKeys(v any) ([]string, error) {
	filled := reflect.New(reflect.TypeOf(v)).Elem()
	fillNonZero(filled)
	raw, err := json.Marshal(filled.Interface())
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("marshalled to a non-object: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func TestWireShapesMatchCapturedFile(t *testing.T) {
	live := wireShapeFile{README: wireShapeREADME, Shapes: map[string][]string{}}
	for name, zero := range wireShapeStructs() {
		keys, err := wireKeys(zero)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		live.Shapes[name] = keys
	}

	if os.Getenv("AGENTKIT_UPDATE_WIRE_SHAPES") != "" {
		// Encoder rather than MarshalIndent: MarshalIndent escapes HTML, which
		// turns the ampersands in the regeneration recipe into & escapes —
		// unreadable in the one place the recipe has to be readable.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(live); err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Clean(wireShapePath), buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write %s: %v", wireShapePath, err)
		}
		t.Logf("regenerated %s — now run the web suite; a red wireShapes.test.ts "+
			"means a browser mirror still has to be updated", wireShapePath)
		return
	}

	data, err := os.ReadFile(filepath.Clean(wireShapePath))
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with AGENTKIT_UPDATE_WIRE_SHAPES=1)", wireShapePath, err)
	}
	var onDisk wireShapeFile
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("parse %s: %v", wireShapePath, err)
	}

	for name := range onDisk.Shapes {
		if _, ok := live.Shapes[name]; !ok {
			t.Errorf("%s carries a shape %q that no guarded struct produces — "+
				"remove it, or add the struct to wireShapeStructs()", wireShapePath, name)
		}
	}
	for name, want := range live.Shapes {
		got, ok := onDisk.Shapes[name]
		if !ok {
			t.Errorf("%s has no shape for %s — regenerate with "+
				"AGENTKIT_UPDATE_WIRE_SHAPES=1 and update its browser mirror", wireShapePath, name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s wire keys drifted.\n on disk: %v\n  in Go: %v\n"+
				"A field changed on the engine side. The console PUTs this object WHOLE, so until "+
				"the browser mirror (coerce%s) carries the same keys, the next human save writes the "+
				"new field back as its zero value. Regenerate with AGENTKIT_UPDATE_WIRE_SHAPES=1 "+
				"and fix web/src accordingly.", name, got, want, name)
		}
	}
}
