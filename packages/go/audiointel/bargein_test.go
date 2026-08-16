package audiointel

import (
	"context"
	"errors"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// bargeInRig drives frames through the chain with an interruption detector
// attached.
type bargeInRig struct {
	*DetectorRig
	bargeIn    *BargeInDetector
	controller *RecordingSpeechController

	// AgentSpeaking is what the session would tell the detector.
	AgentSpeaking bool
}

func newBargeInRig(t *testing.T, cfg Config, clock rt.Clock) *bargeInRig {
	t.Helper()

	rig := NewDetectorRig(t, cfg)
	if clock == nil {
		clock = rig.Clock
	}
	b, err := NewBargeInDetector(cfg, clock)
	if err != nil {
		t.Fatalf("NewBargeInDetector: %v", err)
	}
	return &bargeInRig{
		DetectorRig: rig,
		bargeIn:     b,
		controller:  &RecordingSpeechController{},
	}
}

func (r *bargeInRig) push(t *testing.T, f media.Frame) BargeInDecision {
	t.Helper()

	view, d := r.PushView(t, f)
	return r.bargeIn.Observe(context.Background(), view, d, r.AgentSpeaking, r.controller)
}

func (r *bargeInRig) pushAll(t *testing.T, frames []media.Frame) []BargeInDecision {
	t.Helper()
	out := make([]BargeInDecision, 0, len(frames))
	for _, f := range frames {
		out = append(out, r.push(t, f))
	}
	return out
}

func detections(decisions []BargeInDecision) []BargeInDecision {
	var out []BargeInDecision
	for _, d := range decisions {
		if d.Detected {
			out = append(out, d)
		}
	}
	return out
}

// TestBargeIn_CancelsAgentSpeechThroughThePhase11CPort is the §8 scenario.
func TestBargeIn_CancelsAgentSpeechThroughThePhase11CPort(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newBargeInRig(t, cfg, nil)

	// Warm up while the line is quiet and the agent is not yet talking.
	rig.pushAll(t, WarmupFrames(g, cfg))

	// The agent starts speaking, then the caller interrupts.
	rig.AgentSpeaking = true
	decisions := rig.pushAll(t, g.NormalSpeech(20))

	fired := detections(decisions)
	if len(fired) != 1 {
		t.Fatalf("%d detections, want exactly 1", len(fired))
	}
	if fired[0].Outcome != BargeInDelivered {
		t.Fatalf("outcome = %s, want %s (err: %v)",
			fired[0].Outcome, BargeInDelivered, fired[0].Err)
	}

	if n := rig.controller.InterruptCount(); n != 1 {
		t.Errorf("the speech controller received %d interruptions, want 1", n)
	}
	if got := rig.controller.Interrupts(); len(got) == 1 && got[0] != ReasonCallerSpoke {
		t.Errorf("reason = %q, want %q", got[0], ReasonCallerSpoke)
	}
	if !rig.bargeIn.Active() {
		t.Error("the detector does not report the interruption as active")
	}
}

// TestBargeIn_RefusesWhenTheAgentIsNotSpeaking mirrors Phase 11C.
//
// speech.SpeechSession.Interrupt refuses unless the turn is responding or
// speaking, because a caller talking while we are listening is not
// interrupting — they are just talking, and their audio already belongs to the
// live turn. Firing anyway would cancel a turn that was recognising speech
// perfectly well and throw away the transcript in progress.
func TestBargeIn_RefusesWhenTheAgentIsNotSpeaking(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newBargeInRig(t, cfg, nil)
	rig.AgentSpeaking = false

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)
	decisions := rig.pushAll(t, frames)

	fired := detections(decisions)
	if len(fired) != 1 {
		t.Fatalf("%d detections, want 1 — the detection still HAPPENED and must "+
			"be counted even though it was not delivered", len(fired))
	}
	if fired[0].Outcome != BargeInNotSpeaking {
		t.Errorf("outcome = %s, want %s", fired[0].Outcome, BargeInNotSpeaking)
	}
	if n := rig.controller.InterruptCount(); n != 0 {
		t.Errorf("the speech controller received %d interruptions, want 0", n)
	}
}

// TestBargeIn_DebouncesRepeatDetections is §8's no-duplicate-interruptions
// requirement.
func TestBargeIn_DebouncesRepeatDetections(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	clock := rt.NewFakeClock(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))

	g := newGen()
	rig := newBargeInRig(t, cfg, clock)
	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	// Three separate utterances, each opening a new speech run, all inside the
	// debounce interval — the clock does not move.
	var all []BargeInDecision
	for i := 0; i < 3; i++ {
		all = append(all, rig.pushAll(t, g.NormalSpeech(15))...)
		all = append(all, rig.pushAll(t, g.Silence(30))...)
	}

	fired := detections(all)
	if len(fired) < 2 {
		t.Fatalf("%d detections across three utterances, want at least 2 so the "+
			"debounce has something to suppress", len(fired))
	}

	var delivered, debounced int
	for _, d := range fired {
		switch d.Outcome {
		case BargeInDelivered:
			delivered++
		case BargeInDebounced:
			debounced++
		}
	}

	if delivered != 1 {
		t.Errorf("%d interruptions delivered inside the %s debounce, want 1",
			delivered, cfg.BargeIn.MinInterval)
	}
	if debounced == 0 {
		t.Error("no detection was debounced")
	}
	if n := rig.controller.InterruptCount(); n != 1 {
		t.Errorf("the speech controller received %d interruptions, want 1", n)
	}

	// Past the debounce interval, the next interruption is delivered again.
	clock.Advance(cfg.BargeIn.MinInterval * 2)
	next := detections(rig.pushAll(t, g.NormalSpeech(15)))
	if len(next) != 1 || next[0].Outcome != BargeInDelivered {
		t.Errorf("after the debounce elapsed, got %v, want one delivered", next)
	}
}

