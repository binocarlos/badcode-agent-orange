// Package topology — org charts as data (docs/product/10-topology-library.md,
// work plan 13 item T1).
//
// A topology is a parameterised generator over the EXISTING configuration
// surface: its nodes are workers, its edges are subscriptions, its clock is
// schedules and its shared state is labelled memory. Nothing here invents a
// storage type — Render produces rows of the agentdb types verbatim, and
// applying a topology (T2, not this package) writes them through the same
// config-logged store mutations a human would use.
//
// Two decisions bind this package (work plan 13, D1/D2):
//
//   - D1: topologies are code-defined, built-in and versioned. The registry is
//     code; there is no user-authorable topology row. The shape (an exported
//     Render func over plain data) is chosen so authorability can be added
//     later without rework.
//   - D2: a topology references images and skills BY NAME as preconditions.
//     Rendering records the names; it never creates catalogue entries, and
//     applying with unmet preconditions is T2's loud failure, not ours.
//
// The renderer is PURE: no I/O, no clock, no randomness. Same answers, same
// bundle — a property the tests pin explicitly. That is also why rendered rows
// carry no Project, no IDs and no timestamps: those are apply-time facts about
// one project at one moment, and stamping them here would either break purity
// (uuids, clocks) or bind a topology to a single project.
package topology

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ErrBadAnswers wraps every answer-validation failure so a future HTTP layer
// (T2) can map it to a 400 without string-matching.
var ErrBadAnswers = errors.New("topology: bad answers")

// ErrRender wraps failures inside a topology's renderer (e.g. an answer that
// type-checks but is semantically unusable, like a non-kebab-case worker
// name). Also a 400 at the T2 seam: the caller's answers were the problem.
var ErrRender = errors.New("topology: render failed")

// QuestionType is the closed vocabulary of answer shapes. There is
// deliberately no number type yet: no seed topology needs one, and a closed
// small vocabulary keeps answer validation total.
type QuestionType string

const (
	// QuestionString takes any string; the renderer owns semantic checks.
	QuestionString QuestionType = "string"
	// QuestionBool takes true/false.
	QuestionBool QuestionType = "bool"
	// QuestionChoice takes one string out of Choices, exactly.
	QuestionChoice QuestionType = "choice"
)

// Question is one thing a topology asks before it can render. IDs are
// kebab-case, unique within the topology, and are the keys of Answers.
type Question struct {
	ID      string       `json:"id"`
	Prompt  string       `json:"prompt"`
	Type    QuestionType `json:"type"`
	Choices []string     `json:"choices,omitempty"` // QuestionChoice only
	// Default, when non-nil, is used for an unanswered question — which also
	// satisfies Required. Its dynamic type must match Type (string for
	// string/choice, bool for bool); Register enforces that at boot.
	Default  any  `json:"default,omitempty"`
	Required bool `json:"required"`
}

// Answers maps question ID → answer. Values are the JSON-decoded shapes a
// future HTTP caller would produce: string for string/choice questions, bool
// for bool questions.
type Answers map[string]any

// Preconditions are the images and skills a topology needs to already exist,
// referenced by name (D2). Rendering only records them; T2's apply refuses to
// proceed while any are missing.
type Preconditions struct {
	Images []string `json:"images,omitempty"`
	Skills []string `json:"skills,omitempty"`
}

// Bundle is one rendered org chart: rows of the existing agentdb config types,
// verbatim. Rows are project-agnostic — Project, IDs and timestamps are left
// zero for apply (T2) to stamp — so one bundle can seed any project.
//
// SettingsPatch is nil when the topology imposes nothing on project settings.
// When non-nil, zero-valued fields mean "leave the project's current value";
// merging is apply's business (the settings store itself has no patch
// semantics — §5 — so T2 reads, overlays, and writes whole).
type Bundle struct {
	Workers       []agentdb.Worker         `json:"workers"`
	Subscriptions []agentdb.Subscription   `json:"subscriptions"`
	Schedules     []agentdb.Schedule       `json:"schedules"`
	SettingsPatch *agentdb.ProjectSettings `json:"settings_patch,omitempty"`
	MemorySeeds   []agentdb.Memory         `json:"memory_seeds,omitempty"`
	Preconditions Preconditions            `json:"preconditions"`
}

// RenderFunc turns RESOLVED answers (validated, defaults applied) into a
// bundle. It must be pure: no I/O, no clock, no randomness — the same answers
// must yield the same bundle, byte for byte. Callers go through
// Topology.Instantiate, which resolves the answers first; a RenderFunc may
// therefore assume every required question is answered with the right type,
// but still owns semantic checks (wrap those in ErrRender).
type RenderFunc func(Answers) (*Bundle, error)

// Topology is one named, versioned org chart (D1: defined in code). Identity
// is Name+Version, written name@version — the same naming posture as images
// and skills rather than a third scheme.
type Topology struct {
	Name        string     `json:"name"`    // kebab-case
	Version     string     `json:"version"` // "v1", "v2", ...
	Description string     `json:"description"`
	Questions   []Question `json:"questions"`
	// Render is exported so a later authorability layer can construct
	// topologies outside this package; today every value lives here.
	Render RenderFunc `json:"-"`
}

// Ref is the topology's name@version identity string, e.g. "solo@v1" — the
// value T2's `topology.applied` config event names.
func (t *Topology) Ref() string { return t.Name + "@" + t.Version }

