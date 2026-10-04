package hdrhistogram

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bruteForceOptimal enumerates every choice of k-1 cut positions among the
// n-1 gaps between n non-empty buckets, in lexicographic order, and returns
// the minimal total SSE together with the lexicographically smallest cut
// sequence attaining it. Segment costs come from the same bucketStats used
// by the planner, and each candidate total is accumulated right-to-left to
// mirror the suffix DP's association order, so results are comparable
// bit-for-bit.
func bruteForceOptimal(stats *bucketStats, n, k int) (float64, []int) {
	cuts := make([]int, 0, k-1)
	best := math.Inf(1)
	var bestCuts []int

	var total func() float64
	total = func() float64 {
		// Right-associated sum: cost(seg0) + (cost(seg1) + ...).
		bounds := append(append([]int{0}, cuts...), n)
		acc := stats.cost(bounds[len(bounds)-2], bounds[len(bounds)-1])
		for i := len(bounds) - 3; i >= 0; i-- {
			acc = stats.cost(bounds[i], bounds[i+1]) + acc
		}
		return acc
	}

	var enum func(start int)
	enum = func(start int) {
		if len(cuts) == k-1 {
			c := total()
			if c < best {
				best = c
				bestCuts = append([]int(nil), cuts...)
			}
			return
		}
		// Cuts sit in the n-1 gaps between buckets; leave enough gaps for
		// the remaining cuts.
		for pos := start; pos <= n-k+len(cuts)+1; pos++ {
			cuts = append(cuts, pos)
			enum(pos + 1)
			cuts = cuts[:len(cuts)-1]
		}
	}
	enum(1)
	return best, bestCuts
}

func smallHistograms() map[string]*Histogram {
	h1 := New(1, 1<<20, 2)
	for i, v := range []int64{1, 2, 3, 5, 8, 13, 21, 34} {
		h1.RecordValues(v, int64([]int{3, 1, 4, 1, 5, 9, 2, 6}[i]))
	}

	h2 := New(1, 1<<20, 2)
	for i, v := range []int64{10, 20, 30, 40, 50} {
		h2.RecordValues(v, int64(1<<uint(i)))
	}

	h3 := New(1, 1<<40, 2)
	for i, v := range []int64{7, 100, 1000, 10000, 100000, 1000000} {
		h3.RecordValues(v, int64([]int{100, 1, 1, 1, 1, 100}[i]))
	}

	// Deterministic pseudo-random fill, 8 non-empty buckets.
	h4 := New(1, 1<<20, 2)
	rng := rand.New(rand.NewSource(42))
	for _, v := range []int64{3, 9, 27, 81, 243, 729, 2187, 6561} {
		h4.RecordValues(v, 1+rng.Int63n(50))
	}

	return map[string]*Histogram{"eight": h1, "five": h2, "skewed": h3, "random": h4}
}

func TestPlanSegmentsExhaustive(t *testing.T) {
	for name, h := range smallHistograms() {
		buckets := h.nonEmptyBuckets()
		n := len(buckets.reps)
		require.LessOrEqual(t, n, 8, "exhaustive comparison requires <= 8 buckets")
		stats := newBucketStats(buckets.reps, buckets.weights)

		for k := 1; k <= n; k++ {
			plan, err := PlanSegments(h, k)
			require.NoError(t, err, "%s k=%d", name, k)

			wantCost, wantCuts := bruteForceOptimal(stats, n, k)
			assert.Equal(t, wantCost, plan.TotalSSE, "%s k=%d total SSE", name, k)

			var gotCuts []int
			for _, th := range plan.Thresholds {
				// Map the threshold back to the index of the bucket it
				// starts, to compare cut positions with the brute force.
				idx := 0
				for idx < n && buckets.lows[idx] != th {
					idx++
				}
				require.Less(t, idx, n, "%s k=%d threshold %d is a bucket low", name, k, th)
				gotCuts = append(gotCuts, idx)
			}
			assert.Equal(t, wantCuts, gotCuts, "%s k=%d cut positions", name, k)

			// Structural invariants: k non-empty contiguous segments
			// covering all buckets, thresholds at segment lows.
			require.Len(t, plan.Segments, k)
			require.Len(t, plan.Thresholds, k-1)
			var totalCount int64
			var sseSum float64
			prevHigh := int64(-1)
			for si, s := range plan.Segments {
				assert.Positive(t, s.Count, "%s k=%d segment %d non-empty", name, k, si)
				assert.Greater(t, s.LowValue, prevHigh, "%s k=%d segments ordered", name, k, si)
				assert.LessOrEqual(t, s.LowValue, s.HighValue)
				if si > 0 {
					assert.Equal(t, s.LowValue, plan.Thresholds[si-1])
				}
				totalCount += s.Count
				sseSum += s.SSE
				prevHigh = s.HighValue
			}
			assert.Equal(t, h.TotalCount(), totalCount)
			assert.InDelta(t, plan.TotalSSE, sseSum, math.Abs(plan.TotalSSE)*1e-12+1e-9)
		}
	}
}

