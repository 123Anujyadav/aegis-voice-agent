package audiointel

import "fmt"

// QualityReport is the engine's verdict on whether the audio is usable.
type QualityReport struct {
	// Class is the current judgement.
	Class QualityClass

	// Previous is what it was before this frame.
	Previous QualityClass

	// Changed is set on the frame where the adopted class moved.
	Changed bool

	// Degraded is set when the class moved to a WORSE one.
	Degraded bool

	// Recovered is set when the class moved to a BETTER one.
	Recovered bool

	// Reason is the bounded code for the dominant problem, or empty when the
	// audio is good.
	//
	// THE DOMINANT ONE, not a list. An operator looking at degraded audio needs
	// to know what to fix first, and four simultaneous reasons is four things
	// nobody acts on.
	Reason string

	// Pending is the class the evidence currently supports, which may not yet
	// be the adopted one — hysteresis holds a change back until it persists.
	Pending QualityClass

	// LevelDBFS, SNRDB, ClipRatio and GapRatio are the measurements the
	// judgement rests on, carried so a report explains itself.
	LevelDBFS float64
	SNRDB     float64
	ClipRatio float64
	GapRatio  float64

	// CrestFactorDB is the peak-to-RMS spread — the flatness measure.
	CrestFactorDB float64
}

// String renders the report.
func (r QualityReport) String() string {
	s := fmt.Sprintf("quality %s level=%.1fdBFS snr=%.1fdB clip=%.4f gap=%.3f",
		r.Class, r.LevelDBFS, r.SNRDB, r.ClipRatio, r.GapRatio)
	if r.Reason != "" {
		s += " (" + r.Reason + ")"
	}
	return s
}

// QualityAnalyzer turns measurements into a bounded usability judgement.
//
// # Four classes, and every input is something that was measured
//
// Nothing here is a subjective judgement dressed up as a number. Signal level,
// clipping ratio, signal-to-noise estimate, crest factor and frame loss are all
// either measured by [FrameAnalyzer] or reported by Phase 11B, and each
// threshold is named in [QualityThresholds].
//
// # Hysteresis, because a flapping quality metric is worse than none
//
// A signal sitting exactly on a boundary would otherwise alternate between Good
// and Degraded on successive frames. On a dashboard that is a solid block of
// colour that means nothing; in an alerting rule it is a page every few seconds.
// A new class must persist for HysteresisFrames before it is adopted.
//
// The hysteresis applies in BOTH directions, including recovery. A deployment
// might prefer to adopt bad news immediately and good news slowly, and that
// asymmetry was considered and rejected: it makes the reported class depend on
// which direction it was approached from, so two identical lines report
// differently based on their history, and nobody can reproduce a complaint.
//
// Not safe for concurrent use. One analyser per session.
type QualityAnalyzer struct {
	cfg QualityThresholds

	// adopted is the class currently reported.
	adopted QualityClass

	// pending is the class the evidence supports, and pendingRun is how many
	// consecutive frames have supported it.
	pending    QualityClass
	pendingRun int

	// observed counts frames seen, so the analyser can report Unknown rather
	// than Good before it has enough evidence to judge.
	observed int
}

// NewQualityAnalyzer builds a classifier for one session.
func NewQualityAnalyzer(cfg QualityThresholds) (*QualityAnalyzer, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	return &QualityAnalyzer{
		cfg:     cfg,
		adopted: QualityUnknown,
		pending: QualityUnknown,
	}, nil
}

// Observe folds one frame's evidence in and returns the current judgement.
//
// gapRatio comes from the continuity detector; everything else from the signal
// view.
func (q *QualityAnalyzer) Observe(v SignalView, gapRatio float64) QualityReport {
	q.observed++

	report := QualityReport{
		Previous:      q.adopted,
		LevelDBFS:     v.Frame.LevelDBFS(),
		SNRDB:         v.ExcessDB,
		ClipRatio:     v.Window.ClipRatio,
		GapRatio:      gapRatio,
		CrestFactorDB: v.Frame.CrestFactorDB(),
	}

	candidate, reason := q.judge(v, gapRatio)
	report.Pending = candidate

	// Hysteresis: a candidate must repeat before it is adopted.
	if candidate == q.pending {
		q.pendingRun++
	} else {
		q.pending = candidate
		q.pendingRun = 1
	}

	if candidate != q.adopted && q.pendingRun >= q.cfg.HysteresisFrames {
		previous := q.adopted
		q.adopted = candidate

		report.Changed = true
		report.Degraded = candidate.Rank() > previous.Rank()
		report.Recovered = candidate.Rank() < previous.Rank()
	}

	report.Class = q.adopted
	if q.adopted != QualityGood && q.adopted != QualityUnknown {
		report.Reason = reason
	}

	return report
}

// judge picks the class this frame's evidence supports, and the dominant
// reason for it.
//
// # Ordered worst-first, and the order is the operational priority
//
// A signal can be clipped AND buried in noise AND arriving in pieces. Reporting
// all three leaves an operator with three things and no first one. The order
// here is what to fix first: destroyed samples cannot be recovered by anything
// downstream, a stream arriving in pieces is a network problem, and a poor
// signal-to-noise ratio is the caller's environment.
func (q *QualityAnalyzer) judge(v SignalView, gapRatio float64) (QualityClass, string) {
	// Not enough evidence yet. NOT the same as Good — reporting Good before
	// measuring would be a claim the engine has not earned.
	if !v.Ready || q.observed < q.cfg.HysteresisFrames {
		return QualityUnknown, ""
	}

	clip := v.Window.ClipRatio

	switch {
	case clip >= q.cfg.MaxClipRatio:
		// Samples destroyed at the source. Nothing downstream recovers a
		// flat-topped waveform.
		return QualityUnusable, ReasonClipping

	case gapRatio >= q.cfg.MaxGapRatio:
		// One frame in ten missing is not a stream that carries a
		// conversation.
		return QualityUnusable, ReasonFrameLoss

	case v.Window.MeanRMS < q.cfg.MinSignalRMS:
		// Too quiet for a recogniser to do anything useful with.
		return QualityPoor, ReasonLowLevel

	case v.ExcessDB < q.cfg.MinSNRDB:
		return QualityPoor, ReasonLowSNR

	case clip >= q.cfg.DegradedClipRatio:
		return QualityDegraded, ReasonClipping

	case gapRatio >= q.cfg.DegradedGapRatio:
		return QualityDegraded, ReasonFrameLoss

	case v.ExcessDB < q.cfg.DegradedSNRDB:
		return QualityDegraded, ReasonLowSNR

	case v.Window.MaxRMS > 0 && v.Frame.CrestFactorDB() < q.cfg.MinDynamicRange:
		// Suspiciously flat: a stuck codec, a constant tone, a dead line
		// carrying only its own noise. Real speech has a crest factor far above
		// this. Checked LAST because a flat signal is the least actionable of
		// the problems and the most likely to be a false alarm on a legitimate
		// steady sound.
		return QualityDegraded, ReasonFlatSignal

	default:
		return QualityGood, ""
	}
}

// Class returns the currently adopted judgement.
func (q *QualityAnalyzer) Class() QualityClass { return q.adopted }

// Reset returns the analyser to its initial state.
func (q *QualityAnalyzer) Reset() {
	q.adopted = QualityUnknown
	q.pending = QualityUnknown
	q.pendingRun = 0
	q.observed = 0
}
