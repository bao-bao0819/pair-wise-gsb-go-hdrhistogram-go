package hdrhistogram

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"
)

// oracleCorrected mimics the original single-value RecordCorrectedValue by
// actually materializing each synthesized value, and records count copies of
// it one at a time. It is deliberately independent of the bucketized
// implementation and is only affordable for small inputs, where it serves as
// an exact per-bucket oracle.
func oracleCorrected(h *Histogram, v, count, expectedInterval int64) {
	for rep := int64(0); rep < count; rep++ {
		_ = h.RecordValue(v)
		if expectedInterval <= 0 || v <= expectedInterval {
			continue
		}
		for missing := v - expectedInterval; missing >= expectedInterval; missing -= expectedInterval {
			_ = h.RecordValue(missing)
		}
	}
}

func checkHistogramEqual(t *testing.T, got, want *Histogram, label string) {
	t.Helper()
	if got.TotalCount() != want.TotalCount() {
		t.Errorf("%s: TotalCount = %d, want %d", label, got.TotalCount(), want.TotalCount())
	}
	if len(got.counts) != len(want.counts) {
		t.Fatalf("%s: counts length = %d, want %d", label, len(got.counts), len(want.counts))
	}
	for i := range got.counts {
		if got.counts[i] != want.counts[i] {
			t.Errorf("%s: counts[%d] = %d, want %d", label, i, got.counts[i], want.counts[i])
		}
	}
}

// TestRecordCorrectedValuesMatchesOracle compares the bucketized batch API
// against the materializing old-style loop across precisions, bucket
// boundaries and non-divisible intervals.
func TestRecordCorrectedValuesMatchesOracle(t *testing.T) {
	type config struct {
		name            string
		lowest, highest int64
		sigfig          int
	}
	configs := []config{
		{"sf1", 1, 1_000_000_000, 1},
		{"sf2", 1, 1_000_000_000, 2},
		{"sf3", 1, 1_000_000_000, 3},
		{"sf5", 1, 1_000_000_000, 5},
		{"ldv100", 100, 1_000_000_000, 3},
		{"ldv1000", 1000, 1_000_000_000, 2},
	}

	type scenario struct {
		v, count, interval int64
	}
	mkScenarios := func(v int64) []scenario {
		return []scenario{
			{v, 1, 1},
			{v, 3, 1},
			{v, 1, 7},
			{v, 5, 7},
			{v, 1, 100},
			{v, 4, 100},
			{v, 1, 99},  // non-divisible interval
			{v, 2, 333}, // interval larger than v's low half, non-divisible
			{v, 1, v - 1},
			{v, 1, v},
			{v, 1, v + 1},
			{v, 2, 0}, // interval <= 0: only the raw value
			{v, 2, -10},
		}
	}

	// Values chosen to land on / straddle bucket boundaries at various scales.
	values := []int64{
		1, 2, 3, 7, 8, 15, 16, 31, 32, 99, 100, 127, 128, 255, 256,
		999, 1000, 1001, 1023, 1024, 2047, 2048, 10_000,
		65_535, 65_536, 99_999, 100_000, 1_000_000,
	}

	for _, cfg := range configs {
		for _, v := range values {
			if v > cfg.highest {
				continue
			}
			for si, sc := range mkScenarios(v) {
				hBatch := New(cfg.lowest, cfg.highest, cfg.sigfig)
				if err := hBatch.RecordCorrectedValues(sc.v, sc.count, sc.interval); err != nil {
					t.Fatalf("%s v=%d scenario %d: unexpected error: %v", cfg.name, v, si, err)
				}
				hOracle := New(cfg.lowest, cfg.highest, cfg.sigfig)
				oracleCorrected(hOracle, sc.v, sc.count, sc.interval)
				checkHistogramEqual(t, hBatch, hOracle, cfg.name)
			}
		}
	}
}

// TestRecordCorrectedValuesAccumulatesOnExisting verifies batch records stack
// exactly on top of pre-existing contents.
func TestRecordCorrectedValuesAccumulatesOnExisting(t *testing.T) {
	for _, ldv := range []int64{1, 100} {
		hBatch := New(ldv, 1_000_000, 3)
		hOracle := New(ldv, 1_000_000, 3)
		oracleCorrected(hBatch, 12_345, 1, 1000)
		oracleCorrected(hOracle, 12_345, 1, 1000)

		if err := hBatch.RecordCorrectedValues(123_456, 17, 333); err != nil {
			t.Fatal(err)
		}
		oracleCorrected(hOracle, 123_456, 17, 333)
		checkHistogramEqual(t, hBatch, hOracle, "ldv")
	}
}

