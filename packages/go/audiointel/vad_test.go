package audiointel

import (
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

const testInterval = 20 * time.Millisecond

func testFormat() media.AudioFormat { return media.PCM16Mono8k() }

func newGen() *SignalGenerator { return NewSignalGenerator(testFormat(), testInterval) }

// TestVAD_TransitionTableIsComplete checks the declared table itself, before
// any audio is involved.
func TestVAD_TransitionTableIsComplete(t *testing.T) {
	t.Parallel()

	table := vadTransitions()

	// Every state must appear as a source, or it is a dead end nothing declared
	// as terminal — which is the bug this checks for.
	for _, s := range AllVADStates() {
		if _, ok := table[s]; !ok {
			t.Errorf("%s has no declared outgoing transitions and is not terminal", s)
		}
		if len(table[s]) == 0 {
			t.Errorf("%s declares an empty transition list", s)
		}
	}

	// Every target must be a declared state, and nothing may declare a
	// self-transition — runtime.NewFSM refuses those, but failing here names
	// the offender.
	for from, tos := range table {
		if !from.Valid() {
			t.Errorf("undeclared state %q appears as a transition source", from)
		}
		for _, to := range tos {
			if !to.Valid() {
				t.Errorf("%s declares a transition to undeclared state %q", from, to)
			}
			if from == to {
				t.Errorf("%s declares a self-transition", from)
			}
		}
	}

	// Every state must be reachable from the initial state, or it is code that
	// can never run.
	reachable := map[VADState]bool{VADUncertain: true}
	for changed := true; changed; {
		changed = false
		for from, tos := range table {
			if !reachable[from] {
				continue
			}
			for _, to := range tos {
				if !reachable[to] {
					reachable[to] = true
					changed = true
				}
			}
		}
	}
	for _, s := range AllVADStates() {
		if !reachable[s] {
			t.Errorf("%s is unreachable from %s", s, VADUncertain)
		}
	}
}

// TestVAD_RefusesUndeclaredTransitions proves the table is enforced rather than
// decorative.
func TestVAD_RefusesUndeclaredTransitions(t *testing.T) {
	t.Parallel()

	// The two that matter most, spelled out.
	if CanVADTransition(VADSpeech, VADSilence) {
		t.Error("speech → silence is declared; that edge IS the hangover being " +
			"bypassed, and no code path may exist for it")
	}
	if CanVADTransition(VADSilence, VADSpeech) {
		t.Error("silence → speech is declared; speech must be confirmed through " +
			"candidate_speech, or one loud frame starts a turn")
	}

	for _, from := range AllVADStates() {
		declared := make(map[VADState]bool)
		for _, to := range VADTransitionsFrom(from) {
			declared[to] = true
		}
		for _, to := range AllVADStates() {
			if got := CanVADTransition(from, to); got != declared[to] {
				t.Errorf("CanVADTransition(%s, %s) = %v, table says %v",
					from, to, got, declared[to])
			}
		}
	}
}

// TestVAD_EveryDeclaredEdgeIsReachedByRealAudio drives synthetic signals until
// every declared transition has actually fired.
//
// # Why this matters more than the table test above
//
// A declared edge nothing can reach is dead code that looks like a feature. A
// table test proves the map is well-formed; only driving audio through it
// proves the switch statement can get there.
func TestVAD_EveryDeclaredEdgeIsReachedByRealAudio(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	seen := make(map[[2]VADState]bool)
	record := func(decisions []VADDecision) {
		for _, d := range decisions {
			if d.Changed {
				seen[[2]VADState{d.Previous, d.State}] = true
			}
		}
	}

	scenarios := []struct {
		name  string
		build func(g *SignalGenerator) []media.Frame
	}{
		{
			// uncertain → silence, silence → candidate_speech,
			// candidate_speech → speech, speech → candidate_silence,
			// candidate_silence → silence.
			name: "speech then a long pause",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(20)...)
				return append(f, g.Silence(30)...)
			},
		},
		{
			// candidate_silence → speech: resumed inside the hangover.
			name: "speech, short pause, speech",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(10)...)
				f = append(f, g.Silence(4)...)
				return append(f, g.NormalSpeech(10)...)
			},
		},
		{
			// candidate_speech → silence: an onset that did not persist.
			name: "a single transient",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.Transient(0.8, 1)...)
				return append(f, g.Silence(20)...)
			},
		},
		{
			// silence → noise, noise → silence.
			name: "broadband noise burst",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.Noise(0.05, 40)...)
				return append(f, g.Silence(40)...)
			},
		},
		{
			// speech → noise: a steady tone that passed the onset test because
			// nothing looks steady in its first frames.
			name: "hold music",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				return append(f, g.Tone(1000, 0.3, 80)...)
			},
		},
		{
			// candidate_speech → noise: an onset opens on the first frame of a
			// sound, and the sound then turns out to be broadband hiss rather
			// than a word. Reached with exactly ONE speech frame, because two
			// consecutive would confirm the onset instead.
			name: "one speech frame followed by hiss",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.NormalSpeech(1)...)
				f = append(f, g.Noise(0.08, 30)...)
				return append(f, g.Silence(20)...)
			},
		},
		{
			// noise → candidate_speech: somebody speaks over a noisy line.
			name: "speech emerging from noise",
			build: func(g *SignalGenerator) []media.Frame {
				f := WarmupFrames(g, cfg)
				f = append(f, g.Noise(0.05, 30)...)
				return append(f, g.SpeechOverNoise(0.5, 0.05, 40)...)
			},
		},
	}

	for _, sc := range scenarios {
		rig := NewDetectorRig(t, cfg)
		record(rig.PushAll(t, sc.build(newGen())))
	}

	var missing []string
	for from, tos := range vadTransitions() {
		for _, to := range tos {
			if !seen[[2]VADState{from, to}] {
				missing = append(missing, string(from)+" → "+string(to))
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("declared edges no synthetic signal reached — each is either dead "+
			"code or a missing scenario:\n  %v", missing)
	}
}

// TestVAD_RefusesToAssertBeforeTheFloorConverges is the honest-start property.
func TestVAD_RefusesToAssertBeforeTheFloorConverges(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	rig := NewDetectorRig(t, cfg)

	// Speech from the very first frame, before any background has been seen.
	decisions := rig.PushAll(t, newGen().NormalSpeech(cfg.Noise.WarmupFrames-1))

	for i, d := range decisions {
		if d.State != VADUncertain {
			t.Fatalf("frame %d reported %s before the floor converged; there is "+
				"nothing to compare a frame against yet", i, d.State)
		}
		if d.Confidence != 0 {
			t.Errorf("frame %d reported confidence %g while uncertain", i, d.Confidence)
		}
		if d.Explanation.Verdict != ReasonFloorUncertain {
			t.Errorf("frame %d verdict = %q, want %q",
				i, d.Explanation.Verdict, ReasonFloorUncertain)
		}
	}
}

// TestVAD_DetectsSpeechAtEveryLevel proves the detector is driven by the ratio
// to the floor rather than by an absolute level.
func TestVAD_DetectsSpeechAtEveryLevel(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	levels := map[string]func(*SignalGenerator, int) []media.Frame{
		"quiet":  (*SignalGenerator).QuietSpeech,
		"normal": (*SignalGenerator).NormalSpeech,
		"loud":   (*SignalGenerator).LoudSpeech,
		"rapid":  (*SignalGenerator).RapidSpeech,
	}

	for name, speak := range levels {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := newGen()
			rig := NewDetectorRig(t, cfg)

			frames := WarmupFrames(g, cfg)
			frames = append(frames, speak(g, 40)...)
			decisions := rig.PushAll(t, frames)

			if n := CountOnsets(decisions); n != 1 {
				t.Errorf("%d onsets, want exactly 1", n)
			}
			if rig.Last.State != VADSpeech {
				t.Errorf("final state %s, want %s", rig.Last.State, VADSpeech)
			}
		})
	}
}

