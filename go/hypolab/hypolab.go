// Package hypolab generates synthetic hypothesis-investigation datasets with
// HELD-OUT ground truth — the calibration domain of docs/AGENTS_RESEARCH.md §6
// (work plan 13, item L1).
//
// The point of the package is the thing it refuses to do: it never puts the
// answer inside the dataset. Generate returns the dataset and the ground-truth
// verdict as SEPARATE values, and the Dataset type has no truth field at all,
// so nothing that serialises a dataset (CSV for an event, JSON for a fixture)
// can leak the answer by accident. Truth travels only where the harness
// deliberately sends it — outside the project under test, per
// AGENTS_RESEARCH.md §4 (held-out briefs live in the harness, not the
// database).
//
// Every dataset is a two-arm study with one binary confounder — the
// red-jumpers/trains shape from AGENTS_RESEARCH.md §6: does wearing a red
// jumper (treatment) make you late for the train (outcome), when age
// (covariate) may drive both? Four scenario kinds cover the trap taxonomy:
//
//   - RealEffect: the correlation reflects causation at a chosen effect size.
//     Both a naive and a controlled analysis should find it.
//   - PlantedNull: no effect at a decent sample size. Any confirmation is a
//     false positive — the single most important failure to catch (§6: an org
//     that confirms every hypothesis it is handed is broken).
//   - ConfoundTrap: the covariate drives both treatment and outcome, so the
//     naive comparison shows a large effect that controlling for the covariate
//     removes. A naive estimator reaches the WRONG verdict on this data; the
//     stratified one does not. Pinned by test.
//   - Underpowered: a real effect, but a sample too small to honestly confirm.
//     The honest report is "cannot confirm at this sample size" even though
//     the held-out truth says the effect exists.
//
// Determinism is a hard contract: the same seed and spec yield byte-identical
// output. The generator draws from its own splitmix64 stream (no math/rand,
// whose algorithms are not pinned across Go versions; no clocks, no global
// state), and the tests pin golden bytes. Stdlib only, engine-liftable.
package hypolab

import (
	"errors"
	"fmt"
	"strings"
)

// ScenarioKind names one entry of the trap taxonomy.
type ScenarioKind string

const (
	RealEffect   ScenarioKind = "real-effect"
	PlantedNull  ScenarioKind = "planted-null"
	ConfoundTrap ScenarioKind = "confound-trap"
	Underpowered ScenarioKind = "underpowered"
)

// ErrBadSpec wraps every spec-validation failure.
var ErrBadSpec = errors.New("hypolab: bad scenario spec")

// Labels names the columns and cell values of the rendered dataset, so the
// bytes an investigator reads tell a story ("jumper,age_group,late") rather
// than ("t,c,y"). Presentation only: the estimators work on the typed rows.
// Zero-valued fields take the red-jumpers defaults.
type Labels struct {
	TreatmentColumn    string // default "jumper"
	TreatedValue       string // default "red"
	UntreatedValue     string // default "other"
	CovariateColumn    string // default "age_group"
	CovariateHighValue string // default "young"
	CovariateLowValue  string // default "old"
	OutcomeColumn      string // default "late"
	OutcomeYesValue    string // default "yes"
	OutcomeNoValue     string // default "no"
}

// Spec describes one scenario. Zero values mean "use the kind's default":
// N=0 takes the default sample size (2000; 40 for Underpowered — being too
// small is that scenario's point), EffectSize=0 takes 0.15 for the effect
// kinds. A nonzero EffectSize on PlantedNull or ConfoundTrap is refused — a
// planted null with an effect size is a contradiction, not a parameter.
type Spec struct {
	Kind       ScenarioKind
	N          int
	EffectSize float64
	Labels     Labels
}

// Verdict is the held-out ground truth for one generated dataset. It never
// travels with the dataset: the harness decides who sees it, and when.
type Verdict struct {
	Effect      bool    `json:"effect"`
	EffectSize  float64 `json:"effect_size"`
	Explanation string  `json:"explanation"`
}

// Row is one observation, typed. The estimators read these; the label mapping
// is applied only at render time.
type Row struct {
	Treated       bool
	CovariateHigh bool
	Outcome       bool
}

// Dataset is the generated data plus its resolved spec. Deliberately no truth
// field — see the package comment.
type Dataset struct {
	Spec Spec
	Rows []Row
}

// params are the generating probabilities a resolved spec pins down.
type params struct {
	n                 int
	pCovariateHigh    float64 // P(covariate high)
	pTreatedGivenHigh float64 // P(treated | covariate high)
	pTreatedGivenLow  float64 // P(treated | covariate low)
	pOutcomeBase      float64 // P(outcome | untreated, covariate low)
	covariateEffect   float64 // added to outcome probability when covariate high
	treatmentEffect   float64 // added when treated — the causal effect
}

const (
	defaultN             = 2000
	defaultUnderpoweredN = 40
	defaultEffectSize    = 0.15
	// minN keeps every scenario able to populate its four cells at all; the
	// statistics stay honest about small samples, but a dataset of three rows
	// is a typo, not a study.
	minN = 8
	// maxEffectSize keeps pOutcomeBase + covariateEffect + effect below 1.
	maxEffectSize = 0.5
)

