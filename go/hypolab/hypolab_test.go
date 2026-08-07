package hypolab

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// allKinds is the whole taxonomy; property tests iterate it so a fifth kind
// added later is covered the moment it exists.
var allKinds = []ScenarioKind{RealEffect, PlantedNull, ConfoundTrap, Underpowered}

// The determinism contract: same seed and spec → the same rows and the same
// bytes; a different seed → different bytes. Both halves matter — a generator
// that ignored its seed would pass the first check forever.
func TestDeterminism(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(string(kind), func(t *testing.T) {
			spec := Spec{Kind: kind}
			first, v1, err := Generate(42, spec)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			again, v2, err := Generate(42, spec)
			if err != nil {
				t.Fatalf("generate again: %v", err)
			}
			if !reflect.DeepEqual(first.Rows, again.Rows) {
				t.Fatal("same seed+spec produced different rows")
			}
			if !bytes.Equal(first.CSV(), again.CSV()) {
				t.Fatal("same seed+spec produced different CSV bytes")
			}
			if !bytes.Equal(first.JSON(), again.JSON()) {
				t.Fatal("same seed+spec produced different JSON bytes")
			}
			if !reflect.DeepEqual(v1, v2) {
				t.Fatalf("same seed+spec produced different verdicts: %+v vs %+v", v1, v2)
			}
			other, _, err := Generate(43, spec)
			if err != nil {
				t.Fatalf("generate other seed: %v", err)
			}
			if bytes.Equal(first.CSV(), other.CSV()) {
				t.Fatal("different seeds produced identical CSV — the seed is not reaching the stream")
			}
		})
	}
}

// goldenCSV pins the exact bytes of seed 13, confound-trap, N=12. If this
// test ever fails, the generator's output changed for existing seeds — which
// breaks every recorded experiment and the e2e fixture. That is a contract
// violation, not a refactoring detail.
const goldenCSV = `jumper,age_group,late
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
`

const goldenJSON = `{"columns":["jumper","age_group","late"],"rows":[["other","old","no"],["other","young","yes"],["other","old","no"],["other","old","no"],["other","young","no"],["other","old","yes"],["other","young","yes"],["other","old","no"],["red","young","yes"],["red","young","no"],["red","young","yes"],["other","young","yes"]]}`

func TestGoldenBytes(t *testing.T) {
	d, v, err := Generate(13, Spec{Kind: ConfoundTrap, N: 12})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := string(d.CSV()); got != goldenCSV {
		t.Errorf("CSV drifted from the golden bytes:\n got:\n%s\nwant:\n%s", got, goldenCSV)
	}
	if got := string(d.JSON()); got != goldenJSON {
		t.Errorf("JSON drifted from the golden bytes:\n got: %s\nwant: %s", got, goldenJSON)
	}
	if v.Effect {
		t.Error("confound-trap truth must be effect=false")
	}
}

// The four trap properties, each over five fixed seeds. The seeds are pinned,
// not sampled: determinism means these are exact facts about the generator,
// re-checkable forever. (Not every seed behaves — planted-null seed 9 reaches
// z=+2.87, the honest ~5% false-positive rate of a 5%-level test, and
// underpowered seeds 7 and 8 get lucky. That is the statistics being honest,
// and it is why the seeds here are chosen, and documented as chosen.)
var trapSeeds = []int64{1, 2, 3, 4, 5}

// The headline trap: on confound-trap data the naive estimator confidently
// reaches the WRONG verdict and the controlled one does not. If this fails,
// the trap no longer traps and L1's whole purpose is gone.
func TestConfoundTrapActuallyTraps(t *testing.T) {
	for _, seed := range trapSeeds {
		d, v, err := Generate(seed, Spec{Kind: ConfoundTrap})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		naive := NaiveEstimate(d)
		if !naive.Confirmed {
			t.Errorf("seed %d: naive estimator must confirm the false effect (delta=%.3f z=%.2f) — the trap must trap",
				seed, naive.Delta, naive.Z)
		}
		controlled := ControlledEstimate(d)
		if controlled.Significant {
			t.Errorf("seed %d: controlled estimator must find no effect, got delta=%.3f z=%.2f",
				seed, controlled.Delta, controlled.Z)
		}
		if v.Effect {
			t.Errorf("seed %d: held-out truth must say no effect", seed)
		}
	}
}