// TestVAD_OnsetIsBackdatedToWhereSpeechBegan is the §5 timestamp-accuracy
// requirement.
//
// The detector becomes sure at the confirming frame, but speech began
// MinOnsetFrames earlier. Reporting the confirming frame would place every
// onset late by the confirmation window and make every measured utterance short
// by the same amount — which then propagates into the endpoint measurement and
// into ADR-0011's hop 1 comparison.
func TestVAD_OnsetIsBackdatedToWhereSpeechBegan(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	warmup := WarmupFrames(g, cfg)
	speech := g.NormalSpeech(20)

	decisions := rig.PushAll(t, append(append([]media.Frame{}, warmup...), speech...))

	onset, idx, ok := FirstOnset(decisions)
	if !ok {
		t.Fatal("no onset was confirmed")
	}

	// Speech begins at the first speech frame's timestamp.
	wantStart := speech[0].Timestamp
	if onset.SpeechStart != wantStart {
		t.Errorf("SpeechStart = %s, want %s (the first speech frame)\n"+
			"the onset was confirmed at decision %d, whose frame starts at %s — "+
			"reporting that instead would place every onset %s late",
			onset.SpeechStart, wantStart, idx, decisions[idx].Explanation.SilenceHeld,
			time.Duration(cfg.VAD.MinOnsetFrames-1)*cfg.FrameInterval)
	}

	// And confirmation must arrive exactly MinOnsetFrames after speech started.
	wantIdx := len(warmup) + cfg.VAD.MinOnsetFrames - 1
	if idx != wantIdx {
		t.Errorf("onset confirmed at decision %d, want %d (MinOnsetFrames=%d after "+
			"speech began at %d)", idx, wantIdx, cfg.VAD.MinOnsetFrames, len(warmup))
	}
}