func TestPlanSegmentsBeatsQuantileSplit(t *testing.T) {
	// Four equally-weighted clusters, one far away. An equal-frequency
	// (greedy quantile) split must separate {10,20} from {30,1010}, paying
	// for the huge gap twice; the optimum isolates the outlier instead.
	h := New(1, 100000, 3)
	for _, v := range []int64{10, 20, 30, 1010} {
		require.NoError(t, h.RecordValues(v, 10))
	}

	plan, err := PlanSegments(h, 2)
	require.NoError(t, err)

	// Optimal: {10,20,30} | {1010}, SSE = 2000.
	assert.Equal(t, []int64{1010}, plan.Thresholds)
	require.Len(t, plan.Segments, 2)
	assert.Equal(t, int64(30), plan.Segments[0].Count)
	assert.Equal(t, int64(10), plan.Segments[1].Count)
	assert.Equal(t, 2000.0, plan.TotalSSE)

	// Equal-frequency split at half the total count: {10,20} | {30,1010}.
	buckets := h.nonEmptyBuckets()
	stats := newBucketStats(buckets.reps, buckets.weights)
	quantileCost := stats.cost(0, 2) + stats.cost(2, 4)
	assert.Equal(t, 4802500.0, quantileCost)
	assert.Greater(t, quantileCost, plan.TotalSSE*1000,
		"greedy quantile split should be dramatically worse than the optimum")
}

func TestPlanSegmentsErrors(t *testing.T) {
	h := New(1, 1000, 2)
	require.NoError(t, h.RecordValues(10, 5))
	require.NoError(t, h.RecordValues(20, 5))

	_, err := PlanSegments(h, 0)
	assert.ErrorIs(t, err, ErrInvalidSegmentCount)

	_, err = PlanSegments(h, 3) // only 2 non-empty buckets
	assert.ErrorIs(t, err, ErrInvalidSegmentCount)

	_, err = PlanSegments(New(1, 1000, 2), 1) // empty histogram
	assert.ErrorIs(t, err, ErrInvalidSegmentCount)

	defer func(origB, origS int) {
		MaxPlanBuckets, MaxPlanSegments = origB, origS
	}(MaxPlanBuckets, MaxPlanSegments)

	MaxPlanSegments = 1
	_, err = PlanSegments(h, 2)
	assert.ErrorIs(t, err, ErrSegmentLimit)
	MaxPlanSegments = 256

	MaxPlanBuckets = 1
	_, err = PlanSegments(h, 1)
	assert.ErrorIs(t, err, ErrSegmentLimit)
}

func TestPlanSegmentsLargeCountsNoOverflow(t *testing.T) {
	// Counts and values near int64 product overflow territory: any int64
	// count*value or count*value*value product would wrap, so a correct
	// planner must accumulate in float64.
	h := New(1, 1<<50, 3)
	require.NoError(t, h.RecordValues(1<<30, 1<<40))
	require.NoError(t, h.RecordValues(1<<40, 1<<40))

	plan, err := PlanSegments(h, 1)
	require.NoError(t, err)
	require.Len(t, plan.Segments, 1)

	buckets := h.nonEmptyBuckets()
	require.Len(t, buckets.reps, 2)
	wantMean := (float64(buckets.reps[0]) + float64(buckets.reps[1])) / 2
	assert.Equal(t, wantMean, plan.Segments[0].Mean)
	assert.False(t, math.IsNaN(plan.TotalSSE) || math.IsInf(plan.TotalSSE, 0))
	assert.Greater(t, plan.TotalSSE, 0.0)
	assert.Equal(t, int64(2)<<40, plan.Segments[0].Count)

	// Splitting the two clusters must drive the SSE to zero.
	plan2, err := PlanSegments(h, 2)
	require.NoError(t, err)
	assert.Equal(t, 0.0, plan2.TotalSSE)
}

func TestPlanSegmentsSingleBucketPerSegment(t *testing.T) {
	h := New(1, 1<<20, 2)
	for _, v := range []int64{4, 400, 40000} {
		require.NoError(t, h.RecordValues(v, 7))
	}
	plan, err := PlanSegments(h, 3)
	require.NoError(t, err)
	assert.Equal(t, 0.0, plan.TotalSSE)
	require.Len(t, plan.Segments, 3)
	for _, s := range plan.Segments {
		assert.Equal(t, int64(7), s.Count)
	}
}

func BenchmarkPlanSegments(b *testing.B) {
	h := New(1, 1<<20, 2)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200000; i++ {
		// Right-skewed latency-like fill over a few hundred buckets.
		v := int64(rng.ExpFloat64()*500) + 1
		if err := h.RecordValue(v); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PlanSegments(h, 8); err != nil {
			b.Fatal(err)
		}
	}
}
