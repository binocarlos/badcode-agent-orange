package hypolab

import (
	"math"
	"strings"
	"testing"
)

// addRows appends n observations of one cell, y of them with the outcome.
func addRows(rows *[]Row, treated, covHigh bool, n, y int) {
	for i := 0; i < n; i++ {
		*rows = append(*rows, Row{Treated: treated, CovariateHigh: covHigh, Outcome: i < y})
	}
}

func dataset(rows []Row) *Dataset {
	labels, err := resolveLabels(Labels{})
	if err != nil {
		panic(err)
	}
	return &Dataset{Spec: Spec{Kind: ConfoundTrap, N: len(rows), Labels: labels}, Rows: rows}
}

// The z-test against hand-computed values: 60/100 vs 40/100 has delta 0.2,
// pooled p 0.5, se sqrt(0.25*0.02)=0.0707106…, z 2.8284271….
func TestTwoProportionZKnownValues(t *testing.T) {
	delta, z, ok := twoProportionZ(arm{n: 100, y: 60}, arm{n: 100, y: 40})
	if !ok {
		t.Fatal("statistic must be defined")
	}
	if math.Abs(delta-0.2) > 1e-12 {
		t.Errorf("delta: want 0.2, got %v", delta)
	}
	if want := 0.2 / math.Sqrt(0.25*(0.01+0.01)); math.Abs(z-want) > 1e-12 {
		t.Errorf("z: want %v, got %v", want, z)
	}

	// Undefined cases: an empty arm; zero pooled variance.
	if _, _, ok := twoProportionZ(arm{n: 0}, arm{n: 10, y: 5}); ok {
		t.Error("empty treated arm must be undefined")
	}
	if _, _, ok := twoProportionZ(arm{n: 10, y: 0}, arm{n: 10, y: 0}); ok {
		t.Error("zero pooled variance must be undefined")
	}
}

// A Simpson's-paradox table built by hand, exact to the last digit:
//
//	young: treated 28/40 (0.7)   untreated  7/10 (0.7)
//	old:   treated  2/10 (0.2)   untreated  8/40 (0.2)
//
// Within each age group the rates are identical — the true effect is zero.
// But treated skews young and young skews late, so the naive comparison sees
// 30/50 (0.6) vs 15/50 (0.3): delta 0.3, z = 0.3/sqrt(0.45*0.55*0.04) ≈ 3.02,
// a confident wrong answer. The controlled estimator must read exactly zero.
func TestSimpsonsParadoxByConstruction(t *testing.T) {
	var rows []Row
	addRows(&rows, true, true, 40, 28)
	addRows(&rows, false, true, 10, 7)
	addRows(&rows, true, false, 10, 2)
	addRows(&rows, false, false, 40, 8)
	d := dataset(rows)

	naive := NaiveEstimate(d)
	if math.Abs(naive.Delta-0.3) > 1e-12 {
		t.Errorf("naive delta: want 0.3, got %v", naive.Delta)
	}
	wantZ := 0.3 / math.Sqrt(0.45*0.55*(1.0/50+1.0/50))
	if math.Abs(naive.Z-wantZ) > 1e-12 {
		t.Errorf("naive z: want %v, got %v", wantZ, naive.Z)
	}
	if !naive.Significant || !naive.Confirmed {
		t.Error("the naive estimator must confidently confirm the spurious effect")
	}

	controlled := ControlledEstimate(d)
	if controlled.Delta != 0 || controlled.Z != 0 {
		t.Errorf("controlled estimate must be exactly zero, got delta=%v z=%v", controlled.Delta, controlled.Z)
	}
	if controlled.Significant || controlled.Confirmed {
		t.Error("the controlled estimator must not confirm")
	}
}

// Degenerate inputs must land on "nothing to conclude", never on a panic or a
// confident verdict.
func TestDegenerateDatasets(t *testing.T) {
	t.Run("every outcome positive", func(t *testing.T) {
		var rows []Row
		addRows(&rows, true, true, 10, 10)
		addRows(&rows, false, true, 10, 10)
		addRows(&rows, true, false, 10, 10)
		addRows(&rows, false, false, 10, 10)
		d := dataset(rows)
		if a := NaiveEstimate(d); a.Significant {
			t.Errorf("zero-variance naive must not be significant: %+v", a)
		}
		if a := ControlledEstimate(d); a.Significant || a.Z != 0 {
			t.Errorf("zero-variance controlled must be z=0, not significant: %+v", a)
		}
	})
	t.Run("everyone treated", func(t *testing.T) {
		var rows []Row
		addRows(&rows, true, true, 10, 5)
		addRows(&rows, true, false, 10, 5)
		d := dataset(rows)
		if a := NaiveEstimate(d); a.Significant {
			t.Errorf("one-armed naive must not be significant: %+v", a)
		}
		if a := ControlledEstimate(d); a.Significant {
			t.Errorf("one-armed controlled must not be significant: %+v", a)
		}
	})
	t.Run("one stratum degenerate, the other informative", func(t *testing.T) {
		var rows []Row
		addRows(&rows, true, true, 10, 10) // zero variance: skipped
		addRows(&rows, false, true, 10, 10)
		addRows(&rows, true, false, 100, 60)
		addRows(&rows, false, false, 100, 40)
		a := ControlledEstimate(dataset(rows))
		if math.Abs(a.Delta-0.2) > 1e-12 {
			t.Errorf("controlled must use the informative stratum alone: delta want 0.2, got %v", a.Delta)
		}
		if !a.Significant {
			t.Errorf("0.2 at n=100 per arm is significant, got z=%v", a.Z)
		}
	})
}

// A report must be able to state its method (the investigator's first duty in
// the hypothesis lab), so the method strings name what was and wasn't done.
func TestMethodStrings(t *testing.T) {
	d, _, err := Generate(1, Spec{Kind: PlantedNull, N: 100})
	if err != nil {
		t.Fatal(err)
	}
	if m := NaiveEstimate(d).Method; !strings.Contains(m, "no covariate control") {
		t.Errorf("naive method must admit it controls for nothing: %q", m)
	}
	if m := ControlledEstimate(d).Method; !strings.Contains(m, "age_group") {
		t.Errorf("controlled method must name the covariate column: %q", m)
	}
}