// TestVAD_EmitsExactlyOneOnsetPerRun is §5's no-duplicate-onsets requirement.
//
// The case that breaks a naive implementation: connected speech re-enters
// VADSpeech after every stop closure, so a detector keyed on entering that
// state emits one onset per syllable.
func TestVAD_EmitsExactlyOneOnsetPerRun(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	// Speech over a noisy line, which is what makes the machine oscillate
	// between speech and candidate_silence across syllable closures.
	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.Noise(0.01, 20)...)
	frames = append(frames, g.SpeechOverNoise(0.5, 0.01, 150)...)

	decisions := rig.PushAll(t, frames)

	// The machine may enter Speech many times; the caller must see one onset.
	entries := rig.VAD.EnteredCount(VADSpeech)
	onsets := CountOnsets(decisions)

	if onsets != 1 {
		t.Errorf("%d onsets across one continuous utterance, want exactly 1 "+
			"(the machine entered speech %d times, which is the hangover absorbing "+
			"syllable closures and must NOT be reported as onsets)", onsets, entries)
	}
	if CountOffsets(decisions) != 0 {
		t.Errorf("%d offsets during continuous speech, want 0", CountOffsets(decisions))
	}
}

// TestVAD_OneSilentFrameDoesNotEndSpeech is §6, checked at its sharpest point.
func TestVAD_OneSilentFrameDoesNotEndSpeech(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)
	frames = append(frames, g.Silence(1)...)
	frames = append(frames, g.NormalSpeech(20)...)

	decisions := rig.PushAll(t, frames)

	if n := CountOffsets(decisions); n != 0 {
		t.Errorf("%d offsets across a single silent frame, want 0", n)
	}
	if n := CountOnsets(decisions); n != 1 {
		t.Errorf("%d onsets, want 1 — the run must be continuous across one silent "+
			"frame", n)
	}
	if rig.Last.State != VADSpeech {
		t.Errorf("final state %s, want %s", rig.Last.State, VADSpeech)
	}
}

