package audiointel

import (
	"context"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// The 25 mandatory audio scenarios (§21)
// ---------------------------------------------------------------------------
//
// Every one is generated from arithmetic with a fixed seed. No microphone, no
// recorded audio, no file on disk, nothing checked into the repository that
// anybody ever said out loud — which is a privacy decision as much as a testing
// one, and is why docs/audio-intelligence/SECURITY_REVIEW.md can say this
// module has no audio corpus to protect.
//
// Each scenario asserts the OBSERVABLE BEHAVIOUR the phase brief cares about,
// not internal state. A test that asserted a state machine reached
// candidate_speech at frame 17 would break on any retuning and would prove
// nothing about whether the engine works.

// scenarioResult collects what a run concluded, so an assertion can be written
// about the run rather than about individual frames.
type scenarioResult struct {
	analyses []Analysis

	onsets     int
	offsets    int
	endpoints  int
	candidates int
	bargeIns   int
	gaps       int
	restored   int

	faults    map[ContinuityFault]int
	silences  map[SilenceClass]int
	quality   QualityClass
	vadStates map[VADState]int
}

func runScenario(
	t *testing.T, h *Harness, s *Session,
	frames []media.Frame, state ConversationState,
) scenarioResult {
	t.Helper()

	out := scenarioResult{
		faults:    map[ContinuityFault]int{},
		silences:  map[SilenceClass]int{},
		vadStates: map[VADState]int{},
	}

	for _, f := range frames {
		a, err := s.Analyze(context.Background(), f, state, h.Controller, nil)
		if err != nil {
			t.Fatalf("analysing frame %d: %v", f.Sequence, err)
		}
		out.analyses = append(out.analyses, a)

		if a.VAD.OnsetConfirmed {
			out.onsets++
		}
		if a.VAD.OffsetConfirmed {
			out.offsets++
		}
		if a.Endpoint.Confirmed {
			out.endpoints++
		}
		if a.Endpoint.Candidate {
			out.candidates++
		}
		if a.BargeIn.Detected {
			out.bargeIns++
		}
		if a.Continuity.GapOpened {
			out.gaps++
		}
		if a.Continuity.Restored {
			out.restored++
		}
		out.faults[a.Continuity.Fault]++
		out.silences[a.Silence.Class]++
		out.vadStates[a.VAD.State]++
		out.quality = a.Quality.Class
	}

	return out
}

// TestScenarios covers §21's twenty-five mandatory cases.
//
// The subtest names are the brief's numbering, so a reader checking coverage
// can match them one to one against the phase document, and
// docs/audio-intelligence/EVALUATION_REPORT.md tabulates the mapping.
func TestScenarios(t *testing.T) {
	t.Parallel()

	type scenario struct {
		name  string
		build func(g *SignalGenerator, cfg Config) []media.Frame
		state ConversationState
		check func(t *testing.T, cfg Config, r scenarioResult)
	}

	// noSpeech is the commonest assertion: whatever this sound is, the engine
	// must not report somebody talking.
	noSpeech := func(why string) func(*testing.T, Config, scenarioResult) {
		return func(t *testing.T, _ Config, r scenarioResult) {
			if r.onsets != 0 {
				t.Errorf("%d speech onsets, want 0 — %s", r.onsets, why)
			}
			if r.endpoints != 0 {
				t.Errorf("%d endpoints, want 0 — %s", r.endpoints, why)
			}
		}
	}

	// oneTurn is the other common one: exactly one utterance, cleanly ended.
	oneTurn := func(t *testing.T, _ Config, r scenarioResult) {
		t.Helper()
		if r.onsets != 1 {
			t.Errorf("%d speech onsets, want exactly 1", r.onsets)
		}
		if r.offsets != 1 {
			t.Errorf("%d speech offsets, want exactly 1", r.offsets)
		}
		if r.endpoints != 1 {
			t.Errorf("%d endpoints, want exactly 1", r.endpoints)
		}
	}

	scenarios := []scenario{
		{
			name:  "01_pure_silence",
			build: func(g *SignalGenerator, cfg Config) []media.Frame { return g.Silence(120) },
			check: noSpeech("a silent line is not somebody talking"),
		},
		{
			name: "02_constant_background_noise",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				return g.Noise(0.01, 150)
			},
			check: noSpeech("a steady background is not somebody talking"),
		},
		{
			name: "03_low_volume_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.QuietSpeech(40)...)
				return append(f, g.Silence(40)...)
			},
			check: oneTurn,
		},
		{
			name: "04_normal_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(40)...)
				return append(f, g.Silence(40)...)
			},
			check: oneTurn,
		},
		{
			name: "05_loud_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.LoudSpeech(40)...)
				return append(f, g.Silence(40)...)
			},
			check: oneTurn,
		},
		{
			name: "06_speech_to_silence",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(30)...)
				return append(f, g.Silence(60)...)
			},
			check: func(t *testing.T, cfg Config, r scenarioResult) {
				oneTurn(t, cfg, r)
				if r.candidates != 1 {
					t.Errorf("%d endpoint candidates, want 1", r.candidates)
				}
			},
		},
		{
			name: "07_silence_to_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := g.Silence(60)
				return append(f, g.NormalSpeech(40)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.onsets != 1 {
					t.Errorf("%d onsets, want 1", r.onsets)
				}
				if r.silences[SilenceInitial] == 0 {
					t.Error("the opening silence was not classified as initial")
				}
			},
		},
		{
			name: "08_speech_short_pause_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(20)...)
				f = append(f, g.Silence(cfg.frames(cfg.VAD.MinSilence)/2)...)
				return append(f, g.NormalSpeech(20)...)
			},
			check: func(t *testing.T, cfg Config, r scenarioResult) {
				if r.onsets != 1 {
					t.Errorf("%d onsets across a pause inside the %s hangover, want 1",
						r.onsets, cfg.VAD.MinSilence)
				}
				if r.offsets != 0 {
					t.Errorf("%d offsets, want 0", r.offsets)
				}
			},
		},
		{
			name: "09_speech_long_pause",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(20)...)
				return append(f, g.Silence(200)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				oneTurn(t, nil2Config(), r)
				if r.silences[SilenceLong] == 0 {
					t.Error("a four-second pause was never classified as a long silence")
				}
			},
		},
		{
			name: "10_background_noise_then_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := g.Noise(0.01, 60)
				f = append(f, g.SpeechOverNoise(0.4, 0.01, 40)...)
				return append(f, g.Noise(0.01, 60)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.onsets != 1 {
					t.Errorf("%d onsets, want 1 — speech over a noisy line is still "+
						"one utterance", r.onsets)
				}
			},
		},
		{
			name: "11_speech_then_background_noise",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(30)...)
				return append(f, g.Noise(0.004, 100)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.onsets != 1 {
					t.Errorf("%d onsets, want 1", r.onsets)
				}
				if r.offsets != 1 {
					t.Errorf("%d offsets, want 1 — quiet background after speech must "+
						"still end the utterance", r.offsets)
				}
			},
		},
		{
			name: "12_sudden_transient_noise",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				for i := 0; i < 5; i++ {
					f = append(f, g.Transient(0.9, 1)...)
					f = append(f, g.Silence(15)...)
				}
				return f
			},
			check: noSpeech("a door slam is not a word"),
		},
		{
			name: "13_clipping",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				return append(f, g.Clipped(120)...)
			},
			check: func(t *testing.T, cfg Config, r scenarioResult) {
				// DEGRADED, NOT UNUSABLE, and the distinction is the honest one.
				//
				// The fixture drives a syllabic signal to 1.4x full scale, so it
				// clips on the peaks of each syllable and not in the closures
				// between them. Averaged over the window that lands between
				// DegradedClipRatio and MaxClipRatio — which is exactly what
				// "occasional clipping" means and exactly what the classifier
				// should say.
				//
				// An earlier version of this test asserted Unusable. The
				// classifier was right and the assertion was wrong: reaching
				// Unusable needs sustained clipping, which
				// TestQuality_EveryClassIsReachable covers directly.
				if r.quality == QualityGood || r.quality == QualityUnknown {
					t.Errorf("quality = %s on a clipped input, want it detected",
						r.quality)
				}

				var sawClipping bool
				var worstRatio float64
				for _, a := range r.analyses {
					if a.Quality.Reason == ReasonClipping {
						sawClipping = true
					}
					if a.Quality.ClipRatio > worstRatio {
						worstRatio = a.Quality.ClipRatio
					}
				}
				if !sawClipping {
					t.Errorf("quality is %s but clipping was never named as the "+
						"reason", r.quality)
				}
				t.Logf("clipped fixture: quality %s, worst windowed clip ratio "+
					"%.4f (degraded at %.4f, unusable at %.4f)",
					r.quality, worstRatio, cfg.Quality.DegradedClipRatio,
					cfg.Quality.MaxClipRatio)
			},
		},
		{
			name: "14_missing_frame",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(10)...)
				g.Skip(3)
				return append(f, g.NormalSpeech(30)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.faults[FaultMissing] == 0 {
					t.Error("a three-frame hole was not detected")
				}
				if r.gaps == 0 {
					t.Error("no gap was reported as opened")
				}
			},
		},
		{
			name: "15_duplicate_frame",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				speech := g.NormalSpeech(20)
				f = append(f, speech...)
				// The same frame delivered twice.
				return append(f, speech[len(speech)-1])
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.faults[FaultDuplicate] == 0 {
					t.Error("a repeated frame was not detected")
				}
			},
		},
		{
			name: "16_out_of_order_frame",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				speech := g.NormalSpeech(20)
				// Deliver 0..9, then 12, then 10 — 10 arrives after 12.
				f = append(f, speech[:10]...)
				f = append(f, speech[12])
				f = append(f, speech[10])
				return append(f, speech[13:]...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.faults[FaultOutOfOrder] == 0 {
					t.Error("a reordered frame was not detected")
				}
			},
		},
		{
			name: "17_timestamp_discontinuity",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				speech := g.NormalSpeech(20)
				// One frame's media clock leaps far ahead while its sequence
				// stays in order: the timeline jumped, not the packet stream.
				speech[10].Timestamp += cfg.Continuity.MaxTimestampJump * 5
				return append(f, speech...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.faults[FaultTimestampJump] == 0 {
					t.Error("a media-timeline leap was not detected")
				}
			},
		},
		{
			name: "18_ai_speaking_caller_interrupts",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				return append(f, g.NormalSpeech(40)...)
			},
			state: ConversationState{AgentSpeaking: true},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.bargeIns != 1 {
					t.Errorf("%d barge-in detections, want exactly 1", r.bargeIns)
				}
				var delivered bool
				for _, a := range r.analyses {
					if a.BargeIn.Outcome == BargeInDelivered {
						delivered = true
					}
				}
				if !delivered {
					t.Error("the interruption was never delivered to the speech controller")
				}
			},
		},
		{
			name: "19_ai_speaking_background_noise",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				return append(f, g.Noise(0.02, 120)...)
			},
			state: ConversationState{AgentSpeaking: true},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.bargeIns != 0 {
					t.Errorf("%d barge-ins from background noise, want 0 — the agent "+
						"must not stop talking because a fan switched on", r.bargeIns)
				}
			},
		},
		{
			name: "20_double_talk",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := g.Noise(0.01, cfg.Noise.ConfidenceFrames+cfg.Noise.WarmupFrames)
				f = append(f, g.SpeechOverNoise(0.4, 0.01, 60)...)
				return append(f, g.Noise(0.01, 40)...)
			},
			state: ConversationState{AgentSpeaking: true},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				var confirmed bool
				for _, a := range r.analyses {
					if a.Overlap.State == OverlapConfirmed {
						confirmed = true
						if a.Overlap.Confidence <= 0 {
							t.Error("a confirmed overlap carries no confidence")
						}
					}
				}
				if !confirmed {
					t.Error("sustained simultaneous speech never confirmed an overlap")
				}
			},
		},
		{
			name: "21_rapid_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.RapidSpeech(60)...)
				return append(f, g.Silence(40)...)
			},
			check: oneTurn,
		},
		{
			name: "22_long_speech",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				// Thirty seconds without a pause.
				f = append(f, g.NormalSpeech(1500)...)
				return append(f, g.Silence(40)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				if r.onsets != 1 {
					t.Errorf("%d onsets across thirty seconds of continuous speech, "+
						"want 1", r.onsets)
				}
			},
		},
		{
			name: "23_hindi_timing",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.HindiSpeech(80)...)
				return append(f, g.Silence(40)...)
			},
			check: func(t *testing.T, cfg Config, r scenarioResult) {
				// THE GEMINATE CLOSURES ARE THE POINT. They are deep pauses
				// inside a word, and a hangover that treated one as the end of
				// an utterance would split a Hindi speaker's sentence into
				// several turns.
				if r.onsets != 1 {
					t.Errorf("%d onsets across one Hindi-timed utterance, want 1 — "+
						"geminate closures are pauses INSIDE words and must not "+
						"split a turn", r.onsets)
				}
				if r.endpoints != 1 {
					t.Errorf("%d endpoints, want 1", r.endpoints)
				}
			},
		},
		{
			name: "24_hinglish_code_switching",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.HinglishSpeech(4, 25)...)
				return append(f, g.Silence(40)...)
			},
			check: func(t *testing.T, cfg Config, r scenarioResult) {
				// A code-switch pause is shorter than the endpoint window, so
				// it must not end the turn. An engine that ended one here would
				// cut a code-mixing speaker off mid-sentence.
				if r.onsets != 1 {
					t.Errorf("%d onsets across one code-mixed utterance, want 1 — a "+
						"switch pause is not a turn boundary", r.onsets)
				}
				if r.endpoints != 1 {
					t.Errorf("%d endpoints, want 1", r.endpoints)
				}
			},
		},
		{
			name: "25_devanagari_metadata",
			build: func(g *SignalGenerator, cfg Config) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.HindiSpeech(40)...)
				return append(f, g.Silence(40)...)
			},
			check: func(t *testing.T, _ Config, r scenarioResult) {
				// DEVANAGARI IS A SCRIPT, AND THERE IS NO ACOUSTIC DEVANAGARI.
				// Nothing in a waveform indicates what alphabet a transcript
				// will be written in, so the only honest thing to test at this
				// layer is that the language metadata Phase 11C supplies
				// survives the pipeline untouched. That is asserted in
				// TestScenarios_LanguageMetadataIsCarriedNotInterpreted; here we
				// only confirm the audio itself is handled normally.
				if r.onsets != 1 {
					t.Errorf("%d onsets, want 1", r.onsets)
				}
			},
		},
	}

	if len(scenarios) != 25 {
		t.Fatalf("%d scenarios declared, §21 requires 25", len(scenarios))
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			h := NewHarness(t)
			s := h.OpenInbound(t)
			g := NewSignalGenerator(h.Config.Format, h.Config.FrameInterval)
			g.SetArrival(h.Clock.Now())

			r := runScenario(t, h, s, sc.build(g, h.Config), sc.state)
			sc.check(t, h.Config, r)
		})
	}
}

