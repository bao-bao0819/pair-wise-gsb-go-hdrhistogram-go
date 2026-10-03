package hdrhistogram

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWeightedMoments_NumericalStability drives the accumulator with values
// near 1e15 whose true spread is ~40. Forming (sum x^2)/w - mean^2 in float64
// destroys that spread entirely (x^2 rounds to multiples of 128 and mean^2
// itself rounds), yielding variance 0; the incremental central-moment update
// only takes differences value-mean and recovers the exact population variance.
func TestWeightedMoments_NumericalStability(t *testing.T) {
	const base = 1.0e15
	values := []float64{base - 30, base - 10, base + 10, base + 30}
	weights := []float64{1, 2, 2, 1}

	var m weightedMoments
	var w, sumX, sumX2 float64
	for i, v := range values {
		m.add(v, weights[i])
		w += weights[i]
		sumX += v * weights[i]
		sumX2 += v * v * weights[i]
	}

	assert.InDelta(t, w, m.weight, 1e-12)
	assert.InDelta(t, base, m.mean, 1e-6)

	// True weighted population variance:
	// (1*(-30)^2 + 2*(-10)^2 + 2*(10)^2 + 1*30^2) / 6 = 2197/6.
	wantVariance := 2197.0 / 6
	assert.InDelta(t, wantVariance, m.populationVariance(), 1e-6)

	// The textbook large-second-moment subtraction loses the spread completely.
	naiveVariance := sumX2/w - (sumX/w)*(sumX/w)
	if math.Abs(naiveVariance-wantVariance) < 1.0 {
		t.Fatalf("naive second-moment subtraction unexpectedly accurate: got %v want ~%v",
			naiveVariance, wantVariance)
	}
}

// TestWeightedMoments_ExactSimple checks the small hand-computed case directly.
func TestWeightedMoments_ExactSimple(t *testing.T) {
	var m weightedMoments
	m.add(1, 0.25)
	m.add(10, 3)
	m.add(20, 0.25)
	assert.InDelta(t, 3.5, m.weight, 1e-12)
	assert.InDelta(t, 141.0/14, m.mean, 1e-12)
	assert.InDelta(t, 2533.0/196, m.populationVariance(), 1e-12)
}
