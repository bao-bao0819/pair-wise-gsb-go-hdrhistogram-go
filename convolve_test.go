package hdrhistogram_test

import (
	"math"
	"testing"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// barsByMedian returns count keyed by the representative (median equivalent)
// value of each non-empty bar, so small-distribution answers can be compared
// against an explicit Cartesian product.
func barsByMedian(h *hdrhistogram.Histogram) map[int64]int64 {
	out := make(map[int64]int64)
	for _, bar := range h.Distribution() {
		if bar.Count == 0 {
			continue
		}
		// Bar covers an inclusive [From, To] equivalent-value range; its
		// median representative is From + (To-From+1)/2, i.e. the same
		// medianEquivalentValue Convolve uses.
		median := bar.From + (bar.To-bar.From+1)/2
		out[median] += bar.Count
	}
	return out
}

func record(t *testing.T, h *hdrhistogram.Histogram, value, count int64) {
	t.Helper()
	require.NoError(t, h.RecordValues(value, count))
}

// Convolve is commutative: swapping the inputs must yield identical bucket
// counts (and the same total) for histograms with differing geometries.
func TestConvolve_Commutative(t *testing.T) {
	a := hdrhistogram.New(1, 10_000, 3)
	b := hdrhistogram.New(10, 1_000_000, 2)
	for _, v := range []int64{3, 7, 100, 5000} {
		record(t, a, v, v)
	}
	for _, v := range []int64{12, 90, 4000} {
		record(t, b, v, v+1)
	}

	ab, err := hdrhistogram.Convolve(a, b, 1, 2_000_000, 3)
	require.NoError(t, err)
	ba, err := hdrhistogram.Convolve(b, a, 1, 2_000_000, 3)
	require.NoError(t, err)

	assert.Equal(t, ab.TotalCount(), ba.TotalCount())
	assert.Equal(t, barsByMedian(ab), barsByMedian(ba))
	assert.True(t, ab.Equals(ba), "swapped inputs must give identical histograms")
}

// A distribution supported only at 0 with weight n is the identity element:
// x+0 == x, so every bucket keeps its count.
func TestConvolve_UnitZero(t *testing.T) {
	a := hdrhistogram.New(1, 1000, 3)
	record(t, a, 2, 3)
	record(t, a, 17, 5)
	record(t, a, 200, 2)

	unit := hdrhistogram.New(1, 1000, 3)
	record(t, unit, 0, 1)

	out, err := hdrhistogram.Convolve(a, unit, 1, 1000, 3)
	require.NoError(t, err)

	assert.Equal(t, a.TotalCount(), out.TotalCount())
	// median of bucket 0 (equivalent range [0,0]) is 0: sums land back on a.
	assert.Equal(t, barsByMedian(a), barsByMedian(out))

	// And with unit first.
	out2, err := hdrhistogram.Convolve(unit, a, 1, 1000, 3)
	require.NoError(t, err)
	assert.True(t, out.Equals(out2))
}

// Explicit Cartesian products on small distributions whose values all sit in
// distinct unit-resolution buckets.
func TestConvolve_ExplicitCartesianProducts(t *testing.T) {
	// Case 1: single buckets x in {3 weight 2} and y in {5 weight 4} ->
	// one pair: (3+5, 2*4) = (8, 8).
	a := hdrhistogram.New(1, 100, 3)
	b := hdrhistogram.New(1, 100, 3)
	record(t, a, 3, 2)
	record(t, b, 5, 4)
	out, err := hdrhistogram.Convolve(a, b, 1, 100, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(8), out.TotalCount())
	assert.Equal(t, map[int64]int64{8: 8}, barsByMedian(out))

	// Case 2: x in {1, 4} each weight 2, y in {2, 3} weights {1, 3}.
	// Pairs:
	//   1+2 w 2*1=2 ; 1+3 w 2*3=6
	//   4+2 w 2*1=2 ; 4+3 w 2*3=6
	// -> {3:2, 4:6, 6:2, 7:6}, total 16.
	c := hdrhistogram.New(1, 100, 3)
	d := hdrhistogram.New(1, 100, 3)
	record(t, c, 1, 2)
	record(t, c, 4, 2)
	record(t, d, 2, 1)
	record(t, d, 3, 3)
	out2, err := hdrhistogram.Convolve(c, d, 1, 100, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(16), out2.TotalCount())
	assert.Equal(t, map[int64]int64{3: 2, 4: 6, 6: 2, 7: 6}, barsByMedian(out2))

	// Case 3: two different pairs share the same sum 2048 and must therefore
	// accumulate (weight 2) into one target bucket. At that magnitude the
	// target equivalent range is [2048,2049] (median representative 2049);
	// 2072 lands in [2072,2073] (median 2073).
	e := hdrhistogram.New(1, 10000, 3)
	f := hdrhistogram.New(1, 10000, 3)
	record(t, e, 1000, 1)
	record(t, e, 1024, 1)
	record(t, f, 1048, 1)
	record(t, f, 1024, 1)
	out3, err := hdrhistogram.Convolve(e, f, 1, 100000, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(4), out3.TotalCount())
	// 1000+1048=2048 and 1024+1024=2048 collide (weight 2 at median 2049);
	// 1000+1024=2024 (w1); 1024+1048=2072 -> median 2073 (w1).
	assert.Equal(t, map[int64]int64{2024: 1, 2049: 2, 2073: 1}, barsByMedian(out3))
}

// Empty convolved with anything is an empty result with the target geometry,
// and inputs' time/tag metadata must not be interpreted as a new interval.
func TestConvolve_Empty(t *testing.T) {
	a := hdrhistogram.New(1, 1000, 3)
	b := hdrhistogram.New(1, 1000, 3)
	record(t, b, 42, 7)

	out, err := hdrhistogram.Convolve(a, b, 1, 5000, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.TotalCount())
	assert.Equal(t, int64(2), out.SignificantFigures())
	assert.Equal(t, int64(1), out.LowestTrackableValue())
	assert.Equal(t, int64(5000), out.HighestTrackableValue())

	// b carries time/tag metadata; the derived result must not inherit it.
	b.SetStartTimeMs(100)
	b.SetEndTimeMs(200)
	b.SetTag("stage-2")
	out2, err := hdrhistogram.Convolve(a, b, 1, 5000, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(0), out2.StartTimeMs())
	assert.Equal(t, int64(0), out2.EndTimeMs())
	assert.Equal(t, "", out2.Tag())
}

// Non-empty inputs still do not leak time/tag metadata into the result.
func TestConvolve_NoMetadataLeak(t *testing.T) {
	a := hdrhistogram.New(1, 100, 3)
	b := hdrhistogram.New(1, 100, 3)
	a.SetStartTimeMs(11)
	a.SetEndTimeMs(22)
	a.SetTag("a")
	b.SetStartTimeMs(33)
	b.SetEndTimeMs(44)
	b.SetTag("b")
	record(t, a, 1, 1)
	record(t, b, 2, 1)

	out, err := hdrhistogram.Convolve(a, b, 1, 100, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.StartTimeMs())
	assert.Equal(t, int64(0), out.EndTimeMs())
	assert.Equal(t, "", out.Tag())
}

// The result owns a fresh counts slice: mutating inputs after the call does
// not change the returned histogram.
func TestConvolve_NoSharedCountSlice(t *testing.T) {
	a := hdrhistogram.New(1, 100, 3)
	b := hdrhistogram.New(1, 100, 3)
	record(t, a, 3, 2)
	record(t, b, 5, 4)

	out, err := hdrhistogram.Convolve(a, b, 1, 100, 3)
	require.NoError(t, err)
	before := barsByMedian(out)

	require.NoError(t, a.RecordValues(99, 1000))
	b.Reset()

	assert.Equal(t, int64(8), out.TotalCount())
	assert.Equal(t, before, barsByMedian(out))
}

// Any pairwise sum above the target highest trackable value fails the whole
// operation (no silent Merge-style dropping, no partially filled histogram).
func TestConvolve_OutOfRangeFails(t *testing.T) {
	a := hdrhistogram.New(1, 100, 3)
	b := hdrhistogram.New(1, 100, 3)
	record(t, a, 60, 1)
	record(t, a, 10, 1)
	record(t, b, 50, 1) // 60+50 = 110 > 100 -> whole convolution fails

	out, err := hdrhistogram.Convolve(a, b, 1, 100, 3)
	assert.Nil(t, out)
	require.Error(t, err)

	// Widen the target and the same inputs succeed, total preserved.
	out2, err := hdrhistogram.Convolve(a, b, 1, 200, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(2), out2.TotalCount())
}

// TotalCount product overflow must be rejected rather than wrapping.
func TestConvolve_TotalOverflow(t *testing.T) {
	a := hdrhistogram.New(1, math.MaxInt64, 3)
	b := hdrhistogram.New(1, math.MaxInt64, 3)
	require.NoError(t, a.RecordValues(1, math.MaxInt64))
	require.NoError(t, b.RecordValues(1, 2))

	out, err := hdrhistogram.Convolve(a, b, 1, 10, 3)
	assert.Nil(t, out)
	require.Error(t, err)
}

// Bucket representative value sum overflow must be rejected.
func TestConvolve_ValueSumOverflow(t *testing.T) {
	// With 1 significant figure the top bucket's range at MaxInt64 is 2^58,
	// whose median representative is 2^63-2^57; two of them still exceed
	// MaxInt64 when added — and the histogram itself stays tiny.
	a := hdrhistogram.New(1, math.MaxInt64, 1)
	require.NoError(t, a.RecordValue(math.MaxInt64))

	out, err := hdrhistogram.Convolve(a, a, 1, 10, 1)
	assert.Nil(t, out)
	require.Error(t, err)
}

// One occupied bucket with a huge weight, tiny bucket count: the convolution
// does a single pair visit, so its runtime is independent of the (3e9) sample
// count. Assert the exact Cartesian answer and that it finishes instantly.
func TestConvolve_HugeWeightFewBuckets(t *testing.T) {
	const huge = int64(3_000_000_000)
	a := hdrhistogram.New(1, 100, 3)
	b := hdrhistogram.New(1, 100, 3)
	record(t, a, 10, huge)
	record(t, b, 20, huge)

	done := make(chan struct{})
	var out *hdrhistogram.Histogram
	var err error
	go func() {
		out, err = hdrhistogram.Convolve(a, b, 1, 100, 3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Convolve with 9e18-weight single pair took too long; it must be O(nonEmptyBuckets), not O(samples)")
	}
	require.NoError(t, err)
	assert.Equal(t, huge*huge, out.TotalCount())
	assert.Equal(t, map[int64]int64{30: huge * huge}, barsByMedian(out))
}

// BenchmarkConvolveHugeWeight proves runtime is O(nonEmptyBuckets), not
// O(samples): the 3e9-vs-3e9 case represents 9e18 sample pairs but visits a
// single bucket pair and must take essentially the same ns/op as 1-vs-1.
func BenchmarkConvolveHugeWeight(b *testing.B) {
	weights := []int64{1, 3_000_000_000}
	for _, w := range weights {
		b.Run(fmtWeight(w), func(b *testing.B) {
			a := hdrhistogram.New(1, 100, 3)
			c := hdrhistogram.New(1, 100, 3)
			if err := a.RecordValues(10, w); err != nil {
				b.Fatal(err)
			}
			if err := c.RecordValues(20, w); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out, err := hdrhistogram.Convolve(a, c, 1, 100, 3)
				if err != nil {
					b.Fatal(err)
				}
				if out.TotalCount() != w*w {
					b.Fatalf("total = %d, want %d", out.TotalCount(), w*w)
				}
			}
		})
	}
}

func fmtWeight(w int64) string {
	switch w {
	case 1:
		return "weight_1"
	default:
		return "weight_3e9"
	}
}
