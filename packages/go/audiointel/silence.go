package audiointel

import "time"

// SilenceReport classifies one stretch of silence.
type SilenceReport struct {
	// Class is what kind of silence this is.
	Class SilenceClass

	// Duration is how long it has lasted, in media time.
	Duration time.Duration

	// Started is set on the frame where the silence began.
	Started bool

	// Extended is set when the silence crossed into a longer class.
	Extended bool

	// Previous is the class before this frame, for an extension.
	Previous SilenceClass
}

// SilenceClassifier names stretches of silence by duration and position.
//
// # These are timing signals, and the naming is a hazard worth stating twice
//
// SilenceInterSentence means "a pause of between InterWordMax and
// InterSentenceMax, occurring after speech has been heard". It does NOT mean a
// sentence ended. This engine has no access to words, and acoustic silence
// carries no semantics whatsoever — a speaker pausing to think mid-clause and a
// speaker finishing a sentence produce the same silence, and nothing
// measurable distinguishes them.
//
// The names are operationally useful and semantically empty. §10 of the phase
// brief requires that distinction be made explicitly rather than left for a
// consumer to infer from a suggestive constant name, so it is repeated here, on
// [SilenceClass], and in the documentation.
//
// # One consequence is worth reading before tuning anything
//
// With the endpoint window at ADR-0011's 250 ms, InterSentenceMax cannot exceed
// it — configuration validation refuses that — because a pause longer than the
// endpoint window IS an endpoint. So at the default settings THIS ENGINE CANNOT
// DISTINGUISH A CLAUSE BOUNDARY FROM A TURN END. Anything long enough to be one
// is long enough to be the other. That is a property of a 250 ms endpoint
// window, not a defect in the classifier, and a deployment that needs the
// distinction must lengthen the window and accept the latency.
//
// Not safe for concurrent use. One classifier per session.
type SilenceClassifier struct {
	cfg      SilenceConfig
	endpoint time.Duration

	// sawSpeech latches once the session has heard any speech. Before that,
	// every silence is the initial one.
	sawSpeech bool

	// agentSpokeLast records that the agent held the floor most recently, which
	// is what makes the following silence positional rather than merely long.
	agentSpokeLast bool

	// current is the class reported for the open stretch, so an extension into
	// a longer class can be reported exactly once.
	current SilenceClass
	open    bool
}

// NewSilenceClassifier builds a classifier from the runtime configuration.
//
// Takes the whole [Config] because the endpoint window is an input: the
// boundary between an inter-sentence pause and an endpoint silence is the
// endpoint window itself, and duplicating it in [SilenceConfig] would let the
// two drift apart.
func NewSilenceClassifier(cfg Config) (*SilenceClassifier, error) {
	if problems := cfg.Silence.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	return &SilenceClassifier{
		cfg:      cfg.Silence,
		endpoint: cfg.Endpoint.SilenceWindow,
		current:  SilenceInitial,
	}, nil
}

// NoteSpeech records that speech was heard, ending the initial period.
func (s *SilenceClassifier) NoteSpeech() {
	s.sawSpeech = true
	s.agentSpokeLast = false
	s.open = false
}

// NoteAgentFinished records that the agent stopped speaking.
//
// What makes the following silence a thinking pause rather than merely a pause:
// the classification is POSITIONAL. It says the caller has not started
// responding yet. It does not say they are thinking, and nothing here could
// know that.
func (s *SilenceClassifier) NoteAgentFinished() {
	s.agentSpokeLast = true
	s.open = false
}

// Observe classifies the open silence given its duration.
//
// Called with the voice activity detector's silence duration on every frame
// where no speech is active. A duration of zero closes the open stretch.
func (s *SilenceClassifier) Observe(duration time.Duration) SilenceReport {
	if duration <= 0 {
		s.open = false
		return SilenceReport{Class: s.current, Duration: 0}
	}

	class := s.classify(duration)

	report := SilenceReport{Class: class, Duration: duration, Previous: s.current}

	if !s.open {
		s.open = true
		report.Started = true
	} else if class != s.current {
		report.Extended = true
	}
	s.current = class

	return report
}

// classify picks the class for a duration.
//
// Position first, then duration. A silence before anybody has spoken is the
// initial one however long it runs, because "the call connected and nobody has
// said anything" is a different operational situation from "the caller stopped
// mid-conversation" even when the two last the same time.
func (s *SilenceClassifier) classify(d time.Duration) SilenceClass {
	if !s.sawSpeech {
		return SilenceInitial
	}

	// A long silence outranks the positional thinking classification: past
	// LongSilenceMin the interesting fact is that the line has gone dead, not
	// who spoke last.
	if d >= s.cfg.LongSilenceMin {
		return SilenceLong
	}

	if s.agentSpokeLast && d >= s.cfg.ThinkingMin {
		return SilenceThinking
	}

	switch {
	case d <= s.cfg.InterWordMax:
		return SilenceInterWord
	case d <= s.cfg.InterSentenceMax:
		return SilenceInterSentence
	case d >= s.endpoint:
		return SilenceEndpoint
	default:
		// Past InterSentenceMax but short of the endpoint window. Reachable
		// only when a deployment configures InterSentenceMax strictly below the
		// endpoint window; at the defaults they are equal and this is dead.
		return SilenceInterSentence
	}
}

// Current returns the class of the open stretch.
func (s *SilenceClassifier) Current() SilenceClass { return s.current }

// SawSpeech reports whether any speech has been heard in this session.
func (s *SilenceClassifier) SawSpeech() bool { return s.sawSpeech }

// Reset returns the classifier to its initial state.
func (s *SilenceClassifier) Reset() {
	s.sawSpeech = false
	s.agentSpokeLast = false
	s.current = SilenceInitial
	s.open = false
}