// TestVAD_HangoverSpansAShortPauseAndEndsOnALongOne pins both sides of §6.
func TestVAD_HangoverSpansAShortPauseAndEndsOnALongOne(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	// A pause comfortably inside the hangover, and one comfortably past it.
	shortPause := cfg.frames(cfg.VAD.MinSilence) / 2
	longPause := cfg.frames(cfg.VAD.MinSilence) * 2

	t.Run("short pause keeps the run open", func(t *testing.T) {
		t.Parallel()
		g := newGen()
		rig := NewDetectorRig(t, cfg)

		frames := WarmupFrames(g, cfg)
		frames = append(frames, g.NormalSpeech(15)...)
		frames = append(frames, g.Silence(shortPause)...)
		frames = append(frames, g.NormalSpeech(15)...)

		decisions := rig.PushAll(t, frames)
		if n := CountOffsets(decisions); n != 0 {
			t.Errorf("%d offsets across a %d-frame pause inside the %s hangover, want 0",
				n, shortPause, cfg.VAD.MinSilence)
		}
		if n := CountOnsets(decisions); n != 1 {
			t.Errorf("%d onsets, want 1", n)
		}
	})

	t.Run("long pause closes the run exactly once", func(t *testing.T) {
		t.Parallel()
		g := newGen()
		rig := NewDetectorRig(t, cfg)

		frames := WarmupFrames(g, cfg)
		frames = append(frames, g.NormalSpeech(15)...)
		frames = append(frames, g.Silence(longPause)...)
		frames = append(frames, g.NormalSpeech(15)...)

		decisions := rig.PushAll(t, frames)
		if n := CountOffsets(decisions); n != 1 {
			t.Errorf("%d offsets across a %d-frame pause past the %s hangover, want 1",
				n, longPause, cfg.VAD.MinSilence)
		}
		if n := CountOnsets(decisions); n != 2 {
			t.Errorf("%d onsets, want 2 — the pause ended one run and the following "+
				"speech began another", n)
		}
	})
}

// TestVAD_RunDurationExcludesTheHangover guards a systematic overstatement.
//
// The hangover is silence the detector waited through, not speech the caller
// produced. Including it would make every utterance MinSilence longer than it
// was, and that error would flow straight into the endpoint measurement.
func TestVAD_RunDurationExcludesTheHangover(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	const speechFrames = 20

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(speechFrames)...)
	frames = append(frames, g.Silence(40)...)

	decisions := rig.PushAll(t, frames)

	offset, _, ok := FirstOffset(decisions)
	if !ok {
		t.Fatal("the run never ended")
	}

	want := time.Duration(speechFrames) * cfg.FrameInterval
	if offset.RunDuration != want {
		t.Errorf("RunDuration = %s, want %s\n"+
			"a duration of %s would mean the %s hangover was counted as speech",
			offset.RunDuration, want, want+cfg.VAD.MinSilence, cfg.VAD.MinSilence)
	}
}

// TestVAD_RejectsNonSpeechSounds walks the sounds a naive energy detector
// answers the phone to.
func TestVAD_RejectsNonSpeechSounds(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	cases := []struct {
		name string
		// build produces the sound AFTER warm-up.
		build func(g *SignalGenerator) []media.Frame
		// wantOnsets is how many speech runs the sound may legitimately open.
		wantOnsets int
		why        string
	}{
		{
			name:       "a door slam",
			build:      func(g *SignalGenerator) []media.Frame { return g.Transient(0.9, 1) },
			wantOnsets: 0,
			why: "one loud frame is not a word; MinOnsetFrames requires the sound " +
				"to persist",
		},
		{
			name:       "a low rumble",
			build:      func(g *SignalGenerator) []media.Frame { return g.Tone(30, 0.3, 60) },
			wantOnsets: 0,
			why:        "a 30 Hz tone crosses zero far below the speech band",
		},
		{
			name:       "broadband hiss",
			build:      func(g *SignalGenerator) []media.Frame { return g.Noise(0.05, 60) },
			wantOnsets: 0,
			why:        "white noise crosses zero far above the speech band",
		},
		{
			name:       "a quiet fan",
			build:      func(g *SignalGenerator) []media.Frame { return g.Noise(0.002, 60) },
			wantOnsets: 0,
			why:        "steady background, whatever its level",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := newGen()
			rig := NewDetectorRig(t, cfg)

			frames := WarmupFrames(g, cfg)
			frames = append(frames, tc.build(g)...)
			frames = append(frames, g.Silence(20)...)

			decisions := rig.PushAll(t, frames)
			if n := CountOnsets(decisions); n != tc.wantOnsets {
				t.Errorf("%d speech onsets, want %d — %s\nstates: %v",
					n, tc.wantOnsets, tc.why, StateSequence(decisions))
			}
		})
	}
}