// TestBargeIn_DiscardsStaleDetections guards the worse-than-missing case.
//
// Cancelling speech the agent finished half a second ago cuts off whatever it
// started next, and to the caller that is the agent interrupting itself.
func TestBargeIn_DiscardsStaleDetections(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	start := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	clock := rt.NewFakeClock(start)

	g := newGen()
	g.SetArrival(start)

	rig := newBargeInRig(t, cfg, clock)
	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	// The audio is stamped as having arrived long ago.
	clock.Advance(cfg.BargeIn.MaxAge * 10)

	decisions := rig.pushAll(t, g.NormalSpeech(20))
	fired := detections(decisions)

	if len(fired) != 1 {
		t.Fatalf("%d detections, want 1", len(fired))
	}
	if fired[0].Outcome != BargeInStale {
		t.Errorf("outcome = %s, want %s", fired[0].Outcome, BargeInStale)
	}
	if n := rig.controller.InterruptCount(); n != 0 {
		t.Errorf("a stale detection reached the speech controller %d times", n)
	}
}

// TestBargeIn_CountsADetectionWithNowhereToSendIt guards the configuration
// fault that otherwise looks healthy.
func TestBargeIn_CountsADetectionWithNowhereToSendIt(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()

	rig := NewDetectorRig(t, cfg)
	b, err := NewBargeInDetector(cfg, rig.Clock)
	if err != nil {
		t.Fatal(err)
	}

	var last BargeInDecision
	drive := func(frames []media.Frame) {
		for _, f := range frames {
			features, err := rig.Frames.Analyze(f)
			if err != nil {
				t.Fatal(err)
			}
			view := rig.Signal.Observe(features, rig.Last.SpeechActive())
			rig.Last = rig.VAD.Observe(view)
			// NO CONTROLLER WIRED.
			if d := b.Observe(context.Background(), view, rig.Last, true, nil); d.Detected {
				last = d
			}
		}
	}

	drive(WarmupFrames(g, cfg))
	drive(g.NormalSpeech(20))

	if !last.Detected {
		t.Fatal("no detection was reported")
	}
	if last.Outcome != BargeInNoController {
		t.Errorf("outcome = %s, want %s", last.Outcome, BargeInNoController)
	}
	if !errors.Is(last.Err, ErrNoSpeechController) {
		t.Errorf("err = %v, want ErrNoSpeechController", last.Err)
	}
}

// TestBargeIn_RecordsAControllerRefusal covers Phase 11C legitimately saying no.
func TestBargeIn_RecordsAControllerRefusal(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newBargeInRig(t, cfg, nil)

	refusal := errors.New("turn is finalizing; there is no agent speech to interrupt")
	rig.controller.InterruptErr = refusal

	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true
	fired := detections(rig.pushAll(t, g.NormalSpeech(20)))

	if len(fired) != 1 {
		t.Fatalf("%d detections, want 1", len(fired))
	}
	if fired[0].Outcome != BargeInRefused {
		t.Errorf("outcome = %s, want %s", fired[0].Outcome, BargeInRefused)
	}
	if !errors.Is(fired[0].Err, refusal) {
		t.Errorf("err = %v, want the controller's refusal", fired[0].Err)
	}
	// A refusal must NOT arm the debounce: nothing was cancelled, so a later
	// genuine interruption must still get through.
	if rig.bargeIn.Active() {
		t.Error("a refused interruption was recorded as active")
	}
}

// TestBargeIn_DisabledPolicyStillCounts proves switching the feature off does
// not make it invisible.
func TestBargeIn_DisabledPolicyStillCounts(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.BargeIn.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	g := newGen()
	rig := newBargeInRig(t, cfg, nil)
	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	fired := detections(rig.pushAll(t, g.NormalSpeech(20)))
	if len(fired) != 1 {
		t.Fatalf("%d detections with barge-in disabled, want 1 — the detection "+
			"still happened and an operator needs to see how often", len(fired))
	}
	if fired[0].Outcome != BargeInDisabled {
		t.Errorf("outcome = %s, want %s", fired[0].Outcome, BargeInDisabled)
	}
	if n := rig.controller.InterruptCount(); n != 0 {
		t.Errorf("a disabled policy delivered %d interruptions", n)
	}
}

