package media

import (
	"math"
	"testing"
	"time"
)

// TestPipeline_GapFillIsBoundedForAnyGap pins the MaxGapFill bound for every
// size of sequence gap, including those beyond the range of int.
//
// The gap is a uint64 difference. It used to be converted to int before the
// comparison, so a gap above 2^63 wrapped negative, skipped the clamp, filled
// the output with silence until the buffer refused more, and overflowed the
// timestamp arithmetic.
func TestPipeline_GapFillIsBoundedForAnyGap(t *testing.T) {
	t.Parallel()

	const base = uint64(10)
	at := time.Hour

	cases := []struct {
		name string
		gap  func(bound uint64) uint64
	}{
		{"one frame missing", func(uint64) uint64 { return 1 }},
		{"exactly the bound", func(b uint64) uint64 { return b }},
		{"one above the bound", func(b uint64) uint64 { return b + 1 }},
		{"2^63", func(uint64) uint64 { return 1 << 63 }},
		{"the largest representable gap", func(uint64) uint64 { return math.MaxUint64 - base }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := harness(t)
			p := newPipeline(t, h)
			frameDur := p.cfg.FrameInterval
			bound := uint64(p.cfg.MaxGapFill / frameDur)
			if bound == 0 {
				t.Fatal("the default configuration fills no gap; the test needs one that does")
			}

			p.mu.Lock()
			p.nextExpected, p.haveDelivered = base, true
			p.mu.Unlock()

			gap := tc.gap(bound)
			want := gap
			if want > bound {
				want = bound
			}

			written := p.fillGaps(Frame{Sequence: base + gap, Timestamp: at})
			if uint64(written) != want {
				t.Fatalf("filled %d frames for a gap of %d, want %d (bound %d)",
					written, gap, want, bound)
			}

			// The silence leads up to the frame that ended the gap: contiguous,
			// increasing, and never past it.
			prev := time.Duration(math.MinInt64)
			var n int
			for {
				f, err := p.Buffer().Read()
				if err != nil {
					break
				}
				n++
				if f.Timestamp <= prev {
					t.Fatalf("silence timestamp %v does not follow %v", f.Timestamp, prev)
				}
				if f.Timestamp >= at {
					t.Fatalf("silence timestamp %v is not before the frame at %v", f.Timestamp, at)
				}
				prev = f.Timestamp
			}
			if n != written {
				t.Fatalf("buffer holds %d frames, fillGaps reported %d", n, written)
			}
			if written > 0 && prev != at-frameDur {
				t.Errorf("last silence frame at %v, want %v", prev, at-frameDur)
			}
		})
	}
}
