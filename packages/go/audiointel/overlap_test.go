package audiointel

import (
	"math"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

type overlapRig struct {
	*DetectorRig
	overlap  *OverlapDetector
	envelope OutboundEnvelope

	AgentSpeaking bool
}

func newOverlapRig(t *testing.T, cfg Config) *overlapRig {
	t.Helper()
	rig := NewDetectorRig(t, cfg)
	o, err := NewOverlapDetector(cfg, rig.Clock)
	if err != nil {
		t.Fatalf("NewOverlapDetector: %v", err)
	}
	return &overlapRig{DetectorRig: rig, overlap: o}
}

func (r *overlapRig) push(t *testing.T, f media.Frame) OverlapDecision {
	t.Helper()

	view, d := r.PushView(t, f)
	return r.overlap.Observe(view, d, r.AgentSpeaking, r.envelope)
}

func (r *overlapRig) pushAll(t *testing.T, frames []media.Frame) []OverlapDecision {
	t.Helper()
	out := make([]OverlapDecision, 0, len(frames))
	for _, f := range frames {
		out = append(out, r.push(t, f))
	}
	return out
}

func overlapStates(decisions []OverlapDecision) []OverlapState {
	var out []OverlapState
	for _, d := range decisions {
		if n := len(out); n == 0 || out[n-1] != d.State {
			out = append(out, d.State)
		}
	}
	return out
}

// TestOverlap_TransitionTableIsWellFormed checks the declared table.
func TestOverlap_TransitionTableIsWellFormed(t *testing.T) {
	t.Parallel()

	table := overlapTransitions()

	for _, s := range AllOverlapStates() {
		if len(table[s]) == 0 {
			t.Errorf("%s declares no outgoing transitions", s)
		}
	}
	for from, tos := range table {
		for _, to := range tos {
			if from == to {
				t.Errorf("%s declares a self-transition", from)
			}
			if !CanOverlapTransition(from, to) {
				t.Errorf("CanOverlapTransition(%s, %s) disagrees with the table", from, to)
			}
		}
	}

	// Confirmed must not jump straight back to none: a consumer has to be able
	// to tell an overlap that ENDED from one that never happened.
	if CanOverlapTransition(OverlapConfirmed, OverlapNone) {
		t.Error("confirmed → none is declared; a confirmed overlap must pass " +
			"through resolved so its ending is observable")
	}

	reachable := map[OverlapState]bool{OverlapNone: true}
	for changed := true; changed; {
		changed = false
		for from, tos := range table {
			if !reachable[from] {
				continue
			}
			for _, to := range tos {
				if !reachable[to] {
					reachable[to], changed = true, true
				}
			}
		}
	}
	for _, s := range AllOverlapStates() {
		if !reachable[s] {
			t.Errorf("%s is unreachable from %s", s, OverlapNone)
		}
	}
}

// TestOverlap_WalksTheFullLifecycle drives the four states with real audio.
func TestOverlap_WalksTheFullLifecycle(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newOverlapRig(t, cfg)

	// A REAL background, not digital silence, and long enough for the noise
	// floor to reach full confidence. Overlap confidence is built on speech
	// confidence, which is built on the floor's confidence, so a detector that
	// has only seen a few frames of silence cannot be sure enough of anything
	// to confirm an overlap — which is correct behaviour, and it means this
	// test has to give it a call's worth of background to work with.
	rig.pushAll(t, g.Noise(0.01, cfg.Noise.ConfidenceFrames+cfg.Noise.WarmupFrames))

	// The agent is speaking and the caller talks over it for well past
	// MinDuration.
	rig.AgentSpeaking = true
	during := rig.pushAll(t, g.SpeechOverNoise(0.4, 0.01, 40))

	// The caller stops; the agent is still going.
	after := rig.pushAll(t, g.Noise(0.01, 60))

	all := append(append([]OverlapDecision{}, during...), after...)
	states := overlapStates(all)

	want := []OverlapState{
		OverlapNone, OverlapPossible, OverlapConfirmed, OverlapResolved, OverlapNone,
	}
	if len(states) != len(want) {
		t.Fatalf("state sequence %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state sequence %v, want %v", states, want)
		}
	}
}

