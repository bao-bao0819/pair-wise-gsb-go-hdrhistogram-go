package hdrhistogram

import "testing"

func TestEmpiricalQuantileRank(t *testing.T) {
	cases := []struct {
		p, N float64
		want int64
	}{
		{0.5, 8, 4},          // exact integer product: round-off must not inflate to 5
		{0.56053059, 10, 6},  // ceil(5.6053...) = 6
		{0.9999, 1000, 1000}, // ceil(999.9)
		{1e-16, 10, 1},       // tiny product rounds toward 0; rank must stay 1
		{0.5, 1, 1},          // ceil(0.5) = 1
	}
	for _, c := range cases {
		if got := empiricalQuantileRank(c.p, c.N); got != c.want {
			t.Errorf("empiricalQuantileRank(%v, %v) = %d, want %d", c.p, c.N, got, c.want)
		}
	}
}
