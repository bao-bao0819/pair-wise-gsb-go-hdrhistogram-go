package hdrhistogram_test

import (
	"math"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/stretchr/testify/assert"
)

// bucketCounts flattens a histogram's distribution into a
// lowest-equivalent-value -> count map, keeping only non-empty buckets.
func bucketCounts(h *hdrhistogram.Histogram) map[int64]int64 {
	counts := make(map[int64]int64)
	for _, bar := range h.Distribution() {
		if bar.Count != 0 {
			counts[bar.From] += bar.Count
		}
	}
	return counts
}

func TestConvolveCartesianProduct(t *testing.T) {
	a := hdrhistogram.New(1, 100, 3)
	assert.NoError(t, a.RecordValues(1, 2))
	assert.NoError(t, a.RecordValues(2, 3))

	b := hdrhistogram.New(1, 100, 3)
	assert.NoError(t, b.RecordValues(10, 5))
	assert.NoError(t, b.RecordValues(20, 7))

	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.NoError(t, err)

	// Explicit cartesian product of the discrete bucket values:
	//   1+10 weight 2*5, 1+20 weight 2*7, 2+10 weight 3*5, 2+20 weight 3*7.
	expected := map[int64]int64{
		11: 10,
		21: 14,
		12: 15,
		22: 21,
	}
	assert.Equal(t, expected, bucketCounts(result))
	assert.Equal(t, a.TotalCount()*b.TotalCount(), result.TotalCount())
}

func TestConvolveCommutative(t *testing.T) {
	a := hdrhistogram.New(1, 100000, 3)
	b := hdrhistogram.New(1, 100000, 2)
	for _, v := range []int64{5, 42, 1000, 7777, 65432} {
		assert.NoError(t, a.RecordValues(v, v%13+1))
	}
	for _, v := range []int64{3, 250, 9000, 80000} {
		assert.NoError(t, b.RecordValues(v, v%7+1))
	}

	ab, err := hdrhistogram.Convolve(a, b, 1, 1000000, 3)
	assert.NoError(t, err)
	ba, err := hdrhistogram.Convolve(b, a, 1, 1000000, 3)
	assert.NoError(t, err)

	assert.Equal(t, bucketCounts(ab), bucketCounts(ba))
	assert.Equal(t, ab.TotalCount(), ba.TotalCount())
}

func TestConvolveZeroIdentity(t *testing.T) {
	// A distribution holding only the value 0 is the convolution identity:
	// convolving with it must reproduce the other distribution exactly.
	zero := hdrhistogram.New(1, 100000, 3)
	assert.NoError(t, zero.RecordValue(0))

	h := hdrhistogram.New(1, 100000, 3)
	for _, v := range []int64{1, 7, 100, 999, 4096, 65536} {
		assert.NoError(t, h.RecordValues(v, v%5+1))
	}

	result, err := hdrhistogram.Convolve(zero, h, 1, 100000, 3)
	assert.NoError(t, err)
	assert.Equal(t, bucketCounts(h), bucketCounts(result))
	assert.Equal(t, h.TotalCount(), result.TotalCount())

	// Identity must hold from either side.
	result, err = hdrhistogram.Convolve(h, zero, 1, 100000, 3)
	assert.NoError(t, err)
	assert.Equal(t, bucketCounts(h), bucketCounts(result))
}

func TestConvolveEmpty(t *testing.T) {
	empty := hdrhistogram.New(1, 1000, 3)
	h := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, h.RecordValues(10, 4))

	for _, result := range []*hdrhistogram.Histogram{
		mustConvolve(t, empty, h, 1, 1000, 3),
		mustConvolve(t, h, empty, 1, 1000, 3),
		mustConvolve(t, empty, empty, 1, 1000, 3),
	} {
		assert.Equal(t, int64(0), result.TotalCount())
		assert.Empty(t, bucketCounts(result))
	}
}

func mustConvolve(t *testing.T, a, b *hdrhistogram.Histogram, low, high int64, sigFigs int) *hdrhistogram.Histogram {
	t.Helper()
	result, err := hdrhistogram.Convolve(a, b, low, high, sigFigs)
	assert.NoError(t, err)
	return result
}

func TestConvolveLargeCountsFewBuckets(t *testing.T) {
	// Two single-bucket histograms with enormous weights. The convolution
	// walks bucket pairs, not samples, so this runs in constant time
	// regardless of the recorded counts; if it expanded samples it would
	// never finish.
	const n = int64(3_000_000_000)

	a := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, a.RecordValues(10, n))
	b := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, b.RecordValues(20, n))

	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.NoError(t, err)
	assert.Equal(t, n*n, result.TotalCount())
	assert.Equal(t, map[int64]int64{30: n * n}, bucketCounts(result))
}

func TestConvolveOutOfRangeFails(t *testing.T) {
	a := hdrhistogram.New(1, 2000, 3)
	assert.NoError(t, a.RecordValue(1500))
	b := hdrhistogram.New(1, 2000, 3)
	assert.NoError(t, b.RecordValue(1500))

	// 1500+1500 = 3000 cannot be recorded in a target tracking up to 1000
	// (its highest bucket covers values below 2048). Unlike Merge, the whole
	// convolution must fail instead of silently dropping the combination.
	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestConvolveOverflowFails(t *testing.T) {
	// Total count product overflow: (2^62) * 4 does not fit in an int64.
	a := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, a.RecordValues(10, int64(1)<<62))
	b := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, b.RecordValues(20, 4))

	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.Error(t, err)
	assert.Nil(t, result)

	// Value sum overflow: both representatives near 2^62 push the sum past
	// math.MaxInt64.
	c := hdrhistogram.New(1, math.MaxInt64, 3)
	assert.NoError(t, c.RecordValue(int64(1)<<62))
	d := hdrhistogram.New(1, math.MaxInt64, 3)
	assert.NoError(t, d.RecordValue(int64(1)<<62))

	result, err = hdrhistogram.Convolve(c, d, 1, math.MaxInt64, 3)
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestConvolveDoesNotInheritMetadata(t *testing.T) {
	a := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, a.RecordValue(10))
	a.SetTag("stage-1")
	a.SetStartTimeMs(1000)
	a.SetEndTimeMs(2000)

	b := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, b.RecordValue(20))
	b.SetTag("stage-2")
	b.SetStartTimeMs(2000)
	b.SetEndTimeMs(3000)

	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.NoError(t, err)

	// A convolution is a derived distribution, not a fresh recording
	// interval: it must not inherit the inputs' tag or time range.
	assert.Equal(t, "", result.Tag())
	assert.Equal(t, int64(0), result.StartTimeMs())
	assert.Equal(t, int64(0), result.EndTimeMs())
}

func TestConvolveDoesNotShareCounts(t *testing.T) {
	a := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, a.RecordValues(10, 3))
	b := hdrhistogram.New(1, 1000, 3)
	assert.NoError(t, b.RecordValues(20, 2))

	beforeA := bucketCounts(a)
	beforeB := bucketCounts(b)

	result, err := hdrhistogram.Convolve(a, b, 1, 1000, 3)
	assert.NoError(t, err)

	// Mutating the result must not be observable through either input.
	assert.NoError(t, result.RecordValues(30, 100))
	assert.Equal(t, beforeA, bucketCounts(a))
	assert.Equal(t, beforeB, bucketCounts(b))
}