func TestPlantedNullStaysNull(t *testing.T) {
	for _, seed := range trapSeeds {
		d, v, err := Generate(seed, Spec{Kind: PlantedNull})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		controlled := ControlledEstimate(d)
		if controlled.Significant {
			t.Errorf("seed %d: planted null must yield no significant effect under the controlled estimator, got delta=%.3f z=%.2f",
				seed, controlled.Delta, controlled.Z)
		}
		if v.Effect || v.EffectSize != 0 {
			t.Errorf("seed %d: truth must be a null, got %+v", seed, v)
		}
	}
}

func TestRealEffectIsDetected(t *testing.T) {
	for _, seed := range trapSeeds {
		d, v, err := Generate(seed, Spec{Kind: RealEffect})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		naive := NaiveEstimate(d)
		controlled := ControlledEstimate(d)
		if !naive.Confirmed || !controlled.Confirmed {
			t.Errorf("seed %d: a real effect at N=2000 must be found by both estimators (naive z=%.2f, controlled z=%.2f)",
				seed, naive.Z, controlled.Z)
		}
		if math.Abs(controlled.Delta-defaultEffectSize) > 0.05 {
			t.Errorf("seed %d: controlled estimate %.3f is not near the true effect %.2f", seed, controlled.Delta, defaultEffectSize)
		}
		if !v.Effect || v.EffectSize != defaultEffectSize {
			t.Errorf("seed %d: truth must carry the effect, got %+v", seed, v)
		}
	}
}

// Underpowered is the honesty trap: the effect is REAL (the truth says so),
// but the sample cannot support a confirmation — an org that confirms anyway
// is guessing, even when the guess is right.
func TestUnderpoweredCannotHonestlyConfirm(t *testing.T) {
	for _, seed := range trapSeeds {
		d, v, err := Generate(seed, Spec{Kind: Underpowered})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		controlled := ControlledEstimate(d)
		if controlled.Significant {
			t.Errorf("seed %d: N=%d must be too small to reach significance, got z=%.2f", seed, len(d.Rows), controlled.Z)
		}
		if !v.Effect {
			t.Errorf("seed %d: the held-out truth must say the effect exists", seed)
		}
	}
}

// Defaults: sample sizes and effect sizes resolve per kind, and the resolved
// spec is what the dataset carries.
func TestSpecDefaults(t *testing.T) {
	for _, tc := range []struct {
		kind       ScenarioKind
		wantN      int
		wantEffect float64
	}{
		{RealEffect, defaultN, defaultEffectSize},
		{PlantedNull, defaultN, 0},
		{ConfoundTrap, defaultN, 0},
		{Underpowered, defaultUnderpoweredN, defaultEffectSize},
	} {
		d, _, err := Generate(1, Spec{Kind: tc.kind})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if len(d.Rows) != tc.wantN {
			t.Errorf("%s: default N: want %d rows, got %d", tc.kind, tc.wantN, len(d.Rows))
		}
		if d.Spec.EffectSize != tc.wantEffect {
			t.Errorf("%s: resolved effect size: want %g, got %g", tc.kind, tc.wantEffect, d.Spec.EffectSize)
		}
		if !strings.HasPrefix(string(d.CSV()), "jumper,age_group,late\n") {
			t.Errorf("%s: default labels must render the red-jumpers header, got %q", tc.kind, strings.SplitN(string(d.CSV()), "\n", 2)[0])
		}
	}
}

