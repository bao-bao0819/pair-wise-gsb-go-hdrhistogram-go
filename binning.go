package hdrhistogram

import (
	"fmt"
	"math"
)

// MaxPlanningNonEmptyBuckets is the largest number of non-empty HDR buckets a
// LatencyBinPlan is allowed to optimize over.
//
// The planner is exact: it runs an O(k*n^2) dynamic program over the n
// non-empty buckets, so a public cap is required to bound planning cost on a
// production-sized histogram (which can hold millions of buckets). The check
// runs before any DP work; the source histogram is never resampled or reduced
// in precision.
const MaxPlanningNonEmptyBuckets = 2048

// MaxPlanningBins is the largest accepted bin count k. It is bounded by
// MaxPlanningNonEmptyBuckets; the separate, smaller cap keeps DP time and
// memory predictable.
const MaxPlanningBins = 32

// LatencyBin is one contiguous segment of a LatencyBinPlan.
//
// Low and High are the lowest and highest equivalent values of the HDR buckets
// covered by the segment, so consecutive bins tile the occupied value range
// without splitting an equivalent-value bucket. Count is the total number of
// recorded samples in the segment, and Mean is their count-weighted mean using
// each bucket's medianEquivalentValue.
type LatencyBin struct {
	Low   int64
	High  int64
	Count int64
	Mean  float64
}

// LatencyBinPlan is the result of PlanLatencyBins.
//
// Bins are k non-empty, value-ordered, contiguous segments. Thresholds holds
// the k-1 boundaries between bins: Thresholds[i] equals Bins[i+1].Low, i.e. the
// lowest equivalent value of the first bucket of the following segment, so a
// boundary never falls inside an equivalent-value bucket. A value v belongs to
// bin i when Low <= v <= High (equivalently: to the first bin whose threshold
// is greater than v).
//
// TotalSquaredError is the minimized within-segment weighted sum of squared
// deviations from each segment's mean.
type LatencyBinPlan struct {
	Bins              []LatencyBin
	Thresholds        []int64
	TotalSquaredError float64
}

type binningPoint struct {
	median int64
	low    int64
	high   int64
	count  int64
}

