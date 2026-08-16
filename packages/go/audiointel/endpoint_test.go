package audiointel

import (
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// endpointRig drives frames through the whole chain and collects endpoint
// decisions alongside the voice activity ones.
type endpointRig struct {
	*DetectorRig
	endpoint *EndpointDetector
	gates    EndpointGates
}

func newEndpointRig(t *testing.T, cfg Config) *endpointRig {
	t.Helper()
	e, err := NewEndpointDetector(cfg)
	if err != nil {
		t.Fatalf("NewEndpointDetector: %v", err)
	}
	return &endpointRig{DetectorRig: NewDetectorRig(t, cfg), endpoint: e}
}

func (r *endpointRig) push(t *testing.T, f media.Frame) EndpointDecision {
	t.Helper()

	view, d := r.PushView(t, f)
	return r.endpoint.Observe(view, d, r.gates)
}

func (r *endpointRig) pushAll(t *testing.T, frames []media.Frame) []EndpointDecision {
	t.Helper()
	out := make([]EndpointDecision, 0, len(frames))
	for _, f := range frames {
		out = append(out, r.push(t, f))
	}
	return out
}

func firstConfirmed(decisions []EndpointDecision) (EndpointDecision, int, bool) {
	for i, d := range decisions {
		if d.Confirmed {
			return d, i, true
		}
	}
	return EndpointDecision{}, -1, false
}

func countConfirmed(decisions []EndpointDecision) int {
	var n int
	for _, d := range decisions {
		if d.Confirmed {
			n++
		}
	}
	return n
}

func countCandidates(decisions []EndpointDecision) int {
	var n int
	for _, d := range decisions {
		if d.Candidate {
			n++
		}
	}
	return n
}

// TestEndpoint_CandidateThenConfirmed is the §7 sequence.
func TestEndpoint_CandidateThenConfirmed(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(30)...)
	frames = append(frames, g.Silence(40)...)

	decisions := rig.pushAll(t, frames)

	candidateAt, confirmedAt := -1, -1
	for i, d := range decisions {
		if d.Candidate && candidateAt < 0 {
			candidateAt = i
		}
		if d.Confirmed && confirmedAt < 0 {
			confirmedAt = i
		}
	}

	if candidateAt < 0 {
		t.Fatal("no endpoint candidate was reported")
	}
	if confirmedAt < 0 {
		t.Fatal("no endpoint was confirmed")
	}
	if candidateAt >= confirmedAt {
		t.Errorf("candidate at %d, confirmation at %d; the candidate must come first",
			candidateAt, confirmedAt)
	}
	if n := countConfirmed(decisions); n != 1 {
		t.Errorf("%d confirmations for one turn, want exactly 1", n)
	}
	if n := countCandidates(decisions); n != 1 {
		t.Errorf("%d candidates for one pause, want exactly 1", n)
	}
}

// TestEndpoint_ConfirmsAtTheFrozenWindow measures the hop ADR-0011 budgets.
//
// The window runs from the FIRST sub-threshold frame, not from the moment the
// voice activity hangover elapsed. Measuring from the hangover would silently
// add MinSilence to the figure and make the ADR-0011 comparison wrong by 200 ms
// — most of the budget.
func TestEndpoint_ConfirmsAtTheFrozenWindow(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(30)...)
	frames = append(frames, g.Silence(60)...)

	decisions := rig.pushAll(t, frames)

	confirmed, _, ok := firstConfirmed(decisions)
	if !ok {
		t.Fatal("no endpoint was confirmed")
	}

	window := cfg.Endpoint.SilenceWindow

	// Confirmation happens on the first frame at or past the window, so the
	// held silence lands inside one frame interval of it.
	if confirmed.SilenceHeld < window {
		t.Errorf("confirmed after %s of silence, before the %s window",
			confirmed.SilenceHeld, window)
	}
	if slack := confirmed.SilenceHeld - window; slack >= cfg.FrameInterval {
		t.Errorf("confirmed after %s of silence, %s past the %s window; frames are "+
			"%s so at most one frame of overshoot is expected",
			confirmed.SilenceHeld, slack, window, cfg.FrameInterval)
	}

	if window != DefaultEndpointSilenceWindow {
		t.Errorf("the test configuration uses a %s window; ADR-0011 §5.2 hop 1 "+
			"budgets %s", window, DefaultEndpointSilenceWindow)
	}
	t.Logf("endpoint confirmed after %s of silence (ADR-0011 hop 1 budget: "+
		"%s p50 / 350ms p95)", confirmed.SilenceHeld, window)
}

