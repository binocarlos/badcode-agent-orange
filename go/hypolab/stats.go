package hypolab

// stats.go — the two estimators the trap taxonomy is defined against.
//
// NaiveEstimate is the mistake: a raw treated-vs-untreated difference of
// outcome proportions with a two-sample z-test, ignoring the covariate. On
// ConfoundTrap data it confidently reaches the WRONG verdict — that is the
// trap working, and the tests pin it.
//
// ControlledEstimate is the honest version: stratify by the covariate,
// take the within-stratum differences, and pool them by inverse-variance
// weighting. On ConfoundTrap data it reports the truth (no effect); on
// PlantedNull it stays null; on RealEffect it confirms; on Underpowered it
// honestly fails to reach significance.
//
// The statistics are deliberately simple — difference of two proportions,
// normal approximation, |z| >= 1.96 — because the package calibrates a
// measuring instrument, and an instrument nobody can hand-check calibrates
// nothing.

import (
	"fmt"
	"math"
)

// zCritical is the two-sided 5% significance threshold.
const zCritical = 1.96

// Analysis is one estimator's reading of a dataset.
type Analysis struct {
	// Method says what was computed, so a report can state it.
	Method string
	// Delta is the estimated treated-minus-untreated difference in outcome
	// rate (for ControlledEstimate, the pooled within-stratum difference).
	Delta float64
	// Z is the test statistic; zero when it is undefined (an empty arm, or
	// zero variance everywhere).
	Z float64
	// Significant is |Z| >= 1.96 with Z defined.
	Significant bool
	// Confirmed is the estimator's verdict on "does the treatment raise the
	// outcome rate?": a significant positive delta.
	Confirmed bool
}

// arm counts one group: n observations, y outcomes.
type arm struct{ n, y int }

func (a arm) rate() float64 { return float64(a.y) / float64(a.n) }

// NaiveEstimate compares raw outcome rates between the treated and untreated,
// ignoring the covariate entirely.
func NaiveEstimate(d *Dataset) Analysis {
	var treated, untreated arm
	for _, r := range d.Rows {
		count(&treated, &untreated, r.Treated, r.Outcome)
	}
	delta, z, ok := twoProportionZ(treated, untreated)
	return finish(Analysis{
		Method: "naive difference of proportions (two-sample z-test, no covariate control)",
		Delta:  delta,
		Z:      z,
	}, ok)
}

// ControlledEstimate stratifies by the covariate and pools the within-stratum
// differences of proportions by inverse-variance weighting. Strata with an
// empty arm or zero variance carry no information and are skipped; if no
// stratum is usable the result is not significant, with Z zero.
func ControlledEstimate(d *Dataset) Analysis {
	// arms[covariateHigh][treated]
	var arms [2][2]arm
	for _, r := range d.Rows {
		count(&arms[idx(r.CovariateHigh)][1], &arms[idx(r.CovariateHigh)][0], r.Treated, r.Outcome)
	}
	a := Analysis{
		Method: fmt.Sprintf("stratified difference of proportions controlling for %s (inverse-variance pooled z-test)",
			d.Spec.Labels.CovariateColumn),
	}
	var sumW, sumWD float64
	for _, stratum := range arms {
		treated, untreated := stratum[1], stratum[0]
		if treated.n == 0 || untreated.n == 0 {
			continue
		}
		p1, p0 := treated.rate(), untreated.rate()
		v := p1*(1-p1)/float64(treated.n) + p0*(1-p0)/float64(untreated.n)
		if v == 0 {
			continue
		}
		w := 1 / v
		sumW += w
		sumWD += w * (p1 - p0)
	}
	if sumW == 0 {
		return a // no usable stratum: nothing to conclude
	}
	a.Delta = sumWD / sumW
	a.Z = a.Delta / math.Sqrt(1/sumW)
	return finish(a, true)
}

// count adds one observation to the treated or untreated arm.
func count(treated, untreated *arm, isTreated, outcome bool) {
	a := untreated
	if isTreated {
		a = treated
	}
	a.n++
	if outcome {
		a.y++
	}
}

func idx(b bool) int {
	if b {
		return 1
	}
	return 0
}

// twoProportionZ is the two-sample z-test for a difference of proportions
// with a pooled standard error. ok is false when the statistic is undefined
// (an empty arm, or pooled variance zero).
func twoProportionZ(treated, untreated arm) (delta, z float64, ok bool) {
	if treated.n == 0 || untreated.n == 0 {
		return 0, 0, false
	}
	p1, p0 := treated.rate(), untreated.rate()
	delta = p1 - p0
	pooled := float64(treated.y+untreated.y) / float64(treated.n+untreated.n)
	se := math.Sqrt(pooled * (1 - pooled) * (1/float64(treated.n) + 1/float64(untreated.n)))
	if se == 0 {
		return delta, 0, false
	}
	return delta, delta / se, true
}

// finish derives the verdict flags from a computed statistic.
func finish(a Analysis, ok bool) Analysis {
	a.Significant = ok && math.Abs(a.Z) >= zCritical
	a.Confirmed = a.Significant && a.Delta > 0
	return a
}
