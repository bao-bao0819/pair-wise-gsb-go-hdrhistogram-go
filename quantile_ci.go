package hdrhistogram

import (
	"errors"
	"math"
)

// QuantileConfidenceInterval is a distribution-free, finite-sample confidence
// interval for a single population quantile computed with the
// Dvoretzky–Kiefer–Wolfowitz (DKW) bound. It is a conservative interval with an
// exact (non-asymptotic) coverage guarantee under the independent and
// identically distributed (i.i.d.) sampling assumption — no bootstrap and no
// statistics dependency is involved.
//
// The interval is deliberately distinct from ValueAtPercentile:
// ValueAtPercentile returns a single plug-in estimate of the empirical
// quantile and makes no coverage promise about the population, whereas this
// interval quantifies the sampling uncertainty of that estimate.
//
// Fields:
//
//   - Quantile and Alpha echo the validated request.
//   - N is h.TotalCount(), the number of independent observations the bound
//     uses.
//   - Epsilon is the one-sided DKW half-width
//     sqrt(log(2/Alpha) / (2*N)).
//   - Lower is the conservative lower endpoint. It is 0 whenever the DKW band
//     reaches below the lowest order statistic; see LowerClampedToSupport.
//   - LowerClampedToSupport reports that Lower was not produced by an order
//     statistic but clamped to 0, the assumed non-negative support lower bound
//     (HDR histograms track non-negative values).
//   - UpperFinite reports whether a finite upper endpoint exists. When false,
//     Upper is 0 and the sample carries no finite-sample guarantee about the
//     upper tail: the DKW band reaches above the largest order statistic, so
//     the observed Max must not be presented as a population upper bound.
//   - Upper is the conservative upper endpoint, valid only when UpperFinite.
//
// Both finite endpoints are widened to the low/high edge of the HDR
// equivalence bucket of the selected order statistic, so the guarantee is not
// eroded by the histogram's representational rounding.
type QuantileConfidenceInterval struct {
	Quantile              float64
	Alpha                 float64
	N                     int64
	Epsilon               float64
	Lower                 int64
	LowerClampedToSupport bool
	Upper                 int64
	UpperFinite           bool
}

// Errors returned by QuantileConfidenceIntervalAt. Callers can use errors.Is
// to distinguish a malformed request from an empty histogram.
var (
	// ErrInvalidQuantile is returned when q is not a finite value strictly
	// inside (0, 1).
	ErrInvalidQuantile = errors.New("hdrhistogram: quantile q must be a finite value strictly inside (0, 1)")
	// ErrInvalidAlpha is returned when alpha is not a finite value strictly
	// inside (0, 1).
	ErrInvalidAlpha = errors.New("hdrhistogram: failure probability alpha must be a finite value strictly inside (0, 1)")
	// ErrEmptyHistogram is returned when the histogram contains no recorded
	// values: without observations no DKW interval exists.
	ErrEmptyHistogram = errors.New("hdrhistogram: cannot compute a quantile confidence interval from an empty histogram")
)

// QuantileConfidenceIntervalAt returns a distribution-free confidence interval
// for the q-quantile of the underlying distribution, computed from the
// recorded observations with the DKW bound:
//
//	epsilon = sqrt(log(2/alpha) / (2*N)),  N = TotalCount()
//
// With probability at least 1-alpha over i.i.d. samples of size N, the true
// CDF lies within epsilon of the empirical CDF everywhere at once. The
// generalized-inverse quantiles at q-epsilon and q+epsilon therefore bracket
// the true q-quantile at that confidence level.
//
// q is a quantile in (0, 1) (not a percentile: pass 0.99, not 99) and alpha is
// the acceptable failure probability in (0, 1). Both must be finite.
//
// Endpoint handling is deliberately conservative:
//
//   - If q-epsilon <= 0, the band reaches below the empirical support. Lower
//     is reported as 0 (the non-negative support bound) and
//     LowerClampedToSupport is set.
//   - If q+epsilon >= 1, the band reaches above the empirical support. No
//     finite upper endpoint exists: UpperFinite is false and Upper is 0. In
//     particular the observed Max is never substituted, as it carries no
//     guarantee about the population tail.
//
// Statistical prerequisite: every counted observation must be an independent
// draw from the same distribution. TotalCount() treats each unit of count
// equally, so bulk counts supplied via RecordValues(v, n) are only valid here
// when they genuinely represent n independent observations — weights or
// duplicated/imputed samples do not. Values synthesized by
// RecordCorrectedValue (coordinated-omission fill-ins) are not independent
// observations and likewise do not receive this guarantee.
func (h *Histogram) QuantileConfidenceIntervalAt(q, alpha float64) (*QuantileConfidenceInterval, error) {
	if math.IsNaN(q) || math.IsInf(q, 0) || q <= 0 || q >= 1 {
		return nil, ErrInvalidQuantile
	}
	if math.IsNaN(alpha) || math.IsInf(alpha, 0) || alpha <= 0 || alpha >= 1 {
		return nil, ErrInvalidAlpha
	}
	n := h.totalCount
	if n <= 0 {
		return nil, ErrEmptyHistogram
	}

	N := float64(n)
	epsilon := math.Sqrt(math.Log(2.0/alpha) / (2.0 * N))

	ci := &QuantileConfidenceInterval{
		Quantile: q,
		Alpha:    alpha,
		N:        n,
		Epsilon:  epsilon,
	}

	lowerProb := q - epsilon
	upperProb := q + epsilon

	if lowerProb <= 0 {
		// The band covers below the smallest order statistic. HDR histograms
		// represent non-negative latencies/counts, so 0 is the support lower
		// bound and the only honest endpoint the data supports.
		ci.Lower = 0
		ci.LowerClampedToSupport = true
	} else {
		// Generalized inverse of the empirical CDF: the k-th order statistic,
		// k = ceil(lowerProb*N).
		lowerValue := h.getValueFromIdxUpToCount(empiricalQuantileRank(lowerProb, N))
		// Widen to the low edge of the equivalence bucket so bucket rounding
		// cannot pull the guaranteed endpoint inward.
		ci.Lower = h.lowestEquivalentValue(lowerValue)
	}

	if upperProb >= 1 {
		// The band reaches above the largest order statistic. Nothing in the
		// sample bounds the upper tail; Max() is an observed value, not a
		// population ceiling, so it must not be returned here.
		ci.UpperFinite = false
	} else {
		upperValue := h.getValueFromIdxUpToCount(empiricalQuantileRank(upperProb, N))
		ci.Upper = h.highestEquivalentValue(upperValue)
		ci.UpperFinite = true
	}

	return ci, nil
}

// empiricalQuantileRank returns k = ceil(p*N), the 1-based rank of the
// generalized-inverse empirical quantile at probability p, where 0 < p < 1.
// A tiny relative tolerance snaps values that land within 1e-12 relative
// error of an integer to that integer, so a mathematically exact product
// (e.g. 0.5*8) is not pushed to the next order statistic by float64 round-off.
func empiricalQuantileRank(p, N float64) int64 {
	x := p * N
	nearest := math.Round(x)
	// Only snap to an integer that is an actual 1-based order-statistic rank:
	// a sub-0.5 product that rounds to 0 must fall through to ceil (rank 1),
	// never to rank 0.
	if nearest >= 1 && math.Abs(x-nearest) <= 1e-12*math.Max(1, math.Abs(x)) {
		return int64(nearest)
	}
	rank := int64(math.Ceil(x))
	if rank < 1 {
		return 1
	}
	return rank
}
