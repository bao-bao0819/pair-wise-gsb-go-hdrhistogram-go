package hdrhistogram_test

import (
	"math"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// collectNonEmptyBars returns the non-empty bars of h in ascending value order.
func collectNonEmptyBars(h *hdrhistogram.Histogram) []hdrhistogram.Bar {
	var bars []hdrhistogram.Bar
	for _, b := range h.Distribution() {
		if b.Count != 0 {
			bars = append(bars, b)
		}
	}
	return bars
}

// barMedian mirrors medianEquivalentValue using a Bar's bounds: integer
// lowestEquivalentValue plus half the equivalent range (truncated), so a
// width-1 bucket's median is its lower bound, not lower+0.5.
func barMedian(b hdrhistogram.Bar) float64 {
	return float64(b.From + (b.To-b.From+1)/2)
}

// weightedSSE computes the weighted sum of squared deviations of the given bars
// around their weighted mean, using each bar's median equivalent value. It is
// the independent oracle used by the planner tests.
func weightedSSE(bars []hdrhistogram.Bar) float64 {
	var count, sum float64
	for _, b := range bars {
		x := barMedian(b)
		count += float64(b.Count)
		sum += float64(b.Count) * x
	}
	mean := sum / count
	var sse float64
	for _, b := range bars {
		x := barMedian(b)
		d := x - mean
		sse += float64(b.Count) * d * d
	}
	return sse
}

func assertPlanShape(t *testing.T, p *hdrhistogram.LatencyBinPlan, k int, totalCount int64) {
	t.Helper()
	if len(p.Bins) != k {
		t.Fatalf("got %d bins, want %d", len(p.Bins), k)
	}
	if len(p.Thresholds) != k-1 {
		t.Fatalf("got %d thresholds, want %d", len(p.Thresholds), k-1)
	}
	var count int64
	for j, b := range p.Bins {
		if b.Count <= 0 {
			t.Fatalf("bin %d is empty", j)
		}
		if b.Low > b.High {
			t.Fatalf("bin %d low > high", j)
		}
		if b.Mean < float64(b.Low) || b.Mean > float64(b.High+1) {
			t.Fatalf("bin %d mean %v outside [%d,%d]", j, b.Mean, b.Low, b.High)
		}
		if j > 0 {
			if b.Low <= p.Bins[j-1].High {
				t.Fatalf("bins %d and %d overlap/unsorted", j-1, j)
			}
			if p.Thresholds[j-1] != b.Low {
				t.Fatalf("threshold %d = %d, want next bin low %d", j-1, p.Thresholds[j-1], b.Low)
			}
		}
		count += b.Count
	}
	if count != totalCount {
		t.Fatalf("bin counts sum to %d, want total %d", count, totalCount)
	}
}

func TestPlanLatencyBins(t *testing.T) {
	// Distinct unit-resolution buckets at 1..8 plus a far tail bucket.
	values := []int64{1, 2, 3, 4, 5, 6, 7, 8, 100000}
	h := hdrhistogram.New(1, 100000, 3)
	for _, v := range values {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	for k := 1; k <= 8; k++ {
		p, err := h.PlanLatencyBins(k)
		if err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}
		assertPlanShape(t, p, k, int64(len(values)))
		// Thresholds always land on the low end of a real non-empty bucket.
		bars := collectNonEmptyBars(h)
		for _, th := range p.Thresholds {
			found := false
			for _, b := range bars {
				if th == b.From {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("k=%d: threshold %d is not the low end of a non-empty bucket", k, th)
			}
		}
	}

	// k == n: each bucket is its own bin; total error must be zero.
	p, err := h.PlanLatencyBins(9)
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalSquaredError != 0 {
		t.Fatalf("k=n SSE = %v, want 0", p.TotalSquaredError)
	}

	// k == 1: single bin covers everything and matches the whole-histogram SSE.
	p, err = h.PlanLatencyBins(1)
	if err != nil {
		t.Fatal(err)
	}
	want := weightedSSE(collectNonEmptyBars(h))
	if math.Abs(p.TotalSquaredError-want) > 1e-6*math.Max(1, want) {
		t.Fatalf("k=1 SSE = %v, want %v", p.TotalSquaredError, want)
	}

	// Determinism: repeated calls return identical boundaries and cost.
	p1, _ := h.PlanLatencyBins(3)
	p2, _ := h.PlanLatencyBins(3)
	if len(p1.Thresholds) != len(p2.Thresholds) {
		t.Fatal("repeated plans differ in shape")
	}
	for i := range p1.Thresholds {
		if p1.Thresholds[i] != p2.Thresholds[i] {
			t.Fatalf("repeated plans differ: %v vs %v", p1.Thresholds, p2.Thresholds)
		}
	}
}

func TestPlanLatencyBinsErrors(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 3)
	for _, v := range []int64{1, 2, 3} {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.PlanLatencyBins(0); err == nil {
		t.Fatal("k=0 must error")
	}
	if _, err := h.PlanLatencyBins(4); err == nil {
		t.Fatal("k > non-empty buckets must error")
	}

	empty := hdrhistogram.New(1, 100000, 3)
	if _, err := empty.PlanLatencyBins(1); err == nil {
		t.Fatal("empty histogram must error")
	}

	// k above the public k cap. 50 distinct buckets, k=40 > MaxPlanningBins(32).
	hk := hdrhistogram.New(1, 100000, 3)
	for v := 1; v <= 50; v++ {
		if err := hk.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hk.PlanLatencyBins(40); err == nil {
		t.Fatal("k > MaxPlanningBins must error")
	}

	// Non-empty bucket count above the public cap: unit resolution fills one
	// distinct bucket per recorded value up to 2048, so 2100 points > 2048.
	hn := hdrhistogram.New(1, 1<<20, 3)
	for v := 1; v <= 2100; v++ {
		if err := hn.RecordValue(int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := hn.PlanLatencyBins(4)
	if err == nil {
		t.Fatalf("non-empty bucket count > MaxPlanningNonEmptyBuckets must error, got plan %v", p)
	}
}

// TestPlanLatencyBinsBeatsEqualFrequency constructs a skew distribution where a
// single equal-frequency (quantile/p50-style) cut is far worse than the optimal
// 2-bin partition.
func TestPlanLatencyBinsBeatsEqualFrequency(t *testing.T) {
	// Four equally weighted points near 10 and one equally weighted point at
	// 100000. The p50 equal-frequency cut splits the 10-cluster (10|10,10,10,big)
	// with a huge squared error; the optimal cut isolates the tail.
	values := []int64{10, 10, 11, 12, 100000}
	h := hdrhistogram.New(1, 100000, 3)
	for _, v := range values {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := h.PlanLatencyBins(2)
	if err != nil {
		t.Fatal(err)
	}

	bars := collectNonEmptyBars(h)
	if len(bars) != 4 {
		t.Fatalf("expected 4 non-empty buckets, got %d", len(bars))
	}

	// Equal-frequency (quantile) split: put half the weight in each bin. The
	// p50 boundary falls after the second bucket.
	greedyCut := 2
	greedySSE := weightedSSE(bars[:greedyCut]) + weightedSSE(bars[greedyCut:])

	if plan.TotalSquaredError >= greedySSE/100 {
		t.Fatalf("optimal SSE %v is not at least 100x better than equal-frequency SSE %v",
			plan.TotalSquaredError, greedySSE)
	}
	// The optimum isolates the tail bucket.
	if plan.Thresholds[0] != bars[3].From {
		t.Fatalf("optimal threshold %d, want tail bucket low %d",
			plan.Thresholds[0], bars[3].From)
	}
}

// TestPlanLatencyBinsNoInt64Overflow uses near-maximal counts and large values
// to prove the weighted statistics never multiply int64*int64.
func TestPlanLatencyBinsNoInt64Overflow(t *testing.T) {
	h := hdrhistogram.New(1, 1<<30, 3)
	// ~4.6e18 samples near 1 and the same near 1e9; their int64 count*value
	// product would wrap if computed in integer arithmetic.
	const big = int64(4_600_000_000_000_000_000)
	if err := h.RecordValues(1, big); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordValues(1_000_000_000, big); err != nil {
		t.Fatal(err)
	}

	p, err := h.PlanLatencyBins(2)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(p.TotalSquaredError) || math.IsInf(p.TotalSquaredError, 0) {
		t.Fatalf("SSE must be finite, got %v", p.TotalSquaredError)
	}
	if p.TotalSquaredError != 0 {
		// two singleton buckets with k=2: zero within-bin deviation
		t.Fatalf("two single-bucket bins: SSE = %v, want 0", p.TotalSquaredError)
	}
	if p.Bins[0].Count != big || p.Bins[1].Count != big {
		t.Fatalf("bin counts = %d,%d, want %d each", p.Bins[0].Count, p.Bins[1].Count, big)
	}
}

// TestPlanLatencyBinsScaledHistogram checks thresholds on a histogram whose
// unitMagnitude > 0: each bucket spans several integer values and the
// threshold must be the lowest equivalent value of the next segment's first
// bucket, never a midpoint.
func TestPlanLatencyBinsScaledHistogram(t *testing.T) {
	h := hdrhistogram.New(1000, 1_000_000_000, 3)
	// Three well-separated clusters.
	for _, v := range []int64{1000, 1001, 2000000, 900000000} {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}
	p, err := h.PlanLatencyBins(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Thresholds) != 2 {
		t.Fatalf("got %d thresholds, want 2", len(p.Thresholds))
	}
	bars := collectNonEmptyBars(h)
	for j, th := range p.Thresholds {
		if th != bars[j+1].From {
			t.Fatalf("threshold %d = %d, want bucket low %d", j, th, bars[j+1].From)
		}
		if th > bars[j+1].To {
			t.Fatalf("threshold %d exceeds its bucket high %d", th, bars[j+1].To)
		}
	}
}

func BenchmarkPlanLatencyBins(b *testing.B) {
	// Small, representative workload: 256 non-empty buckets into 8 bins.
	const n = 256
	h := hdrhistogram.New(1, 1<<20, 3)
	for v := 1; v <= n; v++ {
		if err := h.RecordValue(int64(v)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.PlanLatencyBins(8); err != nil {
			b.Fatal(err)
		}
	}
}