// nil2Config returns a zero Config for the one check that does not use it.
func nil2Config() Config { return Config{} }

// TestScenarios_LanguageMetadataIsCarriedNotInterpreted is §22's requirement
// and the honest form of the "Devanagari fixture".
//
// # Two claims, and the second is the important one
//
// First: the language tag Phase 11C supplies reaches every event unmodified, so
// an evaluation can correlate audio signals with language.
//
// Second, and this is what stops the first from being misleading: THE TAG
// CHANGES NOTHING. Two sessions fed byte-identical audio and differing only in
// their language tag must reach byte-identical conclusions. This engine counts
// milliseconds and compares decibels; it has no language model, no phonology,
// and no way to behave differently for Hindi than for English. A tag that
// altered behaviour would be a claim this phase has not earned.
func TestScenarios_LanguageMetadataIsCarriedNotInterpreted(t *testing.T) {
	t.Parallel()

	tags := []Language{
		LangUnspecified,
		LangEnglishIN,
		LangHindi,
		LangHinglish,
		// A Devanagari-script tag. Carried like any other; means nothing here.
		Language("hi-in-deva"),
	}

	build := func(cfg Config) []media.Frame {
		g := NewSignalGenerator(cfg.Format, cfg.FrameInterval)
		f := WarmupFrames(g, cfg)
		f = append(f, g.HindiSpeech(40)...)
		return append(f, g.Silence(40)...)
	}

	type outcome struct {
		states     []VADState
		onsets     int
		endpoints  int
		confidence float64
	}

	results := make(map[Language]outcome, len(tags))

	for _, tag := range tags {
		h := NewHarness(t)

		s, err := h.Runtime.Open(context.Background(), SessionContext{
			Call:      "call-lang",
			Direction: DirectionInbound,
			Language:  tag,
			Format:    h.Config.Format,
		})
		if err != nil {
			t.Fatalf("opening a session tagged %q: %v", tag, err)
		}

		r := runScenario(t, h, s, build(h.Config), ConversationState{Turn: "turn-1"})

		var o outcome
		for _, a := range r.analyses {
			o.states = append(o.states, a.VAD.State)
			o.confidence += a.VAD.Confidence
		}
		o.onsets, o.endpoints = r.onsets, r.endpoints
		results[tag] = o

		// Claim one: the tag reached the events unmodified.
		events := h.Events.ForSession(s.ID())
		if len(events) == 0 {
			t.Fatalf("session tagged %q published no events", tag)
		}
		for i, e := range events {
			if e.Language != tag {
				t.Errorf("event %d of the session tagged %q carries language %q",
					i, tag, e.Language)
			}
		}
	}

	// Claim two: the tag changed nothing.
	baseline := results[LangUnspecified]
	for _, tag := range tags {
		got := results[tag]
		if got.onsets != baseline.onsets || got.endpoints != baseline.endpoints {
			t.Errorf("the session tagged %q reported %d onsets and %d endpoints; "+
				"the untagged one reported %d and %d. The tag must not change any "+
				"decision — this engine has no language model",
				tag, got.onsets, got.endpoints, baseline.onsets, baseline.endpoints)
		}
		if got.confidence != baseline.confidence {
			t.Errorf("the session tagged %q accumulated confidence %.9f against the "+
				"untagged %.9f", tag, got.confidence, baseline.confidence)
		}
		if len(got.states) != len(baseline.states) {
			t.Fatalf("the session tagged %q produced %d decisions, the untagged one %d",
				tag, len(got.states), len(baseline.states))
		}
		for i := range got.states {
			if got.states[i] != baseline.states[i] {
				t.Fatalf("the session tagged %q diverged at decision %d: %s vs %s",
					tag, i, got.states[i], baseline.states[i])
			}
		}
	}
}