// Generate renders the dataset for seed+spec, plus the held-out verdict.
// Same seed and spec always yield byte-identical rows (and therefore CSV and
// JSON); the per-row draw order (covariate, treatment, outcome) is part of
// that contract.
func Generate(seed int64, spec Spec) (*Dataset, *Verdict, error) {
	resolved, p, err := resolveSpec(spec)
	if err != nil {
		return nil, nil, err
	}
	r := newRNG(seed)
	rows := make([]Row, p.n)
	for i := range rows {
		cov := r.chance(p.pCovariateHigh)
		pTreat := p.pTreatedGivenLow
		if cov {
			pTreat = p.pTreatedGivenHigh
		}
		treated := r.chance(pTreat)
		pOut := p.pOutcomeBase
		if cov {
			pOut += p.covariateEffect
		}
		if treated {
			pOut += p.treatmentEffect
		}
		rows[i] = Row{Treated: treated, CovariateHigh: cov, Outcome: r.chance(pOut)}
	}
	return &Dataset{Spec: resolved, Rows: rows}, verdictFor(resolved), nil
}

// resolveSpec validates a spec, fills defaults, and pins the generating
// probabilities for its kind.
func resolveSpec(spec Spec) (Spec, params, error) {
	labels, err := resolveLabels(spec.Labels)
	if err != nil {
		return Spec{}, params{}, err
	}
	spec.Labels = labels

	if spec.N < 0 {
		return Spec{}, params{}, fmt.Errorf("%w: N must not be negative, got %d", ErrBadSpec, spec.N)
	}
	if spec.EffectSize < 0 {
		return Spec{}, params{}, fmt.Errorf("%w: effect size must not be negative, got %g", ErrBadSpec, spec.EffectSize)
	}

	p := params{pCovariateHigh: 0.5}
	switch spec.Kind {
	case RealEffect, Underpowered:
		if spec.EffectSize == 0 {
			spec.EffectSize = defaultEffectSize
		}
		if spec.EffectSize > maxEffectSize {
			return Spec{}, params{}, fmt.Errorf("%w: effect size %g exceeds the maximum %g (outcome probabilities must stay below 1)",
				ErrBadSpec, spec.EffectSize, maxEffectSize)
		}
		if spec.N == 0 {
			spec.N = defaultN
			if spec.Kind == Underpowered {
				spec.N = defaultUnderpoweredN
			}
		}
		// Treatment independent of the covariate: no confounding, so the
		// naive and controlled estimates agree in expectation. The covariate
		// still nudges the outcome, which is what makes controlling for it
		// meaningful rather than a no-op.
		p.pTreatedGivenHigh, p.pTreatedGivenLow = 0.5, 0.5
		p.pOutcomeBase, p.covariateEffect = 0.3, 0.1
		p.treatmentEffect = spec.EffectSize
	case PlantedNull:
		if spec.EffectSize != 0 {
			return Spec{}, params{}, fmt.Errorf("%w: a %s scenario has no effect by definition; effect size %g is a contradiction",
				ErrBadSpec, spec.Kind, spec.EffectSize)
		}
		if spec.N == 0 {
			spec.N = defaultN
		}
		p.pTreatedGivenHigh, p.pTreatedGivenLow = 0.5, 0.5
		p.pOutcomeBase, p.covariateEffect = 0.3, 0.1
	case ConfoundTrap:
		if spec.EffectSize != 0 {
			return Spec{}, params{}, fmt.Errorf("%w: a %s scenario has no effect by definition; effect size %g is a contradiction",
				ErrBadSpec, spec.Kind, spec.EffectSize)
		}
		if spec.N == 0 {
			spec.N = defaultN
		}
		// The trap: the covariate drives BOTH treatment and outcome. High-
		// covariate units are mostly treated (0.8 vs 0.2) and far more likely
		// to show the outcome (+0.5), so the raw treated-vs-untreated gap is
		// large while the causal effect is exactly zero.
		p.pTreatedGivenHigh, p.pTreatedGivenLow = 0.8, 0.2
		p.pOutcomeBase, p.covariateEffect = 0.2, 0.5
	default:
		return Spec{}, params{}, fmt.Errorf("%w: unknown scenario kind %q", ErrBadSpec, spec.Kind)
	}
	if spec.N < minN {
		return Spec{}, params{}, fmt.Errorf("%w: N=%d is below the minimum %d", ErrBadSpec, spec.N, minN)
	}
	p.n = spec.N
	return spec, p, nil
}

