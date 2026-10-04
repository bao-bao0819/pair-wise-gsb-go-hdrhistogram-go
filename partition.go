package hdrhistogram

import (
	"errors"
	"fmt"
)

// Limits guarding the cost of PlanSegments. They are exported so callers can
// trade planning accuracy for CPU/memory; both are validated before any
// planning work starts, and the source histogram is never down-sampled or
// otherwise re-quantized to satisfy them.
var (
	// MaxPlanBuckets caps the number of non-empty buckets PlanSegments will
	// process. Planning cost grows quadratically with this number.
	MaxPlanBuckets = 2048
	// MaxPlanSegments caps the requested segment count k.
	MaxPlanSegments = 256
)

// ErrSegmentLimit is returned when the histogram or the requested segment
// count exceeds MaxPlanBuckets / MaxPlanSegments.
var ErrSegmentLimit = errors.New("hdrhistogram: segment planning limit exceeded")

// ErrInvalidSegmentCount is returned when k is smaller than 1 or greater than
// the number of non-empty buckets in the histogram.
var ErrInvalidSegmentCount = errors.New("hdrhistogram: invalid segment count")

// A Segment is one contiguous tier of an optimal partition. It spans a
// consecutive run of non-empty histogram buckets.
type Segment struct {
	// LowValue is the lowest equivalent value of the segment's first bucket.
	// Used as the threshold separating this segment from the previous one, so
	// a single equivalent bucket is never split across segments.
	LowValue int64
	// HighValue is the highest equivalent value of the segment's last bucket.
	HighValue int64
	// Count is the total number of recorded values in the segment.
	Count int64
	// Mean is the weighted mean of the bucket median-equivalent values.
	Mean float64
	// SSE is the weighted sum of squared deviations from Mean within the
	// segment.
	SSE float64
}

// A Segmentation is the result of PlanSegments: k non-empty contiguous
// segments minimizing the total weighted squared error.
type Segmentation struct {
	Segments []Segment
	// Thresholds holds the k-1 cut points between consecutive segments. Each
	// threshold is the LowValue of the following segment's first bucket.
	Thresholds []int64
	// TotalSSE is the sum of the per-segment squared errors.
	TotalSSE float64
}

// PlanSegments partitions the non-empty buckets of h into k contiguous
// segments such that the total weighted squared error is minimized.
//
// Each non-empty bucket contributes its medianEquivalentValue as the
// representative value and its count as the weight. Cuts are only allowed
// between two adjacent non-empty buckets, so an equivalent bucket is never
// split. The optimum is found exactly by dynamic programming over interval
// costs derived from prefix sums of w, w*x and w*x*x; it is not an
// equal-frequency or greedy local split.
//
// Numeric comparison strategy: all statistics are accumulated in float64
// (converting before multiplying, so large counts never overflow an int64
// product). Candidate costs are compared with a strict less-than and split
// positions are scanned in ascending order, so among exactly equal-cost
// candidates the first - i.e. lexicographically smallest - cut position wins.
// No epsilon fuzzing is applied: for a given histogram the computation is
// deterministic, and ties resolved this way are genuinely equal in float64
// arithmetic.
//
// Complexity: O(k*n^2) time and O(k*n) space for n non-empty buckets, bounded
// by MaxPlanBuckets and MaxPlanSegments which are checked before computing.
func PlanSegments(h *Histogram, k int) (*Segmentation, error) {
	if k < 1 {
		return nil, fmt.Errorf("%w: k must be >= 1, got %d", ErrInvalidSegmentCount, k)
	}
	if k > MaxPlanSegments {
		return nil, fmt.Errorf("%w: k=%d exceeds MaxPlanSegments=%d", ErrSegmentLimit, k, MaxPlanSegments)
	}

	buckets := h.nonEmptyBuckets()
	n := len(buckets.reps)

	if n > MaxPlanBuckets {
		return nil, fmt.Errorf("%w: %d non-empty buckets exceed MaxPlanBuckets=%d", ErrSegmentLimit, n, MaxPlanBuckets)
	}
	if k > n {
		return nil, fmt.Errorf("%w: k=%d exceeds %d non-empty buckets", ErrInvalidSegmentCount, k, n)
	}

	stats := newBucketStats(buckets.reps, buckets.weights)

	// Suffix DP: f[j][i] is the minimal total SSE for partitioning buckets
	// [i, n) into j segments; choice[j][i] is the smallest end index (exclusive)
	// of the first segment attaining that minimum. Scanning end positions in
	// ascending order and updating only on a strict improvement yields the
	// lexicographically smallest cut positions among equal-cost optima.
	f := make([][]float64, k+1)
	choice := make([][]int, k+1)
	for j := 0; j <= k; j++ {
		f[j] = make([]float64, n+1)
		choice[j] = make([]int, n+1)
	}
	for i := 0; i < n; i++ {
		f[1][i] = stats.cost(i, n)
		choice[1][i] = n
	}
	for j := 2; j <= k; j++ {
		// Buckets [i, n) must hold j non-empty segments, so i <= n-j.
		for i := 0; i <= n-j; i++ {
			best := -1.0
			bestEnd := -1
			// First segment covers [i, end), leaving n-end >= j-1 buckets.
			for end := i + 1; end <= n-j+1; end++ {
				c := stats.cost(i, end) + f[j-1][end]
				if bestEnd == -1 || c < best {
					best = c
					bestEnd = end
				}
			}
			f[j][i] = best
			choice[j][i] = bestEnd
		}
	}

	// Backtrack the stored choices from the front, which replays the
	// lexicographically smallest optimal cut sequence.
	seg := &Segmentation{
		Segments:   make([]Segment, 0, k),
		Thresholds: make([]int64, 0, k-1),
		TotalSSE:   f[k][0],
	}
	lo := 0
	for j := k; j >= 1; j-- {
		end := choice[j][lo]
		var count int64
		for idx := lo; idx < end; idx++ {
			count += buckets.counts[idx]
		}
		if len(seg.Segments) > 0 {
			seg.Thresholds = append(seg.Thresholds, buckets.lows[lo])
		}
		seg.Segments = append(seg.Segments, Segment{
			LowValue:  buckets.lows[lo],
			HighValue: buckets.highs[end-1],
			Count:     count,
			Mean:      stats.mean(lo, end),
			SSE:       stats.cost(lo, end),
		})
		lo = end
	}
	return seg, nil
}

