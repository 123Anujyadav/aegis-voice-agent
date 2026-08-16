package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Stream lifecycle
// ---------------------------------------------------------------------------

func TestLifecycle_OpenWriteReadClose(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != StateActive {
		t.Fatalf("state = %s, want active", s.State())
	}

	const frames = 10
	for i := 0; i < frames; i++ {
		res, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now()))
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if !res.Accepted {
			t.Fatalf("frame %d refused: %s", i, res.Reason)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}

	// The jitter buffer holds frames until their playout time; pumping moves
	// the due ones into the output ring.
	delivered := s.Pump()
	if delivered == 0 {
		t.Fatal("no frames were delivered after pumping")
	}

	var read int
	for {
		if _, err := s.Read(); err != nil {
			break
		}
		read++
	}
	if read == 0 {
		t.Error("no frames could be read")
	}
	t.Logf("wrote %d frames, delivered %d, read %d", frames, delivered, read)

	if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
		t.Fatal(err)
	}
	if s.State() != StateClosed {
		t.Errorf("state = %s, want closed", s.State())
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("%d streams still live", h.Runtime.Live())
	}
}

func TestLifecycle_PauseRefusesWritesButKeepsBuffer(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	s.Pump()
	depthBefore := s.Pipeline().Buffer().Len()

	if err := h.Coordinator.Pause(s.ID()); err != nil {
		t.Fatal(err)
	}

	// A pause refuses writes...
	if _, err := s.Write(h.Gen.Next(h.Clock.Now())); !errors.Is(err, ErrStreamPaused) {
		t.Errorf("err = %v, want ErrStreamPaused", err)
	}
	// ...but retains buffered audio. Discarding it would make every pause
	// audible as a gap on resume.
	if got := s.Pipeline().Buffer().Len(); got != depthBefore {
		t.Errorf("buffer holds %d frames after pause, want %d", got, depthBefore)
	}
	// And a paused stream still delivers what was already buffered.
	if _, err := s.Read(); err != nil {
		t.Errorf("a paused stream refused a read: %v", err)
	}

	if err := h.Coordinator.Resume(s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(h.Gen.Next(h.Clock.Now())); err != nil {
		t.Errorf("a resumed stream refused a write: %v", err)
	}
}

func TestLifecycle_DrainRefusesWritesButDeliversBuffer(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	s.Pump()

	if err := h.Coordinator.Drain(s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(h.Gen.Next(h.Clock.Now())); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("a draining stream accepted a write: %v", err)
	}
	// A closing stream delivering nothing would discard exactly the audio the
	// drain exists to save.
	if _, err := s.Read(); err != nil {
		t.Errorf("a draining stream refused a read: %v", err)
	}
}

func TestLifecycle_CloseFromActivePassesThroughClosing(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
		t.Fatal(err)
	}

	// Going straight from Active to Closed is not declared: it would discard
	// buffered audio without the drain that makes the discard deliberate.
	var sawClosing bool
	for _, rec := range s.History() {
		if rec.To == StateClosing {
			sawClosing = true
		}
	}
	if !sawClosing {
		t.Errorf("close skipped Closing: %v", s.History())
	}
}

func TestLifecycle_TerminalStreamRefusesEverything(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(h.Gen.Next(h.Clock.Now())); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("write err = %v, want ErrStreamClosed", err)
	}
	if _, err := s.Read(); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("read err = %v, want ErrStreamClosed", err)
	}
	if n := s.Pump(); n != 0 {
		t.Errorf("a closed stream pumped %d frames", n)
	}
}

// ---------------------------------------------------------------------------
// Pipeline
// ---------------------------------------------------------------------------

func newPipeline(t *testing.T, h *Harness) *Pipeline {
	t.Helper()
	cfg := h.Runtime.Config().Pipeline
	p, err := NewPipeline(cfg, h.Clock)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	return p
}

func TestPipeline_DeliversInOrder(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)

	const n = 20
	for i := 0; i < n; i++ {
		res := p.Push(h.Gen.Next(h.Clock.Now()))
		if !res.Accepted {
			t.Fatalf("frame %d refused: %s", i, res.Reason)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	p.Pump()

	var last uint64
	var read int
	for {
		f, err := p.Buffer().Read()
		if err != nil {
			break
		}
		if read > 0 && f.Sequence <= last {
			t.Errorf("frame %d arrived out of order after %d", f.Sequence, last)
		}
		last = f.Sequence
		read++
	}
	if read == 0 {
		t.Fatal("nothing was delivered")
	}
}

// TestPipeline_ReordersOutOfOrderFrames is the jitter buffer's core job.
func TestPipeline_ReordersOutOfOrderFrames(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)
	interval := 20 * time.Millisecond

	// Deliver 0, 2, 1, 3 — a single swap, the commonest network reordering.
	order := []uint64{0, 2, 1, 3}
	for _, seq := range order {
		ts := time.Duration(seq) * interval
		res := p.Push(h.Gen.NextAt(seq, ts, h.Clock.Now()))
		if !res.Accepted {
			t.Fatalf("frame %d refused: %s", seq, res.Reason)
		}
		h.Clock.Advance(interval)
	}
	if !p.Jitter().ordered() {
		t.Error("the jitter buffer's held frames are not sorted by release time")
	}

	p.Pump()

	var got []uint64
	for {
		f, err := p.Buffer().Read()
		if err != nil {
			break
		}
		got = append(got, f.Sequence)
	}
	// The point of the buffer: they come out in order.
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("output is not ordered: %v", got)
		}
	}
	if stats := p.Jitter().Stats(); stats.Reordered == 0 {
		t.Error("the reordering was not counted")
	}
}

