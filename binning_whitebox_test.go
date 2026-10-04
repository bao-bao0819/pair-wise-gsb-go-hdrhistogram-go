package hdrhistogram

import (
	"math"
	"math/rand"
	"testing"
)

// binningPrefixes holds the exact same prefix statistics the planner builds, so
// the exhaustive oracle performs bit-identical interval arithmetic.
type binningPrefixes struct {
	pc []int64
	ps []float64
	pq []float64
}

func buildBinningPrefixes(points []binningPoint) binningPrefixes {
	n := len(points)
	pre := binningPrefixes{
		pc: make([]int64, n+1),
		ps: make([]float64, n+1),
		pq: make([]float64, n+1),
	}
	for i, p := range points {
		x := float64(p.median)
		w := float64(p.count)
		pre.pc[i+1] = pre.pc[i] + p.count
		pre.ps[i+1] = pre.ps[i] + w*x
		pre.pq[i+1] = pre.pq[i] + w*x*x
	}
	return pre
}

func (pre binningPrefixes) intervalCost(l, r int) float64 {
	q := pre.pq[r] - pre.pq[l]
	s := pre.ps[r] - pre.ps[l]
	w := float64(pre.pc[r] - pre.pc[l])
	c := q - s*s/w
	if c < 0 && (q == 0 || c > -1e-9*math.Abs(q)) {
		c = 0
	}
	return c
}

// enumeratePlans enumerates every way to split n points into k contiguous,
// non-empty groups, invokes best with each cut vector (internal bucket
// positions, ascending), and returns nothing; the callback accumulates the
// minimum cost and its lexicographically smallest cut vector.
func enumeratePlans(n, k int, totalCost func(cuts []int) float64) (float64, []int) {
	best := math.Inf(1)
	var bestCuts []int
	cuts := make([]int, 0, k-1)

	var rec func(start, remaining int)
	// Cuts are the internal boundaries: cut j is the start index of bin j+1.
	rec = func(start, remaining int) {
		if remaining == 0 {
			cost := totalCost(cuts)
			smaller := cost < best
			equal := cost == best
			lexSmaller := false
			if equal && bestCuts != nil {
				for i := range cuts {
					if cuts[i] != bestCuts[i] {
						lexSmaller = cuts[i] < bestCuts[i]
						break
					}
				}
			}
			if smaller || (equal && (bestCuts == nil || lexSmaller)) {
				best = cost
				bestCuts = append([]int(nil), cuts...)
			}
			return
		}
		// Leave at least remaining points after this cut.
		for m := start; m <= n-remaining; m++ {
			cuts = append(cuts, m)
			rec(m+1, remaining-1)
			cuts = cuts[:len(cuts)-1]
		}
	}
	rec(1, k-1)
	return best, bestCuts
}

func checkPlanAgainstOracle(t *testing.T, h *Histogram, k int) {
	t.Helper()
	points := h.nonEmptyBinningPoints()
	n := len(points)
	pre := buildBinningPrefixes(points)

	totalCost := func(cuts []int) float64 {
		// Left-associative accumulation, exactly as the DP recurrence builds it.
		prev := 0
		total := 0.0
		for _, m := range cuts {
			total += pre.intervalCost(prev, m)
			prev = m
		}
		total += pre.intervalCost(prev, n)
		return total
	}

	wantCost, wantCuts := enumeratePlans(n, k, totalCost)

	plan, err := h.PlanLatencyBins(k)
	if err != nil {
		t.Fatalf("PlanLatencyBins(%d): %v", k, err)
	}
	if plan.TotalSquaredError != wantCost {
		t.Fatalf("k=%d: DP SSE %.17g, exhaustive SSE %.17g",
			k, plan.TotalSquaredError, wantCost)
	}

	// Convert thresholds back to internal point positions and compare to the
	// lex-min cut vector.
	if len(plan.Thresholds) != len(wantCuts) {
		t.Fatalf("k=%d: got %d thresholds, want %d", k, len(plan.Thresholds), len(wantCuts))
	}
	for j, cut := range wantCuts {
		if plan.Thresholds[j] != points[cut].low {
			t.Fatalf("k=%d: threshold %d = %d, want low of bucket index %d (%d); cuts DP=%v oracle=%v",
				k, j, plan.Thresholds[j], cut, points[cut].low, plan.Thresholds, wantCuts)
		}
	}

	// Verify per-bin count and weighted mean.
	bounds := append(append([]int{0}, wantCuts...), n)
	for j := 0; j < k; j++ {
		l, r := bounds[j], bounds[j+1]
		count := pre.pc[r] - pre.pc[l]
		if plan.Bins[j].Count != count {
			t.Fatalf("k=%d bin %d: count %d, want %d", k, j, plan.Bins[j].Count, count)
		}
		mean := (pre.ps[r] - pre.ps[l]) / float64(count)
		if plan.Bins[j].Mean != mean {
			t.Fatalf("k=%d bin %d: mean %v, want %v", k, j, plan.Bins[j].Mean, mean)
		}
	}
}

