package topology

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// testQuestions is a fixture covering all three question types plus the
// default/required matrix.
func testQuestions() []Question {
	return []Question{
		{ID: "name", Prompt: "a name", Type: QuestionString, Required: true},
		{ID: "greeting", Prompt: "a greeting", Type: QuestionString, Default: "hello"},
		{ID: "loud", Prompt: "shout?", Type: QuestionBool, Default: false},
		{ID: "flavour", Prompt: "pick one", Type: QuestionChoice, Choices: []string{"sweet", "sour"}, Required: true, Default: "sweet"},
		{ID: "note", Prompt: "optional, no default", Type: QuestionString},
	}
}

func TestResolveAnswers(t *testing.T) {
	tests := []struct {
		name    string
		raw     Answers
		want    Answers // nil ⇒ expect an error
		wantErr string  // substring of the error
	}{
		{
			name: "explicit answers pass through",
			raw:  Answers{"name": "a", "greeting": "hi", "loud": true, "flavour": "sour", "note": "n"},
			want: Answers{"name": "a", "greeting": "hi", "loud": true, "flavour": "sour", "note": "n"},
		},
		{
			name: "defaults fill the gaps; optional-no-default stays absent",
			raw:  Answers{"name": "a"},
			want: Answers{"name": "a", "greeting": "hello", "loud": false, "flavour": "sweet"},
		},
		{
			name: "a non-nil false default is a default, not a gap",
			raw:  Answers{"name": "a", "loud": true},
			want: Answers{"name": "a", "greeting": "hello", "loud": true, "flavour": "sweet"},
		},
		{
			name:    "nil answers still fail the required question",
			raw:     nil,
			wantErr: `question "name" is required`,
		},
		{
			name:    "unknown id is refused, not dropped",
			raw:     Answers{"name": "a", "nmae": "typo"},
			wantErr: `no question with id "nmae"`,
		},
		{
			name:    "string question refuses a bool",
			raw:     Answers{"name": true},
			wantErr: `question "name" wants a string, got bool`,
		},
		{
			name:    "bool question refuses a string",
			raw:     Answers{"name": "a", "loud": "yes"},
			wantErr: `question "loud" wants a bool, got string`,
		},
		{
			name:    "choice question refuses a non-string",
			raw:     Answers{"name": "a", "flavour": 3},
			wantErr: `question "flavour" wants one of [sweet sour], got int`,
		},
		{
			name:    "choice question refuses a string outside the list",
			raw:     Answers{"name": "a", "flavour": "umami"},
			wantErr: `question "flavour" wants one of [sweet sour], got "umami"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAnswers(testQuestions(), tc.raw)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("want error containing %q, got resolved %v", tc.wantErr, got)
				}
				if !errors.Is(err, ErrBadAnswers) {
					t.Fatalf("error does not wrap ErrBadAnswers: %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolved answers:\n got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// ResolveAnswers must hand back a fresh map: mutating the result must not
// reach into the caller's raw answers.
func TestResolveAnswersReturnsFreshMap(t *testing.T) {
	raw := Answers{"name": "a"}
	got, err := ResolveAnswers(testQuestions(), raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got["name"] = "mutated"
	if raw["name"] != "a" {
		t.Fatalf("mutating the resolved map reached the caller's raw answers")
	}
}

// Bad answers must never reach a topology's renderer — Instantiate validates
// first.
func TestInstantiateValidatesBeforeRendering(t *testing.T) {
	rendered := false
	top := &Topology{
		Name: "probe", Version: "v1", Description: "d",
		Questions: []Question{{ID: "name", Prompt: "p", Type: QuestionString, Required: true}},
		Render: func(a Answers) (*Bundle, error) {
			rendered = true
			return &Bundle{}, nil
		},
	}
	if _, err := top.Instantiate(Answers{}); !errors.Is(err, ErrBadAnswers) {
		t.Fatalf("want ErrBadAnswers, got %v", err)
	}
	if rendered {
		t.Fatalf("renderer ran on invalid answers")
	}
}

// A renderer returning (nil, nil) is a definition bug, surfaced as an error
// rather than a caller-side nil dereference.
func TestInstantiateRefusesNilBundle(t *testing.T) {
	top := &Topology{
		Name: "probe", Version: "v1", Description: "d",
		Render: func(Answers) (*Bundle, error) { return nil, nil },
	}
	if _, err := top.Instantiate(nil); !errors.Is(err, ErrRender) {
		t.Fatalf("want ErrRender for nil bundle, got %v", err)
	}
}

func TestInstantiateRefusesNilRenderer(t *testing.T) {
	top := &Topology{Name: "probe", Version: "v1", Description: "d"}
	if _, err := top.Instantiate(nil); !errors.Is(err, ErrRender) {
		t.Fatalf("want ErrRender for missing renderer, got %v", err)
	}
}

// probeAnswers builds a valid answer set for any topology: defaults where they
// exist, a type-appropriate probe value for required questions without one.
func probeAnswers(t *testing.T, top *Topology) Answers {
	t.Helper()
	a := Answers{}
	for _, q := range top.Questions {
		if q.Default != nil || !q.Required {
			continue
		}
		switch q.Type {
		case QuestionString:
			a[q.ID] = "determinism-probe"
		case QuestionBool:
			a[q.ID] = true
		case QuestionChoice:
			a[q.ID] = q.Choices[0]
		}
	}
	return a
}

// The purity contract, pinned as a property over EVERY registered topology:
// same answers → same bundle, on repeated calls, compared both structurally
// and as marshalled bytes (which would catch map-ordering or pointer-identity
// sneaking into the output).
func TestRegisteredRenderersAreDeterministic(t *testing.T) {
	tops := List()
	if len(tops) == 0 {
		t.Fatal("no built-in topologies registered; expected at least solo@v1")
	}
	for _, top := range tops {
		t.Run(top.Ref(), func(t *testing.T) {
			answers := probeAnswers(t, top)
			first, err := top.Instantiate(answers)
			if err != nil {
				t.Fatalf("instantiate: %v", err)
			}
			for i := 0; i < 3; i++ {
				again, err := top.Instantiate(answers)
				if err != nil {
					t.Fatalf("instantiate (repeat %d): %v", i, err)
				}
				if !reflect.DeepEqual(first, again) {
					t.Fatalf("repeat %d: bundle differs structurally:\nfirst %#v\nagain %#v", i, first, again)
				}
				a, err := json.Marshal(first)
				if err != nil {
					t.Fatalf("marshal first: %v", err)
				}
				b, err := json.Marshal(again)
				if err != nil {
					t.Fatalf("marshal again: %v", err)
				}
				if string(a) != string(b) {
					t.Fatalf("repeat %d: bundle differs as bytes:\n%s\nvs\n%s", i, a, b)
				}
			}
		})
	}
}

// Rendered rows must be project-agnostic (apply stamps Project/IDs/timestamps)
// — for every registered topology, not just solo.
func TestRegisteredBundlesAreProjectAgnostic(t *testing.T) {
	for _, top := range List() {
		t.Run(top.Ref(), func(t *testing.T) {
			b, err := top.Instantiate(probeAnswers(t, top))
			if err != nil {
				t.Fatalf("instantiate: %v", err)
			}
			for _, w := range b.Workers {
				if w.Project != "" || w.CreatedAt != 0 || w.UpdatedAt != 0 {
					t.Errorf("worker %q carries apply-time fields: project=%q created=%d updated=%d", w.Name, w.Project, w.CreatedAt, w.UpdatedAt)
				}
			}
			for _, s := range b.Subscriptions {
				if s.Project != "" || s.ID != "" {
					t.Errorf("subscription for %q carries apply-time fields: project=%q id=%q", s.Worker, s.Project, s.ID)
				}
			}
			for _, s := range b.Schedules {
				if s.Project != "" || s.ID != "" {
					t.Errorf("schedule for %q carries apply-time fields: project=%q id=%q", s.Worker, s.Project, s.ID)
				}
			}
			for i, m := range b.MemorySeeds {
				if m.Project != "" || m.ID != "" || m.CreatedBySession != "" {
					t.Errorf("memory seed %d carries apply-time fields: project=%q id=%q session=%q", i, m.Project, m.ID, m.CreatedBySession)
				}
			}
		})
	}
}

// Preconditions are names recorded verbatim (D2) — rendering never invents,
// reorders or deduplicates them into catalogue actions.
func TestPreconditionsAreListedVerbatim(t *testing.T) {
	top := &Topology{
		Name: "needs-things", Version: "v1", Description: "d",
		Render: func(Answers) (*Bundle, error) {
			return &Bundle{
				Workers: []agentdb.Worker{{Name: "scorer", Enabled: true, MaxInstances: 1, Image: "analyst-base"}},
				Preconditions: Preconditions{
					Images: []string{"analyst-base"},
					Skills: []string{"scoring", "reporting"},
				},
			}, nil
		},
	}
	b, err := top.Instantiate(nil)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if want := []string{"analyst-base"}; !reflect.DeepEqual(b.Preconditions.Images, want) {
		t.Fatalf("images: want %v, got %v", want, b.Preconditions.Images)
	}
	if want := []string{"scoring", "reporting"}; !reflect.DeepEqual(b.Preconditions.Skills, want) {
		t.Fatalf("skills: want %v, got %v", want, b.Preconditions.Skills)
	}
}

func TestRef(t *testing.T) {
	top := &Topology{Name: "solo", Version: "v1"}
	if got := top.Ref(); got != "solo@v1" {
		t.Fatalf("ref: want solo@v1, got %s", got)
	}
}
