package hdrhistogram

import (
	"fmt"
	"math"
	"math/bits"
)

// weightedBucket is one non-empty bucket of an input histogram, represented by
// its median equivalent value and the number of recorded values in the bucket.
type weightedBucket struct {
	value int64
	count int64
}

// nonEmptyBuckets returns one weightedBucket per non-empty bucket, in ascending
// value order. Only non-empty buckets are visited, so the cost of a convolution
// is proportional to the number of occupied buckets, never to TotalCount.
func (h *Histogram) nonEmptyBuckets() []weightedBucket {
	buckets := make([]weightedBucket, 0)
	for idx, count := range h.counts {
		if count == 0 {
			continue
		}
		value := h.medianEquivalentValue(h.valueFromFlatIndex(int32(idx)))
		buckets = append(buckets, weightedBucket{value: value, count: count})
	}
	return buckets
}

// Convolve returns a new Histogram holding the distribution of the sum of two
// independent random variables represented by a and b, discretized onto the
// caller-specified target range [lowestTrackableValue, highestTrackableValue]
// with the requested precision (numberOfSignificantValueDigits, [1,5]).
//
// # Independence assumption
//
// The result is the arithmetic convolution a*b (weighted Cartesian product),
// which is the distribution of x+y only when the two inputs are statistically
// independent. The two histograms are treated as separate serial stages (for
// example two sequential phases of a request) whose durations are assumed not
// to be correlated. If the stages are correlated (a slow stage-1 implies a
// slow stage-2, or vice versa) the convolved percentiles are not the
// percentiles of the real combined duration.
//
// # Bucket representative approximation
//
// Every sample recorded in a bucket is replaced by that bucket's
// medianEquivalentValue (the midpoint of its equivalent-value range) before the
// pairwise sums are computed and re-bucketed into the target histogram. This is
// the same "all samples in a bucket share one representative value"
// approximation already used by Mean/StdDev; it introduces an error no larger
// than the source buckets' resolution around each sum. No raw samples are
// expanded: the work is the Cartesian product of NON-EMPTY BUCKETS
// (len(bucketsA) * len(bucketsB) pair visits), not of the recorded samples, so
// runtime is independent of either input's TotalCount.
//
// Each non-empty bucket pair (x, countX) from a and (y, countY) from b
// contributes countX*countY to the target bucket of x+y. On success the
// result's TotalCount equals a.TotalCount() * b.TotalCount() exactly.
//
// Unlike Merge, which silently drops values outside the receiver's range and
// reports how many were dropped, Convolve is all-or-nothing: if any pairwise
// sum falls outside the target trackable range, or if any int64 intermediate
// (the countX*countY weight, the x+y sum, the accumulated target-bucket count,
// or the running total) overflows, Convolve returns a non-nil error and a nil
// histogram without partially populating one.
//
// If either input is empty (TotalCount == 0) the result is an empty histogram
// with the requested target geometry. The returned histogram shares no
// counts slice with either input, and its start/end timestamps and tag are left
// at their zero values: a convolution is a derived distribution, not a new
// recording interval, so the inputs' time metadata is never copied over.
func Convolve(a, b *Histogram, lowestTrackableValue, highestTrackableValue int64, numberOfSignificantValueDigits int) (*Histogram, error) {
	result := New(lowestTrackableValue, highestTrackableValue, numberOfSignificantValueDigits)

	// Empty convolved with anything is empty. Metadata (times/tag) is not
	// inherited; the freshly New()ed histogram already carries zero metadata.
	if a.totalCount == 0 || b.totalCount == 0 {
		return result, nil
	}

	// Fail up front if the required final total cannot be represented.
	expectedTotal, ok := multiply64(a.totalCount, b.totalCount)
	if !ok {
		return nil, fmt.Errorf("hdrhistogram: convolution total %d*%d overflows int64", a.totalCount, b.totalCount)
	}

	bucketsA := a.nonEmptyBuckets()
	bucketsB := b.nonEmptyBuckets()

	// Accumulate into a private slice first: on any range/overflow failure the
	// caller gets nil instead of a half-populated histogram.
	counts := make([]int64, result.countsLen)
	var runningTotal int64

	for _, ba := range bucketsA {
		for _, bb := range bucketsB {
			weight, ok := multiply64(ba.count, bb.count)
			if !ok {
				return nil, fmt.Errorf("hdrhistogram: bucket weight %d*%d overflows int64", ba.count, bb.count)
			}

			sum, ok := add64(ba.value, bb.value)
			if !ok {
				return nil, fmt.Errorf("hdrhistogram: bucket value sum %d+%d overflows int64", ba.value, bb.value)
			}
			// Enforce the caller-declared upper bound strictly (this is the
			// whole point of failing instead of Merge-style dropping). The low
			// side mirrors RecordValues: lowestTrackableValue only sets the
			// resolution, and 0 always lives in the first bucket even when it
			// exceeds that "lowest". Rejecting sums below lowestTrackableValue
			// would also make convolution with a unit-at-0 distribution fail
			// for every histogram. Bucket representatives are non-negative, so
			// the negative check is only a defensive guard.
			if sum < 0 || sum > highestTrackableValue {
				return nil, fmt.Errorf("hdrhistogram: convolved value %d is outside the target recordable range [0, %d]",
					sum, highestTrackableValue)
			}

			idx := result.countsIndexFor(sum)
			if idx < 0 || idx >= len(counts) {
				// Defensive: countsIndexFor can wrap negative for some inputs;
				// the explicit range check above should always have caught this.
				return nil, fmt.Errorf("hdrhistogram: convolved value %d is outside the target range [%d, %d]",
					sum, lowestTrackableValue, highestTrackableValue)
			}

			newBucketCount, ok := add64(counts[idx], weight)
			if !ok {
				return nil, fmt.Errorf("hdrhistogram: target bucket count overflows int64 at value %d", sum)
			}
			counts[idx] = newBucketCount

			newTotal, ok := add64(runningTotal, weight)
			if !ok {
				return nil, fmt.Errorf("hdrhistogram: convolution accumulated total overflows int64")
			}
			runningTotal = newTotal
		}
	}

	if runningTotal != expectedTotal {
		// Invariant: every input pair was visited exactly once, so these can
		// never disagree. Keep the check as a guard against future edits.
		return nil, fmt.Errorf("hdrhistogram: convolution internal error: total %d != expected %d",
			runningTotal, expectedTotal)
	}

	result.counts = counts
	result.totalCount = runningTotal
	return result, nil
}

// multiply64 returns x*y and false when the product overflows int64. Inputs to
// a convolution are non-negative bucket counts, so the result is valid exactly
// when the 128-bit product has no high word and its low word fits int64.
func multiply64(x, y int64) (int64, bool) {
	hi, lo := bits.Mul64(uint64(x), uint64(y))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, false
	}
	return int64(lo), true
}

// add64 returns x+y and false when the sum overflows int64.
func add64(x, y int64) (int64, bool) {
	if (y > 0 && x > math.MaxInt64-y) || (y < 0 && x < math.MinInt64-y) {
		return 0, false
	}
	return x + y, true
}