// planBuckets is the per-non-empty-bucket view of a histogram used by the
// segment planner.
type planBuckets struct {
	lows, highs, reps []int64
	weights           []float64
	counts            []int64
}

// nonEmptyBuckets flattens the histogram into its non-empty buckets. Each
// bucket contributes its medianEquivalentValue as representative value and
// its count as weight.
func (h *Histogram) nonEmptyBuckets() *planBuckets {
	b := &planBuckets{}
	i := h.rIterator()
	for i.next() {
		b.lows = append(b.lows, i.valueFromIdx)
		b.highs = append(b.highs, i.highestEquivalentValue)
		b.reps = append(b.reps, h.medianEquivalentValue(i.valueFromIdx))
		b.weights = append(b.weights, float64(i.countAtIdx))
		b.counts = append(b.counts, i.countAtIdx)
	}
	return b
}

// bucketStats holds prefix sums of w, w*x and w*x*x over the per-bucket
// representative values, all in float64 so that count*value products cannot
// overflow an int64. Interval costs and means are O(1) lookups.
type bucketStats struct {
	pw, px, pxx []float64
}

func newBucketStats(reps []int64, weights []float64) *bucketStats {
	n := len(reps)
	s := &bucketStats{
		pw:  make([]float64, n+1),
		px:  make([]float64, n+1),
		pxx: make([]float64, n+1),
	}
	for idx := range reps {
		w := weights[idx]
		x := float64(reps[idx])
		s.pw[idx+1] = s.pw[idx] + w
		s.px[idx+1] = s.px[idx] + w*x
		s.pxx[idx+1] = s.pxx[idx] + w*x*x
	}
	return s
}

// cost returns the weighted squared deviation of the segment covering
// buckets [lo, hi) (exclusive hi) from its weighted mean.
func (s *bucketStats) cost(lo, hi int) float64 {
	w := s.pw[hi] - s.pw[lo]
	sx := s.px[hi] - s.px[lo]
	sxx := s.pxx[hi] - s.pxx[lo]
	return sxx - sx*sx/w
}

// mean returns the weighted mean of buckets [lo, hi) (exclusive hi).
func (s *bucketStats) mean(lo, hi int) float64 {
	return (s.px[hi] - s.px[lo]) / (s.pw[hi] - s.pw[lo])
}
