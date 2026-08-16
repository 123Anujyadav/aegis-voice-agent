package conversation

import (
	"sync"
)

// ClarificationKind names why clarification is needed.
//
// The kind matters because it determines the SHAPE of the question, and the
// shape determines how the answer is interpreted. "Did you mean A or B" expects
// a disambiguation; "is that right" expects a yes/no; "what is the reference
// number" expects a slot value. A system that models only "ask again" cannot
// interpret any of them correctly.
type ClarificationKind int

const (
	// ClarifyNone means no clarification is required.
	ClarifyNone ClarificationKind = iota

	// ClarifyAmbiguous means two or more intents scored closely.
	ClarifyAmbiguous

	// ClarifyLowConfidence means the top intent is below the accept threshold
	// but above the reject threshold.
	ClarifyLowConfidence

	// ClarifyMissingSlot means the intent is understood but incomplete.
	ClarifyMissingSlot

	// ClarifyContradiction means new information conflicts with context
	// already established.
	ClarifyContradiction

	// ClarifyNoise means the audio was not usable speech.
	ClarifyNoise

	// ClarifyIncomplete means the utterance was truncated mid-thought.
	ClarifyIncomplete
)

// String renders the kind for logs and metric labels.
func (k ClarificationKind) String() string {
	switch k {
	case ClarifyAmbiguous:
		return "ambiguous"
	case ClarifyLowConfidence:
		return "low_confidence"
	case ClarifyMissingSlot:
		return "missing_slot"
	case ClarifyContradiction:
		return "contradiction"
	case ClarifyNoise:
		return "noise"
	case ClarifyIncomplete:
		return "incomplete"
	default:
		return "none"
	}
}

// Expectation returns the answer shape this clarification establishes.
func (k ClarificationKind) Expectation() Expectation {
	switch k {
	case ClarifyAmbiguous:
		return ExpectDisambiguation
	case ClarifyLowConfidence, ClarifyContradiction:
		return ExpectYesNo
	case ClarifyMissingSlot:
		return ExpectSlotValue
	default:
		// Noise and truncation are not questions about meaning; they are
		// requests to repeat, and a repeat is unconstrained.
		return ExpectNothing
	}
}

// Request is a decision that clarification is needed, and of what kind.
//
// It carries no question TEXT. Producing the words is prompt work, which the
// Phase 10B brief excludes; this engine decides that a question is needed, of
// which kind, about which slot, and the layer above renders it.
type Request struct {
	// Kind names why.
	Kind ClarificationKind
	// Slot is the target for ClarifyMissingSlot. Empty otherwise.
	Slot string
	// Candidates are the competing intents for ClarifyAmbiguous.
	Candidates []IntentName
	// Round is which attempt this is for the current subject, from 1.
	Round int
	// Final reports that the budget is now spent and this is the last attempt.
	Final bool
}

// ClarificationConfig tunes the clarification engine.
type ClarificationConfig struct {
	// MaxRoundsPerSubject bounds attempts at the same thing. Distinct from the
	// persona's overall budget: asking three different questions is reasonable,
	// asking the same question three times is not.
	MaxRoundsPerSubject int

	// RepeatOnNoise bounds requests to repeat before giving up. Kept low: a
	// caller on a bad line does not get better by being asked a third time.
	RepeatOnNoise int
}

// DefaultClarificationConfig returns the configuration used unless overridden.
func DefaultClarificationConfig() ClarificationConfig {
	return ClarificationConfig{MaxRoundsPerSubject: 2, RepeatOnNoise: 2}
}

func (c ClarificationConfig) validate() []string {
	var p []string
	if c.MaxRoundsPerSubject < 1 {
		p = append(p, "clarification: MaxRoundsPerSubject must be at least 1")
	}
	if c.RepeatOnNoise < 1 {
		p = append(p, "clarification: RepeatOnNoise must be at least 1")
	}
	return p
}

// ClarificationEngine decides whether and how to clarify, and enforces budgets.
//
// THE BUDGET IS THE POINT. A clarification engine without one produces the
// single most recognisable voice-product failure: the loop where the system
// asks, mishears, asks again, and the caller hangs up. Every path here is
// bounded, and exhaustion escalates rather than repeating.
type ClarificationEngine struct {
	cfg     ClarificationConfig
	metrics *Metrics

	mu          sync.Mutex
	total       int
	perSubject  map[string]int
	noiseRounds int
}