func histogramFromPoints(t *testing.T, points []binningPoint) *Histogram {
	t.Helper()
	h := New(1, 100000, 3)
	for _, p := range points {
		if err := h.RecordValues(p.median, p.count); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// TestPlanLatencyBinsExhaustive brute-forces every legal partition for every
// histogram with at most 8 non-empty buckets and proves the DP reports the
// globally optimal cost with the lexicographically smallest stable boundary.
func TestPlanLatencyBinsExhaustive(t *testing.T) {
	// Symmetric 1,2,3 with equal weights forces an exact cost tie at k=2:
	// {1}|{2,3} and {1,2}|{3} both cost 0.5, so lex-min must pick the former.
	tiePoints := []binningPoint{
		{median: 1, low: 1, high: 1, count: 1},
		{median: 2, low: 2, high: 2, count: 1},
		{median: 3, low: 3, high: 3, count: 1},
	}
	tieHist := histogramFromPoints(t, tiePoints)
	checkPlanAgainstOracle(t, tieHist, 2)
	if got := tieHistPlanThreshold(t, tieHist); got != 2 {
		t.Fatalf("tie-break threshold = %d, want lex-min 2", got)
	}

	// Fixed skew distribution (the blackbox scenario, re-derived from buckets).
	h := New(1, 100000, 3)
	for _, v := range []int64{10, 10, 11, 12, 100000} {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}
	for k := 1; k <= 4; k++ {
		checkPlanAgainstOracle(t, h, k)
	}

	// Random histograms with up to 8 non-empty buckets and varied counts.
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 200; trial++ {
		n := 1 + rng.Intn(8)
		// Pick well-separated medians so each lands in a distinct unit bucket.
		points := make([]binningPoint, n)
		for i := range points {
			med := int64(1 + 2*i)
			points[i] = binningPoint{
				median: med,
				low:    med,
				high:   med,
				count:  1 + rng.Int63n(20),
			}
		}
		h := histogramFromPoints(t, points)
		if len(h.nonEmptyBinningPoints()) != n {
			t.Fatalf("trial %d: expected %d non-empty buckets", trial, n)
		}
		for k := 1; k <= n; k++ {
			checkPlanAgainstOracle(t, h, k)
		}
	}
}

func tieHistPlanThreshold(t *testing.T, h *Histogram) int64 {
	t.Helper()
	p, err := h.PlanLatencyBins(2)
	if err != nil {
		t.Fatal(err)
	}
	return p.Thresholds[0]
}

// TestPlanLatencyBinsLimitsCheckedBeforeCompute proves the public caps are
// enforced before DP work and the histogram is left untouched.
func TestPlanLatencyBinsLimitsCheckedBeforeCompute(t *testing.T) {
	// k cap is hit even though the histogram has fewer buckets than k would
	// otherwise allow: k=40 > MaxPlanningBins on 50 buckets.
	h := New(1, 100000, 3)
	for v := 1; v <= 50; v++ {
		if err := h.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.PlanLatencyBins(MaxPlanningBins + 1); err == nil {
		t.Fatal("k over cap must error")
	}
	if h.TotalCount() != 50 {
		t.Fatalf("histogram mutated: total count %d", h.TotalCount())
	}

	// n cap: bucket count over the limit is rejected regardless of a small k.
	hn := New(1, 1<<20, 3)
	for v := 1; v <= MaxPlanningNonEmptyBuckets+52; v++ {
		if err := hn.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := hn.PlanLatencyBins(2)
	if err == nil {
		t.Fatalf("over-cap histograms must error, got %v", p)
	}

	// A histogram exactly at the n cap still plans with a small k (boundary).
	hb := New(1, 1<<20, 3)
	for v := 1; v <= MaxPlanningNonEmptyBuckets; v++ {
		if err := hb.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hb.PlanLatencyBins(2); err != nil {
		t.Fatalf("boundary at MaxPlanningNonEmptyBuckets should succeed: %v", err)
	}
	// A histogram exactly at the k cap still plans (boundary).
	hk := New(1, 1<<20, 3)
	for v := 1; v <= MaxPlanningBins; v++ {
		if err := hk.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hk.PlanLatencyBins(MaxPlanningBins); err != nil {
		t.Fatalf("boundary at MaxPlanningBins should succeed: %v", err)
	}
}