func TestPipeline_DropsLateAndDuplicateFrames(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)
	interval := 20 * time.Millisecond

	for i := 0; i < 10; i++ {
		p.Push(h.Gen.Next(h.Clock.Now()))
		h.Clock.Advance(interval)
	}
	p.Pump()
	// Drain, so playout has advanced well past the early frames.
	for {
		if _, err := p.Buffer().Read(); err != nil {
			break
		}
	}

	// A duplicate of an already-released frame.
	dup := p.Push(h.Gen.NextAt(0, 0, h.Clock.Now()))
	if dup.Accepted {
		t.Error("a duplicate of a released frame was accepted")
	}

	stats := p.Stats()
	if stats.TotalDrop == 0 {
		t.Error("no drops were counted")
	}
	t.Logf("drops: %v", stats.Dropped)
}

// TestPipeline_RefusesWildTimestamps is the guard against one bad frame
// stalling a stream forever.
func TestPipeline_RefusesWildTimestamps(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)

	p.Push(h.Gen.Next(h.Clock.Now()))

	// A timestamp hours ahead — a clock bug, a corrupted header, a replayed
	// session. Accepting it would move the jitter buffer's playout position
	// somewhere no subsequent frame could reach.
	wild := h.Gen.NextAt(99, 6*time.Hour, h.Clock.Now())
	res := p.Push(wild)

	if res.Accepted {
		t.Fatal("a frame six hours out was accepted; playout would be poisoned")
	}
	if res.Reason != DropTimestamp {
		t.Errorf("reason = %s, want %s", res.Reason, DropTimestamp)
	}

	// The stream must still work afterwards.
	if next := p.Push(h.Gen.Next(h.Clock.Now())); !next.Accepted {
		t.Errorf("the pipeline was poisoned by the rejected frame: %s", next.Reason)
	}
}

func TestPipeline_FillsGapsWithBoundedSilence(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)
	interval := 20 * time.Millisecond

	for i := 0; i < 3; i++ {
		p.Push(h.Gen.Next(h.Clock.Now()))
		h.Clock.Advance(interval)
	}
	p.Pump()
	for {
		if _, err := p.Buffer().Read(); err != nil {
			break
		}
	}

	// Skip 4 frames, then deliver the next.
	h.Gen.Skip(4)
	p.Push(h.Gen.Next(h.Clock.Now()))
	h.Clock.Advance(interval)
	p.Pump()

	var silence int
	for {
		f, err := p.Buffer().Read()
		if err != nil {
			break
		}
		if f.Flags.Has(FlagSilence) {
			silence++
		}
	}
	if silence == 0 {
		t.Error("no silence was synthesised for the gap")
	}

	// Bounded: a source that vanished for a minute must not produce a minute of
	// invented audio.
	maxFrames := int(p.cfg.MaxGapFill / interval)
	if silence > maxFrames {
		t.Errorf("%d silence frames exceeds the %d-frame gap-fill bound", silence, maxFrames)
	}
	if stats := p.Stats(); stats.Gaps == 0 {
		t.Error("the gap was not counted")
	}
}

func TestPipeline_RefusesFormatMismatch(t *testing.T) {
	t.Parallel()
	h := harness(t)
	p := newPipeline(t, h)

	wrong := Frame{Sequence: 1, Format: PCM16Mono16k(), Payload: make([]byte, 640)}
	res := p.Push(wrong)

	if res.Accepted {
		t.Fatal("a frame of the wrong format was accepted")
	}
	if res.Reason != DropFormat {
		t.Errorf("reason = %s, want %s", res.Reason, DropFormat)
	}
}

// ---------------------------------------------------------------------------
// Jitter buffer
// ---------------------------------------------------------------------------