// TestEndpoint_EveryGateBlocksConfirmation walks each gate in §7.
func TestEndpoint_EveryGateBlocksConfirmation(t *testing.T) {
	t.Parallel()

	base := TestConfig(testFormat())

	cases := []struct {
		name    string
		mutate  func(*Config)
		gates   EndpointGates
		build   func(g *SignalGenerator, cfg Config) []media.Frame
		want    string
		confirm bool
	}{
		{
			name:  "the agent holds the floor",
			gates: EndpointGates{AgentSpeaking: true},
			build: speechThenSilence(30, 60),
			want:  ReasonAgentSpeaking,
		},
		{
			name:  "an interruption is unresolved",
			gates: EndpointGates{BargeInActive: true},
			build: speechThenSilence(30, 60),
			want:  ReasonBargeInActive,
		},
		{
			name: "the utterance was too short to be a turn",
			mutate: func(c *Config) {
				// Longer than anything the fixture below produces.
				c.Endpoint.MinSpeechDuration = 2 * time.Second
			},
			build: speechThenSilence(5, 60),
			want:  ReasonSpeechTooShort,
		},
		{
			name: "the caller is audibly winding up to say more",
			mutate: func(c *Config) {
				c.Endpoint.RequireFallingEnergy = true
				// Any rise at all defers.
				c.Endpoint.EnergyTrendTolerance = 0
			},
			build: risingThenSilence(),
			want:  ReasonEnergyRising,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("the mutated configuration is invalid: %v", err)
			}

			g := newGen()
			rig := newEndpointRig(t, cfg)
			rig.gates = tc.gates

			decisions := rig.pushAll(t, tc.build(g, cfg))

			if n := countConfirmed(decisions); n != 0 {
				t.Errorf("%d endpoints confirmed; the %s gate must block them",
					n, tc.want)
			}

			var sawReason bool
			for _, d := range decisions {
				if d.Suppressed && d.Reason == tc.want {
					sawReason = true
					break
				}
			}
			if !sawReason {
				var reasons []string
				for _, d := range decisions {
					if d.Suppressed {
						reasons = append(reasons, d.Reason)
					}
				}
				t.Errorf("no suppression reported %q; saw %v", tc.want, reasons)
			}
		})
	}
}

func speechThenSilence(speech, silence int) func(*SignalGenerator, Config) []media.Frame {
	return func(g *SignalGenerator, cfg Config) []media.Frame {
		f := WarmupFrames(g, cfg)
		f = append(f, g.NormalSpeech(speech)...)
		return append(f, g.Silence(silence)...)
	}
}

// risingThenSilence produces speech whose level climbs, so the window trend is
// positive when the silence window closes.
func risingThenSilence() func(*SignalGenerator, Config) []media.Frame {
	return func(g *SignalGenerator, cfg Config) []media.Frame {
		f := WarmupFrames(g, cfg)
		for i := 0; i < 30; i++ {
			amp := 0.02 + float64(i)*0.01
			f = append(f, g.Speech(amp, defaultF0, defaultSyllableHz, 1)...)
		}
		return append(f, g.Silence(20)...)
	}
}

// TestEndpoint_ForcesAnEndpointOnAnEndlessTurn guards the case where the gates
// would otherwise suppress forever.
//
// A caller on a noisy line can hold the voice activity detector in speech
// indefinitely. Without the forced endpoint the conversation never advances,
// and the symptom is an agent that has apparently stopped listening.
func TestEndpoint_ForcesAnEndpointOnAnEndlessTurn(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.Endpoint.MaxTurnDuration = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	g := newGen()
	rig := newEndpointRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(200)...) // four seconds, no pause

	decisions := rig.pushAll(t, frames)

	confirmed, _, ok := firstConfirmed(decisions)
	if !ok {
		t.Fatal("a turn with no pause never produced an endpoint")
	}
	if confirmed.Reason != ReasonMaxTurn {
		t.Errorf("reason = %q, want %q", confirmed.Reason, ReasonMaxTurn)
	}
	if confirmed.TurnDuration < cfg.Endpoint.MaxTurnDuration {
		t.Errorf("forced at %s, before the %s limit",
			confirmed.TurnDuration, cfg.Endpoint.MaxTurnDuration)
	}
	if n := countConfirmed(decisions); n != 1 {
		t.Errorf("%d forced endpoints, want 1", n)
	}
}

