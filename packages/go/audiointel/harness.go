package audiointel

import (
	"context"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// TB is the slice of *testing.T this harness needs.
//
// DECLARED RATHER THAN IMPORTING "testing". No frozen phase's harness imports
// the testing package into a production file, and for good reason: it is
// compiled into every binary that links this module, it registers flags the
// moment anything calls testing.Init, and it invites production code to grow a
// dependency on test scaffolding.
//
// An interface with two methods costs nothing, is satisfied by *testing.T and
// *testing.B without any adapter, and keeps this module's dependency closure
// the standard library plus three first-party packages.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// TestConfig returns a configuration suited to tests.
//
// EXPORTED BECAUSE THE ALTERNATIVE IS A FOOTGUN, following the convention every
// phase since 10A has established. A service embedding this engine needs to
// test its own code against it, and forcing every consumer to rebuild this
// scaffolding is how six subtly different fakes come to exist.
//
// Identical to [DefaultConfig] apart from the session bound, which is lowered
// so a capacity test does not have to open a thousand sessions to reach it.
func TestConfig(format media.AudioFormat) Config {
	cfg := DefaultConfig(format)
	cfg.MaxSessions = 8
	return cfg
}

// DetectorRig wires the analysis stages for one direction of audio and drives
// frames through them.
//
// The composition every detector test needs: frames in, decisions out, with the
// one-frame speech gate between the detector and the noise floor wired
// correctly. Assembling it by hand in each test is how two tests come to
// disagree about the ordering.
type DetectorRig struct {
	Config Config
	Frames *FrameAnalyzer
	Signal *SignalAnalyzer
	VAD    *SpeechDetector
	Clock  *rt.FakeClock

	// Last is the most recent decision, and the source of the speech gate's
	// one-frame lag.
	Last VADDecision
}

// NewDetectorRig builds a rig for one configuration.
func NewDetectorRig(tb TB, cfg Config) *DetectorRig {
	tb.Helper()

	frames, err := NewFrameAnalyzer(cfg.Format)
	if err != nil {
		tb.Fatalf("NewFrameAnalyzer: %v", err)
	}
	signal, err := NewSignalAnalyzer(cfg)
	if err != nil {
		tb.Fatalf("NewSignalAnalyzer: %v", err)
	}
	clock := rt.NewFakeClock(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))
	vad, err := NewSpeechDetector(cfg, clock)
	if err != nil {
		tb.Fatalf("NewSpeechDetector: %v", err)
	}

	return &DetectorRig{Config: cfg, Frames: frames, Signal: signal, VAD: vad, Clock: clock}
}

// Push drives one frame through the whole analysis chain.
func (r *DetectorRig) Push(tb TB, f media.Frame) VADDecision {
	tb.Helper()
	_, d := r.PushView(tb, f)
	return d
}

// PushView drives one frame and returns the signal view alongside the decision.
//
// THE ONE PLACE THE CHAIN IS ASSEMBLED. Every rig in the test suite goes
// through here rather than repeating the sequence, because the ordering has
// three subtleties — the gate's one-frame lag, the onset retraction, and the
// fact that the noise floor updates before the window — and a rig that
// reimplemented it would get one of them wrong and test something else. One of
// them did, which is why this exists.
func (r *DetectorRig) PushView(tb TB, f media.Frame) (SignalView, VADDecision) {
	tb.Helper()

	features, err := r.Frames.Analyze(f)
	if err != nil {
		tb.Fatalf("analysing frame %d: %v", f.Sequence, err)
	}

	// The gate reads the PREVIOUS frame's verdict — see SignalAnalyzer.
	view := r.Signal.Observe(features, r.Last.SpeechActive())
	r.Last = r.VAD.Observe(view)

	// The frames between where the utterance began and where it was confirmed
	// reached the noise estimator labelled as background. They were not.
	if r.Last.OnsetConfirmed {
		r.Signal.RetractOnsetLeak()
	}

	return view, r.Last
}

// PushAll drives a sequence of frames and returns every decision.
func (r *DetectorRig) PushAll(tb TB, frames []media.Frame) []VADDecision {
	tb.Helper()

	out := make([]VADDecision, 0, len(frames))
	for _, f := range frames {
		out = append(out, r.Push(tb, f))
	}
	return out
}

// StateRun is one uninterrupted stretch of a single voice activity state.
type StateRun struct {
	State  VADState
	Frames int
	// First is the index of the first decision in the run.
	First int
}