// Instantiate validates raw answers against the question list, applies
// defaults, and renders. This is the only path callers should use — calling
// Render directly skips validation.
func (t *Topology) Instantiate(raw Answers) (*Bundle, error) {
	if t.Render == nil {
		return nil, fmt.Errorf("%w: topology %s has no renderer", ErrRender, t.Ref())
	}
	resolved, err := ResolveAnswers(t.Questions, raw)
	if err != nil {
		return nil, err
	}
	b, err := t.Render(resolved)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("%w: topology %s rendered a nil bundle without an error", ErrRender, t.Ref())
	}
	return b, nil
}

// ResolveAnswers checks raw against the question list and returns a fresh map
// with defaults filled in. Errors (all wrapping ErrBadAnswers):
//
//   - an answer whose ID matches no question (a typo must not vanish silently);
//   - a required question with no answer and no default;
//   - an answer whose dynamic type does not match the question's Type;
//   - a choice answer not in the question's Choices.
//
// An optional question with no answer and no default is simply absent from the
// result — renderers use the comma-ok lookup for those.
func ResolveAnswers(qs []Question, raw Answers) (Answers, error) {
	byID := make(map[string]Question, len(qs))
	for _, q := range qs {
		byID[q.ID] = q
	}
	for id := range raw {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("%w: no question with id %q", ErrBadAnswers, id)
		}
	}
	resolved := make(Answers, len(qs))
	for _, q := range qs {
		v, answered := raw[q.ID]
		if !answered {
			if q.Default != nil {
				resolved[q.ID] = q.Default
				continue
			}
			if q.Required {
				return nil, fmt.Errorf("%w: question %q is required and unanswered", ErrBadAnswers, q.ID)
			}
			continue
		}
		if err := checkAnswerType(q, v); err != nil {
			return nil, err
		}
		resolved[q.ID] = v
	}
	return resolved, nil
}

// checkAnswerType enforces the Type vocabulary on one supplied answer.
func checkAnswerType(q Question, v any) error {
	switch q.Type {
	case QuestionString:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%w: question %q wants a string, got %T", ErrBadAnswers, q.ID, v)
		}
	case QuestionBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%w: question %q wants a bool, got %T", ErrBadAnswers, q.ID, v)
		}
	case QuestionChoice:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%w: question %q wants one of %v, got %T", ErrBadAnswers, q.ID, q.Choices, v)
		}
		for _, c := range q.Choices {
			if s == c {
				return nil
			}
		}
		return fmt.Errorf("%w: question %q wants one of %v, got %q", ErrBadAnswers, q.ID, q.Choices, s)
	default:
		// Unreachable through the registry (validateTopology refuses unknown
		// types at boot), kept so a hand-built Topology fails loudly too.
		return fmt.Errorf("%w: question %q has unknown type %q", ErrBadAnswers, q.ID, q.Type)
	}
	return nil
}

// ── Definition-time validation (what Register enforces, at boot) ─────────────

// topologyNameRe is the same kebab-case identity rule workers use — one naming
// posture across the product layer.
var topologyNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// topologyVersionRe pins versions to v1, v2, ... so List can order them
// numerically and "@v1" reads the same everywhere.
var topologyVersionRe = regexp.MustCompile(`^v[1-9][0-9]*$`)

// validateTopology is the boot-time contract on a definition. A violation is a
// programming error in a built-in, so Register turns it into a panic (same
// posture as mcpserver.register).
func validateTopology(t *Topology) error {
	if t == nil {
		return errors.New("topology is nil")
	}
	if !topologyNameRe.MatchString(t.Name) {
		return fmt.Errorf("topology name %q is not kebab-case", t.Name)
	}
	if !topologyVersionRe.MatchString(t.Version) {
		return fmt.Errorf("topology %q version %q is not of the form v1, v2, ...", t.Name, t.Version)
	}
	if t.Description == "" {
		return fmt.Errorf("topology %s has no description", t.Ref())
	}
	if t.Render == nil {
		return fmt.Errorf("topology %s has no renderer", t.Ref())
	}
	seen := map[string]bool{}
	for i, q := range t.Questions {
		if !topologyNameRe.MatchString(q.ID) {
			return fmt.Errorf("topology %s question %d: id %q is not kebab-case", t.Ref(), i, q.ID)
		}
		if seen[q.ID] {
			return fmt.Errorf("topology %s: duplicate question id %q", t.Ref(), q.ID)
		}
		seen[q.ID] = true
		if q.Prompt == "" {
			return fmt.Errorf("topology %s question %q has no prompt", t.Ref(), q.ID)
		}
		switch q.Type {
		case QuestionString, QuestionBool:
			if len(q.Choices) > 0 {
				return fmt.Errorf("topology %s question %q: choices on a %s question", t.Ref(), q.ID, q.Type)
			}
		case QuestionChoice:
			if len(q.Choices) == 0 {
				return fmt.Errorf("topology %s question %q: choice question with no choices", t.Ref(), q.ID)
			}
		default:
			return fmt.Errorf("topology %s question %q has unknown type %q", t.Ref(), q.ID, q.Type)
		}
		if q.Default != nil {
			if err := checkAnswerType(q, q.Default); err != nil {
				return fmt.Errorf("topology %s question %q: default does not satisfy the question: %v", t.Ref(), q.ID, err)
			}
		}
	}
	return nil
}

// sortTopologies orders by name, then numeric version — the List contract.
func sortTopologies(ts []*Topology) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Name != ts[j].Name {
			return ts[i].Name < ts[j].Name
		}
		return versionNum(ts[i].Version) < versionNum(ts[j].Version)
	})
}

// versionNum extracts the integer from a validated "vN" string. Registered
// topologies always validate, so a parse failure cannot happen through the
// registry; 0 is returned for a hand-built stray rather than panicking a sort.
func versionNum(v string) int {
	n := 0
	for _, r := range v[1:] {
		n = n*10 + int(r-'0')
	}
	return n
}