// TestVAD_SteadyToneIsReclassifiedWithinABoundedTime is the honest statement of
// a limitation, pinned as a bound rather than hidden.
//
// A tone that begins during silence passes the onset test, because at that
// instant NOTHING has had time to look steady — a word and a tone are
// genuinely indistinguishable by energy modulation in their first 40 ms. What
// can be guaranteed is that the mistake is caught quickly and bounded.
func TestVAD_SteadyToneIsReclassifiedWithinABoundedTime(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	warmup := WarmupFrames(g, cfg)
	frames := append(append([]media.Frame{}, warmup...), g.Tone(1000, 0.3, 100)...)

	decisions := rig.PushAll(t, frames)

	onset, onsetIdx, ok := FirstOnset(decisions)
	if !ok {
		// If a future change rejects the tone outright, that is strictly better
		// and this test should record it rather than fail.
		t.Log("the tone opened no speech run at all, which is better than the " +
			"bound this test guarantees")
		return
	}
	_ = onset

	offset, offsetIdx, ok := FirstOffset(decisions)
	if !ok {
		t.Fatalf("the tone opened a speech run at decision %d that never closed; "+
			"hold music would hold the turn open for the length of the call", onsetIdx)
	}
	if offset.Explanation.Verdict != verdictProfileLost {
		t.Errorf("the run closed with verdict %q, want %q",
			offset.Explanation.Verdict, verdictProfileLost)
	}

	// The bound: the short modulation window must fill, then the grace must
	// elapse. Anything inside that is the design working.
	held := time.Duration(offsetIdx-onsetIdx) * cfg.FrameInterval
	bound := time.Duration(cfg.VAD.ModulationWindowFrames+cfg.VAD.ProfileGraceFrames+
		cfg.VAD.MinOnsetFrames) * cfg.FrameInterval

	if held > bound {
		t.Errorf("a steady tone was treated as speech for %s; the bound is %s "+
			"(%d modulation frames + %d grace frames + %d onset frames)",
			held, bound, cfg.VAD.ModulationWindowFrames, cfg.VAD.ProfileGraceFrames,
			cfg.VAD.MinOnsetFrames)
	}
	t.Logf("a steady tone was treated as speech for %s before reclassification "+
		"(bound %s)", held, bound)
}

// TestVAD_DoesNotFlap is §3's explicit requirement.
//
// Flapping is speech → silence → speech: a caller's single utterance reported
// as several turns. It is measured in ONSETS, not in state entries, because the
// machine legitimately re-enters Speech after every stop closure and that is the
// hangover working rather than failing.
func TestVAD_DoesNotFlap(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	cases := []struct {
		name  string
		build func(g *SignalGenerator) []media.Frame
	}{
		{
			name:  "continuous speech",
			build: func(g *SignalGenerator) []media.Frame { return g.NormalSpeech(200) },
		},
		{
			name:  "rapid speech",
			build: func(g *SignalGenerator) []media.Frame { return g.RapidSpeech(200) },
		},
		{
			name: "speech on a noisy line",
			build: func(g *SignalGenerator) []media.Frame {
				return g.SpeechOverNoise(0.4, 0.02, 200)
			},
		},
		{
			name: "speech at the edge of audibility",
			build: func(g *SignalGenerator) []media.Frame {
				return g.Speech(0.006, defaultF0, defaultSyllableHz, 200)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := newGen()
			rig := NewDetectorRig(t, cfg)

			frames := WarmupFrames(g, cfg)
			frames = append(frames, tc.build(g)...)

			decisions := rig.PushAll(t, frames)

			if n := CountOnsets(decisions); n > 1 {
				t.Errorf("one continuous utterance produced %d onsets; the caller "+
					"said one thing and the engine reported %d turns", n, n)
			}
			if n := CountOffsets(decisions); n > 0 {
				t.Errorf("one continuous utterance produced %d offsets", n)
			}
		})
	}
}

