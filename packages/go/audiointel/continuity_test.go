package audiointel

import (
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

func mustContinuity(t *testing.T, cfg ContinuityConfig) *ContinuityDetector {
	t.Helper()
	c, err := NewContinuityDetector(cfg)
	if err != nil {
		t.Fatalf("NewContinuityDetector: %v", err)
	}
	return c
}

// seqFeatures builds features for one clean frame in a continuous stream.
func seqFeatures(seq uint64) FrameFeatures {
	return FrameFeatures{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * testInterval,
		Duration:  testInterval,
		RMS:       0.01,
		Energy:    0.0001,
		Samples:   160,
	}
}

// TestContinuity_DetectsEveryTransportFault walks §13's list.
func TestContinuity_DetectsEveryTransportFault(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity

	t.Run("a clean stream reports nothing", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 100; i++ {
			r := c.Observe(seqFeatures(i))
			if r.Fault != FaultNone {
				t.Fatalf("frame %d on a clean stream reported %s", i, r.Fault)
			}
			if r.Degraded {
				t.Fatalf("frame %d on a clean stream reported degraded", i)
			}
		}
		if got := c.GapRatio(); got != 0 {
			t.Errorf("GapRatio = %g on a clean stream, want 0", got)
		}
	})

	t.Run("missing sequence", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 5; i++ {
			c.Observe(seqFeatures(i))
		}

		// 5, 6 and 7 never arrive.
		r := c.Observe(seqFeatures(8))
		if r.Fault != FaultMissing {
			t.Fatalf("fault = %s, want %s", r.Fault, FaultMissing)
		}
		if r.MissingBefore != 3 {
			t.Errorf("MissingBefore = %d, want 3", r.MissingBefore)
		}
		if !r.GapOpened {
			t.Error("GapOpened was not set on the first fault")
		}
		if !r.Degraded {
			t.Error("continuity was not marked degraded")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 5; i++ {
			c.Observe(seqFeatures(i))
		}

		// The newest sequence repeated.
		if r := c.Observe(seqFeatures(4)); r.Fault != FaultDuplicate {
			t.Errorf("repeating the newest sequence reported %s, want %s",
				r.Fault, FaultDuplicate)
		}
		// An older sequence, still inside the 64-frame window.
		if r := c.Observe(seqFeatures(1)); r.Fault != FaultDuplicate {
			t.Errorf("repeating an older seen sequence reported %s, want %s",
				r.Fault, FaultDuplicate)
		}
	})

	t.Run("out of order", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for _, seq := range []uint64{0, 1, 2, 5} {
			c.Observe(seqFeatures(seq))
		}

		// 3 arrives late: below the highest, never seen before.
		if r := c.Observe(seqFeatures(3)); r.Fault != FaultOutOfOrder {
			t.Errorf("a late unseen sequence reported %s, want %s",
				r.Fault, FaultOutOfOrder)
		}
		// And now it IS seen, so a repeat is a duplicate.
		if r := c.Observe(seqFeatures(3)); r.Fault != FaultDuplicate {
			t.Errorf("repeating a late sequence reported %s, want %s",
				r.Fault, FaultDuplicate)
		}
	})

	t.Run("timestamp jump", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 5; i++ {
			c.Observe(seqFeatures(i))
		}

		f := seqFeatures(5)
		f.Timestamp += cfg.MaxTimestampJump * 3
		r := c.Observe(f)
		if r.Fault != FaultTimestampJump {
			t.Fatalf("fault = %s, want %s", r.Fault, FaultTimestampJump)
		}
		if r.TimestampDelta <= cfg.MaxTimestampJump {
			t.Errorf("TimestampDelta = %s, want more than %s",
				r.TimestampDelta, cfg.MaxTimestampJump)
		}
	})

	t.Run("timestamp running backwards", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 10; i++ {
			c.Observe(seqFeatures(i))
		}

		// A NEW sequence carrying an old timestamp: the sequence numbers say
		// forward, the media clock says backward. A different upstream bug from
		// a forward leap, and reported differently.
		f := seqFeatures(10)
		f.Timestamp = 0
		if r := c.Observe(f); r.Fault != FaultTimestampReverse {
			t.Errorf("fault = %s, want %s", r.Fault, FaultTimestampReverse)
		}
	})

	t.Run("synthesised gap fill", func(t *testing.T) {
		t.Parallel()
		c := mustContinuity(t, cfg)
		for i := uint64(0); i < 5; i++ {
			c.Observe(seqFeatures(i))
		}

		f := seqFeatures(5)
		f.Flags = media.FlagSilence | media.FlagDiscontinuity
		r := c.Observe(f)
		if r.Fault != FaultSynthesised {
			t.Errorf("fault = %s, want %s — Phase 11B covering a gap means there "+
				"WAS a gap", r.Fault, FaultSynthesised)
		}
		if !r.Degraded {
			t.Error("invented audio did not count against continuity")
		}
	})
}

