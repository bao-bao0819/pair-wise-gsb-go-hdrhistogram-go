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

## Quantile confidence intervals (DKW)

`ValueAtPercentile` returns a single plug-in quantile of the recorded sample.
With only a few dozen observations that point is a noisy estimate of the
population quantile; dashboards that display it as an exact p99 invite
misreading. For that use case the histogram also offers
`QuantileConfidenceIntervalAt(q, alpha)`, which returns a distribution-free,
finite-sample interval based on the Dvoretzky–Kiefer–Wolfowitz (DKW) bound —
no random bootstrap and no statistics dependency:

```go
ci, err := h.QuantileConfidenceIntervalAt(0.99, 0.05)
if err != nil {
    // ErrInvalidQuantile / ErrInvalidAlpha / ErrEmptyHistogram
}
// With probability >= 0.95 over the sampling process the true p99 lies in
// [ci.Lower, ci.Upper] when ci.UpperFinite is true.
```

Given `N = TotalCount()` and

```
epsilon = sqrt(log(2/alpha) / (2*N))
```

the endpoints are the generalized-inverse empirical quantiles at `q-epsilon`
and `q+epsilon`, each widened to the low/high edge of its HDR equivalence
bucket so bucket rounding cannot erode the guarantee. Demanding more
confidence (smaller `alpha`) only widens the interval; it never narrows it.

When the DKW band reaches beyond the empirical support the interval says so
explicitly instead of pretending the sample bounds the population:

- `q-epsilon <= 0` sets `Lower = 0` with `LowerClampedToSupport = true` (HDR
  histograms track non-negative values, so 0 is the support lower bound).
- `q+epsilon >= 1` sets `UpperFinite = false` (`Upper = 0`). The observed
  `Max` is deliberately **not** returned as an upper-tail bound; e.g. the p99
  of 50 observations at `alpha = 0.05` has no finite upper endpoint.

**Independence prerequisite.** The guarantee assumes every count unit in
`TotalCount()` is an independent, identically distributed observation. Bulk
counts recorded with `RecordValues(v, n)` qualify only when they truly
represent `n` independent observations. Weighted frequencies, imputed or
duplicated samples, and the coordinated-omission fill-ins synthesized by
`RecordCorrectedValue` are not independent observations; on such data the DKW
interval does not carry its coverage guarantee.

## Credits
-------

Many thanks for Coda Hale for contributing the initial implementation and transfering the repository here.