// PlanLatencyBins partitions the histogram into exactly k non-empty,
// contiguous latency bins that minimize the total within-bin weighted sum of
// squared deviations from each bin's weighted mean.
//
// Each non-empty HDR bucket is treated as a weighted point: its representative
// value is medianEquivalentValue and its weight is the bucket count. Cuts are
// only allowed between two adjacent non-empty buckets, so an equivalent-value
// bucket is never split. The optimum is found with an exact dynamic program
// (prefix statistics for O(1) interval cost, backtracking for the boundaries);
// equal-frequency binning and local greedy searches are not used and are not
// guaranteed to reach this optimum.
//
// When several partitions have an identical (bit-equal float64) cost, the one
// with the lexicographically smallest sequence of cut positions is chosen.
//
// It returns an error if k < 1, the histogram is empty, k exceeds the number
// of non-empty buckets, or either public planning limit is exceeded. The
// limit checks run before the dynamic program. The histogram is read only and
// is never reduced in precision.
//
// Time complexity is O(n + k*n^2) and space O(k*n), where n is the number of
// non-empty buckets (the initial bucket scan is O(countsLen)).
func (h *Histogram) PlanLatencyBins(k int) (*LatencyBinPlan, error) {
	if k < 1 {
		return nil, fmt.Errorf("k must be at least 1, got %d", k)
	}

	points := h.nonEmptyBinningPoints()
	n := len(points)
	if n == 0 {
		return nil, fmt.Errorf("cannot plan latency bins for an empty histogram")
	}
	if k > n {
		return nil, fmt.Errorf("k (%d) cannot exceed the number of non-empty buckets (%d)", k, n)
	}
	// Public cost caps: validated before any DP allocation or computation.
	if n > MaxPlanningNonEmptyBuckets {
		return nil, fmt.Errorf("histogram has %d non-empty buckets, exceeds MaxPlanningNonEmptyBuckets (%d)",
			n, MaxPlanningNonEmptyBuckets)
	}
	if k > MaxPlanningBins {
		return nil, fmt.Errorf("k (%d) exceeds MaxPlanningBins (%d)", k, MaxPlanningBins)
	}

	// Prefix statistics over weighted points, 1-indexed:
	//   pc[i] = sum of counts of points [0,i)
	//   ps[i] = sum of count*median of points [0,i)
	//   pq[i] = sum of count*median^2 of points [0,i)
	// The float64 conversions happen BEFORE the multiply: an int64 product of
	// count*median can wrap ~9.2e18, while float64 holds the whole feasible
	// magnitude (count <= MaxInt64, median <= MaxInt64 => count*median^2 < 8e56,
	// far below the float64 max of ~1.8e308).
	pc := make([]int64, n+1)
	ps := make([]float64, n+1)
	pq := make([]float64, n+1)
	for i, p := range points {
		x := float64(p.median)
		w := float64(p.count)
		pc[i+1] = pc[i] + p.count
		ps[i+1] = ps[i] + w*x
		pq[i+1] = pq[i] + w*x*x
	}

	// intervalCost is the weighted sum of squared deviations of points [l,r)
	// around their weighted mean: sum(w*x^2) - sum(w*x)^2/sum(w).
	intervalCost := func(l, r int) float64 {
		q := pq[r] - pq[l]
		s := ps[r] - ps[l]
		w := float64(pc[r] - pc[l])
		c := q - s*s/w
		// Mathematically non-negative. A small negative value can only come from
		// floating-point cancellation (e.g. a single-bucket interval), so clamp
		// only residuals that are negligible relative to the sum of squares.
		if c < 0 && (q == 0 || c > -1e-9*math.Abs(q)) {
			c = 0
		}
		return c
	}

	// Right-to-left DP:
	//   prev[i] = best cost of covering points [i,n) with a-1 bins.
	// Base (a=1): the whole suffix is one bin.
	// choose[a][i] = start m of the next bin in the optimal partition of [i,n)
	// into a bins.
	//
	// Candidate m is scanned ascending and the incumbent is replaced only on a
	// STRICTLY smaller cost, so a tie keeps the smallest m. Starting the
	// reconstruction at the first cut therefore fixes the smallest possible
	// first cut, then the smallest second cut conditional on it, etc.: the
	// lexicographically smallest cut-position vector among all minimum-cost
	// partitions. Costs are compared as exact float64 values (no epsilon), which
	// makes the tie-break deterministic across platforms under IEEE-754.
	prev := make([]float64, n)
	for i := 0; i < n; i++ {
		prev[i] = intervalCost(i, n)
	}

	choose := make([][]int, k+1)
	for a := 2; a <= k; a++ {
		cur := make([]float64, n)
		ch := make([]int, n)
		// i points need a bins: the suffix [i,n) holds at least a points, so
		// i <= n-a.
		for i := 0; i <= n-a; i++ {
			best := math.Inf(1)
			bestM := -1
			// First bin is [i,m); remaining [m,n) must fit a-1 bins, so m <= n-a+1.
			for m := i + 1; m <= n-a+1; m++ {
				cand := intervalCost(i, m) + prev[m]
				if cand < best {
					best = cand
					bestM = m
				}
			}
			cur[i] = best
			ch[i] = bestM
		}
		prev = cur
		choose[a] = ch
	}

	// Backtrack the boundaries. bounds[0]=0, bounds[k]=n; bin j is
	// points[bounds[j], bounds[j+1]).
	bounds := make([]int, 0, k+1)
	bounds = append(bounds, 0)
	cur := 0
	for a := k; a >= 2; a-- {
		cur = choose[a][cur]
		bounds = append(bounds, cur)
	}
	bounds = append(bounds, n)

	bins := make([]LatencyBin, k)
	thresholds := make([]int64, 0, k-1)
	for j := 0; j < k; j++ {
		l, r := bounds[j], bounds[j+1]
		count := pc[r] - pc[l]
		bins[j] = LatencyBin{
			Low:   points[l].low,
			High:  points[r-1].high,
			Count: count,
			Mean:  (ps[r] - ps[l]) / float64(count),
		}
		if j > 0 {
			// Boundary is the low end of the next segment's first bucket.
			thresholds = append(thresholds, points[l].low)
		}
	}

	return &LatencyBinPlan{
		Bins:              bins,
		Thresholds:        thresholds,
		TotalSquaredError: prev[0],
	}, nil
}

// nonEmptyBinningPoints collects the non-empty HDR buckets in ascending value
// order with their representative median value, equivalent-value range and
// count.
func (h *Histogram) nonEmptyBinningPoints() []binningPoint {
	points := make([]binningPoint, 0)
	it := h.iterator()
	for it.next() {
		if it.countAtIdx == 0 {
			continue
		}
		points = append(points, binningPoint{
			median: h.medianEquivalentValue(it.valueFromIdx),
			low:    h.lowestEquivalentValue(it.valueFromIdx),
			high:   it.highestEquivalentValue,
			count:  it.countAtIdx,
		})
	}
	return points
}