// TestVAD_FlapRequiresTheFullOnsetAndHangoverBudget states the anti-flap bound
// arithmetically.
//
// A speech → silence → speech cycle cannot complete in fewer than the frames
// needed to confirm an onset plus the frames needed to elapse a hangover plus
// another onset confirmation. That bound is structural — it follows from the
// state machine having no shortcut edges — and this measures it.
func TestVAD_FlapRequiresTheFullOnsetAndHangoverBudget(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	// Alternate speech and silence as fast as the fixture allows.
	frames := WarmupFrames(g, cfg)
	for i := 0; i < 20; i++ {
		frames = append(frames, g.NormalSpeech(3)...)
		frames = append(frames, g.Silence(3)...)
	}

	decisions := rig.PushAll(t, frames)

	minCycle := cfg.VAD.MinOnsetFrames + cfg.frames(cfg.VAD.MinSilence)
	maxPossible := len(decisions) / minCycle

	onsets := CountOnsets(decisions)
	if onsets > maxPossible {
		t.Errorf("%d onsets in %d frames; the state machine cannot complete a "+
			"speech→silence→speech cycle in fewer than %d frames "+
			"(%d onset + %d hangover), so at most %d are possible",
			onsets, len(decisions), minCycle, cfg.VAD.MinOnsetFrames,
			cfg.frames(cfg.VAD.MinSilence), maxPossible)
	}
	t.Logf("%d onsets in %d frames of 60 ms alternation (bound %d)",
		onsets, len(decisions), maxPossible)
}

// TestVAD_EveryDecisionIsExplained is §14, enforced structurally.
func TestVAD_EveryDecisionIsExplained(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(30)...)
	frames = append(frames, g.Silence(20)...)
	frames = append(frames, g.Noise(0.05, 20)...)
	frames = append(frames, g.Tone(1000, 0.3, 40)...)
	frames = append(frames, g.Silence(20)...)

	decisions := rig.PushAll(t, frames)

	vocabulary := make(map[string]bool)
	for _, v := range allVerdictCodes() {
		vocabulary[v] = true
	}
	for _, v := range allReasonCodes() {
		vocabulary[v] = true
	}

	for i, d := range decisions {
		ex := d.Explanation

		if ex.Verdict == "" {
			t.Fatalf("decision %d (%s) carries no verdict; a decision nobody can "+
				"explain is one nobody can debug", i, d.State)
		}
		if !vocabulary[ex.Verdict] {
			t.Errorf("decision %d carries verdict %q, which is not a declared code",
				i, ex.Verdict)
		}

		// The thresholds must be carried, so a reader can interpret the
		// measurement without fetching the configuration.
		if ex.OnsetThresholdDB != cfg.VAD.OnsetThresholdDB {
			t.Errorf("decision %d reports onset threshold %g, configured %g",
				i, ex.OnsetThresholdDB, cfg.VAD.OnsetThresholdDB)
		}
		if ex.ReleaseThresholdDB != cfg.VAD.ReleaseThresholdDB {
			t.Errorf("decision %d reports release threshold %g, configured %g",
				i, ex.ReleaseThresholdDB, cfg.VAD.ReleaseThresholdDB)
		}

		// The comparisons must agree with the measurements they claim to be.
		if want := ex.ExcessDB >= ex.OnsetThresholdDB; ex.AboveOnset != want {
			t.Errorf("decision %d: AboveOnset=%v but excess %.2f vs threshold %.2f",
				i, ex.AboveOnset, ex.ExcessDB, ex.OnsetThresholdDB)
		}
		if want := ex.ZCRInBand && ex.Modulated; ex.SpeechProfile != want {
			t.Errorf("decision %d: SpeechProfile=%v but ZCRInBand=%v Modulated=%v",
				i, ex.SpeechProfile, ex.ZCRInBand, ex.Modulated)
		}

		if d.Confidence < 0 || d.Confidence > 1 {
			t.Errorf("decision %d reports confidence %g, outside [0,1]", i, d.Confidence)
		}
		if ex.String() == "" {
			t.Errorf("decision %d renders an empty explanation", i)
		}
	}
}