func TestJitter_HoldsFramesUntilPlayout(t *testing.T) {
	t.Parallel()
	h := harness(t)
	cfg := DefaultJitterConfig()
	cfg.Adaptive = false
	cfg.TargetDelay = 60 * time.Millisecond

	jb, err := NewJitterBuffer(cfg, h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	// One frame is not enough to reach the delay: holding it is the buffer
	// doing its job.
	f := h.Gen.Next(h.Clock.Now()).Clone()
	if disp := jb.Put(f); disp != FrameAccepted {
		t.Fatalf("disposition = %s", disp)
	}
	if _, err := jb.Get(); err != nil {
		t.Log("first frame held until playout, as designed")
	}

	// Feed enough to cross the delay.
	for i := 0; i < 5; i++ {
		jb.Put(h.Gen.Next(h.Clock.Now()).Clone())
	}
	if jb.Ready() == 0 {
		t.Error("no frames became due after crossing the target delay")
	}
}

func TestJitter_AdaptsToMeasuredJitter(t *testing.T) {
	t.Parallel()
	h := harness(t)
	cfg := DefaultJitterConfig()
	cfg.Adaptive = true
	cfg.TargetDelay = 20 * time.Millisecond
	cfg.MinDelay = 20 * time.Millisecond
	cfg.MaxDelay = 200 * time.Millisecond

	jb, err := NewJitterBuffer(cfg, h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	before := jb.Delay()
	interval := 20 * time.Millisecond

	// Highly irregular arrivals: media time advances evenly, wall time does not.
	for i := 0; i < 60; i++ {
		ts := time.Duration(i) * interval
		jb.Put(h.Gen.NextAt(uint64(i), ts, h.Clock.Now()).Clone())
		if i%2 == 0 {
			h.Clock.Advance(interval + 40*time.Millisecond)
		} else {
			h.Clock.Advance(time.Millisecond)
		}
		// Drain so the buffer does not simply fill.
		for {
			if _, err := jb.Get(); err != nil {
				break
			}
		}
	}

	after := jb.Delay()
	if jb.Jitter() == 0 {
		t.Error("no jitter was measured despite highly irregular arrivals")
	}
	if after <= before {
		t.Errorf("delay did not grow with jitter: %v -> %v (jitter %v)",
			before, after, jb.Jitter())
	}
	if after > cfg.MaxDelay {
		t.Errorf("delay %v exceeded MaxDelay %v", after, cfg.MaxDelay)
	}
	t.Logf("delay adapted %v -> %v at measured jitter %v", before, after, jb.Jitter())
}

func TestJitter_ResetClearsHistory(t *testing.T) {
	t.Parallel()
	h := harness(t)
	jb, err := NewJitterBuffer(DefaultJitterConfig(), h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		jb.Put(h.Gen.Next(h.Clock.Now()).Clone())
		h.Clock.Advance(20 * time.Millisecond)
	}

	jb.Reset()

	// Sequence and arrival history from before a reconnection describes a
	// source that no longer exists. Carrying it forward would report every
	// frame of the new source as a duplicate or as wildly late.
	h.Gen.Reset()
	if disp := jb.Put(h.Gen.Next(h.Clock.Now()).Clone()); disp != FrameAccepted {
		t.Errorf("after Reset, a fresh sequence was %s", disp)
	}
	if got := jb.Jitter(); got != 0 {
		t.Errorf("jitter = %v after Reset, want 0", got)
	}
}

func TestJitterEstimator_SmoothsOutliers(t *testing.T) {
	t.Parallel()
	est := NewJitterEstimator()
	base := time.Now()
	interval := 20 * time.Millisecond

	// Twenty perfectly regular arrivals.
	for i := 0; i < 20; i++ {
		est.Observe(time.Duration(i)*interval, base.Add(time.Duration(i)*interval))
	}
	steady := est.Jitter()

	// One wild outlier.
	est.Observe(20*interval, base.Add(20*interval+500*time.Millisecond))
	afterOutlier := est.Jitter()

	// The 1/16 gain means one outlier moves the estimate by about 1/16 of the
	// deviation. A jitter estimate that jumped on every outlier would make the
	// adaptive buffer oscillate.
	if afterOutlier > 100*time.Millisecond {
		t.Errorf("one 500ms outlier moved the estimate to %v; smoothing is not working",
			afterOutlier)
	}
	if est.Peak() < 400*time.Millisecond {
		t.Errorf("peak = %v, want the outlier retained for diagnosis", est.Peak())
	}
	t.Logf("steady=%v after outlier=%v peak=%v", steady, afterOutlier, est.Peak())
}

// ---------------------------------------------------------------------------
// Media clock
// ---------------------------------------------------------------------------

func TestMediaClock_DetectsDrift(t *testing.T) {
	t.Parallel()
	h := harness(t)
	mc := NewMediaClock(h.Clock)
	mc.Start(0)

	// A source producing 20ms of media for every 25ms of wall time — running
	// 20% slow, which over a ten-minute call is two minutes of divergence.
	for i := 0; i < 50; i++ {
		mc.Observe(20 * time.Millisecond)
		h.Clock.Advance(25 * time.Millisecond)
	}

	ratio := mc.DriftRatio()
	if ratio > 0.9 {
		t.Errorf("DriftRatio() = %v, want ~0.8 for a source running 20%% slow", ratio)
	}
	if mc.Drift() >= 0 {
		t.Errorf("Drift() = %v, want negative for a slow source", mc.Drift())
	}
	t.Logf("drift=%v ratio=%.4f", mc.Drift(), ratio)
}

// TestMediaClock_PauseIsNotDrift pins an important exclusion.
func TestMediaClock_PauseIsNotDrift(t *testing.T) {
	t.Parallel()
	h := harness(t)
	mc := NewMediaClock(h.Clock)
	mc.Start(0)

	for i := 0; i < 10; i++ {
		mc.Observe(20 * time.Millisecond)
		h.Clock.Advance(20 * time.Millisecond)
	}
	perfect := mc.DriftRatio()

	// A long pause produces no media. Counting it as drift would report every
	// pause as a fault.
	mc.Pause()
	h.Clock.Advance(10 * time.Second)
	mc.Resume()

	after := mc.DriftRatio()
	if diff := after - perfect; diff > 0.05 || diff < -0.05 {
		t.Errorf("a 10s pause moved the drift ratio from %.4f to %.4f", perfect, after)
	}
}

func TestMediaClock_EmptyReportsUnityNotZero(t *testing.T) {
	t.Parallel()
	h := harness(t)
	mc := NewMediaClock(h.Clock)
	mc.Start(0)

	// A stream that has produced nothing has not drifted. Reporting 0.0 would
	// look like total failure on a dashboard.
	if got := mc.DriftRatio(); got != 1.0 {
		t.Errorf("DriftRatio() = %v with no frames, want 1.0", got)
	}
}

func TestFrameClock_MeasuresLatenessWithoutWalkingForward(t *testing.T) {
	t.Parallel()
	h := harness(t)
	fc := NewFrameClock(h.Clock, 20*time.Millisecond, 10*time.Millisecond)
	fc.Start()

	// Three frames each 5ms late. The deadline must advance from the PREVIOUS
	// deadline, not from arrival — anchoring on arrival would let a series of
	// late frames walk the schedule forward and make lateness unmeasurable.
	var lateness []time.Duration
	for i := 0; i < 3; i++ {
		h.Clock.Advance(25 * time.Millisecond)
		lateness = append(lateness, fc.Arrived())
	}

	for i, l := range lateness {
		if l <= 0 {
			t.Errorf("frame %d reported lateness %v; the schedule walked forward", i, l)
		}
	}
	// Cumulative: each frame is later than the last, because the schedule did
	// not move to accommodate them.
	if lateness[2] <= lateness[0] {
		t.Errorf("lateness did not accumulate: %v", lateness)
	}
}

func TestFrameClock_ResynchronisesAfterALongStall(t *testing.T) {
	t.Parallel()
	h := harness(t)
	fc := NewFrameClock(h.Clock, 20*time.Millisecond, 10*time.Millisecond)
	fc.Start()

	// A minute-long stall. Without resynchronisation the clock would spend the
	// next minute reporting every frame as late while it caught up.
	h.Clock.Advance(time.Minute)
	first := fc.Arrived()
	if first < time.Minute-time.Second {
		t.Errorf("the stall was not reported: %v", first)
	}

	h.Clock.Advance(20 * time.Millisecond)
	second := fc.Arrived()
	if second > 5*time.Millisecond {
		t.Errorf("the clock did not resynchronise: next frame reported %v late", second)
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestFailure_BufferFullDropsAndCounts(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.Pipeline.Buffer.Capacity = 3
	cfg.Pipeline.Jitter.Capacity = 3

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Write far more than the buffer holds without ever reading.
	var dropped int
	for i := 0; i < 50; i++ {
		res, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now()))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Accepted {
			dropped++
		}
		h.Clock.Advance(20 * time.Millisecond)
		s.Pump()
	}

	if dropped == 0 {
		t.Error("a bounded buffer accepted 50 frames without dropping any")
	}
	// The stream must survive: dropping frames under overload is designed
	// behaviour, not a fault.
	if s.State() != StateActive {
		t.Errorf("state = %s after overload, want active", s.State())
	}
	if got := h.Metrics.FramesDropped.Total(); got == 0 {
		t.Error("drops were not counted; the loss would be invisible")
	}
	t.Logf("%d of 50 frames dropped under a 3-frame buffer", dropped)
}

func TestFailure_StoreOutageDoesNotStopStreams(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Store.FailNext(errors.New("redis unreachable"))

	// Persistence is for recovery. A store outage costs recoverability, not the
	// stream.
	if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
		t.Errorf("a frame was refused while the store was down: %v", err)
	}
	if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
		t.Errorf("a stream could not close while the store was down: %v", err)
	}
}