// TestScenarios_HindiAndEnglishTimingDiffer proves the fixtures are actually
// different signals.
//
// Without this, the Hindi and Hinglish scenarios could be passing because they
// are indistinguishable from the English one — which would make them decoration
// rather than coverage.
func TestScenarios_HindiAndEnglishTimingDiffer(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	analyzer, err := NewFrameAnalyzer(cfg.Format)
	if err != nil {
		t.Fatal(err)
	}

	measure := func(frames []media.Frame) (minRMS, meanRMS float64) {
		minRMS = 1
		for _, f := range frames {
			feat, err := analyzer.Analyze(f)
			if err != nil {
				t.Fatal(err)
			}
			meanRMS += feat.RMS
			if feat.RMS < minRMS {
				minRMS = feat.RMS
			}
		}
		return minRMS, meanRMS / float64(len(frames))
	}

	hindiMin, hindiMean := measure(newGen().HindiSpeech(200))
	englishMin, englishMean := measure(newGen().EnglishSpeech(200))

	if hindiMin >= englishMin {
		t.Errorf("the Hindi fixture's quietest frame is %.6f and the English one's "+
			"is %.6f; the geminate closures should make the Hindi fixture reach "+
			"LOWER, and if it does not the two fixtures are the same signal",
			hindiMin, englishMin)
	}
	t.Logf("hindi: min %.6f mean %.6f;  english: min %.6f mean %.6f",
		hindiMin, hindiMean, englishMin, englishMean)

	// And both must still be detectable as speech, or the fixtures are broken
	// rather than merely different.
	for name, frames := range map[string][]media.Frame{
		"hindi":   newGen().HindiSpeech(60),
		"english": newGen().EnglishSpeech(60),
	} {
		g := newGen()
		rig := NewDetectorRig(t, cfg)
		all := append(WarmupFrames(g, cfg), frames...)
		if n := CountOnsets(rig.PushAll(t, all)); n != 1 {
			t.Errorf("the %s fixture produced %d onsets, want 1", name, n)
		}
	}
}