// TestRecordCorrectedValuesSingleAPIMatches verifies the old single-value API,
// now delegating to the batch implementation, keeps its old bucket
// distribution.
func TestRecordCorrectedValuesSingleAPIMatches(t *testing.T) {
	for _, v := range []int64{1, 100, 1000, 1001, 65_535, 65_536, 123_456, 1_000_000} {
		for _, d := range []int64{-5, 0, 1, 3, 100, v - 1, v, v + 1} {
			hBatch := New(1, 10_000_000, 3)
			hOracle := New(1, 10_000_000, 3)
			if err := hBatch.RecordCorrectedValue(v, d); err != nil {
				t.Fatal(err)
			}
			oracleCorrected(hOracle, v, 1, d)
			checkHistogramEqual(t, hBatch, hOracle, "single")
		}
	}
}

// TestRecordCorrectedValuesZeroCount verifies a zero count is a no-op even
// for an out-of-range value.
func TestRecordCorrectedValuesZeroCount(t *testing.T) {
	h := New(1, 1_000, 3)
	_ = h.RecordValue(50)
	before := h.Export()

	if err := h.RecordCorrectedValues(1_000_000, 0, 1); err != nil {
		t.Fatalf("zero count with out-of-range value: %v", err)
	}
	if err := h.RecordCorrectedValues(-5, 0, 1); err != nil {
		t.Fatalf("zero count with negative value: %v", err)
	}
	checkHistogramEqual(t, h, Import(before), "zero-count")
}

// TestRecordCorrectedValuesRandomOracle fuzzes random configurations and
// inputs against the materializing oracle, including batches applied over a
// pre-populated histogram.
func TestRecordCorrectedValuesRandomOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		sigfig := 1 + rng.Intn(5)
		ldv := int64(1) << uint(rng.Intn(4)) // 1,2,4,8
		highest := int64(1000) << uint(rng.Intn(4))
		hBatch := New(ldv, highest, sigfig)
		hOracle := New(ldv, highest, sigfig)

		steps := rng.Intn(5)
		for step := 0; step < steps; step++ {
			v := int64(rng.Intn(int(highest)) + 1)
			count := int64(rng.Intn(4))
			var d int64
			switch rng.Intn(6) {
			case 0:
				d = 0
			case 1:
				d = -int64(rng.Intn(10) + 1)
			default:
				d = int64(rng.Intn(int(v)+1)) + 1
			}
			if err := hBatch.RecordCorrectedValues(v, count, d); err != nil {
				t.Fatalf("iter %d step %d: unexpected error: %v", iter, step, err)
			}
			oracleCorrected(hOracle, v, count, d)
		}
		checkHistogramEqual(t, hBatch, hOracle, "random")
	}
}

// TestRecordCorrectedValuesErrorsAtomic covers negative count, out-of-range
// value and int64 overflow of bucket/TotalCount; every failure must leave the
// histogram untouched.
func TestRecordCorrectedValuesErrorsAtomic(t *testing.T) {
	cases := []struct {
		name                string
		ldv, highest        int64
		v, count, d         int64
		preRecord, preCount int64
	}{
		{"negative count", 1, 1 << 40, 1000, -1, 100, 0, 0},
		{"value too large", 1, 1 << 20, 1 << 30, 1, 1, 0, 0},
		{
			name:      "total count overflow",
			ldv:       1,
			highest:   1 << 40,
			v:         1 << 40,
			count:     3,
			d:         1, // ~1e12 observations per occurrence; *3 overflows int64
			preRecord: 1,
			preCount:  10,
		},
		{
			name:      "bucket count overflow",
			ldv:       1,
			highest:   1 << 22,
			v:         1 << 21,
			count:     math.MaxInt64,
			d:         0, // no synthesized values: all land in v's bucket
			preRecord: 1 << 21,
			preCount:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := New(tc.ldv, tc.highest, 3)
			if tc.preCount != 0 {
				if err := h.RecordValues(tc.preRecord, tc.preCount); err != nil {
					t.Fatal(err)
				}
			}
			before := h.Export()

			err := h.RecordCorrectedValues(tc.v, tc.count, tc.d)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			checkHistogramEqual(t, h, Import(before), tc.name)
		})
	}
}