// TestBargeIn_ConfirmFramesDelaysTheDetection covers the tuning knob and the
// cost it carries.
func TestBargeIn_ConfirmFramesDelaysTheDetection(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.BargeIn.ConfirmFrames = 3
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	g := newGen()
	rig := newBargeInRig(t, cfg, nil)
	warmup := WarmupFrames(g, cfg)
	rig.pushAll(t, warmup)
	rig.AgentSpeaking = true

	decisions := rig.pushAll(t, g.NormalSpeech(20))

	firedAt := -1
	for i, d := range decisions {
		if d.Detected {
			firedAt = i
			break
		}
	}
	if firedAt < 0 {
		t.Fatal("no detection with ConfirmFrames set")
	}

	// The onset confirms at MinOnsetFrames; the extra confirmation adds
	// ConfirmFrames more.
	wantAt := cfg.VAD.MinOnsetFrames - 1 + cfg.BargeIn.ConfirmFrames
	if firedAt != wantAt {
		t.Errorf("detection at frame %d, want %d (%d onset + %d confirm)",
			firedAt, wantAt, cfg.VAD.MinOnsetFrames, cfg.BargeIn.ConfirmFrames)
	}
	t.Logf("ConfirmFrames=%d delayed the detection by %s against a %s budget",
		cfg.BargeIn.ConfirmFrames,
		time.Duration(cfg.BargeIn.ConfirmFrames)*cfg.FrameInterval, BargeInBudget)
}

// TestBargeIn_LatencyIsWithinTheFrozenBudget measures ADR-0004 §12.
//
// # On the real clock, deliberately
//
// Every other test here injects a FakeClock, and a latency claim measured on a
// fake clock asserts nothing at all. This one runs on the system clock, exactly
// as Phase 11C's equivalent test does.
//
// Read the result precisely. It measures the ORCHESTRATION cost of an
// interruption — the detection stamp, the policy checks, and the call through
// the port — with a controller that returns immediately. It does NOT measure
// end-to-end barge-in, which additionally includes Phase 11C's cancellation,
// the media relay and the carrier leg, none of which this package implements.
func TestBargeIn_LatencyIsWithinTheFrozenBudget(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	var worst time.Duration
	const runs = 10

	for i := 0; i < runs; i++ {
		g := newGen()
		g.SetArrival(time.Now())

		// The SYSTEM clock: a wall-clock claim needs a wall clock.
		rig := newBargeInRig(t, cfg, rt.SystemClock{})
		rig.pushAll(t, WarmupFrames(g, cfg))
		rig.AgentSpeaking = true

		for _, d := range detections(rig.pushAll(t, g.NormalSpeech(20))) {
			if d.Outcome != BargeInDelivered {
				t.Fatalf("run %d: outcome %s, want %s", i, d.Outcome, BargeInDelivered)
			}
			if d.Latency > worst {
				worst = d.Latency
			}
		}
	}

	if worst > BargeInBudget {
		t.Errorf("worst orchestration latency across %d runs was %s, above the "+
			"ADR-0004 §12 budget of %s", runs, worst, BargeInBudget)
	}
	t.Logf("worst barge-in orchestration latency across %d runs: %s "+
		"(ADR-0004 §12 budget: %s). This is orchestration only — Phase 11C's "+
		"cancellation, the media relay and the carrier leg are not included.",
		runs, worst, BargeInBudget)
}

// TestBargeIn_ReportsOnsetLatencySeparately keeps the two halves of the budget
// from being conflated.
func TestBargeIn_ReportsOnsetLatencySeparately(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newBargeInRig(t, cfg, nil)

	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true
	fired := detections(rig.pushAll(t, g.NormalSpeech(20)))

	if len(fired) != 1 {
		t.Fatalf("%d detections, want 1", len(fired))
	}

	// The onset latency is the media time from where speech began to the end of
	// the confirming frame: exactly MinOnsetFrames of audio.
	want := time.Duration(cfg.VAD.MinOnsetFrames) * cfg.FrameInterval
	if fired[0].OnsetLatency != want {
		t.Errorf("OnsetLatency = %s, want %s (%d confirmation frames)",
			fired[0].OnsetLatency, want, cfg.VAD.MinOnsetFrames)
	}
}

// TestBargeIn_ResolvesWhenTheCallerStops proves the active flag clears, so the
// endpoint gate does not suppress forever.
func TestBargeIn_ResolvesWhenTheCallerStops(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newBargeInRig(t, cfg, nil)

	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true
	rig.pushAll(t, g.NormalSpeech(20))

	if !rig.bargeIn.Active() {
		t.Fatal("setup: the interruption is not active")
	}

	rig.AgentSpeaking = false
	rig.pushAll(t, g.Silence(40))

	if rig.bargeIn.Active() {
		t.Error("the interruption is still active after the caller stopped; the " +
			"endpoint gate would suppress every endpoint for the rest of the call")
	}
}

func TestNewBargeInDetector_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.BargeIn.MinInterval = 0
	if _, err := NewBargeInDetector(cfg, nil); err == nil {
		t.Error("a zero debounce was accepted while barge-in is enabled")
	}
}