// TestContinuity_OutOfOrderDoesNotPoisonTheTimestampReference guards a
// second-order fault.
//
// A late frame carries an old timestamp. Letting it move the reference
// backwards would make the NEXT in-order frame look like a forward jump —
// turning one transport fault into two.
func TestContinuity_OutOfOrderDoesNotPoisonTheTimestampReference(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	c := mustContinuity(t, cfg)

	for _, seq := range []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		c.Observe(seqFeatures(seq))
	}

	// Frame 2 arrives very late.
	if r := c.Observe(seqFeatures(2)); r.Fault != FaultDuplicate {
		t.Fatalf("setup: fault = %s", r.Fault)
	}

	// The next in-order frame must still be clean.
	if r := c.Observe(seqFeatures(11)); r.Fault != FaultNone {
		t.Errorf("the frame after a late arrival reported %s; the late frame "+
			"dragged the timestamp reference backwards", r.Fault)
	}
}

// TestContinuity_RestoresAfterCleanFrames pins both ends of the degradation
// cycle.
func TestContinuity_RestoresAfterCleanFrames(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	c := mustContinuity(t, cfg)

	for i := uint64(0); i < 10; i++ {
		c.Observe(seqFeatures(i))
	}

	// Break it.
	if r := c.Observe(seqFeatures(20)); !r.GapOpened {
		t.Fatal("the gap was not reported as opened")
	}

	// Clean frames, but not yet enough.
	seq := uint64(21)
	for i := 0; i < cfg.RestoreFrames-1; i++ {
		r := c.Observe(seqFeatures(seq))
		seq++
		if r.Restored {
			t.Fatalf("restored after %d clean frames, want %d", i+1, cfg.RestoreFrames)
		}
		if !r.Degraded {
			t.Fatalf("stopped reporting degraded after %d clean frames", i+1)
		}
	}

	r := c.Observe(seqFeatures(seq))
	if !r.Restored {
		t.Fatalf("not restored after %d clean frames", cfg.RestoreFrames)
	}
	if r.Degraded {
		t.Error("still degraded on the frame that reported restoration")
	}

	// And only once.
	if r := c.Observe(seqFeatures(seq + 1)); r.Restored {
		t.Error("restoration was reported twice")
	}
}

// TestContinuity_GapRatioIsBounded checks the window arithmetic and that the
// ring never grows.
func TestContinuity_GapRatioIsBounded(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	cfg.WindowFrames = 10
	c := mustContinuity(t, cfg)

	// Ten clean frames.
	seq := uint64(0)
	for i := 0; i < 10; i++ {
		c.Observe(seqFeatures(seq))
		seq++
	}
	if got := c.GapRatio(); got != 0 {
		t.Errorf("GapRatio = %g after ten clean frames, want 0", got)
	}

	// Five faulty frames push out five clean ones.
	for i := 0; i < 5; i++ {
		f := seqFeatures(seq)
		f.Flags = media.FlagSilence
		c.Observe(f)
		seq++
	}
	if got := c.GapRatio(); got != 0.5 {
		t.Errorf("GapRatio = %g after five faults in a ten-frame window, want 0.5", got)
	}

	// Ten more clean frames flush the window completely.
	for i := 0; i < 10; i++ {
		c.Observe(seqFeatures(seq))
		seq++
	}
	if got := c.GapRatio(); got != 0 {
		t.Errorf("GapRatio = %g after the window refilled with clean frames, want 0", got)
	}

	if len(c.faults) != 10 {
		t.Errorf("the fault ring grew to %d entries", len(c.faults))
	}
}