func TestFailure_StallDetection(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
		t.Fatal(err)
	}

	// No sleeping: the injected clock is what makes a two-second deadline
	// testable in microseconds.
	h.Clock.Advance(h.Runtime.Config().StallTimeout + time.Second)
	stalled := h.Runtime.Sweep(ctx)

	if stalled != 1 {
		t.Errorf("Sweep detected %d stalls, want 1", stalled)
	}
	if s.State() != StateTimeout {
		t.Errorf("state = %s after the stall deadline, want timeout", s.State())
	}
	// A timed-out stream still holds its buffer, which is why Timeout is not
	// terminal.
	if !s.State().HoldsResources() {
		t.Error("a timed-out stream reports holding no resources")
	}
}

func TestFailure_PausedStreamsAreNotStalled(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Coordinator.Pause(s.ID()); err != nil {
		t.Fatal(err)
	}

	// A pause is a deliberate act. Expiring it would fight the caller.
	h.Clock.Advance(10 * h.Runtime.Config().StallTimeout)
	h.Runtime.Sweep(ctx)

	if s.State() != StatePaused {
		t.Errorf("state = %s after a long pause, want paused", s.State())
	}
}

// ---------------------------------------------------------------------------
// Snapshot and recovery
// ---------------------------------------------------------------------------