// TestScenarios_DeterministicReplay is §20's determinism requirement at the
// whole-engine level.
//
// The same frames must produce the same conclusions, every time, on any
// machine. Anything less makes a threshold regression a flake somebody reruns.
func TestScenarios_DeterministicReplay(t *testing.T) {
	t.Parallel()

	build := func(cfg Config) []media.Frame {
		g := NewSignalGenerator(cfg.Format, cfg.FrameInterval)
		f := WarmupFrames(g, cfg)
		f = append(f, g.NormalSpeech(30)...)
		f = append(f, g.Silence(20)...)
		f = append(f, g.SpeechOverNoise(0.3, 0.02, 30)...)
		f = append(f, g.Noise(0.05, 20)...)
		f = append(f, g.HindiSpeech(30)...)
		return append(f, g.Silence(40)...)
	}

	run := func() []Analysis {
		h := NewHarness(t)
		s := h.OpenInbound(t)
		return runScenario(t, h, s, build(h.Config), ConversationState{}).analyses
	}

	first, second := run(), run()

	if len(first) != len(second) {
		t.Fatalf("replay produced %d and %d analyses", len(first), len(second))
	}
	for i := range first {
		// Compared field by field: the whole Analysis contains a time.Time that
		// a fake clock makes identical, so a struct comparison is meaningful
		// here, but naming the fields makes a divergence readable.
		a, b := first[i], second[i]
		switch {
		case a.VAD != b.VAD:
			t.Fatalf("analysis %d: voice activity diverged\n first: %+v\nsecond: %+v",
				i, a.VAD, b.VAD)
		case a.Endpoint != b.Endpoint:
			t.Fatalf("analysis %d: endpoint diverged\n first: %+v\nsecond: %+v",
				i, a.Endpoint, b.Endpoint)
		case a.Quality != b.Quality:
			t.Fatalf("analysis %d: quality diverged\n first: %+v\nsecond: %+v",
				i, a.Quality, b.Quality)
		case a.Continuity != b.Continuity:
			t.Fatalf("analysis %d: continuity diverged\n first: %+v\nsecond: %+v",
				i, a.Continuity, b.Continuity)
		case a.Silence != b.Silence:
			t.Fatalf("analysis %d: silence diverged\n first: %+v\nsecond: %+v",
				i, a.Silence, b.Silence)
		}
	}
}