// TestVAD_ConfidenceFormula checks the documented arithmetic directly.
//
// The formula is stated in the doc comment on SpeechDetector.confidence so it
// can be argued with; this pins it so it cannot drift away from the
// documentation silently.
func TestVAD_ConfidenceFormula(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	d, err := NewSpeechDetector(cfg, rt.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	onset := cfg.VAD.OnsetThresholdDB

	cases := []struct {
		name      string
		state     VADState
		excessDB  float64
		floorConf float64
		want      float64
	}{
		// At exactly the onset threshold the detector is on the line and says
		// so: evidence 0.5.
		{"speech exactly at the threshold", VADSpeech, onset, 1.0, 0.5},
		// At twice the threshold above the floor, evidence saturates.
		{"speech well above the threshold", VADSpeech, 3 * onset, 1.0, 1.0},
		// At the floor itself, evidence is zero.
		{"speech at the floor", VADSpeech, 0, 1.0, 0.0},
		// Silence reports the complement.
		{"silence at the floor", VADSilence, 0, 1.0, 1.0},
		{"silence at the threshold", VADSilence, onset, 1.0, 0.5},
		// AN UNCERTAIN FLOOR MAKES EVERY DECISION UNCERTAIN. A detector
		// reporting 0.95 while its reference rests on four frames of a building
		// site would be lying with precision.
		{"strong evidence, half-confident floor", VADSpeech, 3 * onset, 0.5, 0.5},
		{"strong evidence, no floor confidence", VADSpeech, 3 * onset, 0.0, 0.0},
		// Uncertain asserts nothing at all.
		{"uncertain", VADUncertain, 3 * onset, 1.0, 0.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := d.confidence(tc.state, Explanation{
				ExcessDB:        tc.excessDB,
				FloorConfidence: tc.floorConf,
			})
			if got != tc.want {
				t.Errorf("confidence(%s, excess=%.1f, floorConf=%.2f) = %.4f, want %.4f",
					tc.state, tc.excessDB, tc.floorConf, got, tc.want)
			}
		})
	}
}

// TestVAD_ConfidenceRisesWithEvidenceWithinASession is the integration form.
//
// # Why the comparison is made WITHIN one session
//
// Confidence is evidence multiplied by the noise floor's own confidence, and
// the second term is not a constant across sessions. A loud talker's first two
// frames reach the estimator before the one-frame gate has classified them, and
// two loud frames in a hundred-frame background ring look exactly like a burst
// of background noise — which is not a bug, it is how NoiseTransient is
// detected in the first place. A loud session therefore legitimately carries a
// LOWER floor stability than a quiet one.
//
// An earlier version of this test compared a loud session against a quiet one
// and failed for that reason: it was measuring floor stability while claiming
// to measure evidence.
//
// Comparing pairs of frames that share a floor confidence isolates the evidence
// term against real measured data, which is what this is meant to check.
func TestVAD_ConfidenceRisesWithEvidenceWithinASession(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	const background = 0.02

	frames := g.Noise(background, cfg.Noise.ConfidenceFrames+cfg.Noise.WarmupFrames)
	frames = append(frames, g.SpeechOverNoise(0.5, background, 120)...)

	decisions := rig.PushAll(t, frames)

	var speech []VADDecision
	for _, d := range decisions {
		if d.State == VADSpeech {
			speech = append(speech, d)
		}
	}
	if len(speech) < 20 {
		t.Fatalf("only %d speech frames; not enough to compare", len(speech))
	}

	var compared, varied int
	for i := range speech {
		for j := range speech {
			a, b := speech[i], speech[j]
			if a.Explanation.FloorConfidence != b.Explanation.FloorConfidence {
				continue
			}
			compared++
			if a.Explanation.ExcessDB > b.Explanation.ExcessDB {
				if a.Confidence < b.Confidence {
					t.Fatalf("with an identical floor confidence of %.4f, a frame at "+
						"%.2f dB reported confidence %.4f while a frame at %.2f dB "+
						"reported %.4f — confidence must not fall as evidence rises",
						a.Explanation.FloorConfidence,
						a.Explanation.ExcessDB, a.Confidence,
						b.Explanation.ExcessDB, b.Confidence)
				}
				if a.Confidence > b.Confidence {
					varied++
				}
			}
		}
	}

	if compared == 0 {
		t.Fatal("no two speech frames shared a floor confidence; the comparison " +
			"never ran")
	}
	if varied == 0 {
		t.Error("confidence never varied with evidence across any comparable pair; " +
			"the evidence term may have saturated for the whole run")
	}
	t.Logf("%d comparable pairs, %d showing confidence rising with evidence",
		compared, varied)
}