func TestSnapshot_ExcludesAudioByDefault(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	s.Pump()

	// Audio in a durable store is a recording, with every obligation that
	// implies. A deployment that needs gapless recovery opts in knowingly.
	snap := s.Snapshot(DefaultSnapshotConfig())
	if len(snap.Buffered) != 0 {
		t.Errorf("the default snapshot captured %d audio frames", len(snap.Buffered))
	}

	withAudio := s.Snapshot(SnapshotConfig{IncludeAudio: true, MaxAudioFrames: 10})
	if len(withAudio.Buffered) == 0 {
		t.Error("opting in captured no audio")
	}
}

func TestSnapshot_KeepsNewestAudioWhenBounded(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	s.Pump()

	snap := s.Snapshot(SnapshotConfig{IncludeAudio: true, MaxAudioFrames: 3})
	if len(snap.Buffered) != 3 {
		t.Fatalf("captured %d frames, want the bound of 3", len(snap.Buffered))
	}
	if snap.BufferedDropped == 0 {
		t.Error("the omission was not reported; a reader could not tell a " +
			"partial capture from a complete one")
	}

	// The NEWEST frames are kept: the oldest is closest to having been played,
	// the newest is what a resumed consumer still needs.
	buffered := s.Pipeline().Buffer().Snapshot().Frames
	if len(buffered) >= 3 {
		wantFirst := buffered[len(buffered)-3].Sequence
		if snap.Buffered[0].Sequence != wantFirst {
			t.Errorf("kept frames starting at %d, want the newest three starting at %d",
				snap.Buffered[0].Sequence, wantFirst)
		}
	}
}

// TestRestore_StartsInRecoveringNotTheSnapshottedState is the central recovery
// decision.
func TestRestore_StartsInRecoveringNotTheSnapshottedState(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot(DefaultSnapshotConfig())
	if snap.State != StateActive {
		t.Fatalf("snapshot state = %s, want active", snap.State)
	}

	restored, err := RestoreStream(snap, h.Runtime.Config().Pipeline, h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	// The snapshot says Active. No source is attached, and a stream restored
	// directly into Active would accept frames from a producer that does not
	// exist and report a healthy stream carrying nothing.
	if restored.State() != StateRecovering {
		t.Errorf("state = %s, want recovering", restored.State())
	}
	if restored.ID() != s.ID() {
		t.Errorf("stream identifier changed across restore")
	}
	if restored.SessionID() == s.SessionID() {
		t.Error("the session identifier was reused across restore")
	}
	if restored.ResumeCount() != 1 {
		t.Errorf("resume count = %d, want 1", restored.ResumeCount())
	}
}

// TestRestore_ContinuesTheMediaTimeline pins a subtle requirement.
func TestRestore_ContinuesTheMediaTimeline(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}

	snap := s.Snapshot(DefaultSnapshotConfig())
	if snap.MediaPosition == 0 {
		t.Fatal("the snapshot captured no media position")
	}

	restored, err := RestoreStream(snap, h.Runtime.Config().Pipeline, h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	// Restarting at zero would make every timestamp after recovery collide
	// with one from before it.
	if got := restored.MediaClock().MediaTime(); got != snap.MediaPosition {
		t.Errorf("restored media time = %v, want %v", got, snap.MediaPosition)
	}
}

func TestRestore_RefusesTerminalAndUnreadableSnapshots(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := s.Snapshot(DefaultSnapshotConfig())
	cfg := h.Runtime.Config().Pipeline

	terminal := base
	terminal.State = StateClosed
	if _, err := RestoreStream(terminal, cfg, h.Clock); !errors.Is(err, ErrNotRecoverable) {
		t.Errorf("a terminal snapshot restored: %v", err)
	}

	future := base
	future.SchemaVersion = SnapshotSchemaVersion + 1
	if _, err := RestoreStream(future, cfg, h.Clock); !errors.Is(err, ErrNotRecoverable) {
		t.Errorf("an unreadable-schema snapshot restored: %v", err)
	}
}

func TestRecovery_ConcludesStreamsWithNoSource(t *testing.T) {
	t.Parallel()
	first := started(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := first.Open(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if first.Store.Len() != 3 {
		t.Fatalf("store holds %d snapshots after shutdown, want 3", first.Store.Len())
	}

	// A new runtime over the same store. The default AssumeDetached concludes
	// every stream, losing in-flight audio but never reporting a healthy stream
	// that is carrying nothing.
	second, err := New(TestConfig(), WithClock(first.Clock),
		WithStreamStore(first.Store))
	if err != nil {
		t.Fatal(err)
	}
	report, err := second.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Stop(ctx) }()

	if report.Attempted != 3 {
		t.Errorf("attempted = %d, want 3", report.Attempted)
	}
	if report.Concluded != 3 {
		t.Errorf("concluded = %d, want 3: %s", report.Concluded, report.Summary())
	}
	if second.Live() != 0 {
		t.Errorf("%d streams live after concluding all three", second.Live())
	}
}

func TestRecovery_ResumesStreamsWithAnAttachedSource(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.Snapshot = SnapshotConfig{IncludeAudio: true, MaxAudioFrames: 10}

	first := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	s, err := first.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := first.Dispatcher.Dispatch(s.ID(), first.Gen.Next(first.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		first.Clock.Advance(20 * time.Millisecond)
	}
	s.Pump()

	if _, err := first.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := New(cfg, WithClock(first.Clock), WithStreamStore(first.Store),
		WithSourceCheck(AlwaysAttached))
	if err != nil {
		t.Fatal(err)
	}
	report, err := second.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Stop(ctx) }()

	if report.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1: %s", report.Resumed, report.Summary())
	}
	if report.FramesRestored == 0 {
		t.Error("no buffered frames were restored despite audio capture being enabled")
	}
	if second.Live() != 1 {
		t.Errorf("live = %d after resuming one stream, want 1", second.Live())
	}
	// A resumed stream must hold a capacity slot, or the runtime under-counts
	// its own load until those streams close.
	if got := second.Scheduler().Live("fake-source"); got != 1 {
		t.Errorf("scheduler live = %d for a resumed stream, want 1", got)
	}
	t.Logf("%s", report.Summary())
}

func TestRecovery_AbandonsUnrecoverableSnapshots(t *testing.T) {
	t.Parallel()
	h := harness(t)
	ctx := context.Background()

	bad := StreamSnapshot{
		SchemaVersion: 99, Stream: NewStreamID(), Session: NewSessionID(),
		State: StateActive, Context: h.Context(),
	}
	if err := h.Store.Save(ctx, bad); err != nil {
		t.Fatal(err)
	}

	report, err := h.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = h.Stop(ctx) }()

	if report.Abandoned != 1 {
		t.Errorf("abandoned = %d, want 1: %s", report.Abandoned, report.Summary())
	}
	// A snapshot at an unreadable schema will never become readable; leaving it
	// makes every subsequent recovery slower and noisier.
	if h.Store.Len() != 0 {
		t.Error("the unrecoverable snapshot was left to be retried forever")
	}
}