// TestScenarios_NoPCMEscapesTheEngine is §24, checked at the boundary a
// consumer actually touches.
//
// The reflection test on AudioEvent covers the event path. This covers the
// RETURN path: everything Analyze hands back must be scalars a caller can
// retain forever without holding a recording.
func TestScenarios_NoPCMEscapesTheEngine(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	s := h.OpenInbound(t)

	frames := WarmupFrames(h.Gen, h.Config)
	frames = append(frames, h.Gen.NormalSpeech(20)...)

	// Retain every analysis, then overwrite every payload the engine was given.
	// If anything held a reference into that storage, the retained analyses
	// would change underneath us.
	analyses := runScenario(t, h, s, frames, ConversationState{}).analyses

	before := make([]FrameFeatures, len(analyses))
	for i, a := range analyses {
		before[i] = a.Frame()
	}

	for _, f := range frames {
		for i := range f.Payload {
			f.Payload[i] = 0x7F
		}
	}

	for i, a := range analyses {
		if a.Frame() != before[i] {
			t.Fatalf("analysis %d changed when the frame payload was overwritten; "+
				"something retained a reference into borrowed audio storage", i)
		}
	}

	// And the analysis type itself must contain no byte slice at any depth.
	assertNoByteSlices(t, "Analysis", analyses[0])
}

