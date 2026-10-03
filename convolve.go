package hdrhistogram

import (
	"fmt"
	"math"
)

// Convolve returns a new Histogram approximating the distribution of the sum
// of two statistically independent random variables whose distributions are
// given by a and b — e.g. the end-to-end latency of two serial stages whose
// per-stage latencies were recorded separately.
//
// INDEPENDENCE ASSUMPTION: the result is only meaningful if the two
// distributions are independent. Convolution computes P(X+Y = z) as
// sum over x of P(X = x) * P(Y = z - x); if the two stages are correlated
// (a slow first stage makes the second more likely to be slow, say), the
// returned distribution does not describe their sum.
//
// APPROXIMATION: the computation is performed on the recorded buckets, not
// the original samples. Each non-empty bucket is represented by its median
// equivalent value (medianEquivalentValue), so every pair of buckets
// contributes a single discrete value x+y with weight countX*countY to the
// result. The true sums of the underlying samples are spread around these
// representative values, so the result carries a discretization error bounded
// by the bucket resolution of the inputs and of the target histogram.
//
// The target histogram's geometry (lowest/highest trackable value and number
// of significant value digits) is chosen by the caller and is independent of
// the inputs' geometry. Every bucket-pair combination must be recordable in
// the target: if any combination's value falls outside the target's trackable
// range, Convolve fails and returns an error rather than silently dropping
// the weight the way Merge does. The result's TotalCount always equals the
// product of the inputs' TotalCounts; all intermediate multiplications,
// additions, bucket indices and the running totals are checked for int64
// overflow and any overflow fails the whole operation.
//
// Convolving an empty distribution with any other distribution yields an
// empty result (TotalCount 0) and no error.
//
// The returned Histogram is freshly allocated: it shares no counts storage
// with the inputs, and none of the inputs' metadata (tag, start/end
// timestamps) is carried over — a convolution is a derived distribution, not
// a newly recorded time interval.
//
// Runtime is proportional to the product of the numbers of NON-EMPTY buckets
// in the inputs, not to their TotalCounts, so convolving a few very heavy
// buckets is as cheap as convolving a few light ones.
func Convolve(a, b *Histogram, lowestTrackableValue, highestTrackableValue int64, numberOfSignificantValueDigits int) (*Histogram, error) {
	// The result's total count is the product of the inputs' totals; if even
	// that is not representable there is no point iterating.
	if a.totalCount != 0 && b.totalCount > math.MaxInt64/a.totalCount {
		return nil, fmt.Errorf("convolution total count overflow: %d * %d exceeds int64",
			a.totalCount, b.totalCount)
	}

	result := New(lowestTrackableValue, highestTrackableValue, numberOfSignificantValueDigits)

	ia := a.rIterator()
	for ia.next() {
		x := a.medianEquivalentValue(ia.valueFromIdx)
		countX := ia.countAtIdx

		ib := b.rIterator()
		for ib.next() {
			y := b.medianEquivalentValue(ib.valueFromIdx)
			countY := ib.countAtIdx

			// countX and countY come from non-empty buckets, so both are > 0.
			if countX > math.MaxInt64/countY {
				return nil, fmt.Errorf("convolution weight overflow: %d * %d exceeds int64",
					countX, countY)
			}
			weight := countX * countY

			// Bucket representative values are non-negative, so the sum wraps
			// below x exactly when it overflows.
			sum := x + y
			if sum < x {
				return nil, fmt.Errorf("convolution value overflow: %d + %d exceeds int64", x, y)
			}

			// Same guard as RecordValues: a negative index wraps to a huge
			// unsigned value and is caught by the single comparison.
			idx := result.countsIndexFor(sum)
			if uint(idx) >= uint(len(result.counts)) {
				return nil, fmt.Errorf("convolution result value %d is out of range for the target histogram", sum)
			}

			newCount := result.counts[idx] + weight
			if newCount < result.counts[idx] {
				return nil, fmt.Errorf("convolution bucket count overflow recording weight %d", weight)
			}
			result.counts[idx] = newCount

			newTotal := result.totalCount + weight
			if newTotal < result.totalCount {
				return nil, fmt.Errorf("convolution total count overflow recording weight %d", weight)
			}
			result.totalCount = newTotal
		}
	}

	return result, nil
}