// NewClarificationEngine constructs a clarification engine.
func NewClarificationEngine(cfg ClarificationConfig, metrics *Metrics) (*ClarificationEngine, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &ClarificationEngine{cfg: cfg, metrics: metrics, perSubject: make(map[string]int)}, nil
}

// Assess decides whether clarification is needed.
//
// It is a pure decision over the supplied facts — no clock, no external call —
// which is what makes the clarification ladder exhaustively table-testable.
//
// Order matters and encodes the diagnosis:
//
//	noise        — the audio was not speech. Nothing else can be true yet.
//	incomplete   — it was speech but was cut off. Also prior to meaning.
//	contradiction— it was understood and conflicts with what we know. This
//	               outranks ambiguity: a contradiction is a stronger signal
//	               than a close second candidate.
//	ambiguous    — understood, but two readings compete.
//	low conf     — understood weakly.
//	missing slot — understood, but incomplete in its parameters.
func (c *ClarificationEngine) Assess(u Utterance, in Intent, verdict IntentVerdict, contradicts bool) Request {
	switch {
	case verdict == IntentNoise:
		return Request{Kind: ClarifyNoise}
	case u.Truncated:
		return Request{Kind: ClarifyIncomplete}
	case contradicts:
		return Request{Kind: ClarifyContradiction}
	case verdict != IntentClarify:
		return Request{Kind: ClarifyNone}
	}

	// verdict is IntentClarify — establish which flavour.
	if len(in.Alternatives) > 1 && in.Margin() < 0.15 {
		names := make([]IntentName, 0, 2)
		for _, a := range in.Alternatives {
			names = append(names, a.Name)
			if len(names) == 2 {
				break
			}
		}
		return Request{Kind: ClarifyAmbiguous, Candidates: names}
	}
	if missing := in.MissingRequired(); len(missing) > 0 {
		// MissingRequired sorts, so the same incomplete intent always asks
		// about the same slot first. Without that the engine would ask about a
		// different field each time and never converge.
		return Request{Kind: ClarifyMissingSlot, Slot: missing[0]}
	}
	return Request{Kind: ClarifyLowConfidence}
}

// Reserve records a clarification attempt and reports whether it may proceed.
//
// It returns the request annotated with its round and finality, and false when
// the budget is spent. A false return means escalate; it never means ask again.
func (c *ClarificationEngine) Reserve(r Request, personaBudget int) (Request, bool) {
	if r.Kind == ClarifyNone {
		return r, true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if r.Kind == ClarifyNoise {
		if c.noiseRounds >= c.cfg.RepeatOnNoise {
			c.metrics.ClarificationGaveUp.Inc()
			return r, false
		}
		c.noiseRounds++
		c.total++
		r.Round = c.noiseRounds
		r.Final = c.noiseRounds >= c.cfg.RepeatOnNoise
		c.metrics.Clarifications.Inc(r.Kind.String())
		return r, true
	}

	if personaBudget > 0 && c.total >= personaBudget {
		c.metrics.ClarificationGaveUp.Inc()
		return r, false
	}

	subject := r.Kind.String()
	if r.Slot != "" {
		subject += ":" + r.Slot
	}
	if c.perSubject[subject] >= c.cfg.MaxRoundsPerSubject {
		// Asking the same thing again will not work. Give up on this subject
		// specifically rather than on the conversation.
		c.metrics.ClarificationGaveUp.Inc()
		return r, false
	}

	c.perSubject[subject]++
	c.total++
	r.Round = c.perSubject[subject]
	r.Final = c.perSubject[subject] >= c.cfg.MaxRoundsPerSubject ||
		(personaBudget > 0 && c.total >= personaBudget)

	c.metrics.Clarifications.Inc(r.Kind.String())
	return r, true
}

// Resolved clears the per-subject counter for a subject that has been answered.
//
// Without this, a caller who clarifies successfully and later hits the same
// ambiguity again would be refused, because the counter would still be at its
// limit from the earlier, resolved exchange.
func (c *ClarificationEngine) Resolved(r Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	subject := r.Kind.String()
	if r.Slot != "" {
		subject += ":" + r.Slot
	}
	delete(c.perSubject, subject)
	if r.Kind == ClarifyNoise {
		c.noiseRounds = 0
	}
}

// Used returns the total clarifications requested.
func (c *ClarificationEngine) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Complete records the final round count for the conversation.
func (c *ClarificationEngine) Complete() {
	c.mu.Lock()
	total := c.total
	c.mu.Unlock()
	c.metrics.ClarificationRounds.Observe(float64(total))
}