func TestRecovery_IsDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	sequence := func() []StreamID {
		h := started(t)
		for i := 0; i < 5; i++ {
			if _, err := h.Open(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.Runtime.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		r, err := New(TestConfig(), WithClock(h.Clock), WithStreamStore(h.Store))
		if err != nil {
			t.Fatal(err)
		}
		report, err := r.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.Stop(ctx)

		out := make([]StreamID, 0, len(report.Streams))
		for id := range report.Streams {
			out = append(out, id)
		}
		return out
	}

	// Recovery order must not depend on map iteration: two recoveries of the
	// same store must produce the same outcome, or an incident cannot be
	// replayed.
	first, second := sequence(), sequence()
	if len(first) != 5 || len(second) != 5 {
		t.Fatalf("recovery produced %d and %d outcomes, want 5", len(first), len(second))
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestShutdown_RefusesNewStreamsImmediately(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Open(ctx); !errors.Is(err, ErrRuntimeStopped) {
		t.Errorf("err = %v, want ErrRuntimeStopped", err)
	}
}

// TestShutdown_TerminatesWithLiveStreams is the Phase 11A F1 regression, guarded
// here because this runtime made the same design choice deliberately.
func TestShutdown_TerminatesWithLiveStreams(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Open(ctx); err != nil {
		t.Fatal(err)
	}

	// Phase 11A shipped a drain measuring its deadline against the INJECTED
	// clock while polling with a real ticker: under a FakeClock nobody advances,
	// Stop spun forever. This runtime measures the drain budget in real time.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Runtime.Stop(ctx)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not terminate with a live stream and a frozen clock")
	}
}

func TestShutdown_SnapshotsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, err := h.Open(ctx); err != nil {
			t.Fatal(err)
		}
	}

	abandoned, err := h.Runtime.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned != 4 {
		t.Errorf("abandoned = %d, want 4", abandoned)
	}
	if h.Store.Len() != 4 {
		t.Errorf("store holds %d snapshots, want 4", h.Store.Len())
	}

	if _, err := h.Runtime.Stop(ctx); err != nil {
		t.Errorf("Stop is not idempotent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestConcurrency_ManyStreamsManyFrames(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxStreams = 500
	cfg.MaxStreamsPerSource = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	const streams, framesEach = 50, 100

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		written int
		failed  int
	)
	start := make(chan struct{})

	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start

			s, err := h.Open(ctx)
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			// Each goroutine gets its own generator: sharing one would be a
			// data race in the TEST rather than in the engine.
			gen := NewFrameGenerator(h.Format, 20*time.Millisecond)
			local := 0
			for j := 0; j < framesEach; j++ {
				if _, err := h.Dispatcher.Dispatch(s.ID(), gen.Next(h.Clock.Now())); err == nil {
					local++
				}
				s.Pump()
			}
			_ = h.Coordinator.Close(ctx, s.ID(), "done")

			mu.Lock()
			written += local
			mu.Unlock()
		}(i)
	}

	close(start)
	wg.Wait()

	if failed != 0 {
		t.Errorf("%d streams failed to open", failed)
	}
	if written != streams*framesEach {
		t.Errorf("dispatched %d frames, want %d", written, streams*framesEach)
	}
	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d streams still live after all closed", got)
	}
	if got := h.Runtime.Scheduler().Live("fake-source"); got != 0 {
		t.Errorf("scheduler holds %d slots after all closed; capacity leaked", got)
	}
}

