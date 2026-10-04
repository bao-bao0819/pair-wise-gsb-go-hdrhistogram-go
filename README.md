hdrhistogram-go
===============

<a href="https://pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go"><img src="https://pkg.go.dev/badge/github.com/HdrHistogram/hdrhistogram-go" alt="PkgGoDev"></a>
[![Gitter](https://badges.gitter.im/Join_Chat.svg)](https://gitter.im/HdrHistogram/HdrHistogram)
![Test](https://github.com/HdrHistogram/hdrhistogram-go/workflows/Test/badge.svg?branch=master)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/HdrHistogram/hdrhistogram-go/blob/master/LICENSE)
[![Codecov](https://codecov.io/gh/HdrHistogram/hdrhistogram-go/branch/master/graph/badge.svg)](https://codecov.io/gh/HdrHistogram/hdrhistogram-go)


A pure Go implementation of the [HDR Histogram](https://github.com/HdrHistogram/HdrHistogram).

> A Histogram that supports recording and analyzing sampled data value counts
> across a configurable integer value range with configurable value precision
> within the range. Value precision is expressed as the number of significant
> digits in the value recording, and provides control over value quantization
> behavior across the value range and the subsequent value resolution at any
> given level.

For documentation, check [godoc](https://pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go).


## Getting Started

### Installing
Use `go get` to retrieve the hdrhistogram-go implementation and to add it to your `GOPATH` workspace, or project's Go module dependencies.

```go
go get github.com/HdrHistogram/hdrhistogram-go
```

To update the implementation use `go get -u` to retrieve the latest version of the hdrhistogram.

```go
go get github.com/HdrHistogram/hdrhistogram-go
```


### Go Modules

If you are using Go modules, your `go get` will default to the latest tagged
release version of the histogram. To get a specific release version, use
`@<tag>` in your `go get` command.

```go
go get github.com/HdrHistogram/hdrhistogram-go@v0.9.0
```

To get the latest HdrHistogram/hdrhistogram-go master repository change use `@latest`.

```go
go get github.com/HdrHistogram/hdrhistogram-go@latest
```

### Repo transfer and impact on go dependencies
-------------------------------------------
This repository has been transferred under the github HdrHstogram umbrella with the help from the orginal
author in Sept 2020. The main reasons are to group all implementations under the same roof and to provide more active contribution
from the community as the orginal repository was archived several years ago.

Unfortunately such URL change will break go applications that depend on this library
directly or indirectly, as discussed [here](https://github.com/HdrHistogram/hdrhistogram-go/issues/30#issuecomment-696365251).

The dependency URL should be modified to point to the new repository URL.
The tag "v0.9.0" was applied at the point of transfer and will reflect the exact code that was frozen in the
original repository.

If you are using Go modules, you can update to the exact point of transfter using the `@v0.9.0` tag in your `go get` command.

```
go mod edit -replace github.com/codahale/hdrhistogram=github.com/HdrHistogram/hdrhistogram-go@v0.9.0
```

## Credits
-------

Many thanks for Coda Hale for contributing the initial implementation and transfering the repository here.

## Latency bin planning

`Histogram.PlanLatencyBins(k)` partitions a latency distribution into exactly
`k` non-empty, contiguous bins whose boundaries are derived from the observed
distribution rather than fixed p50/p90 quantiles:

```go
plan, err := h.PlanLatencyBins(4)
// plan.Bins:               k segments with Low, High, Count, Mean
// plan.Thresholds:         k-1 boundaries (== next segment's first-bucket low)
// plan.TotalSquaredError:  minimized within-bin weighted squared deviation
```

Each non-empty HDR bucket is one weighted point: the representative value is
`medianEquivalentValue` and the weight is the bucket count. A cut is only
allowed between two adjacent non-empty buckets, and a threshold is the low end
of the next segment's first bucket, so an equivalent-value bucket is never
split.

The partition minimizes the within-bin weighted sum of squared deviations. It
is solved exactly with dynamic programming — interval costs in O(1) from
prefix sums of `count`, `count*median` and `count*median^2`, followed by
backtracking — not by equal-frequency binning or a local greedy search. When
multiple partitions share the same cost, the lexicographically smallest cut
vector is returned.

**Numerics.** Every `count*median` product is computed in `float64`
(`float64(count) * float64(median)`) before any multiplication, so large
counts/values cannot wrap an `int64` product. DP costs are compared as exact
IEEE-754 values with no epsilon (strict `<`), which makes the tie-break
deterministic; mathematically zero residuals that come out slightly negative
from cancellation are clamped to zero.

**Cost limits.** The DP is O(k*n^2) time and O(k*n) space over the `n`
non-empty buckets. The public caps `MaxPlanningNonEmptyBuckets` (2048) and
`MaxPlanningBins` (32) are checked before any DP work; the source histogram is
never resampled or reduced in precision. `k > n`, `k < 1`, and empty
histograms return an error.

A small benchmark (`BenchmarkPlanLatencyBins`, 256 non-empty buckets, k=8)
completes in about 0.5 ms/op on a modern desktop CPU.

Equal-frequency cuts are demonstrably worse on skewed data: with equally
weighted samples near 10 and one at 100000, the p50 cut splits the tight
cluster and produces a squared error over 100x larger than the optimum, which
isolates the tail.