// TestOverlap_ShortArtifactsNeverConfirm is what §9 asks for: a click, a
// handset bump and a codec transient must not read as double-talk.
func TestOverlap_ShortArtifactsNeverConfirm(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	cases := []struct {
		name  string
		build func(g *SignalGenerator) []media.Frame
	}{
		{"a click", func(g *SignalGenerator) []media.Frame { return g.Transient(0.9, 1) }},
		{"a handset bump", func(g *SignalGenerator) []media.Frame { return g.Transient(0.6, 2) }},
		{
			"a codec transient",
			func(g *SignalGenerator) []media.Frame { return g.NormalSpeech(2) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := newGen()
			rig := newOverlapRig(t, cfg)
			rig.pushAll(t, WarmupFrames(g, cfg))

			rig.AgentSpeaking = true
			decisions := rig.pushAll(t, tc.build(g))
			decisions = append(decisions, rig.pushAll(t, g.Silence(30))...)

			for i, d := range decisions {
				if d.State == OverlapConfirmed {
					t.Fatalf("decision %d confirmed an overlap from %s; confirmation "+
						"needs %s of sustained speech", i, tc.name, cfg.Overlap.MinDuration)
				}
			}
		})
	}
}

// TestOverlap_RequiresBothPartiesToHoldTheFloor pins the precondition.
func TestOverlap_RequiresBothPartiesToHoldTheFloor(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newOverlapRig(t, cfg)

	rig.pushAll(t, WarmupFrames(g, cfg))

	// The caller talks at length while the agent is silent. That is a turn, not
	// an overlap.
	rig.AgentSpeaking = false
	decisions := rig.pushAll(t, g.NormalSpeech(60))

	for i, d := range decisions {
		if d.State != OverlapNone {
			t.Fatalf("decision %d reported %s while only the caller was speaking",
				i, d.State)
		}
	}
}

// TestOverlap_ConfidenceRisesWithDuration is what excludes short artifacts
// without a hard cutoff, so an operator can see how often they happen.
func TestOverlap_ConfidenceRisesWithDuration(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newOverlapRig(t, cfg)

	rig.pushAll(t, g.Noise(0.01, cfg.Noise.ConfidenceFrames+cfg.Noise.WarmupFrames))
	rig.AgentSpeaking = true

	decisions := rig.pushAll(t, g.SpeechOverNoise(0.4, 0.01, 40))

	var first, last float64
	var haveFirst bool
	for _, d := range decisions {
		if d.State == OverlapNone {
			continue
		}
		if !haveFirst {
			first, haveFirst = d.Confidence, true
		}
		last = d.Confidence
	}

	if !haveFirst {
		t.Fatal("no overlap was ever reported")
	}
	if last <= first {
		t.Errorf("confidence went from %.3f to %.3f across a sustained overlap; "+
			"it must rise as the evidence accumulates", first, last)
	}
	for _, d := range decisions {
		if d.Confidence < 0 || d.Confidence > 1 {
			t.Fatalf("confidence %g is outside [0,1]", d.Confidence)
		}
	}
}

// TestOverlap_EchoCorrelationOnlyLowersConfidence is the documented limitation,
// enforced.
//
// Correlation with our own output is weak evidence of echo. It must never raise
// confidence, and its absence must never be read as proof of genuine
// double-talk.
func TestOverlap_EchoCorrelationOnlyLowersConfidence(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	// Two identical runs, differing only in whether an outbound envelope that
	// the inbound signal tracks is supplied.
	run := func(envelope OutboundEnvelope) (confidence float64, correlated bool) {
		g := newGen()
		rig := newOverlapRig(t, cfg)
		rig.envelope = envelope

		rig.pushAll(t, WarmupFrames(g, cfg))
		rig.AgentSpeaking = true

		// Speech whose syllabic envelope has the same period as the modulated
		// outbound envelope below, so the two genuinely correlate.
		decisions := rig.pushAll(t, g.NormalSpeech(60))

		var last OverlapDecision
		for _, d := range decisions {
			if d.State == OverlapConfirmed || d.State == OverlapPossible {
				last = d
			}
		}
		return last.Confidence, last.EchoMeasured && last.EchoCorrelation > 0
	}

	// The speech fixture modulates at defaultSyllableHz, so an envelope with
	// the matching period is what echo of it would look like.
	echoLike := ModulatedEnvelope{
		Base:   0.2,
		Depth:  0.15,
		Period: time.Duration(float64(time.Second) / defaultSyllableHz),
	}

	withoutEnvelope, _ := run(nil)
	withEnvelope, correlated := run(echoLike)

	if withoutEnvelope <= 0 {
		t.Fatalf("the run without an envelope reported confidence %g", withoutEnvelope)
	}
	if withEnvelope > withoutEnvelope {
		t.Errorf("supplying an outbound envelope RAISED confidence from %.3f to "+
			"%.3f; correlation with our own output may only ever lower it",
			withoutEnvelope, withEnvelope)
	}
	if correlated && withEnvelope >= withoutEnvelope {
		t.Errorf("a positive echo correlation did not lower confidence: %.3f with, "+
			"%.3f without", withEnvelope, withoutEnvelope)
	}
	t.Logf("confidence without an envelope %.3f, with an echo-like envelope %.3f "+
		"(correlated: %t)", withoutEnvelope, withEnvelope, correlated)
}