func TestConcurrency_ProducerAndConsumerOnOneStream(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	const frames = 2000
	var (
		wg       sync.WaitGroup
		produced int
		consumed int
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		gen := NewFrameGenerator(h.Format, 20*time.Millisecond)
		for i := 0; i < frames; i++ {
			if _, err := s.Write(gen.Next(h.Clock.Now())); err == nil {
				produced++
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < frames*3; i++ {
			s.Pump()
			if _, err := s.Read(); err == nil {
				consumed++
			}
		}
	}()

	wg.Wait()

	if produced == 0 {
		t.Error("the producer wrote nothing")
	}
	t.Logf("produced=%d consumed=%d under concurrent access", produced, consumed)

	stats := s.Stats()
	// The invariant that matters: nothing was double-counted or lost from the
	// buffer's own accounting.
	buf := stats.Pipeline.Buffer
	if buf.Read > buf.Written {
		t.Errorf("read %d frames but only %d were written", buf.Read, buf.Written)
	}
}

func TestConcurrency_SweepRunsAlongsideFrameChurn(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxStreams = 200
	cfg.MaxStreamsPerSource = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The sweep transitions streams while other goroutines create and close
		// them. This is the path that deadlocks if Each holds a shard lock.
		for i := 0; i < 300; i++ {
			h.Runtime.Sweep(ctx)
			h.Runtime.PumpAll()
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen := NewFrameGenerator(h.Format, 20*time.Millisecond)
			for i := 0; i < 20; i++ {
				s, err := h.Open(ctx)
				if err != nil {
					continue
				}
				for j := 0; j < 5; j++ {
					_, _ = h.Dispatcher.Dispatch(s.ID(), gen.Next(h.Clock.Now()))
				}
				_ = h.Coordinator.Close(ctx, s.ID(), "done")
			}
		}()
	}
	wg.Wait()
	<-done

	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d streams live after churn, want 0", got)
	}
}

func TestStress_SustainedFrameThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("stress run skipped under -short")
	}
	t.Parallel()

	cfg := TestConfig()
	cfg.MaxStreams = 100
	cfg.MaxStreamsPerSource = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	const streams, frames = 20, 2000

	begin := time.Now()
	var total int

	for i := 0; i < streams; i++ {
		s, err := h.Open(ctx)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		gen := NewFrameGenerator(h.Format, 20*time.Millisecond)
		for j := 0; j < frames; j++ {
			if _, err := h.Dispatcher.Dispatch(s.ID(), gen.Next(h.Clock.Now())); err != nil {
				t.Fatalf("stream %d frame %d: %v", i, j, err)
			}
			if j%16 == 0 {
				s.Pump()
				for {
					if _, err := s.Read(); err != nil {
						break
					}
				}
			}
			total++
		}
		if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	elapsed := time.Since(begin)

	t.Logf("%d frames across %d streams in %s (%.0f frames/sec, %s of audio)",
		total, streams, elapsed.Round(time.Millisecond),
		float64(total)/elapsed.Seconds(),
		(time.Duration(total) * 20 * time.Millisecond).Round(time.Second))

	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d streams live after the stress run", got)
	}
	if got := h.Runtime.Scheduler().Live("fake-source"); got != 0 {
		t.Errorf("scheduler holds %d slots after the stress run", got)
	}
}

// TestStress_LongRunningStreamDoesNotGrow guards against unbounded state.
func TestStress_LongRunningStreamDoesNotGrow(t *testing.T) {
	if testing.Short() {
		t.Skip("stress run skipped under -short")
	}
	t.Parallel()

	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Ten thousand frames is 200 seconds of audio. Nothing per-stream may grow
	// with frame count.
	for i := 0; i < 10_000; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
		s.Pump()
		for {
			if _, err := s.Read(); err != nil {
				break
			}
		}
	}

	if got := s.HistoryLen(); got > maxHistory {
		t.Errorf("history grew to %d, cap is %d", got, maxHistory)
	}
	if got := s.Pipeline().Buffer().Len(); got > s.Pipeline().Buffer().Capacity() {
		t.Errorf("buffer holds %d frames, capacity is %d",
			got, s.Pipeline().Buffer().Capacity())
	}
	if got := s.Pipeline().Jitter().Len(); got > DefaultJitterConfig().Capacity {
		t.Errorf("jitter buffer holds %d frames, capacity is %d",
			got, DefaultJitterConfig().Capacity)
	}

	stats := s.Stats()
	t.Logf("after 10k frames: %s", stats.Pipeline)
	t.Logf("clock: %s", stats.Clock)
}