// StateRuns collapses a decision sequence into its state runs.
//
// What a test usually wants to assert about: "silence, then speech, then
// silence" is the claim, and comparing frame-by-frame states makes a test that
// breaks whenever a threshold moves by a frame.
func StateRuns(decisions []VADDecision) []StateRun {
	var runs []StateRun
	for i, d := range decisions {
		if n := len(runs); n > 0 && runs[n-1].State == d.State {
			runs[n-1].Frames++
			continue
		}
		runs = append(runs, StateRun{State: d.State, Frames: 1, First: i})
	}
	return runs
}

// StateSequence returns the states a decision sequence passed through, with
// consecutive repeats collapsed.
func StateSequence(decisions []VADDecision) []VADState {
	runs := StateRuns(decisions)
	out := make([]VADState, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.State)
	}
	return out
}

// CountOnsets returns how many speech runs began.
func CountOnsets(decisions []VADDecision) int {
	var n int
	for _, d := range decisions {
		if d.OnsetConfirmed {
			n++
		}
	}
	return n
}

// CountOffsets returns how many speech runs ended.
func CountOffsets(decisions []VADDecision) int {
	var n int
	for _, d := range decisions {
		if d.OffsetConfirmed {
			n++
		}
	}
	return n
}

// FirstOnset returns the decision that opened the first speech run.
func FirstOnset(decisions []VADDecision) (VADDecision, int, bool) {
	for i, d := range decisions {
		if d.OnsetConfirmed {
			return d, i, true
		}
	}
	return VADDecision{}, -1, false
}

// FirstOffset returns the decision that closed the first speech run.
func FirstOffset(decisions []VADDecision) (VADDecision, int, bool) {
	for i, d := range decisions {
		if d.OffsetConfirmed {
			return d, i, true
		}
	}
	return VADDecision{}, -1, false
}

// WarmupFrames returns silence long enough to converge the noise floor.
//
// Every detector test needs this prefix, because until the floor converges the
// detector refuses to assert anything — which is the correct behaviour and a
// tedious thing to open each test with.
func WarmupFrames(g *SignalGenerator, cfg Config) []media.Frame {
	return g.Silence(cfg.Noise.WarmupFrames + 1)
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// RecordingSpeechController captures what the engine asked Phase 11C to do.
//
// EXPORTED, following the same reasoning as [DetectorRig]: a service wiring
// this engine to a real speech session needs to test its own wiring, and every
// consumer rebuilding this fake is how six subtly different ones come to exist.
//
// Safe for concurrent use, because a session isolation test drives several at
// once.
type RecordingSpeechController struct {
	mu sync.Mutex

	interrupts []string
	endpoints  int

	// InterruptErr, when set, is returned by every Interrupt. Models Phase 11C
	// refusing because the turn has already moved on, which is a legitimate
	// outcome rather than a fault.
	InterruptErr error

	// EndOfSpeechErr, when set, is returned by every EndOfSpeech.
	EndOfSpeechErr error

	// Delay, when set, is how long each call blocks. For proving that a slow
	// controller shows up in the measured barge-in latency rather than being
	// hidden by it.
	Delay time.Duration
}

// Interrupt records the request.
func (c *RecordingSpeechController) Interrupt(_ context.Context, reason string) error {
	if c.Delay > 0 {
		time.Sleep(c.Delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupts = append(c.interrupts, reason)
	return c.InterruptErr
}

// EndOfSpeech records the request.
func (c *RecordingSpeechController) EndOfSpeech(context.Context) error {
	if c.Delay > 0 {
		time.Sleep(c.Delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoints++
	return c.EndOfSpeechErr
}

// Interrupts returns the reasons passed to Interrupt, in order.
func (c *RecordingSpeechController) Interrupts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.interrupts...)
}

// InterruptCount returns how many interruptions were delivered.
func (c *RecordingSpeechController) InterruptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.interrupts)
}

// EndOfSpeechCount returns how many endpoints were delivered.
func (c *RecordingSpeechController) EndOfSpeechCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endpoints
}

// Reset clears the recording.
func (c *RecordingSpeechController) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupts = nil
	c.endpoints = 0
}

// StaticEnvelope is an [OutboundEnvelope] returning one level whenever the
// agent is nominally speaking.
//
// For the echo-correlation path: a CONSTANT outbound level produces no variance
// and therefore no correlation, which is the honest result — see [pearson].
// Use [ModulatedEnvelope] to model an envelope the inbound signal can track.
type StaticEnvelope struct{ Level float64 }

// LevelAt returns the fixed level.
func (e StaticEnvelope) LevelAt(time.Duration) (float64, bool) { return e.Level, true }

