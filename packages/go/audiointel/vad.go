package audiointel

import (
	"fmt"
	"sort"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// vadTransitions is THE declaration of every legal voice activity move.
//
// The single source of truth for the detection model. Nothing else decides
// whether a transition is allowed, and no code path assigns a state directly —
// runtime.FSM refuses anything absent here.
//
//	uncertain → silence ⇄ candidate_speech → speech ⇄ candidate_silence
//	            silence ⇄ noise ← speech
//	            noise → candidate_speech
//
// # Why Uncertain has exactly one exit
//
// The noise floor's convergence LATCHES: once warm-up completes it never
// un-completes. An edge back into Uncertain would therefore be unreachable, and
// an unreachable declared edge is worse than an absent one — the state machine
// test would have to fabricate a way to exercise it, which means testing a
// path production can never take. See ENGINEERING_AUDIT.md D-1.
//
// # Why Speech cannot reach Silence directly
//
// Speech leaves toward quiet only through CandidateSilence. That is the
// hangover, expressed structurally: no code path can end an utterance on one
// quiet frame, because the state machine has no edge for it.
func vadTransitions() map[VADState][]VADState {
	return map[VADState][]VADState{
		// The floor has converged; there is now something to compare against.
		VADUncertain: {VADSilence},

		// Energy appeared. Whether it looks like speech decides which way.
		VADSilence: {VADCandidateSpeech, VADNoise},

		// Confirmed, abandoned, or reclassified as a non-speech sound.
		VADCandidateSpeech: {VADSpeech, VADSilence, VADNoise},

		// THE HANGOVER, STRUCTURALLY: there is no edge to Silence, so no code
		// path exists by which one quiet frame ends an utterance.
		//
		// The edge to Noise is the escape hatch documented on SpeechDetector —
		// steady noise that began during silence passes the onset test, because
		// at that instant nothing has had time to look steady, and without this
		// it would hold the detector in Speech for as long as it continued.
		VADSpeech: {VADCandidateSilence, VADNoise},

		// Resumed within the hangover, or the hangover elapsed.
		VADCandidateSilence: {VADSpeech, VADSilence},

		// The background went quiet, or something speech-shaped emerged from it.
		VADNoise: {VADSilence, VADCandidateSpeech},
	}
}

// VADTransitionsFrom returns the states reachable from one state, sorted.
//
// Exported so a caller can ask before assuming, and so the documentation
// generator and the state machine test read the same table the detector does.
func VADTransitionsFrom(s VADState) []VADState {
	out := append([]VADState(nil), vadTransitions()[s]...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CanVADTransition reports whether from → to is declared.
func CanVADTransition(from, to VADState) bool {
	for _, allowed := range vadTransitions()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Explanation
// ---------------------------------------------------------------------------

// Explanation is why the detector decided what it decided.
//
// # §14 requires every decision to be explainable, and this is that
//
// A voice agent that cannot say why it decided somebody stopped talking cannot
// be debugged when it is wrong — and it will be wrong, on a noisy line, on a
// speaker who pauses mid-sentence, on a language whose rhythm the thresholds
// were not tuned for. Every field here is either a measurement or the threshold
// it was compared against, so a wrong decision reduces to a pair of numbers
// somebody can look at.
//
// Scalars and booleans. No audio, and nothing unbounded.
type Explanation struct {
	// Audible reports whether the frame cleared VADConfig.AbsoluteSilenceRMS.
	//
	// The first gate, and it is absolute rather than relative. On a digitally
	// silent line the floor sits at its clamp and a frame at twice that clamp
	// is +6 dB — enough to look like speech to a purely relative test, and
	// inaudible in fact.
	Audible bool

	// ExcessDB is how far the frame sits above the noise floor.
	ExcessDB float64

	// OnsetThresholdDB and ReleaseThresholdDB are what it was compared against.
	// Both are carried so a reader does not have to fetch the configuration to
	// interpret the excess.
	OnsetThresholdDB   float64
	ReleaseThresholdDB float64

	// AboveOnset and AboveRelease are the two comparisons. The gap between the
	// thresholds is the hysteresis.
	AboveOnset   bool
	AboveRelease bool

	// ZCR and ZCRInBand are the tone-and-hiss rejector.
	ZCR       float64
	ZCRInBand bool

	// Modulation and Modulated are the stationary-noise rejector, measured over
	// the SHORT modulation window — see VADConfig.ModulationWindowFrames for
	// why the long one cannot be used at an onset.
	Modulation float64
	Modulated  bool

	// WindowModulation is the same statistic over the full history. Carried for
	// the reader rather than the decision: when the two disagree, the sound
	// recently changed character, and that is usually the interesting part.
	WindowModulation float64

	// SpeechProfile is ZCRInBand and Modulated together: the frame looks like
	// speech rather than like a tone or a fan.
	SpeechProfile bool

	// FloorConverged and FloorConfidence describe the estimate everything above
	// was measured against. A confident decision against an unconfident floor
	// is not a confident decision.
	FloorConverged  bool
	FloorConfidence float64

	// OnsetFrames is how many consecutive frames have supported this onset.
	OnsetFrames int

	// SilenceHeld is how long sub-threshold audio has persisted, in media time.
	SilenceHeld time.Duration

	// Verdict is the bounded reason code for the state chosen.
	Verdict string
}

// String renders the explanation as a single reviewable line.
func (e Explanation) String() string {
	return fmt.Sprintf(
		"excess=%.1fdB (onset %.1f, release %.1f) audible=%t zcr=%.3f/%t mod=%.2f/%t "+
			"profile=%t onset_frames=%d silence=%s floor_conf=%.2f verdict=%s",
		e.ExcessDB, e.OnsetThresholdDB, e.ReleaseThresholdDB, e.Audible,
		e.ZCR, e.ZCRInBand, e.Modulation, e.Modulated, e.SpeechProfile,
		e.OnsetFrames, e.SilenceHeld.Round(time.Millisecond), e.FloorConfidence,
		e.Verdict)
}

// ---------------------------------------------------------------------------
// Decision
// ---------------------------------------------------------------------------

// VADDecision is the detector's verdict for one frame.
type VADDecision struct {
	// State is the voice activity state after this frame.
	State VADState

	// Previous is the state before it.
	Previous VADState

	// Changed reports whether the frame moved the machine.
	Changed bool

	// OnsetConfirmed reports that a NEW speech run began on this frame.
	//
	// # Not the same as entering VADSpeech, and the difference is the point
	//
	// Speech is re-entered every time a caller resumes after a stop closure or
	// an inter-word gap, because the hangover holds the run open across them.
	// During ordinary connected speech over a noisy line the machine can
	// oscillate between Speech and CandidateSilence several times a second, and
	// every one of those is the SAME utterance.
	//
	// A consumer that emitted speech_started on entering VADSpeech would emit
	// one per syllable. This flag is set only on the CandidateSpeech → Speech
	// promotion, so exactly one onset is reported per run, and §5's "do not
	// emit duplicate onset events" is satisfied structurally rather than by a
	// deduplication somebody has to remember to write.
	OnsetConfirmed bool

	// OffsetConfirmed reports that the speech run ENDED on this frame — the
	// hangover elapsed without speech resuming.
	//
	// Exactly one per run, for the same reason.
	OffsetConfirmed bool

	// RunDuration is the length of the run that just ended, set only on the
	// frame where OffsetConfirmed is true.
	//
	// Measured from the backdated onset to the first sub-threshold frame, so it
	// EXCLUDES the hangover. The hangover is silence the detector waited
	// through, not speech the caller produced, and including it would overstate
	// every utterance by MinSilence.
	RunDuration time.Duration

	// Confidence is how sure the detector is of State, in [0,1].
	Confidence float64

	// SpeechStart is the media time speech began, backdated to the FIRST frame
	// of the run rather than the frame that confirmed it. Zero when no speech
	// run is open.
	SpeechStart time.Duration

	// SpeechDuration is how long the open speech run has lasted, in media time.
	SpeechDuration time.Duration

	// SilenceStart is the media time the current silence began.
	SilenceStart time.Duration

	// SilenceDuration is how long the current silence has lasted.
	//
	// Measured from the FIRST sub-threshold frame, not from the moment the
	// hangover elapsed. That is what makes it directly comparable with
	// EndpointPolicy.SilenceWindow and therefore with ADR-0011 hop 1.
	SilenceDuration time.Duration

	// Explanation is why.
	Explanation Explanation
}

// SpeechActive reports whether a recogniser should be receiving this audio.
func (d VADDecision) SpeechActive() bool { return d.State.Active() }

// ---------------------------------------------------------------------------
// SpeechDetector
// ---------------------------------------------------------------------------

// SpeechDetector is the voice activity state machine.
//
// # Three features to ENTER speech, one to STAY in it
//
// This asymmetry is the most consequential decision in the detector and it is
// deliberate.
//
// Entering costs a false trigger if it is wrong, so entering is strict: the
// frame must be audible in absolute terms, clear the onset threshold above the
// adaptive floor, sit inside the speech zero-crossing band, and belong to a
// window with real energy modulation. A tone fails the third test. A fan fails
// the fourth. A door slam fails the consecutive-frame requirement.
//
// STAYING costs a clipped word if it is wrong, and a clipped word makes the
// caller repeat themselves — much worse. So once speech is confirmed, only the
// energy test applies, at the lower release threshold, with the hangover behind
// it. A speaker who goes momentarily monotone, or whose window fills with two
// seconds of unbroken vowel, does not get cut off because a rejector designed
// to keep fans out started rejecting them.
//
// Not safe for concurrent use. One detector per session.
type SpeechDetector struct {
	cfg   VADConfig
	fsm   *rt.FSM[VADState]
	clock rt.Clock

	// onsetFrames counts consecutive frames supporting the open onset.
	onsetFrames int

	// noiseFrames counts consecutive above-floor frames with a non-speech
	// profile, before speech is confirmed.
	noiseFrames int

	// profileFailFrames counts consecutive frames whose speech profile failed
	// while ALREADY in Speech. Reset by any frame that passes.
	profileFailFrames int

	// runStart is the media time of the first frame of the open candidate run,
	// held so a confirmed onset can be BACKDATED to where speech actually
	// began rather than to where the detector became sure.
	runStart time.Duration

	// speechStart is the backdated start of the confirmed speech run.
	speechStart time.Duration
	inSpeech    bool

	// silenceStart is the media time the current silence began.
	silenceStart time.Duration
	inSilence    bool

	// lastFalseTrigger records why the most recent candidate onset was
	// abandoned, for the session to count.
	lastFalseTrigger string
}

// NewSpeechDetector builds the voice activity state machine for one session.
func NewSpeechDetector(cfg Config, clock rt.Clock) (*SpeechDetector, error) {
	if problems := cfg.VAD.validate(cfg.FrameInterval); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}

	fsm, err := rt.NewFSM(rt.FSMSpec[VADState]{
		Initial:     VADUncertain,
		Transitions: vadTransitions(),
		// No terminal states. Voice activity has no end — a session's detector
		// runs until the session closes, and declaring Silence terminal would
		// mean the first pause ended the call.
	}, clock)
	if err != nil {
		return nil, err
	}

	return &SpeechDetector{cfg: cfg.VAD, fsm: fsm, clock: clock}, nil
}

// State returns the current voice activity state.
func (d *SpeechDetector) State() VADState { return d.fsm.State() }

// EnteredCount reports how many times a state has been entered.
//
// The flap detector: a current-state read cannot distinguish a detector that
// settled in Speech from one that has entered it forty times in a second.
func (d *SpeechDetector) EnteredCount(s VADState) int { return d.fsm.EnteredCount(s) }

// LastFalseTrigger returns why the most recent candidate onset was abandoned,
// or the empty string if none has been.
func (d *SpeechDetector) LastFalseTrigger() string { return d.lastFalseTrigger }

// Observe folds one frame's signal view into the state machine.
//
// Runs inline on the caller's goroutine, allocates nothing, and returns the
// complete decision including its explanation.
func (d *SpeechDetector) Observe(v SignalView) VADDecision {
	ex := d.explain(v)
	prev := d.fsm.State()
	next := prev
	d.lastFalseTrigger = ""

	frameEnd := v.Frame.End()

	var onsetConfirmed, offsetConfirmed bool
	var runDuration time.Duration

	// # A frame Phase 11B invented is not evidence, so nothing is decided on it
	//
	// FlagSilence means the gap filler produced these samples, not the caller.
	// The obvious handling — treat them as quiet audio — is wrong in a way that
	// costs a conversation: a lost packet burst would run the hangover down and
	// end the caller's turn mid-sentence, and to the caller the agent simply
	// interrupts them whenever the network hiccups.
	//
	// Treating them as speech would be worse. So the machine HOLDS: no
	// transition, no timer advanced, no onset or offset reported. The detector
	// does not know what happened during the gap and says so.
	//
	// This is bounded by Phase 11B, which caps gap fill at MaxGapFill — 200 ms
	// by default. A source that vanishes for longer produces no frames at all,
	// and the session's continuity detector reports starvation instead.
	if v.Frame.Synthesised() {
		ex.Verdict = verdictNoEvidence
		ex.SilenceHeld = d.silenceHeld(frameEnd)
		return VADDecision{
			State:           prev,
			Previous:        prev,
			Confidence:      d.confidence(prev, ex),
			SpeechStart:     d.openSpeechStart(),
			SpeechDuration:  d.speechHeld(frameEnd),
			SilenceStart:    d.openSilenceStart(),
			SilenceDuration: ex.SilenceHeld,
			Explanation:     ex,
		}
	}

	switch prev {
	case VADUncertain:
		// Nothing is asserted until there is a floor to assert against.
		if v.Ready {
			next = VADSilence
			d.beginSilence(v.Frame.Timestamp)
			ex.Verdict = verdictFloorReady
		} else {
			ex.Verdict = ReasonFloorUncertain
		}

	case VADSilence:
		switch {
		case ex.Audible && ex.AboveOnset && ex.SpeechProfile:
			next = VADCandidateSpeech
			d.runStart = v.Frame.Timestamp
			d.onsetFrames = 1
			d.noiseFrames = 0
			ex.OnsetFrames = 1
			ex.Verdict = verdictOnsetOpened

		case ex.Audible && ex.AboveOnset:
			// Energy without a speech profile. Held for NoiseHoldFrames before
			// being called noise, so a single transient does not reclassify a
			// quiet line as a noisy one.
			d.noiseFrames++
			if d.noiseFrames >= d.cfg.NoiseHoldFrames {
				next = VADNoise
				ex.Verdict = verdictNoiseSustained
			} else {
				ex.Verdict = verdictNonSpeechEnergy
			}

		default:
			d.noiseFrames = 0
			ex.Verdict = verdictBelowThreshold
		}

	case VADCandidateSpeech:
		switch {
		case ex.Audible && ex.AboveOnset && ex.SpeechProfile:
			d.onsetFrames++
			ex.OnsetFrames = d.onsetFrames
			if d.onsetFrames >= d.cfg.MinOnsetFrames {
				next = VADSpeech
				// BACKDATED. The onset is where speech began, not where the
				// detector became sure of it — reporting the confirming frame
				// would place every onset MinOnsetFrames late and make the
				// speech duration short by the same amount.
				d.speechStart = d.runStart
				d.inSpeech = true
				d.inSilence = false
				d.profileFailFrames = 0
				onsetConfirmed = true
				ex.Verdict = ReasonOnsetConfirmed
			} else {
				ex.Verdict = verdictOnsetPending
			}

		case ex.Audible && ex.AboveRelease && !ex.SpeechProfile:
			d.noiseFrames++
			if d.noiseFrames >= d.cfg.NoiseHoldFrames {
				next = VADNoise
				d.abandonOnset(ex.zcrOrModulationReason())
				ex.Verdict = verdictNoiseSustained
			} else {
				ex.Verdict = verdictOnsetPending
			}

		default:
			next = VADSilence
			d.abandonOnset(ReasonOnsetTooShort)
			d.beginSilence(v.Frame.Timestamp)
			ex.Verdict = ReasonOnsetTooShort
		}

	case VADSpeech:
		switch {
		case !ex.Audible || !ex.AboveRelease:
			next = VADCandidateSilence
			d.beginSilence(v.Frame.Timestamp)
			d.profileFailFrames = 0
			ex.Verdict = verdictHangoverOpened

		case ex.SpeechProfile:
			// The ordinary case: energy above the release threshold and still
			// looking like speech. The grace counter resets.
			d.profileFailFrames = 0
			ex.Verdict = verdictSpeechContinues

		default:
			// Energy is present but the profile has failed. ONE TEST TO STAY
			// still governs the short term — a speaker whose delivery goes
			// briefly flat is not reclassified — but a failure sustained for
			// ProfileGraceFrames is not a speaker, it is a fan or a tone that
			// slipped through the onset test before it had time to look steady.
			d.profileFailFrames++
			if d.profileFailFrames >= d.cfg.ProfileGraceFrames {
				next = VADNoise
				// The run is over: what we were calling speech turns out to be
				// a steady sound. Reported as an offset so a consumer that
				// opened a turn on the onset can close it, rather than leaving
				// it open forever waiting for a hangover that will not come.
				runDuration = v.Frame.Timestamp - d.speechStart
				offsetConfirmed = true
				d.inSpeech = false
				d.profileFailFrames = 0
				ex.Verdict = verdictProfileLost
			} else {
				ex.Verdict = verdictSpeechContinues
			}
		}

	case VADCandidateSilence:
		switch {
		case ex.Audible && ex.AboveRelease:
			// Resumed inside the hangover. This is a stop closure or an
			// inter-word gap, not the end of an utterance, and NOTHING is
			// emitted — the speech run is the same run it always was.
			next = VADSpeech
			d.inSilence = false
			d.profileFailFrames = 0
			ex.Verdict = verdictSpeechResumed

		case frameEnd-d.silenceStart >= d.cfg.MinSilence:
			next = VADSilence
			// The run ended where the audio went quiet, not where the hangover
			// expired. Measuring to here instead would add MinSilence to every
			// utterance.
			runDuration = d.silenceStart - d.speechStart
			offsetConfirmed = true
			d.inSpeech = false
			ex.Verdict = ReasonHangoverElapsed

		default:
			ex.Verdict = verdictHangoverHeld
		}

	case VADNoise:
		switch {
		case ex.Audible && ex.AboveOnset && ex.SpeechProfile:
			next = VADCandidateSpeech
			d.runStart = v.Frame.Timestamp
			d.onsetFrames = 1
			d.noiseFrames = 0
			ex.OnsetFrames = 1
			ex.Verdict = verdictOnsetOpened

		case ex.Audible && ex.AboveRelease:
			ex.Verdict = verdictNoiseSustained

		default:
			next = VADSilence
			d.noiseFrames = 0
			d.beginSilence(v.Frame.Timestamp)
			ex.Verdict = verdictBelowThreshold
		}
	}

	changed := next != prev
	if changed {
		// A refused transition here is a bug in the switch above, not a
		// caller error: every branch moves only along a declared edge. It is
		// surfaced as a verdict rather than a panic, because a detector that
		// crashed a live call to report its own inconsistency would be worse
		// than one that held its previous state and said so.
		if _, err := d.fsm.To(next); err != nil {
			ex.Verdict = verdictTransitionRefused
			next = prev
			changed = false
		}
	}

	ex.SilenceHeld = d.silenceHeld(frameEnd)

	// A refused transition means nothing happened, so nothing is reported.
	if !changed {
		onsetConfirmed, offsetConfirmed = false, false
	}

	return VADDecision{
		State:           next,
		Previous:        prev,
		Changed:         changed,
		OnsetConfirmed:  onsetConfirmed,
		OffsetConfirmed: offsetConfirmed,
		RunDuration:     runDuration,
		Confidence:      d.confidence(next, ex),
		SpeechStart:     d.openSpeechStart(),
		SpeechDuration:  d.speechHeld(frameEnd),
		SilenceStart:    d.openSilenceStart(),
		SilenceDuration: ex.SilenceHeld,
		Explanation:     ex,
	}
}

// explain measures this frame against every configured threshold.
func (d *SpeechDetector) explain(v SignalView) Explanation {
	ex := Explanation{
		Audible:            v.Frame.RMS >= d.cfg.AbsoluteSilenceRMS,
		ExcessDB:           v.ExcessDB,
		OnsetThresholdDB:   d.cfg.OnsetThresholdDB,
		ReleaseThresholdDB: d.cfg.ReleaseThresholdDB,
		AboveOnset:         v.ExcessDB >= d.cfg.OnsetThresholdDB,
		AboveRelease:       v.ExcessDB >= d.cfg.ReleaseThresholdDB,
		ZCR:                v.Frame.ZCR,
		Modulation:         v.Modulation,
		WindowModulation:   v.Window.EnergyModulation,
		FloorConverged:     v.Noise.Converged,
		FloorConfidence:    v.Noise.Confidence,
		OnsetFrames:        d.onsetFrames,
	}

	ex.ZCRInBand = ex.ZCR >= d.cfg.ZCRMin && ex.ZCR <= d.cfg.ZCRMax
	ex.Modulated = ex.Modulation >= d.cfg.MinEnergyModulation
	ex.SpeechProfile = ex.ZCRInBand && ex.Modulated

	return ex
}

// zcrOrModulationReason names which rejector fired, for the false-trigger
// counter.
func (e Explanation) zcrOrModulationReason() string {
	if !e.ZCRInBand {
		return ReasonNonSpeechZCR
	}
	return ReasonStationary
}

// confidence scores how sure the detector is of the state it reported.
//
// # The formula, stated so it can be argued with
//
//	evidence = clamp01(0.5 + (excessDB − onsetDB) / (2 × onsetDB))
//
// At exactly the onset threshold the evidence is 0.5 — the detector is on the
// line and says so. At twice the threshold above the floor it saturates at 1;
// at the floor itself it is 0. Linear in decibels, which is linear in perceived
// loudness, which is the domain the thresholds are set in.
//
// That evidence is then multiplied by the noise floor's own confidence, because
// A CONFIDENT COMPARISON AGAINST AN UNCERTAIN REFERENCE IS NOT A CONFIDENT
// DECISION. A detector that reported 0.95 while its floor estimate rested on
// four frames of a building site would be lying with precision.
//
// Silence states report the complement: confidence that the state is SILENCE
// rises as the evidence for speech falls.
func (d *SpeechDetector) confidence(state VADState, ex Explanation) float64 {
	if state == VADUncertain {
		return 0
	}

	evidence := 0.5 + (ex.ExcessDB-d.cfg.OnsetThresholdDB)/(2*d.cfg.OnsetThresholdDB)
	evidence = clamp01(evidence)

	switch state {
	case VADSpeech, VADCandidateSpeech:
		return evidence * ex.FloorConfidence
	case VADSilence, VADCandidateSilence:
		return (1 - evidence) * ex.FloorConfidence
	case VADNoise:
		// The detector is confident there is ENERGY and confident it is not
		// speech-shaped. Both halves are needed, so the score is the evidence
		// that something is there times the certainty it failed the profile.
		return evidence * ex.FloorConfidence
	default:
		return 0
	}
}

func (d *SpeechDetector) beginSilence(at time.Duration) {
	if !d.inSilence {
		d.silenceStart = at
		d.inSilence = true
	}
}

func (d *SpeechDetector) abandonOnset(reason string) {
	d.lastFalseTrigger = reason
	d.onsetFrames = 0
	d.runStart = 0
}

func (d *SpeechDetector) openSpeechStart() time.Duration {
	if !d.inSpeech {
		return 0
	}
	return d.speechStart
}

func (d *SpeechDetector) speechHeld(frameEnd time.Duration) time.Duration {
	if !d.inSpeech {
		return 0
	}
	return frameEnd - d.speechStart
}

func (d *SpeechDetector) openSilenceStart() time.Duration {
	if !d.inSilence {
		return 0
	}
	return d.silenceStart
}

func (d *SpeechDetector) silenceHeld(frameEnd time.Duration) time.Duration {
	if !d.inSilence {
		return 0
	}
	return frameEnd - d.silenceStart
}

// Reset returns the detector to its initial state.
//
// # It rebuilds the state machine rather than assigning to it
//
// runtime.FSM has no reset, deliberately: a machine that could be moved to an
// arbitrary state on demand is not a machine whose transition table means
// anything. Rebuilding costs a map allocation, which is why this is a RECOVERY
// operation and not something the frame path does.
//
// A failure here leaves the detector untouched rather than half-reset. The only
// way NewFSM fails is a malformed table, and the table is a compile-time
// constant that already built successfully once — so this cannot fail in
// practice, and pretending to handle it by zeroing the detector would be worse
// than leaving it working.
func (d *SpeechDetector) Reset() {
	fsm, err := rt.NewFSM(rt.FSMSpec[VADState]{
		Initial:     VADUncertain,
		Transitions: vadTransitions(),
	}, d.clock)
	if err != nil {
		return
	}

	d.fsm = fsm
	d.onsetFrames = 0
	d.noiseFrames = 0
	d.profileFailFrames = 0
	d.runStart = 0
	d.speechStart, d.inSpeech = 0, false
	d.silenceStart, d.inSilence = 0, false
	d.lastFalseTrigger = ""
}

// clamp01 holds a value inside [0,1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Verdict codes. Bounded literals, declared alongside the reason codes in
// classifications.go so the whole label vocabulary stays reviewable in one
// place — these are the ones only the state machine emits.
const (
	verdictFloorReady        = "floor_converged"
	verdictOnsetOpened       = "onset_opened"
	verdictOnsetPending      = "onset_pending"
	verdictBelowThreshold    = "below_threshold"
	verdictNonSpeechEnergy   = "non_speech_energy"
	verdictNoiseSustained    = "noise_sustained"
	verdictSpeechContinues   = "speech_continues"
	verdictSpeechResumed     = "speech_resumed_in_hangover"
	verdictHangoverOpened    = "hangover_opened"
	verdictHangoverHeld      = "hangover_held"
	verdictNoEvidence        = "synthesised_frame"
	verdictProfileLost       = "speech_profile_lost"
	verdictTransitionRefused = "transition_refused"
)

// allVerdictCodes is every verdict the state machine can emit, for the
// vocabulary test.
func allVerdictCodes() []string {
	return []string{
		verdictFloorReady, verdictOnsetOpened, verdictOnsetPending,
		verdictBelowThreshold, verdictNonSpeechEnergy, verdictNoiseSustained,
		verdictSpeechContinues, verdictSpeechResumed, verdictHangoverOpened,
		verdictHangoverHeld, verdictProfileLost, verdictNoEvidence,
		verdictTransitionRefused,
	}
}
