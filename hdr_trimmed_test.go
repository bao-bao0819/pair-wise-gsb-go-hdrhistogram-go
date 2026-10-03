package hdrhistogram_test

import (
	"math"
	"reflect"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

func recordTrimmedTestValues(t *testing.T, h *hdrhistogram.Histogram, values map[int64]int64) {
	t.Helper()
	for value, count := range values {
		if err := h.RecordValues(value, count); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrimmedStatisticsHandCalculated(t *testing.T) {
	h := hdrhistogram.New(1, 1000000, 3)
	recordTrimmedTestValues(t, h, map[int64]int64{
		100: 1,
		200: 4,
		300: 3,
		400: 2,
	})

	// N=10, rank interval [1.5, 8.5):
	// weights are 3.5 at 200, 3 at 300, and 0.5 at 400.
	stats, err := h.TrimmedStatistics(0.15, 0.85)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stats.RetainedWeight, 7.0; got != want {
		t.Fatalf("RetainedWeight = %v, want %v", got, want)
	}
	if got, want := stats.Mean, 1800.0/7.0; math.Abs(got-want) > 1e-10 {
		t.Fatalf("Mean = %v, want %v", got, want)
	}
	if got, want := stats.Variance, 2660000.0/686.0; math.Abs(got-want) > 1e-7 {
		t.Fatalf("Variance = %v, want %v", got, want)
	}
}

func TestTrimmedStatisticsSingleBucketAndFullRange(t *testing.T) {
	singleBucket := hdrhistogram.New(1, 1000000, 3)
	if err := singleBucket.RecordValues(123, 10); err != nil {
		t.Fatal(err)
	}

	stats, err := singleBucket.TrimmedStatistics(0.25, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RetainedWeight != 5 || stats.Mean != 123 || stats.Variance != 0 {
		t.Fatalf("single-bucket stats = %+v, want weight 5, mean 123, variance 0", stats)
	}

	fullRange := hdrhistogram.New(1, 1000000, 3)
	recordTrimmedTestValues(t, fullRange, map[int64]int64{
		100: 1,
		200: 4,
		300: 3,
		400: 2,
	})

	stats, err = fullRange.TrimmedStatistics(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RetainedWeight != 10 || stats.Mean != 260 || stats.Variance != 8400 {
		t.Fatalf("full-range stats = %+v, want weight 10, mean 260, variance 8400", stats)
	}
}

func TestTrimmedStatisticsMatchesExpandedReference(t *testing.T) {
	h := hdrhistogram.New(1, 1000000, 3)
	orderedValues := []int64{100, 200, 300, 400}
	counts := map[int64]int64{100: 1, 200: 4, 300: 3, 400: 2}
	recordTrimmedTestValues(t, h, counts)

	var samples []float64
	for _, value := range orderedValues {
		for range counts[value] {
			samples = append(samples, float64(value))
		}
	}

	const lo, hi = 0.1, 0.9
	start := int(lo * float64(len(samples)))
	end := int(hi * float64(len(samples)))
	retained := samples[start:end]
	var expectedMean float64
	for _, value := range retained {
		expectedMean += value
	}
	expectedMean /= float64(len(retained))
	var expectedVariance float64
	for _, value := range retained {
		delta := value - expectedMean
		expectedVariance += delta * delta
	}
	expectedVariance /= float64(len(retained))

	stats, err := h.TrimmedStatistics(lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RetainedWeight != float64(len(retained)) {
		t.Fatalf("RetainedWeight = %v, want %d", stats.RetainedWeight, len(retained))
	}
	if math.Abs(stats.Mean-expectedMean) > 1e-10 {
		t.Fatalf("Mean = %v, want %v", stats.Mean, expectedMean)
	}
	if math.Abs(stats.Variance-expectedVariance) > 1e-10 {
		t.Fatalf("Variance = %v, want %v", stats.Variance, expectedVariance)
	}
}

func TestTrimmedStatisticsLargeTotalCount(t *testing.T) {
	h := hdrhistogram.New(1, 1000000, 5)
	if err := h.RecordValues(100000, 5_000_000_000_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordValues(100001, 3_000_000_000_000_000_000); err != nil {
		t.Fatal(err)
	}

	stats, err := h.TrimmedStatistics(0.1, 0.9)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stats.RetainedWeight, 6.4e18; math.Abs(got-want) > 1e6 {
		t.Fatalf("RetainedWeight = %v, want %v", got, want)
	}
	if got, want := stats.Mean, 100000.34375; math.Abs(got-want) > 1e-10 {
		t.Fatalf("Mean = %v, want %v", got, want)
	}
	if got, want := stats.Variance, 0.2255859375; math.Abs(got-want) > 1e-10 {
		t.Fatalf("Variance = %v, want %v", got, want)
	}
}

func TestTrimmedStatisticsTranslationInvariance(t *testing.T) {
	original := hdrhistogram.New(1, 200000, 5)
	recordTrimmedTestValues(t, original, map[int64]int64{
		10000: 2,
		11000: 3,
		12000: 5,
	})

	translated := hdrhistogram.New(1, 200000, 5)
	recordTrimmedTestValues(t, translated, map[int64]int64{
		110000: 2,
		111000: 3,
		112000: 5,
	})

	originalStats, err := original.TrimmedStatistics(0.25, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	translatedStats, err := translated.TrimmedStatistics(0.25, 0.75)
	if err != nil {
		t.Fatal(err)
	}

	if originalStats.RetainedWeight != 5 || translatedStats.RetainedWeight != 5 {
		t.Fatalf("weights = %v and %v, want 5", originalStats.RetainedWeight, translatedStats.RetainedWeight)
	}
	if translatedStats.Mean-originalStats.Mean != 100000 {
		t.Fatalf("means shifted by %v, want 100000", translatedStats.Mean-originalStats.Mean)
	}
	if math.Abs(translatedStats.Variance-originalStats.Variance) > 1e-10 {
		t.Fatalf("variances = %v and %v, want equal values", originalStats.Variance, translatedStats.Variance)
	}
}

func TestTrimmedStatisticsEmptyAndInvalid(t *testing.T) {
	empty := hdrhistogram.New(1, 1000000, 3)
	stats, err := empty.TrimmedStatistics(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (hdrhistogram.TrimmedStatistics{}) {
		t.Fatalf("empty histogram stats = %+v, want zero value", stats)
	}

	invalidBounds := []struct {
		name string
		lo   float64
		hi   float64
	}{
		{"negative lo", -0.1, 1},
		{"hi above one", 0, 1.1},
		{"equal bounds", 0.5, 0.5},
		{"inverted bounds", 0.7, 0.2},
		{"NaN lo", math.NaN(), 1},
		{"NaN hi", 0, math.NaN()},
		{"negative infinity lo", math.Inf(-1), 1},
		{"positive infinity hi", 0, math.Inf(1)},
	}
	for _, bounds := range invalidBounds {
		t.Run(bounds.name, func(t *testing.T) {
			if _, err := empty.TrimmedStatistics(bounds.lo, bounds.hi); err == nil {
				t.Fatal("TrimmedStatistics returned nil error for invalid bounds")
			}
		})
	}
}

func TestTrimmedStatisticsDoesNotModifyHistogram(t *testing.T) {
	h := hdrhistogram.New(1, 1000000, 3)
	recordTrimmedTestValues(t, h, map[int64]int64{
		100: 1,
		200: 4,
		300: 3,
		400: 2,
	})
	before := h.Export()

	if _, err := h.TrimmedStatistics(0.15, 0.85); err != nil {
		t.Fatal(err)
	}

	after := h.Export()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("TrimmedStatistics modified the histogram")
	}
	if h.TotalCount() != 10 {
		t.Fatalf("TotalCount = %d, want 10", h.TotalCount())
	}
}
