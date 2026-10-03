package hdrhistogram

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func recordCorrectedValuesByLoop(h *Histogram, v, count, expectedInterval int64) error {
	for i := int64(0); i < count; i++ {
		if err := h.RecordValue(v); err != nil {
			return err
		}
		if expectedInterval <= 0 || v <= expectedInterval {
			continue
		}

		missingValue := v - expectedInterval
		for missingValue >= expectedInterval {
			if err := h.RecordValue(missingValue); err != nil {
				return err
			}
			missingValue -= expectedInterval
		}
	}
	return nil
}

func correctedTestValues(t *testing.T, h *Histogram, highest int64) []int64 {
	t.Helper()

	values := map[int64]struct{}{}
	add := func(v int64) {
		if v >= 0 && v <= highest {
			values[v] = struct{}{}
		}
	}

	for v := int64(0); v <= 80; v++ {
		add(v)
	}
	for power := int64(1); power <= highest; power <<= 1 {
		add(power - 1)
		add(power)
		add(power + 1)
		if power > highest/2 {
			break
		}
	}
	for bucket := int32(0); bucket <= h.bucketCount; bucket++ {
		boundary := h.valueFromIndex(bucket, h.subBucketHalfCount)
		add(boundary - 1)
		add(boundary)
		add(boundary + 1)
	}

	result := make([]int64, 0, len(values))
	for v := range values {
		result = append(result, v)
	}
	return result
}

func TestRecordCorrectedValuesMatchesLoopOracle(t *testing.T) {
	const highest = 1 << 12
	intervals := []int64{-7, 0, 1, 2, 3, 7, 13, 64, 257, 500}
	counts := []int64{1, 3}

	for _, sig := range []int{1, 3, 5} {
		template := New(1, highest, sig)
		values := correctedTestValues(t, template, highest)

		for _, expectedInterval := range intervals {
			for _, count := range counts {
				for _, v := range values {
					if expectedInterval > 0 && v >= expectedInterval && v/expectedInterval > 20 {
						continue
					}

					got := New(1, highest, sig)
					want := New(1, highest, sig)
					if err := got.RecordValues(11, 3); err != nil {
						t.Fatal(err)
					}
					if err := want.RecordValues(11, 3); err != nil {
						t.Fatal(err)
					}

					err := got.RecordCorrectedValues(v, count, expectedInterval)
					oracleErr := recordCorrectedValuesByLoop(want, v, count, expectedInterval)
					if (err == nil) != (oracleErr == nil) {
						t.Fatalf("sig=%d v=%d count=%d interval=%d: error %v, oracle error %v",
							sig, v, count, expectedInterval, err, oracleErr)
					}
					if err != nil {
						continue
					}
					if got.totalCount != want.totalCount {
						t.Fatalf("sig=%d v=%d count=%d interval=%d: total %d, want %d",
							sig, v, count, expectedInterval, got.totalCount, want.totalCount)
					}
					if !reflect.DeepEqual(got.counts, want.counts) {
						t.Fatalf("sig=%d v=%d count=%d interval=%d: bucket counts differ",
							sig, v, count, expectedInterval)
					}
				}
			}
		}
	}
}

func TestRecordCorrectedValuesBillionsOfVirtualObservations(t *testing.T) {
	h := New(1, math.MaxInt64, 3)
	v := int64(3_000_000_000)
	count := int64(20)
	expectedInterval := int64(1)
	wantTotal := (v / expectedInterval) * count

	start := time.Now()
	if err := h.RecordCorrectedValues(v, count, expectedInterval); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("recorded %d virtual observations in %v", wantTotal, elapsed)
	if elapsed > time.Second {
		t.Fatalf("RecordCorrectedValues took %v for %d virtual observations", elapsed, wantTotal)
	}

	if got := h.TotalCount(); got != wantTotal {
		t.Fatalf("TotalCount = %d, want %d", got, wantTotal)
	}
	if got := sumCounts(h); got != wantTotal {
		t.Fatalf("sum(counts) = %d, want %d", got, wantTotal)
	}
}

func histogramSnapshot(h *Histogram) (int64, []int64) {
	counts := append([]int64(nil), h.counts...)
	return h.totalCount, counts
}

func assertHistogramUnchanged(t *testing.T, h *Histogram, total int64, counts []int64) {
	t.Helper()
	if h.totalCount != total || !reflect.DeepEqual(h.counts, counts) {
		t.Fatalf("histogram changed after failed call: total=(%d,%d), countsEqual=%v",
			h.totalCount, total, reflect.DeepEqual(h.counts, counts))
	}
}

func TestRecordCorrectedValuesRejectsInvalidInputAtomically(t *testing.T) {
	t.Run("negative count", func(t *testing.T) {
		h := New(1, 100000, 3)
		if err := h.RecordValue(50); err != nil {
			t.Fatal(err)
		}
		total, counts := histogramSnapshot(h)

		if err := h.RecordCorrectedValues(50, -1, 10); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)
	})

	t.Run("zero count ignores value", func(t *testing.T) {
		h := New(1, 1000, 3)
		if err := h.RecordCorrectedValues(1_000_000_000, 0, 1); err != nil {
			t.Fatalf("zero count should be a no-op: %v", err)
		}
		if h.TotalCount() != 0 {
			t.Fatalf("TotalCount = %d, want 0", h.TotalCount())
		}
	})

	t.Run("value out of range", func(t *testing.T) {
		h := New(1, 1000, 3)
		total, counts := histogramSnapshot(h)

		if err := h.RecordCorrectedValues(1_000_000_000, 1, 10); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)

		if err := h.RecordCorrectedValues(-1, 1, 10); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)

		if err := h.RecordCorrectedValues(1_000_000_000, 1, 0); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)
	})
}

func TestRecordCorrectedValuesRejectsOverflowAtomically(t *testing.T) {
	t.Run("total count overflow", func(t *testing.T) {
		h := New(1, math.MaxInt64, 3)
		if err := h.RecordValues(1, math.MaxInt64); err != nil {
			t.Fatal(err)
		}
		total, counts := histogramSnapshot(h)

		if err := h.RecordCorrectedValues(2, 1, 0); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)
	})

	t.Run("bucket count overflow", func(t *testing.T) {
		h := New(1, math.MaxInt64, 3)
		if err := h.RecordValues(100, math.MaxInt64); err != nil {
			t.Fatal(err)
		}
		total, counts := histogramSnapshot(h)

		if err := h.RecordCorrectedValues(100, 1, 40); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)
	})

	t.Run("multiplication overflow", func(t *testing.T) {
		h := New(1, math.MaxInt64, 3)
		total, counts := histogramSnapshot(h)

		if err := h.RecordCorrectedValues(4096, math.MaxInt64, 1); err == nil {
			t.Fatal("expected an error")
		}
		assertHistogramUnchanged(t, h, total, counts)
	})
}