// verdictFor writes the held-out truth for a resolved spec. Deterministic:
// the truth is a property of the generating process, never of one sample.
func verdictFor(spec Spec) *Verdict {
	l := spec.Labels
	switch spec.Kind {
	case RealEffect:
		return &Verdict{
			Effect:     true,
			EffectSize: spec.EffectSize,
			Explanation: fmt.Sprintf("%s=%s causally raises the %s rate by %+.2f; both a naive and a %s-controlled analysis should find it.",
				l.TreatmentColumn, l.TreatedValue, l.OutcomeColumn, spec.EffectSize, l.CovariateColumn),
		}
	case PlantedNull:
		return &Verdict{
			Effect: false,
			Explanation: fmt.Sprintf("%s has no effect on %s; any confirmation is a false positive.",
				l.TreatmentColumn, l.OutcomeColumn),
		}
	case ConfoundTrap:
		return &Verdict{
			Effect: false,
			Explanation: fmt.Sprintf("%s has no effect on %s: %s drives both, so the naive comparison shows an effect that controlling for %s removes.",
				l.TreatmentColumn, l.OutcomeColumn, l.CovariateColumn, l.CovariateColumn),
		}
	case Underpowered:
		return &Verdict{
			Effect:     true,
			EffectSize: spec.EffectSize,
			Explanation: fmt.Sprintf("%s=%s really does raise the %s rate by %+.2f, but N=%d is too small to confirm it honestly; the correct report is a null at this sample size.",
				l.TreatmentColumn, l.TreatedValue, l.OutcomeColumn, spec.EffectSize, spec.N),
		}
	}
	// Unreachable: resolveSpec refused every other kind.
	return nil
}

// resolveLabels fills defaults and checks the values survive CSV rendering.
func resolveLabels(l Labels) (Labels, error) {
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	def(&l.TreatmentColumn, "jumper")
	def(&l.TreatedValue, "red")
	def(&l.UntreatedValue, "other")
	def(&l.CovariateColumn, "age_group")
	def(&l.CovariateHighValue, "young")
	def(&l.CovariateLowValue, "old")
	def(&l.OutcomeColumn, "late")
	def(&l.OutcomeYesValue, "yes")
	def(&l.OutcomeNoValue, "no")

	for _, v := range []string{
		l.TreatmentColumn, l.TreatedValue, l.UntreatedValue,
		l.CovariateColumn, l.CovariateHighValue, l.CovariateLowValue,
		l.OutcomeColumn, l.OutcomeYesValue, l.OutcomeNoValue,
	} {
		if strings.ContainsAny(v, ",\"\n\r") {
			return Labels{}, fmt.Errorf("%w: label %q contains CSV metacharacters", ErrBadSpec, v)
		}
	}
	for _, pair := range [][2]string{
		{l.TreatedValue, l.UntreatedValue},
		{l.CovariateHighValue, l.CovariateLowValue},
		{l.OutcomeYesValue, l.OutcomeNoValue},
	} {
		if pair[0] == pair[1] {
			return Labels{}, fmt.Errorf("%w: the two values of a column must differ, both are %q", ErrBadSpec, pair[0])
		}
	}
	return l, nil
}

// CSV renders the dataset as bytes: a header of the three column names, one
// line per row, values through the label mapping. Deterministic by
// construction (row order is generation order; no floats are printed).
func (d *Dataset) CSV() []byte {
	l := d.Spec.Labels
	var sb strings.Builder
	sb.WriteString(l.TreatmentColumn + "," + l.CovariateColumn + "," + l.OutcomeColumn + "\n")
	for _, r := range d.Rows {
		sb.WriteString(pick(r.Treated, l.TreatedValue, l.UntreatedValue))
		sb.WriteByte(',')
		sb.WriteString(pick(r.CovariateHigh, l.CovariateHighValue, l.CovariateLowValue))
		sb.WriteByte(',')
		sb.WriteString(pick(r.Outcome, l.OutcomeYesValue, l.OutcomeNoValue))
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// JSON renders the dataset as {"columns":[...],"rows":[[...],...]} — arrays,
// not maps, so the byte output is deterministic.
func (d *Dataset) JSON() []byte {
	l := d.Spec.Labels
	var sb strings.Builder
	sb.WriteString(`{"columns":[`)
	sb.WriteString(jsonString(l.TreatmentColumn) + "," + jsonString(l.CovariateColumn) + "," + jsonString(l.OutcomeColumn))
	sb.WriteString(`],"rows":[`)
	for i, r := range d.Rows {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("[" +
			jsonString(pick(r.Treated, l.TreatedValue, l.UntreatedValue)) + "," +
			jsonString(pick(r.CovariateHigh, l.CovariateHighValue, l.CovariateLowValue)) + "," +
			jsonString(pick(r.Outcome, l.OutcomeYesValue, l.OutcomeNoValue)) + "]")
	}
	sb.WriteString("]}")
	return []byte(sb.String())
}

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// jsonString quotes s as a JSON string. Labels passed resolveLabels contain no
// quotes or control characters, so plain quoting is exact.
func jsonString(s string) string { return `"` + s + `"` }

// ── The deterministic stream ─────────────────────────────────────────────────

// rng is a splitmix64 stream: tiny, well-mixed, and — unlike math/rand — its
// output is pinned by this file rather than by the Go release notes.
type rng struct{ state uint64 }

func newRNG(seed int64) *rng { return &rng{state: uint64(seed)} }

func (r *rng) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// float64 returns a uniform draw in [0, 1) with 53 bits of precision.
func (r *rng) float64() float64 { return float64(r.next()>>11) / (1 << 53) }

// chance returns true with probability p.
func (r *rng) chance(p float64) bool { return r.float64() < p }