// TestOverlap_UnknownEnvelopeIsNoEvidence guards against an uninstrumented
// outbound path fabricating an anti-correlation.
func TestOverlap_UnknownEnvelopeIsNoEvidence(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newOverlapRig(t, cfg)
	rig.envelope = UnknownEnvelope{}

	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	for _, d := range rig.pushAll(t, g.NormalSpeech(40)) {
		if d.EchoMeasured {
			t.Fatal("an envelope reporting nothing known was treated as a measurement")
		}
	}
}

// TestOverlap_ConstantEnvelopeYieldsNoCorrelation pins the honest answer for a
// signal with no variance.
func TestOverlap_ConstantEnvelopeYieldsNoCorrelation(t *testing.T) {
	t.Parallel()

	// A constant series has no variance, so the coefficient is undefined. Zero
	// — "no evidence of correlation" — is the honest answer rather than a
	// division by zero.
	constant := make([]float64, echoWindowFrames)
	varying := make([]float64, echoWindowFrames)
	for i := range constant {
		constant[i] = 0.2
		varying[i] = float64(i)
	}

	if got := pearson(constant, varying); got != 0 {
		t.Errorf("pearson(constant, varying) = %g, want 0", got)
	}
	if got := pearson(varying, constant); got != 0 {
		t.Errorf("pearson(varying, constant) = %g, want 0", got)
	}
}

// TestOverlap_PearsonIsCorrect checks the correlation arithmetic against known
// series.
func TestOverlap_PearsonIsCorrect(t *testing.T) {
	t.Parallel()

	up := make([]float64, 16)
	down := make([]float64, 16)
	for i := range up {
		up[i] = float64(i)
		down[i] = float64(len(down) - i)
	}

	if got := pearson(up, up); math.Abs(got-1) > 1e-9 {
		t.Errorf("pearson(x, x) = %g, want 1", got)
	}
	if got := pearson(up, down); math.Abs(got+1) > 1e-9 {
		t.Errorf("pearson(x, -x) = %g, want -1", got)
	}
}

// TestOverlap_DisabledPolicyReportsNothing pins the switch.
func TestOverlap_DisabledPolicyReportsNothing(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.Overlap.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	g := newGen()
	rig := newOverlapRig(t, cfg)
	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	for i, d := range rig.pushAll(t, g.NormalSpeech(60)) {
		if d.State != OverlapNone {
			t.Fatalf("decision %d reported %s with overlap detection disabled",
				i, d.State)
		}
	}
}

// TestOverlap_LowConfidenceIsNotReportedAsConfirmed keeps the state from
// claiming more than the evidence supports.
func TestOverlap_LowConfidenceIsNotReportedAsConfirmed(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	// Nothing this fixture produces can clear a confidence floor of 0.99.
	cfg.Overlap.MinConfidence = 0.99
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	g := newGen()
	rig := newOverlapRig(t, cfg)
	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true

	for i, d := range rig.pushAll(t, g.NormalSpeech(60)) {
		if d.State == OverlapConfirmed {
			t.Fatalf("decision %d reached %s at confidence %.3f, below the %.2f "+
				"floor; MinConfidence gates the promotion, so the state must never "+
				"claim more than the evidence supports",
				i, d.State, d.Confidence, cfg.Overlap.MinConfidence)
		}
	}
}

func TestOverlap_Reset(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := newOverlapRig(t, cfg)

	rig.pushAll(t, WarmupFrames(g, cfg))
	rig.AgentSpeaking = true
	rig.pushAll(t, g.NormalSpeech(40))

	rig.overlap.Reset()

	if rig.overlap.filled != 0 {
		t.Errorf("Reset left %d echo samples", rig.overlap.filled)
	}
	if len(rig.overlap.inbound) != echoWindowFrames {
		t.Error("Reset reallocated the echo ring")
	}
}

func TestNewOverlapDetector_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.Overlap.MinDuration = 0
	if _, err := NewOverlapDetector(cfg, nil); err == nil {
		t.Error("a zero minimum duration was accepted while overlap is enabled")
	}
}
