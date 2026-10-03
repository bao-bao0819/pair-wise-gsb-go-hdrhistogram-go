package hdrhistogram_test

import (
	"errors"
	"math"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

func TestQuantileConfidenceIntervalHandCalculatedRanksAndBuckets(t *testing.T) {
	// N=10, with cumulative ranks: 1=10; 2-3=20; 4-6=40; 7-10=320.
	// With two significant digits, values 256-511 occupy two-unit HDR buckets,
	// so the equivalent bucket containing 320 is [320, 321].
	h := hdrhistogram.New(0, 1000, 2)
	records := []struct {
		value int64
		count int64
	}{
		{10, 1},
		{20, 2},
		{40, 3},
		{320, 4},
	}
	for _, record := range records {
		if err := h.RecordValues(record.value, record.count); err != nil {
			t.Fatal(err)
		}
	}

	// alpha=.2: epsilon=sqrt(log(10)/20)=0.3393; generalized-inverse ranks are
	// ceil(1.607)=2 and ceil(8.393)=9.
	loose, err := h.QuantileConfidenceInterval(0.5, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	assertInterval(t, loose, hdrhistogram.QuantileConfidenceInterval{
		Quantile:    0.5,
		Alpha:       0.2,
		N:           10,
		Epsilon:     0.339307,
		Lower:       20,
		Upper:       321,
		UpperFinite: true,
	}, 1e-6)

	// alpha=.05: epsilon=sqrt(log(40)/20)=0.4295; ranks are 1 and 10.
	tighterFailure, err := h.QuantileConfidenceInterval(0.5, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	assertInterval(t, tighterFailure, hdrhistogram.QuantileConfidenceInterval{
		Quantile:    0.5,
		Alpha:       0.05,
		N:           10,
		Epsilon:     0.429469,
		Lower:       10,
		Upper:       321,
		UpperFinite: true,
	}, 1e-6)

	// alpha=.01: epsilon=sqrt(log(200)/20)=0.5147, so q-epsilon<0 and
	// q+epsilon>1. The observed maximum cannot bound the population tail.
	strict, err := h.QuantileConfidenceInterval(0.5, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	assertInterval(t, strict, hdrhistogram.QuantileConfidenceInterval{
		Quantile:    0.5,
		Alpha:       0.01,
		N:           10,
		Epsilon:     0.514700,
		Lower:       0,
		Upper:       0,
		UpperFinite: false,
	}, 1e-6)

	if !(loose.Lower >= tighterFailure.Lower && tighterFailure.Lower >= strict.Lower) {
		t.Fatal("lower endpoints moved upward as alpha decreased")
	}
	if !loose.UpperFinite || !tighterFailure.UpperFinite || strict.UpperFinite {
		t.Fatal("upper endpoint did not become unbounded as alpha decreased")
	}
}

func TestQuantileConfidenceIntervalLowNP99HasNoFiniteUpperBound(t *testing.T) {
	h := hdrhistogram.New(0, 1000, 3)
	for value := 1; value <= 50; value++ {
		if err := h.RecordValue(int64(value)); err != nil {
			t.Fatal(err)
		}
	}

	// alpha=.05, N=50: epsilon=sqrt(log(40)/100)=0.1921, so the upper
	// probability 1.1821 exceeds the empirical distribution's support.
	interval, err := h.QuantileConfidenceInterval(0.99, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if interval.UpperFinite {
		t.Fatalf("UpperFinite = true, upper = %d; want no finite upper bound", interval.Upper)
	}
	if interval.Upper != 0 {
		t.Fatalf("Upper for an unbounded interval = %d, want sentinel zero", interval.Upper)
	}
	if interval.Lower != 40 {
		t.Fatalf("Lower = %d, want rank ceil((0.99-0.1921)*50)=40 expanded within its HDR bucket", interval.Lower)
	}
	if got := h.ValueAtPercentile(99); got != 50 {
		t.Fatalf("ValueAtPercentile changed to %d; the sample point estimate must remain 50", got)
	}
}

func TestQuantileConfidenceIntervalErrors(t *testing.T) {
	h := hdrhistogram.New(0, 1000, 3)
	if err := h.RecordValue(10); err != nil {
		t.Fatal(err)
	}
	empty := hdrhistogram.New(0, 1000, 3)

	tests := []struct {
		name      string
		histogram *hdrhistogram.Histogram
		q         float64
		alpha     float64
		want      error
	}{
		{"nil histogram", nil, 0.5, 0.05, hdrhistogram.ErrNilHistogram},
		{"empty histogram", empty, 0.5, 0.05, hdrhistogram.ErrEmptyHistogram},
		{"zero quantile", h, 0, 0.05, hdrhistogram.ErrInvalidQuantile},
		{"one quantile", h, 1, 0.05, hdrhistogram.ErrInvalidQuantile},
		{"negative quantile", h, -0.1, 0.05, hdrhistogram.ErrInvalidQuantile},
		{"quantile above one", h, 1.1, 0.05, hdrhistogram.ErrInvalidQuantile},
		{"NaN quantile", h, math.NaN(), 0.05, hdrhistogram.ErrInvalidQuantile},
		{"infinite quantile", h, math.Inf(1), 0.05, hdrhistogram.ErrInvalidQuantile},
		{"zero alpha", h, 0.5, 0, hdrhistogram.ErrInvalidFailureProbability},
		{"one alpha", h, 0.5, 1, hdrhistogram.ErrInvalidFailureProbability},
		{"negative alpha", h, 0.5, -0.1, hdrhistogram.ErrInvalidFailureProbability},
		{"alpha above one", h, 0.5, 1.1, hdrhistogram.ErrInvalidFailureProbability},
		{"NaN alpha", h, 0.5, math.NaN(), hdrhistogram.ErrInvalidFailureProbability},
		{"infinite alpha", h, 0.5, math.Inf(-1), hdrhistogram.ErrInvalidFailureProbability},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.histogram.QuantileConfidenceInterval(test.q, test.alpha)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if got != (hdrhistogram.QuantileConfidenceInterval{}) {
				t.Fatalf("result on error = %+v, want zero value", got)
			}
		})
	}
}

func assertInterval(t *testing.T, got, want hdrhistogram.QuantileConfidenceInterval, epsilon float64) {
	t.Helper()
	if got.Quantile != want.Quantile ||
		got.Alpha != want.Alpha ||
		got.N != want.N ||
		got.Lower != want.Lower ||
		got.Upper != want.Upper ||
		got.UpperFinite != want.UpperFinite {
		t.Fatalf("interval = %+v, want %+v", got, want)
	}
	if math.Abs(got.Epsilon-want.Epsilon) > epsilon {
		t.Fatalf("epsilon = %v, want %v within %v", got.Epsilon, want.Epsilon, epsilon)
	}
}