// TestRecordCorrectedValuesHugeStall materializes a billion virtual
// observations. The reference loop must never run for this input; the batch
// call must finish quickly and produce an exact total.
func TestRecordCorrectedValuesHugeStall(t *testing.T) {
	h := New(1, 10_000_000_000, 3)

	const (
		v     = int64(3_000_000_000)
		d     = int64(3)
		count = int64(1)
		reps  = int64(3)
	)
	// Each occurrence records v itself plus (v-1)/d synthesized values:
	// exactly one billion terms here.
	perOccurrence := big.NewInt(1 + (v-1)/d)
	wantTotal := new(big.Int).Mul(perOccurrence, big.NewInt(reps))
	if !wantTotal.IsInt64() {
		t.Fatalf("test setup: expected total fits int64, got %s", wantTotal)
	}

	start := time.Now()
	for i := int64(0); i < reps; i++ {
		if err := h.RecordCorrectedValues(v, count, d); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("batch recording took %s, work must not scale with v/expectedInterval", elapsed)
	}

	if got := h.TotalCount(); got != wantTotal.Int64() {
		t.Fatalf("TotalCount = %d, want %s", got, wantTotal)
	}

	// Independent, non-materialized check of every touched bucket: for a bucket
	// spanning [lo, hi], the descending progression v, v-d, ... contributes
	// reps * (hiK - loK + 1) observations with indices clamped to the
	// progression's valid k range [0, n-1].
	dBig := big.NewInt(d)
	vBig := big.NewInt(v)
	nBig := perOccurrence
	checkedTerms := big.NewInt(0)
	iter := h.iterator()
	for iter.next() {
		if iter.countAtIdx == 0 {
			continue
		}
		lo := big.NewInt(iter.valueFromIdx)
		width := big.NewInt(h.sizeOfEquivalentValueRange(iter.valueFromIdx))
		hi := new(big.Int).Add(lo, width)
		hi.Sub(hi, big.NewInt(1))
		if hi.Cmp(vBig) > 0 {
			hi.Set(vBig)
		}
		if lo.Cmp(vBig) > 0 {
			t.Fatalf("unexpected count above v: lo=%s", lo)
		}

		loK := ceilDivBig(new(big.Int).Sub(vBig, hi), dBig)
		hiK := new(big.Int).Quo(new(big.Int).Sub(vBig, lo), dBig)
		nMinus1 := new(big.Int).Sub(nBig, big.NewInt(1))
		if hiK.Cmp(nMinus1) > 0 {
			hiK.Set(nMinus1)
		}
		terms := big.NewInt(0)
		if hiK.Cmp(loK) >= 0 {
			terms.Sub(hiK, loK)
			terms.Add(terms, big.NewInt(1))
		}
		checkedTerms.Add(checkedTerms, terms)

		want := new(big.Int).Mul(terms, big.NewInt(reps))
		if !want.IsInt64() {
			t.Fatalf("expected bucket count overflows int64 at lo=%s", lo)
		}
		if iter.countAtIdx != want.Int64() {
			t.Errorf("bucket lo=%d width=%s: count = %d, want %s",
				iter.valueFromIdx, width, iter.countAtIdx, want)
		}
	}
	if checkedTerms.Cmp(nBig) != 0 {
		t.Errorf("checked %s of %s progression terms across buckets", checkedTerms, nBig)
	}
}

// TestRecordCorrectedValuesHugeStallRepeatedCount mirrors the huge stall but
// packs the replication into the count argument instead of repeated calls.
func TestRecordCorrectedValuesHugeStallRepeatedCount(t *testing.T) {
	h := New(1, 10_000_000_000, 3)
	const (
		v     = int64(3_000_000_000)
		d     = int64(3)
		count = int64(7)
	)
	want := int64(1+(v-1)/d) * count

	start := time.Now()
	if err := h.RecordCorrectedValues(v, count, d); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("batch recording took %s", elapsed)
	}
	if got := h.TotalCount(); got != want {
		t.Fatalf("TotalCount = %d, want %d", got, want)
	}
}

func ceilDivBig(a, b *big.Int) *big.Int {
	// a, b are non-negative; ceil(a/b) = (a+b-1)/b
	r := new(big.Int).Add(a, b)
	r.Sub(r, big.NewInt(1))
	return r.Quo(r, b)
}