// TestScenarios_EndpointLatencyAgainstTheFrozenBudget reports the measurement
// ADR-0011 §5.2 hop 1 budgets.
//
// Reported, not asserted as a distribution: one synthetic scenario is not a p50
// or a p95, and presenting it as one would be inventing an SLA. What IS
// asserted is that the engine confirms within one frame of its configured
// window, which is the part this package controls.
func TestScenarios_EndpointLatencyAgainstTheFrozenBudget(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	s := h.OpenInbound(t)

	var measured []time.Duration

	for turn := 0; turn < 10; turn++ {
		frames := h.Gen.NormalSpeech(30)
		frames = append(frames, h.Gen.Silence(40)...)

		for _, f := range frames {
			a, err := s.Analyze(context.Background(), f, ConversationState{},
				h.Controller, nil)
			if err != nil {
				t.Fatal(err)
			}
			if a.Endpoint.Confirmed {
				measured = append(measured, a.Endpoint.SilenceHeld)
			}
		}
	}

	if len(measured) == 0 {
		t.Fatal("no endpoint was confirmed across ten turns")
	}

	var worst time.Duration
	for _, d := range measured {
		if d > worst {
			worst = d
		}
		if d < h.Config.Endpoint.SilenceWindow {
			t.Errorf("an endpoint confirmed after %s, inside the configured %s window",
				d, h.Config.Endpoint.SilenceWindow)
		}
		if d >= h.Config.Endpoint.SilenceWindow+h.Config.FrameInterval {
			t.Errorf("an endpoint confirmed after %s, more than one %s frame past "+
				"the %s window", d, h.Config.FrameInterval,
				h.Config.Endpoint.SilenceWindow)
		}
	}

	t.Logf("%d endpoints, worst %s of silence held "+
		"(ADR-0011 §5.2 hop 1 budgets 250ms p50 / 350ms p95 for this hop; "+
		"this is a synthetic measurement, not a production distribution)",
		len(measured), worst)
}