// TestContinuity_ConsumesPhase11BDeliveryVerdicts covers the faults no arriving
// frame can show.
func TestContinuity_ConsumesPhase11BDeliveryVerdicts(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity

	cases := []struct {
		name   string
		result media.PipelineResult
		want   ContinuityFault
	}{
		{"accepted", media.PipelineResult{Accepted: true}, FaultNone},
		{"duplicate", media.PipelineResult{Reason: media.DropDuplicate}, FaultDuplicate},
		{"late", media.PipelineResult{Reason: media.DropLate}, FaultOutOfOrder},
		{"too early", media.PipelineResult{Reason: media.DropTooEarly}, FaultOutOfOrder},
		{"bad timestamp", media.PipelineResult{Reason: media.DropTimestamp}, FaultTimestampJump},
		{"buffer full", media.PipelineResult{Reason: media.DropBufferFull}, FaultStarvation},
		{"not accepting", media.PipelineResult{Reason: media.DropNotAccepting}, FaultStarvation},

		// Malformed input is the producer disagreeing with the pipeline about
		// the format. The media runtime already counts it, and it says nothing
		// about whether the audio is CONTINUOUS.
		{"invalid frame", media.PipelineResult{Reason: media.DropInvalid}, FaultNone},
		{"format mismatch", media.PipelineResult{Reason: media.DropFormat}, FaultNone},
		{"oversized", media.PipelineResult{Reason: media.DropOversized}, FaultNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := mustContinuity(t, cfg)
			if got := c.ObserveDelivery(tc.result); got != tc.want {
				t.Errorf("ObserveDelivery(%v) = %s, want %s", tc.result.Reason, got, tc.want)
			}
		})
	}
}

// TestContinuity_DuplicateWindowIsBoundedAt64Frames documents the limit
// honestly rather than pretending to unbounded memory.
func TestContinuity_DuplicateWindowIsBoundedAt64Frames(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	c := mustContinuity(t, cfg)

	for i := uint64(0); i < 200; i++ {
		c.Observe(seqFeatures(i))
	}

	// Well inside the window: recognised as a duplicate.
	if r := c.Observe(seqFeatures(180)); r.Fault != FaultDuplicate {
		t.Errorf("a repeat 19 frames back reported %s, want %s", r.Fault, FaultDuplicate)
	}

	// Beyond it: reported as out-of-order, which is the honest answer. At that
	// distance a duplicate and a very late frame are indistinguishable without
	// the unbounded memory this deliberately does not keep.
	if r := c.Observe(seqFeatures(10)); r.Fault != FaultOutOfOrder {
		t.Errorf("a repeat 189 frames back reported %s, want %s — beyond the "+
			"64-frame window the two cases cannot be told apart",
			r.Fault, FaultOutOfOrder)
	}
}

func TestContinuity_ResetKeepsItsStorage(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	c := mustContinuity(t, cfg)

	for i := uint64(0); i < 50; i++ {
		c.Observe(seqFeatures(i))
	}
	c.Observe(seqFeatures(100))
	before := &c.faults[0]

	c.Reset()

	if c.Degraded() {
		t.Error("Reset left the detector degraded")
	}
	if c.GapRatio() != 0 {
		t.Errorf("Reset left a gap ratio of %g", c.GapRatio())
	}
	if &c.faults[0] != before {
		t.Error("Reset reallocated the fault ring")
	}
	if r := c.Observe(seqFeatures(500)); r.Fault != FaultNone {
		t.Errorf("the first frame after Reset reported %s; the detector kept "+
			"state across the reset", r.Fault)
	}
}

func TestNewContinuityDetector_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Continuity
	cfg.WindowFrames = 0
	if _, err := NewContinuityDetector(cfg); err == nil {
		t.Error("a zero window was accepted")
	}
}

func BenchmarkContinuityDetector_Observe(b *testing.B) {
	cfg := DefaultConfig(testFormat()).Continuity
	c, err := NewContinuityDetector(cfg)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Observe(seqFeatures(uint64(i)))
	}
}