// Custom labels flow into both renderings; the typed rows (and therefore the
// estimators) are unaffected.
func TestCustomLabels(t *testing.T) {
	labels := Labels{
		TreatmentColumn: "diet", TreatedValue: "vegan", UntreatedValue: "omnivore",
		CovariateColumn: "gym", CovariateHighValue: "member", CovariateLowValue: "none",
		OutcomeColumn: "marathon", OutcomeYesValue: "finished", OutcomeNoValue: "dnf",
	}
	d, v, err := Generate(7, Spec{Kind: ConfoundTrap, N: 12, Labels: labels})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	csv := string(d.CSV())
	if !strings.HasPrefix(csv, "diet,gym,marathon\n") {
		t.Errorf("CSV header must use the custom labels, got %q", strings.SplitN(csv, "\n", 2)[0])
	}
	for _, stale := range []string{"jumper", "red", "young", "late"} {
		if strings.Contains(csv, stale) {
			t.Errorf("CSV still contains default label %q", stale)
		}
	}
	if !strings.Contains(string(d.JSON()), `"diet"`) {
		t.Error("JSON must carry the custom column names")
	}
	if !strings.Contains(v.Explanation, "gym") {
		t.Errorf("the verdict explanation must speak the custom vocabulary, got %q", v.Explanation)
	}

	plain, _, err := Generate(7, Spec{Kind: ConfoundTrap, N: 12})
	if err != nil {
		t.Fatalf("generate plain: %v", err)
	}
	if !reflect.DeepEqual(d.Rows, plain.Rows) {
		t.Error("labels are presentation only — they must not change the generated rows")
	}
}

// The separation contract: nothing a Dataset renders contains the verdict.
// (The structural half is compile-time — Dataset has no truth field; this
// pins the rendered bytes too.)
func TestDatasetBytesCarryNoVerdict(t *testing.T) {
	for _, kind := range allKinds {
		d, v, err := Generate(3, Spec{Kind: kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for name, rendered := range map[string][]byte{"CSV": d.CSV(), "JSON": d.JSON()} {
			if bytes.Contains(rendered, []byte("effect")) {
				t.Errorf("%s: %s bytes mention \"effect\" — the answer is leaking into the dataset", kind, name)
			}
			if v.Explanation != "" && bytes.Contains(rendered, []byte(v.Explanation)) {
				t.Errorf("%s: %s bytes contain the verdict explanation", kind, name)
			}
		}
	}
}

func TestSpecRefusals(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"unknown kind", Spec{Kind: "poetry"}, `unknown scenario kind "poetry"`},
		{"empty kind", Spec{}, "unknown scenario kind"},
		{"negative N", Spec{Kind: RealEffect, N: -1}, "must not be negative"},
		{"N below minimum", Spec{Kind: RealEffect, N: 4}, "below the minimum"},
		{"negative effect", Spec{Kind: RealEffect, EffectSize: -0.1}, "must not be negative"},
		{"effect too large", Spec{Kind: RealEffect, EffectSize: 0.6}, "exceeds the maximum"},
		{"effect on planted null", Spec{Kind: PlantedNull, EffectSize: 0.2}, "no effect by definition"},
		{"effect on confound trap", Spec{Kind: ConfoundTrap, EffectSize: 0.2}, "no effect by definition"},
		{"label with comma", Spec{Kind: RealEffect, Labels: Labels{TreatedValue: "red,ish"}}, "CSV metacharacters"},
		{"label with newline", Spec{Kind: RealEffect, Labels: Labels{OutcomeColumn: "la\nte"}}, "CSV metacharacters"},
		{"identical column values", Spec{Kind: RealEffect, Labels: Labels{TreatedValue: "same", UntreatedValue: "same"}}, "must differ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Generate(1, tc.spec)
			if err == nil {
				t.Fatalf("want error containing %q, got a dataset", tc.wantErr)
			}
			if !errors.Is(err, ErrBadSpec) {
				t.Fatalf("error does not wrap ErrBadSpec: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