// TestEndpoint_ShortPauseDoesNotEndTheTurn is the case a naive silence timer
// gets wrong.
func TestEndpoint_ShortPauseDoesNotEndTheTurn(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	// A pause comfortably inside the endpoint window.
	pause := cfg.frames(cfg.Endpoint.SilenceWindow) / 3

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)
	frames = append(frames, g.Silence(pause)...)
	frames = append(frames, g.NormalSpeech(20)...)

	decisions := rig.pushAll(t, frames)

	if n := countConfirmed(decisions); n != 0 {
		t.Errorf("%d endpoints across a %d-frame pause inside the %s window, want 0",
			n, pause, cfg.Endpoint.SilenceWindow)
	}
}

// TestEndpoint_ASecondPauseInTheSameTurnProducesAFreshCandidate proves the
// candidate is withdrawn when speech resumes.
func TestEndpoint_ASecondPauseInTheSameTurnProducesAFreshCandidate(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	pause := cfg.frames(cfg.Endpoint.SilenceWindow) / 3

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)
	frames = append(frames, g.Silence(pause)...)
	frames = append(frames, g.NormalSpeech(20)...)
	frames = append(frames, g.Silence(pause)...)
	frames = append(frames, g.NormalSpeech(20)...)

	decisions := rig.pushAll(t, frames)

	if n := countCandidates(decisions); n < 2 {
		t.Errorf("%d candidates across two pauses in one turn, want at least 2 — "+
			"a candidate must be withdrawn when speech resumes so the next pause "+
			"reports a fresh one", n)
	}
}

// TestEndpoint_SpeechDurationExcludesTheHangover guards the figure the
// MinSpeechDuration gate is measured against.
func TestEndpoint_SpeechDurationExcludesTheHangover(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	const speechFrames = 25

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(speechFrames)...)
	frames = append(frames, g.Silence(60)...)

	decisions := rig.pushAll(t, frames)

	confirmed, _, ok := firstConfirmed(decisions)
	if !ok {
		t.Fatal("no endpoint was confirmed")
	}

	want := time.Duration(speechFrames) * cfg.FrameInterval
	if confirmed.SpeechDuration != want {
		t.Errorf("SpeechDuration = %s, want %s; %s would mean the hangover was "+
			"counted as speech",
			confirmed.SpeechDuration, want, want+cfg.VAD.MinSilence)
	}
}

// TestEndpoint_IsDeterministic pins replay reproducibility for the endpointer.
func TestEndpoint_IsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	build := func() []media.Frame {
		g := newGen()
		f := WarmupFrames(g, cfg)
		f = append(f, g.NormalSpeech(25)...)
		f = append(f, g.Silence(30)...)
		f = append(f, g.NormalSpeech(25)...)
		return append(f, g.Silence(30)...)
	}

	first := newEndpointRig(t, cfg).pushAll(t, build())
	second := newEndpointRig(t, cfg).pushAll(t, build())

	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("endpoint decision %d diverged on replay\n first: %+v\nsecond: %+v",
				i, first[i], second[i])
		}
	}
}

func TestEndpoint_Reset(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newEndpointRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)
	rig.pushAll(t, frames)

	if !rig.endpoint.TurnOpen() {
		t.Fatal("setup: no turn is open")
	}
	rig.endpoint.Reset()
	if rig.endpoint.TurnOpen() {
		t.Error("Reset left a turn open")
	}
}

func TestNewEndpointDetector_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.Endpoint.SilenceWindow = 0
	if _, err := NewEndpointDetector(cfg); err == nil {
		t.Error("a zero silence window was accepted")
	}
}