// UnknownEnvelope reports that the outbound level is not instrumented.
type UnknownEnvelope struct{}

// LevelAt reports nothing known.
func (UnknownEnvelope) LevelAt(time.Duration) (float64, bool) { return 0, false }

// ModulatedEnvelope is an [OutboundEnvelope] whose level varies with media time,
// so a correlation against it is measurable.
type ModulatedEnvelope struct {
	// Base is the mean level.
	Base float64
	// Depth is how far it swings either side of Base.
	Depth float64
	// Period is one full cycle of the swing.
	Period time.Duration
}

// LevelAt returns the modulated level.
func (e ModulatedEnvelope) LevelAt(at time.Duration) (float64, bool) {
	if e.Period <= 0 {
		return e.Base, true
	}
	phase := 2 * math.Pi * float64(at%e.Period) / float64(e.Period)
	return e.Base + e.Depth*math.Sin(phase), true
}

// ---------------------------------------------------------------------------
// Runtime harness
// ---------------------------------------------------------------------------

// Harness wires a runtime with fakes for testing.
//
// EXPORTED ON PURPOSE, following the convention every phase since 10A has
// established: a service embedding this engine needs to test its own code
// against it, and forcing every consumer to rebuild this scaffolding is how six
// subtly different fakes come to exist.
type Harness struct {
	Runtime    *AudioIntelligenceRuntime
	Clock      *rt.FakeClock
	Metrics    *AudioIntelligenceMetrics
	Events     *RecordingEventPublisher
	Controller *RecordingSpeechController
	Config     Config

	// Gen produces well-formed frames for the harness format.
	Gen *SignalGenerator
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg     Config
	haveCfg bool
	format  media.AudioFormat
	logger  *slog.Logger
}

// WithHarnessConfig overrides the configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg, o.haveCfg = c, true }
}

// WithHarnessFormat sets the audio format.
func WithHarnessFormat(f media.AudioFormat) HarnessOption {
	return func(o *harnessOptions) { o.format = f }
}

// WithHarnessLogger attaches a logger, for debugging a failing test.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// NewHarness builds a runtime with a fake clock, a recording event publisher
// and a recording speech controller.
func NewHarness(tb TB, opts ...HarnessOption) *Harness {
	tb.Helper()

	o := harnessOptions{
		format: media.PCM16Mono8k(),
		// Discard by default: a test that fails should fail on an assertion, not
		// be found in a wall of log output.
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, apply := range opts {
		apply(&o)
	}
	if !o.haveCfg {
		o.cfg = TestConfig(o.format)
	}

	clock := rt.NewFakeClock(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))
	metrics := NewAudioIntelligenceMetrics()
	events := NewRecordingEventPublisher()

	runtime, err := New(o.cfg,
		WithClock(clock),
		WithLogger(o.logger),
		WithMetrics(metrics),
		WithEventPublisher(events),
	)
	if err != nil {
		tb.Fatalf("building the runtime: %v", err)
	}

	gen := NewSignalGenerator(o.cfg.Format, o.cfg.FrameInterval)
	gen.SetArrival(clock.Now())

	return &Harness{
		Runtime:    runtime,
		Clock:      clock,
		Metrics:    metrics,
		Events:     events,
		Controller: &RecordingSpeechController{},
		Config:     o.cfg,
		Gen:        gen,
	}
}

// OpenInbound opens a session for caller audio.
func (h *Harness) OpenInbound(tb TB) *Session {
	tb.Helper()

	s, err := h.Runtime.Open(context.Background(), SessionContext{
		Call:      "call-test",
		Direction: DirectionInbound,
		Format:    h.Config.Format,
	})
	if err != nil {
		tb.Fatalf("opening a session: %v", err)
	}
	return s
}

// Drive pushes frames through a session with a fixed conversation state.
func (h *Harness) Drive(
	tb TB, s *Session, frames []media.Frame, state ConversationState,
) []Analysis {
	tb.Helper()

	out := make([]Analysis, 0, len(frames))
	for _, f := range frames {
		a, err := s.Analyze(context.Background(), f, state, h.Controller, nil)
		if err != nil {
			tb.Fatalf("analysing frame %d: %v", f.Sequence, err)
		}
		out = append(out, a)
	}
	return out
}

// Warmup drives enough silence to converge the noise floor.
func (h *Harness) Warmup(tb TB, s *Session) {
	tb.Helper()
	h.Drive(tb, s, WarmupFrames(h.Gen, h.Config), ConversationState{})
}
