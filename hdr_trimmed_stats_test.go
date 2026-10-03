package hdrhistogram_test

import (
	"math"
	"math/rand"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// trimmedOracle is an independent, intentionally simple reference for the
// rank-trimmed statistics. It expands the recorded buckets into their
// (representative value, count) pairs and intersects each bucket's rank
// interval [cum-count, cum) with [lo*N, hi*N). The moments below use the same
// stable weighted recurrence as the implementation; the overlap arithmetic is
// the independent part under test.
type trimmedResult struct {
	weight, mean, variance float64
}

func trimmedOracle(h *hdrhistogram.Histogram, lo, hi float64) trimmedResult {
	n := float64(h.TotalCount())
	loRank, hiRank := lo*n, hi*n
	var weight, mean, m2, cum float64
	for _, bar := range h.Distribution() {
		if bar.Count == 0 {
			continue
		}
		rep := float64(bar.From + (bar.To-bar.From+1)/2) // medianEquivalentValue
		c := float64(bar.Count)
		bucketLo, bucketHi := cum, cum+c
		cum = bucketHi
		w := math.Min(bucketHi, hiRank) - math.Max(bucketLo, loRank)
		if w <= 0 {
			continue
		}
		newWeight := weight + w
		delta := rep - mean
		mean += delta * (w / newWeight)
		m2 += w * delta * (rep - mean)
		weight = newWeight
	}
	return trimmedResult{weight: weight, mean: mean, variance: m2 / weight}
}

func assertTrimmedNear(t *testing.T, h *hdrhistogram.Histogram, lo, hi float64, want trimmedResult, tol float64) {
	t.Helper()
	got, err := h.TrimmedStatistics(lo, hi)
	require.NoError(t, err)
	assert.InDelta(t, want.weight, got.Weight, tol)
	assert.InDelta(t, want.mean, got.Mean, tol)
	if want.variance == 0 {
		assert.Equal(t, 0.0, got.Variance)
	} else {
		assert.InDelta(t, want.variance, got.Variance, math.Abs(want.variance)*1e-12)
	}
}

// TestTrimmedStatistics_HandCalculatedIntegerCuts is the hand-computed example
// with cut ranks on bucket boundaries: 4x1, 8x10, 8x20 (N=20), [0.2,0.8) keeps
// ranks [4,16) -> 8x10 and 4x20.
func TestTrimmedStatistics_HandCalculatedIntegerCuts(t *testing.T) {
	h := hdrhistogram.New(1, 100, 5)
	require.NoError(t, h.RecordValues(1, 4))
	require.NoError(t, h.RecordValues(10, 8))
	require.NoError(t, h.RecordValues(20, 8))

	// w = 12; mean = (8*10 + 4*20)/12 = 40/3;
	// var = (8*(10-40/3)^2 + 4*(20-40/3)^2)/12 = 200/9.
	assertTrimmedNear(t, h, 0.2, 0.8,
		trimmedResult{weight: 12, mean: 40.0 / 3, variance: 200.0 / 9}, 1e-9)
}

// TestTrimmedStatistics_HandCalculatedFractionalCuts is hand-computed with BOTH
// cut ranks landing inside a bucket: 2x1, 3x10, 2x20 (N=7), [0.25,0.75) keeps
// ranks [1.75,5.25). The buckets are rank intervals [0,2), [2,5), [5,7), so
// the retained weights are 0.25 of 1, all 3 tens, and 0.25 of 20.
func TestTrimmedStatistics_HandCalculatedFractionalCuts(t *testing.T) {
	h := hdrhistogram.New(1, 100, 5)
	require.NoError(t, h.RecordValues(1, 2))
	require.NoError(t, h.RecordValues(10, 3))
	require.NoError(t, h.RecordValues(20, 2))

	// w = 3.5; mean = (0.25 + 30 + 5)/3.5 = 141/14;
	// var: fractions 1/14, 12/14, 1/14 at values 1, 10, 20.
	want := trimmedResult{
		weight:   3.5,
		mean:     141.0 / 14,
		variance: 2533.0 / 196,
	}
	assertTrimmedNear(t, h, 0.25, 0.75, want, 1e-9)

	// Both cuts again inside buckets, asymmetric: N=10, 2x1, 5x10, 3x20,
	// [0.15,0.85) keeps ranks [1.5,8.5) -> 0.5/5/1.5, w=7, mean=11.5.
	h2 := hdrhistogram.New(1, 100, 5)
	require.NoError(t, h2.RecordValues(1, 2))
	require.NoError(t, h2.RecordValues(10, 5))
	require.NoError(t, h2.RecordValues(20, 3))
	assertTrimmedNear(t, h2, 0.15, 0.85,
		trimmedResult{weight: 7, mean: 11.5, variance: 3495.0 / 140}, 1e-9)
}

// TestTrimmedStatistics_SmallExpansionOracle cross-checks the API against the
// independent overlap oracle across a sweep of bounds, including bounds whose
// cut ranks fall at fractional positions inside buckets.
func TestTrimmedStatistics_SmallExpansionOracle(t *testing.T) {
	values := []int64{1, 1, 3, 7, 7, 15, 16, 31, 32, 64, 100, 127, 256, 500, 999}
	h := hdrhistogram.New(1, 1000, 3)
	for _, v := range values {
		require.NoError(t, h.RecordValue(v))
	}
	for lo := 0.0; lo < 0.5; lo += 0.07 {
		for hi := lo + 0.1; hi <= 1.0; hi += 0.11 {
			want := trimmedOracle(h, lo, hi)
			got, err := h.TrimmedStatistics(lo, hi)
			require.NoError(t, err)
			assert.InDelta(t, want.weight, got.Weight, 1e-9)
			assert.InDelta(t, want.mean, got.Mean, 1e-9)
			assert.InDelta(t, want.variance, got.Variance, 1e-9, lo, hi)
		}
	}
}

// TestTrimmedStatistics_FullRangeMatchesMeanStdDev checks lo=0/hi=1 reduces to
// the ordinary Mean/StdDev over all bucket representative values.
func TestTrimmedStatistics_FullRangeMatchesMeanStdDev(t *testing.T) {
	h := hdrhistogram.New(1, 1_000_000, 3)
	rng := newRng(7)
	for i := 0; i < 10000; i++ {
		require.NoError(t, h.RecordValue(1+rng.Int63n(1_000_000)))
	}
	got, err := h.TrimmedStatistics(0, 1)
	require.NoError(t, err)
	assert.InDelta(t, float64(h.TotalCount()), got.Weight, 1e-9)
	assert.InDelta(t, h.Mean(), got.Mean, 1e-9)
	assert.InDelta(t, h.StdDev()*h.StdDev(), got.Variance, got.Variance*1e-12)
}

// TestTrimmedStatistics_SingleBucket: everything collapses to one bucket
// representative, so any trimmed range is constant -> variance 0. This also
// covers two distinct recorded values that are equivalent at the histogram's
// resolution.
func TestTrimmedStatistics_SingleBucket(t *testing.T) {
	h := hdrhistogram.New(1, 1_000_000, 3)
	require.NoError(t, h.RecordValue(2100))
	require.NoError(t, h.RecordValue(2101)) // equivalent to 2100: same bucket, rep 2101
	require.True(t, h.ValuesAreEquivalent(2100, 2101))

	for _, bounds := range [][2]float64{{0, 1}, {0.1, 0.9}, {0.25, 0.75}} {
		got, err := h.TrimmedStatistics(bounds[0], bounds[1])
		require.NoError(t, err)
		assert.InDelta(t, 2*(bounds[1]-bounds[0]), got.Weight, 1e-12)
		assert.Equal(t, 2101.0, got.Mean)
		assert.Equal(t, 0.0, got.Variance)
	}
}

// TestTrimmedStatistics_HugeTotalCountNoOverflow records a value 8e18 times so
// value*count far exceeds int64 max (~9.2e18); weight and mean must remain
// correct in float64 without any int64 product.
func TestTrimmedStatistics_HugeTotalCountNoOverflow(t *testing.T) {
	const value = int64(1) << 50
	const count = int64(8_000_000_000_000_000_000)
	h := hdrhistogram.New(1, 1<<60, 3)
	require.NoError(t, h.RecordValues(value, count))

	got, err := h.TrimmedStatistics(0.2, 0.8)
	require.NoError(t, err)
	assert.InDelta(t, 4.8e18, got.Weight, 1)
	// The single bucket [1125899906842624, 1126999418470399] has rep 1126449662656512.
	assert.InDelta(t, 1126449662656512.0, got.Mean, 1)
	assert.Equal(t, 0.0, got.Variance)
}

// TestTrimmedStatistics_LargeShiftVarianceInvariant shifts every observation by
// ~1.1e17 while keeping the same within-bucket shape (all values land in
// width-2^40 buckets). The trimmed mean shifts by exactly the offset while the
// variance is bit-for-bit identical; a two-large-second-moments subtraction
// loses precision at this magnitude, which the stable central-moment update
// avoids.
func TestTrimmedStatistics_LargeShiftVarianceInvariant(t *testing.T) {
	const k = uint(40)
	width := int64(1) << k
	const a = int64(131073) // places values in bucket 30 (width 2^40)
	shift := int64(100_000) * width

	low := hdrhistogram.New(1, 1<<62, 5)
	high := hdrhistogram.New(1, 1<<62, 5)
	for j := int64(0); j < 40; j++ {
		require.NoError(t, low.RecordValue((a+j)*width))
		require.NoError(t, high.RecordValue(shift+(a+j)*width))
	}

	s1, err := low.TrimmedStatistics(0.1, 0.9)
	require.NoError(t, err)
	s2, err := high.TrimmedStatistics(0.1, 0.9)
	require.NoError(t, err)

	assert.Equal(t, s1.Weight, s2.Weight)
	assert.Equal(t, float64(shift), s2.Mean-s1.Mean)
	assert.Equal(t, s1.Variance, s2.Variance)
	assert.Greater(t, s1.Variance, 0.0)
}

// TestTrimmedStatistics_EmptyHistogram: an empty histogram with valid bounds
// returns the zero result and no error.
func TestTrimmedStatistics_EmptyHistogram(t *testing.T) {
	h := hdrhistogram.New(1, 1000, 3)
	got, err := h.TrimmedStatistics(0.1, 0.9)
	require.NoError(t, err)
	assert.Equal(t, hdrhistogram.TrimmedStats{}, got)
}

// TestTrimmedStatistics_InvalidBounds covers non-finite arguments, out-of-range
// bounds and the empty retained interval lo>=hi.
func TestTrimmedStatistics_InvalidBounds(t *testing.T) {
	h := hdrhistogram.New(1, 1000, 3)
	require.NoError(t, h.RecordValue(5))
	for _, bounds := range [][2]float64{
		{-0.01, 0.5},
		{0.5, 1.01},
		{0.5, 0.5},
		{0.8, 0.2},
		{math.NaN(), 0.5},
		{0.5, math.NaN()},
		{math.Inf(1), 1},
		{0, math.Inf(-1)},
	} {
		_, err := h.TrimmedStatistics(bounds[0], bounds[1])
		assert.Error(t, err, "bounds %v", bounds)
	}
	// Invalid bounds are rejected even on an empty histogram.
	_, err := hdrhistogram.New(1, 1000, 3).TrimmedStatistics(0.5, 0.5)
	assert.Error(t, err)
}

// TestTrimmedStatistics_IsReadOnly: querying must not alter any recorded state.
func TestTrimmedStatistics_IsReadOnly(t *testing.T) {
	h := hdrhistogram.New(1, 1000, 3)
	for _, v := range []int64{1, 5, 50, 900} {
		require.NoError(t, h.RecordValue(v))
	}
	before := h.Export()
	totalBefore := h.TotalCount()
	_, _ = h.TrimmedStatistics(0.1, 0.9)
	_, _ = h.TrimmedStatistics(0.25, 0.75)
	assert.Equal(t, totalBefore, h.TotalCount())
	assert.Equal(t, before, h.Export())
}
