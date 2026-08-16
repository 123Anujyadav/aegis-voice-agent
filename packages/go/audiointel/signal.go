package audiointel

import "fmt"

// SignalView is everything the detectors know about one frame.
//
// Assembled once per frame by [SignalAnalyzer] and passed by value to every
// detector. One assembly per frame rather than each detector recomputing what
// it needs: the window statistics cost a few hundred nanoseconds and five
// detectors want them, so computing once and sharing is the difference between
// 350 ns and 1.75 µs per frame.
//
// Scalars throughout. No payload reaches here — see [FrameFeatures].
type SignalView struct {
	// Frame is what this frame measured.
	Frame FrameFeatures

	// Window is the bounded history's statistics.
	Window SignalStats

	// Noise is the adaptive floor's current view.
	Noise NoiseEstimate

	// Modulation is the coefficient of variation of energy across the SHORT
	// modulation window, and it is what the speech-profile test reads.
	//
	// Distinct from Window.EnergyModulation, which spans the full history and
	// is the operator-facing statistic. See [FeatureWindow.RecentModulation]
	// for why the onset decision cannot use the long one.
	Modulation float64

	// ExcessDB is how far this frame's level sits above the noise floor.
	//
	// THE NUMBER EVERY SPEECH DECISION TURNS ON. Identical to Noise.SNRDB and
	// duplicated here deliberately: the detectors read it constantly and
	// "excess over the floor" is what they are actually asking, while "SNR" is
	// what an operator reading a dashboard is asking. Same measurement, two
	// audiences, and naming it twice costs eight bytes.
	ExcessDB float64

	// Ready reports whether the noise floor has converged and enough history
	// exists for the window statistics to mean anything.
	//
	// While false, every detector refuses to assert. That is the honest
	// behaviour at the start of a call: there is nothing to compare against
	// yet, and guessing would mean the first word of every conversation is
	// decided by luck.
	Ready bool
}

// String renders the view.
func (v SignalView) String() string {
	return fmt.Sprintf("signal %.1fdBFS excess=%.1fdB mod=%.2f zcr=%.3f %s ready=%t",
		v.Frame.LevelDBFS(), v.ExcessDB, v.Window.EnergyModulation,
		v.Frame.ZCR, v.Noise.Class, v.Ready)
}

// SignalAnalyzer turns a stream of frames into a stream of [SignalView].
//
// It owns the bounded feature window and the adaptive noise floor, and it
// enforces the ordering between them: the floor is updated using the PREVIOUS
// frame's speech verdict, then this frame's view is assembled against the
// updated floor.
//
// # Why the gate lags by one frame
//
// The noise floor must not observe speech, and whether a frame is speech is
// decided by comparing it against the noise floor. That is circular, and
// something has to break it.
//
// Breaking it with a one-frame lag is the least-bad option: the cost is that
// the very first frame of an utterance can reach the estimator ungated, and the
// benefit is that no detector has to run twice or guess. One frame at the
// default cadence is 20 ms, well inside the onset confirmation window, and the
// rise clamp bounds what a single ungated frame can do to about 0.12 dB —
// which the fast downward rate erases within a few frames of the next silence.
//
// The alternative — deciding speech first and updating the floor afterwards —
// simply moves the circularity: the decision would then be made against a floor
// that is one frame stale, which is the same lag wearing a different name.
//
// Not safe for concurrent use. One analyser per session.
type SignalAnalyzer struct {
	cfg    Config
	window *FeatureWindow
	noise  *NoiseAnalyzer

	// minWindowFrames is how much history the window statistics need before
	// they are trusted, derived from configuration at construction.
	minWindowFrames int
}

// NewSignalAnalyzer builds the shared analysis stage for one session.
func NewSignalAnalyzer(cfg Config) (*SignalAnalyzer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	window, err := NewFeatureWindow(cfg.SignalWindowFrames)
	if err != nil {
		return nil, err
	}
	noise, err := NewNoiseAnalyzer(cfg)
	if err != nil {
		return nil, err
	}

	// The modulation statistic is the one that needs history: a coefficient of
	// variation over three frames is a number, not a measurement. Requiring the
	// same span the noise floor warms up over keeps the two ready together, so
	// there is one moment the engine starts asserting rather than two.
	minWindow := cfg.Noise.WarmupFrames
	if minWindow > cfg.SignalWindowFrames {
		minWindow = cfg.SignalWindowFrames
	}

	return &SignalAnalyzer{
		cfg:             cfg,
		window:          window,
		noise:           noise,
		minWindowFrames: minWindow,
	}, nil
}

// Observe folds one frame in and returns the complete view.
//
// prevSpeechActive is the voice activity verdict for the PREVIOUS frame. See
// the type comment for why it lags.
func (s *SignalAnalyzer) Observe(f FrameFeatures, prevSpeechActive bool) SignalView {
	// The floor is updated FIRST, so this frame is judged against the most
	// recent background estimate rather than one that is a frame stale.
	noise := s.noise.Observe(f, prevSpeechActive)

	s.window.Push(f)
	stats := s.window.Stats()

	return SignalView{
		Frame:      f,
		Window:     stats,
		Noise:      noise,
		Modulation: s.window.RecentModulation(s.cfg.VAD.ModulationWindowFrames),
		ExcessDB:   noise.SNRDB,
		Ready:      noise.Converged && stats.Frames >= s.minWindowFrames,
	}
}

// RetractOnsetLeak tells the noise estimator that the frames it recorded
// between the start of an utterance and its confirmation were speech.
//
// Called by the caller — [DetectorRig], [Session] — on the frame where the
// voice activity detector reports OnsetConfirmed. It exists because the speech
// gate lags by one frame and this is the correction for that lag; see
// [NoiseAnalyzer.Retract] for what goes wrong without it.
//
// It is a separate call rather than something Observe does on its own because
// Observe runs BEFORE the detector that produces the verdict. Folding it in
// would mean the analyser asking the detector for a decision the detector has
// not made yet.
func (s *SignalAnalyzer) RetractOnsetLeak() {
	s.noise.Retract(s.cfg.VAD.MinOnsetFrames)
}

// Window returns the bounded feature history.
func (s *SignalAnalyzer) Window() *FeatureWindow { return s.window }

// Noise returns the adaptive floor estimator.
func (s *SignalAnalyzer) Noise() *NoiseAnalyzer { return s.noise }

// Reset returns the analyser to its initial state, keeping its storage.
func (s *SignalAnalyzer) Reset() {
	s.window.Reset()
	s.noise.Reset()
}