// TestVAD_SynthesisedFramesAreNotEvidence proves a network gap cannot end a
// turn.
//
// Phase 11B fills a lost frame with zeros it generated. Treating that invented
// silence as measured silence would end the caller's turn because a packet went
// missing, which is a conversation failure caused by a transport hiccup.
func TestVAD_SynthesisedFramesAreNotEvidence(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	g := newGen()
	rig := NewDetectorRig(t, cfg)

	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(20)...)

	// A burst of gap-fill in the middle of the utterance, exactly as 11B would
	// produce it — long enough to outlast the hangover if it were believed.
	gap := g.Silence(cfg.frames(cfg.VAD.MinSilence) + 5)
	for i := range gap {
		gap[i].Flags = media.FlagSilence | media.FlagDiscontinuity
	}
	frames = append(frames, gap...)
	frames = append(frames, g.NormalSpeech(20)...)

	decisions := rig.PushAll(t, frames)

	// The gap frames are refused as evidence, so the detector holds its
	// pre-gap state rather than treating the hole as a pause the caller took.
	if n := CountOnsets(decisions); n != 1 {
		t.Errorf("%d onsets across a synthesised gap, want 1 — a lost packet must "+
			"not split one utterance into two turns\nstates: %v",
			n, StateSequence(decisions))
	}
}

// TestVAD_IsDeterministic is §14's replay requirement at the detector level.
func TestVAD_IsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())

	build := func() []media.Frame {
		g := newGen()
		frames := WarmupFrames(g, cfg)
		frames = append(frames, g.NormalSpeech(30)...)
		frames = append(frames, g.Silence(20)...)
		frames = append(frames, g.SpeechOverNoise(0.3, 0.02, 30)...)
		frames = append(frames, g.Noise(0.05, 20)...)
		return frames
	}

	first := NewDetectorRig(t, cfg).PushAll(t, build())
	second := NewDetectorRig(t, cfg).PushAll(t, build())

	if len(first) != len(second) {
		t.Fatalf("decision counts differ: %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("decision %d diverged on replay\n first: %+v\nsecond: %+v",
				i, first[i], second[i])
		}
	}
}

func TestNewSpeechDetector_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := TestConfig(testFormat())
	cfg.VAD.ReleaseThresholdDB = cfg.VAD.OnsetThresholdDB

	if _, err := NewSpeechDetector(cfg, rt.NewFakeClock(time.Now())); err == nil {
		t.Error("a configuration with no hysteresis was accepted")
	}
}

func TestNewSpeechDetector_DefaultsItsClock(t *testing.T) {
	t.Parallel()

	d, err := NewSpeechDetector(TestConfig(testFormat()), nil)
	if err != nil {
		t.Fatalf("a nil clock was refused: %v", err)
	}
	if d.State() != VADUncertain {
		t.Errorf("initial state %s, want %s", d.State(), VADUncertain)
	}
}

func BenchmarkSpeechDetector_Observe(b *testing.B) {
	cfg := TestConfig(testFormat())
	rig := NewDetectorRig(b, cfg)

	g := newGen()
	frames := WarmupFrames(g, cfg)
	frames = append(frames, g.NormalSpeech(128)...)

	// Warm the pipeline so the benchmark measures steady state rather than
	// convergence.
	rig.PushAll(b, frames)

	speech := g.NormalSpeech(128)
	views := make([]SignalView, 0, len(speech))
	for _, f := range speech {
		feat, err := rig.Frames.Analyze(f)
		if err != nil {
			b.Fatal(err)
		}
		views = append(views, rig.Signal.Observe(feat, true))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rig.VAD.Observe(views[i%len(views)])
	}
}

// BenchmarkDetectorRig_FullChain measures the complete per-frame cost:
// measurement, window statistics, noise adaptation and the state machine.
func BenchmarkDetectorRig_FullChain(b *testing.B) {
	cfg := TestConfig(testFormat())
	rig := NewDetectorRig(b, cfg)

	g := newGen()
	rig.PushAll(b, WarmupFrames(g, cfg))
	frames := g.NormalSpeech(128)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rig.Push(b, frames[i%len(frames)])
	}
}
