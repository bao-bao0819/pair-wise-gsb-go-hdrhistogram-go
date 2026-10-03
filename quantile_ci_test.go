package hdrhistogram_test

import (
	"errors"
	"math"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

func nearlyEqual(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// Hand-computed on the short distribution 10,20,...,100 (N=10, 3 sig figs):
//
//	alpha=0.05: eps=sqrt(log(40)/20)≈0.429469
//	  q-eps≈0.56053 -> ceil(5.6053)=6th order stat = 60 (bucket low 60)
//	  q+eps≈1.41947 >= 1 -> no finite upper endpoint
//	alpha=0.01: eps=sqrt(log(200)/20)≈0.514700
//	  q-eps≈0.47530 -> ceil(4.7530)=5th order stat = 50
//	  q+eps≈1.50470 >= 1 -> no finite upper endpoint
func TestQuantileCI_SmallNP99IsUnboundedAbove(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 3)
	for v := int64(10); v <= 100; v += 10 {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	ci, err := h.QuantileConfidenceIntervalAt(0.99, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if ci.N != 10 {
		t.Errorf("N = %d, want 10", ci.N)
	}
	if !nearlyEqual(ci.Epsilon, math.Sqrt(math.Log(40)/20), 1e-15) {
		t.Errorf("epsilon = %.15f", ci.Epsilon)
	}
	if ci.Lower != 60 || ci.LowerClampedToSupport {
		t.Errorf("lower = %d (clamped=%v), want 60 non-clamped", ci.Lower, ci.LowerClampedToSupport)
	}
	if ci.UpperFinite || ci.Upper != 0 {
		t.Errorf("upper finite=%v value=%d, want infinite (0)", ci.UpperFinite, ci.Upper)
	}

	ci01, err := h.QuantileConfidenceIntervalAt(0.99, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if ci01.Lower != 50 {
		t.Errorf("alpha=0.01 lower = %d, want 50", ci01.Lower)
	}
	if ci01.UpperFinite {
		t.Errorf("alpha=0.01 upper should still be infinite, got %d", ci01.Upper)
	}
}

// Shrinking alpha (demanding more confidence) must never narrow the interval.
func TestQuantileCI_SmallerAlphaNeverNarrows(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 3)
	for v := int64(1); v <= 2000; v++ {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	// N=2000, q=0.5, hand-computed ranks:
	//	alpha=0.5 : eps≈0.018616 -> ranks 963, 1038 (values 963, 1038)
	//	alpha=0.05: eps≈0.030368 -> ranks 940, 1061 (values 940, 1061)
	//	alpha=0.01: eps≈0.036395 -> ranks 928, 1073 (values 928, 1073)
	//	alpha=1e-3: eps≈0.043592 -> ranks 913, 1088 (values 913, 1088)
	cases := []struct {
		alpha      float64
		lo, hiRank int64
	}{
		{0.5, 963, 1038},
		{0.05, 940, 1061},
		{0.01, 928, 1073},
		{0.001, 913, 1088},
	}
	var prev *hdrhistogram.QuantileConfidenceInterval
	for _, c := range cases {
		ci, err := h.QuantileConfidenceIntervalAt(0.5, c.alpha)
		if err != nil {
			t.Fatal(err)
		}
		if ci.Lower != c.lo || ci.Upper != c.hiRank || !ci.UpperFinite {
			t.Errorf("alpha=%v: [%d, %d] finite=%v, want [%d, %d]",
				c.alpha, ci.Lower, ci.Upper, ci.UpperFinite, c.lo, c.hiRank)
		}
		if prev != nil {
			if ci.Lower > prev.Lower {
				t.Errorf("alpha=%v lower %d moved inward from %d", c.alpha, ci.Lower, prev.Lower)
			}
			if ci.Upper < prev.Upper {
				t.Errorf("alpha=%v upper %d moved inward from %d", c.alpha, ci.Upper, prev.Upper)
			}
		}
		prev = ci
	}

	// Epsilon must be strictly monotone as alpha shrinks.
	if !(cases[0].alpha > cases[1].alpha) {
		t.Fatal("case ordering broken")
	}
	eps50, _ := h.QuantileConfidenceIntervalAt(0.5, 0.5)
	eps05, _ := h.QuantileConfidenceIntervalAt(0.5, 0.05)
	if !(eps05.Epsilon > eps50.Epsilon) {
		t.Errorf("epsilon did not grow as alpha shrank: %v !> %v", eps05.Epsilon, eps50.Epsilon)
	}
}

// Order-statistic ranks land on values whose HDR equivalence bucket is wider
// than one unit; the endpoints must widen to the bucket low/high edges.
// 2 sig figs, N=20: five each of 100,200,300,600.
//
//	alpha=0.05: eps≈0.303681 -> ranks 4 (100) and 17 (600).
//	  100 is in a width-1 bucket [100,100]; 600 is in a width-4 bucket
//	  [600,603], so the upper endpoint is 603.
//	alpha=0.5 : eps≈0.186165 -> ranks 7 (200) and 14 (300).
//	  200 -> [200,201]; 300 -> [300,301].
func TestQuantileCI_WidensToHDRBucketEdges(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 2)
	for _, v := range []int64{100, 200, 300, 600} {
		if err := h.RecordValues(v, 5); err != nil {
			t.Fatal(err)
		}
	}

	ci, err := h.QuantileConfidenceIntervalAt(0.5, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Lower != 100 || ci.Upper != 603 || !ci.UpperFinite {
		t.Errorf("got [%d, %d] finite=%v, want [100, 603]", ci.Lower, ci.Upper, ci.UpperFinite)
	}

	ci, err = h.QuantileConfidenceIntervalAt(0.5, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Lower != 200 || ci.Upper != 301 {
		t.Errorf("got [%d, %d], want [200, 301]", ci.Lower, ci.Upper)
	}
}

// Once enough independent observations exist, p99's upper endpoint becomes
// finite; immediately below the threshold it is still unbounded. This pins the
// rank boundary (eps = 0.01 exactly at N=18444.6...) and proves Max is used
// only when the band actually closes.
func TestQuantileCI_P99UpperFiniteThreshold(t *testing.T) {
	mk := func(n int64) *hdrhistogram.Histogram {
		h := hdrhistogram.New(1, 1000000, 3)
		for v := int64(1); v <= n; v++ {
			if err := h.RecordValue(v); err != nil {
				t.Fatal(err)
			}
		}
		return h
	}

	loose := mk(18444)
	ci, err := loose.QuantileConfidenceIntervalAt(0.99, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if ci.UpperFinite {
		t.Errorf("N=18444: upper unexpectedly finite (%d); want unbounded", ci.Upper)
	}
	if ci.LowerClampedToSupport {
		t.Errorf("N=18444: lower should be an order statistic, not the support clamp")
	}

	tight := mk(18445)
	ci, err = tight.QuantileConfidenceIntervalAt(0.99, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	// 18445 lies in the 3-sig-fig width-16 bucket [18432, 18447], and both
	// endpoints are widened to their bucket edges: rank 18262 (the lower rank)
	// maps to its own bucket low, and the top order statistic maps to 18447.
	if !ci.UpperFinite || ci.Upper != 18447 {
		t.Errorf("N=18445: upper finite=%v value=%d, want 18447", ci.UpperFinite, ci.Upper)
	}
	if ci.Lower >= ci.Upper || ci.Lower <= 0 {
		t.Errorf("N=18445: degenerate lower %d", ci.Lower)
	}
}

// A low quantile on a tiny sample hits the non-negative support lower bound;
// N=10, q=0.01, alpha=0.05 -> q-eps≈-0.41947 <= 0, q+eps≈0.43947 -> rank 5.
func TestQuantileCI_LowerClampedToSupport(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 3)
	for v := int64(10); v <= 100; v += 10 {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}
	ci, err := h.QuantileConfidenceIntervalAt(0.01, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Lower != 0 || !ci.LowerClampedToSupport {
		t.Errorf("lower=%d clamped=%v, want 0 with clamp flag", ci.Lower, ci.LowerClampedToSupport)
	}
	if !ci.UpperFinite || ci.Upper != 50 {
		t.Errorf("upper finite=%v value=%d, want finite 50", ci.UpperFinite, ci.Upper)
	}
}

func TestQuantileCI_Errors(t *testing.T) {
	h := hdrhistogram.New(1, 100000, 3)
	for v := int64(10); v <= 100; v += 10 {
		if err := h.RecordValue(v); err != nil {
			t.Fatal(err)
		}
	}

	badQ := []float64{0, 1, -0.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, q := range badQ {
		if _, err := h.QuantileConfidenceIntervalAt(q, 0.05); !errors.Is(err, hdrhistogram.ErrInvalidQuantile) {
			t.Errorf("q=%v: err=%v, want ErrInvalidQuantile", q, err)
		}
	}
	badA := []float64{0, 1, -0.05, 1.01, math.NaN(), math.Inf(1)}
	for _, a := range badA {
		if _, err := h.QuantileConfidenceIntervalAt(0.99, a); !errors.Is(err, hdrhistogram.ErrInvalidAlpha) {
			t.Errorf("alpha=%v: err=%v, want ErrInvalidAlpha", a, err)
		}
	}

	empty := hdrhistogram.New(1, 100000, 3)
	if _, err := empty.QuantileConfidenceIntervalAt(0.99, 0.05); !errors.Is(err, hdrhistogram.ErrEmptyHistogram) {
		t.Errorf("empty histogram: err=%v, want ErrEmptyHistogram", err)
	}
	// NaN on an empty histogram is still a parameter error first.
	if _, err := empty.QuantileConfidenceIntervalAt(math.NaN(), 0.05); !errors.Is(err, hdrhistogram.ErrInvalidQuantile) {
		t.Errorf("empty+NaN: err=%v, want ErrInvalidQuantile", err)
	}
}

// Bulk counts that genuinely represent n independent observations must give
// the same interval as recording them one at a time.
func TestQuantileCI_BulkCountEqualsIndividual(t *testing.T) {
	bulk := hdrhistogram.New(1, 100000, 3)
	for _, v := range []int64{10, 20, 30} {
		if err := bulk.RecordValues(v, 4); err != nil {
			t.Fatal(err)
		}
	}
	individual := hdrhistogram.New(1, 100000, 3)
	for _, v := range []int64{10, 20, 30} {
		for i := 0; i < 4; i++ {
			if err := individual.RecordValue(v); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, err := bulk.QuantileConfidenceIntervalAt(0.5, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := individual.QuantileConfidenceIntervalAt(0.5, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if a.N != b.N || a.Lower != b.Lower || a.Upper != b.Upper || a.UpperFinite != b.UpperFinite {
		t.Errorf("bulk %+v != individual %+v", a, b)
	}
}
