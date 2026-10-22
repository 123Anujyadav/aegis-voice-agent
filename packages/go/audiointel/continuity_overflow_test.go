package audiointel

import (
	"math"
	"testing"
)

// TestContinuity_MissingBeforeNeverWrapsNegative pins MissingBefore for every
// size of sequence gap. The gap is a uint64 difference and used to be
// converted straight to int, so a gap above 2^63 reported a negative number of
// missing frames.
func TestContinuity_MissingBeforeNeverWrapsNegative(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig(testFormat()).Continuity

	cases := []struct {
		name string
		next uint64 // the sequence that arrives after frames 0..4
		want int
	}{
		{"three missing", 8, 3},
		{"largest gap that fits in int", 4 + 1 + math.MaxInt, math.MaxInt},
		{"2^63 missing", 4 + 1 + 1<<63, math.MaxInt},
		{"the largest representable sequence", math.MaxUint64, math.MaxInt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := mustContinuity(t, cfg)
			for i := uint64(0); i < 5; i++ {
				c.Observe(seqFeatures(i))
			}

			r := c.Observe(seqFeatures(tc.next))
			if r.Fault != FaultMissing {
				t.Fatalf("fault = %s, want %s", r.Fault, FaultMissing)
			}
			if r.MissingBefore < 0 {
				t.Fatalf("MissingBefore = %d: a gap was reported as negative", r.MissingBefore)
			}
			if r.MissingBefore != tc.want {
				t.Errorf("MissingBefore = %d, want %d", r.MissingBefore, tc.want)
			}
		})
	}
}