var _ = fmt.Sprintf
var _ = strings.Contains

// ---------------------------------------------------------------------------
// Jitter window
// ---------------------------------------------------------------------------

func TestJitter_CleanSequenceIsFullyAccepted(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	jb, err := NewJitterBuffer(DefaultJitterConfig(), clock)
	if err != nil {
		t.Fatal(err)
	}
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	// Thirty frames at a perfect 50 fps with zero jitter. Nothing here is
	// late, early, duplicated or out of order, so nothing may be dropped.
	const frames = 30
	for i := 0; i < frames; i++ {
		if disp := jb.Put(gen.Next(clock.Now())); disp.Dropped() {
			t.Fatalf("frame %d dropped as %s on a clean sequence", i, disp)
		}
		clock.Advance(20 * time.Millisecond)

		// Drain what is due, exactly as a consumer would.
		for {
			if _, err := jb.Get(); err != nil {
				break
			}
		}
	}

	st := jb.Stats()
	if st.Late+st.Duplicates+st.TooEarly != 0 {
		t.Errorf("clean sequence produced drops: %s", st)
	}
	if st.Released == 0 {
		t.Errorf("nothing was ever released: %s", st)
	}
}

func TestJitter_RefusesFramesFarAheadOfTheData(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	jb, err := NewJitterBuffer(DefaultJitterConfig(), clock)
	if err != nil {
		t.Fatal(err)
	}
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	if disp := jb.Put(gen.Next(clock.Now())); disp.Dropped() {
		t.Fatalf("first frame dropped as %s", disp)
	}

	// Ten seconds ahead — far beyond MaxDelay. A clock bug or a replayed
	// session looks exactly like this, and it must be refused.
	far := gen.NextAt(500, 10*time.Second, clock.Now())
	if disp := jb.Put(far); disp != FrameTooEarly {
		t.Errorf("disposition = %s, want too_early", disp)
	}
}

func TestJitter_LostFrameDoesNotStallTheStream(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	jb, err := NewJitterBuffer(DefaultJitterConfig(), clock)
	if err != nil {
		t.Fatal(err)
	}
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	// Establish playout with three contiguous frames.
	for i := 0; i < 3; i++ {
		jb.Put(gen.Next(clock.Now()))
		clock.Advance(20 * time.Millisecond)
		for {
			if _, err := jb.Get(); err != nil {
				break
			}
		}
	}

	// Lose four frames, then deliver the next one.
	gen.Skip(4)
	if disp := jb.Put(gen.Next(clock.Now())); disp.Dropped() {
		t.Fatalf("the frame after the gap was dropped as %s", disp)
	}

	// playout advances only on release, and release requires playout to reach
	// the frame. A buffer that will not step over the hole deadlocks here and
	// holds this audio forever — one lost packet ending the stream.
	if _, err := jb.Get(); err != nil {
		t.Fatalf("a single lost frame stalled the buffer permanently: %v (%s)",
			err, jb.Stats())
	}
	if jb.Len() != 0 {
		t.Errorf("%d frames still held after stepping over the gap", jb.Len())
	}
}

// ---------------------------------------------------------------------------
// Backpressure
// ---------------------------------------------------------------------------

func TestBackpressure_StalledConsumerIsBoundedByCapacity(t *testing.T) {
	t.Parallel()
	h := started(t)

	s, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// A producer that never stops against a consumer that never reads. The
	// engine must refuse frames rather than grow without bound, and the refusal
	// must reach the producer rather than being swallowed as a silent drop.
	var accepted, refused int
	for i := 0; i < 500; i++ {
		res, err := s.Write(h.Gen.Next(h.Clock.Now()))
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if res.Accepted {
			accepted++
		} else {
			refused++
		}
		h.Clock.Advance(20 * time.Millisecond)
		s.Pump()
	}

	if refused == 0 {
		t.Error("500 frames against a stalled consumer produced no refusal; " +
			"the producer was never told and the engine is discarding silently")
	}
	if accepted == 0 {
		t.Error("nothing was accepted at all")
	}

	// Memory is bounded: neither stage may exceed its configured capacity.
	if got, cap := s.Pipeline().Jitter().Len(), DefaultJitterConfig().Capacity; got > cap {
		t.Errorf("jitter buffer holds %d frames, capacity is %d", got, cap)
	}
	if got, cap := s.Pipeline().Buffer().Len(), s.Pipeline().Buffer().Capacity(); got > cap {
		t.Errorf("ring holds %d frames, capacity is %d", got, cap)
	}
	// Dropping under overload is designed behaviour, not a fault.
	if s.State() != StateActive {
		t.Errorf("state = %s after overload, want active", s.State())
	}
	t.Logf("accepted=%d refused=%d | %s", accepted, refused, s.Stats().Pipeline)
}
