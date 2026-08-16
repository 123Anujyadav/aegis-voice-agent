package conversation

import (
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// IntentName identifies a kind of user goal. Authored, stable, and used as a
// metric label — so it must be a closed vocabulary the deploying service owns,
// not free text derived from a model's output.
type IntentName string

// Reserved names the engine itself understands.
const (
	// IntentUnknown is classification producing nothing usable.
	IntentUnknown IntentName = "unknown"
	// IntentFallback is the configured catch-all.
	IntentFallback IntentName = "fallback"
	// IntentAffirm is a yes, recognised only when a confirmation is expected.
	IntentAffirm IntentName = "affirm"
	// IntentDeny is a no, recognised only when a confirmation is expected.
	IntentDeny IntentName = "deny"
)

// Slot is a named piece of information an intent needs.
type Slot struct {
	// Name identifies the slot.
	Name string
	// Filled reports whether a value is present. The value itself is NOT held
	// here: slot values are caller-derived and therefore SENSITIVE, and they
	// live in the context engine under its scoping and expiry rules.
	Filled bool
	// Confidence in the extracted value, 0..1.
	Confidence float64
	// Required reports whether the intent cannot proceed without it.
	Required bool
}

// Intent is a classified user goal.
//
// It carries no caller text. The engine reasons about the SHAPE of what was
// said — which intent, how confident, which slots are filled — and never about
// the words, which stay in the transcript where they are classified SENSITIVE.
type Intent struct {
	// Name identifies the goal.
	Name IntentName
	// Confidence is the classifier's confidence, 0..1.
	Confidence float64
	// Slots are the intent's parameters.
	Slots []Slot
	// Alternatives are other candidates, highest first. Ambiguity is detected
	// from the gap between the top two, so discarding them would make
	// ambiguity undetectable.
	Alternatives []Candidate
	// TurnID is the turn the intent was derived from.
	TurnID TurnID
	// At is when it was proposed.
	At time.Time
}

// Candidate is a scored alternative from classification.
type Candidate struct {
	Name       IntentName
	Confidence float64
}

// MissingRequired returns the names of unfilled required slots, in a stable
// order so a clarification sequence is deterministic.
func (i Intent) MissingRequired() []string {
	var out []string
	for _, s := range i.Slots {
		if s.Required && !s.Filled {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Complete reports whether every required slot is filled.
func (i Intent) Complete() bool { return len(i.MissingRequired()) == 0 }

// Margin returns the confidence gap to the nearest alternative. A small margin
// means ambiguity even when the top confidence is high, which is precisely the
// case a confidence threshold alone cannot catch.
func (i Intent) Margin() float64 {
	best := 0.0
	for _, a := range i.Alternatives {
		if a.Name != i.Name && a.Confidence > best {
			best = a.Confidence
		}
	}
	return i.Confidence - best
}

// IntentClassifier turns an utterance into candidate intents.
//
// THIS INTERFACE HAS NO IMPLEMENTATION IN THIS MODULE, DELIBERATELY.
//
// The Phase 10B brief specifies the intent engine as "framework only, no model
// implementation". Classification is a model concern; routing, confidence
// handling, lifecycle, transitions, fallback and validation are conversation
// concerns and are what this file implements.
//
// The parameter is deliberately opaque. Passing an Utterance rather than a
// string keeps the engine from ever touching caller text: it hands the
// utterance through and receives a shape back.
type IntentClassifier interface {
	// Classify returns candidates ordered highest-confidence first. An empty
	// result is legitimate and means "nothing recognised".
	Classify(u Utterance, expect Expectation) ([]Candidate, []Slot, error)
}

// Utterance is one completed caller contribution.
//
// Text is present because a classifier needs it, and it is the ONLY field in
// this package that carries caller content. It is classified SENSITIVE: it must
// never be logged, never be a metric label, and never appear in a span
// attribute. The engine passes it to the classifier and holds it no longer than
// the turn.
type Utterance struct {
	// Text is what the caller said. SENSITIVE.
	Text string
	// ASRConfidence is the recogniser's confidence, 0..1.
	ASRConfidence float64
	// Truncated reports that the utterance was cut off — endpointing fired
	// mid-word, or the caller was interrupted.
	Truncated bool
	// DurationMS is how long the caller spoke.
	DurationMS int
	// TurnID is the turn it belongs to.
	TurnID TurnID
}

// IntentState is a position in the intent lifecycle.
type IntentState int

const (
	// IntentProposed is classified but not yet validated.
	IntentProposed IntentState = iota
	// IntentValidated passed confidence and slot validation.
	IntentValidated
	// IntentActive is being pursued.
	IntentActive
	// IntentFulfilled completed successfully.
	IntentFulfilled
	// IntentAbandoned was given up on — usually a clarification budget spent.
	IntentAbandoned
	// IntentSuperseded was replaced by a new intent mid-conversation.
	IntentSuperseded
)

// String renders the lifecycle state for logs and metric labels.
func (s IntentState) String() string {
	switch s {
	case IntentValidated:
		return "validated"
	case IntentActive:
		return "active"
	case IntentFulfilled:
		return "fulfilled"
	case IntentAbandoned:
		return "abandoned"
	case IntentSuperseded:
		return "superseded"
	default:
		return "proposed"
	}
}

// IntentConfig tunes classification handling.
type IntentConfig struct {
	// AcceptThreshold is the confidence at or above which an intent is acted
	// on without clarification.
	AcceptThreshold float64

	// RejectThreshold is the confidence below which an intent is discarded
	// entirely rather than clarified. Between the two lies the clarification
	// band.
	RejectThreshold float64

	// AmbiguityMargin is the minimum gap to the nearest alternative. Below it
	// the intent is ambiguous regardless of absolute confidence.
	AmbiguityMargin float64

	// MinASRConfidence is the recogniser confidence below which the utterance
	// is treated as noise rather than misclassified.
	MinASRConfidence float64

	// Fallback is the intent used when nothing is recognised. Empty means no
	// fallback, and classification failure then produces ErrNoIntent.
	Fallback IntentName
}

// DefaultIntentConfig returns the configuration used unless overridden.
//
// The band between 0.45 and 0.75 is deliberately wide. On a noisy phone line
// with Indic-accented speech, a narrow band produces confident wrong answers,
// and asking one clarifying question costs far less than acting on a
// misclassification.
func DefaultIntentConfig() IntentConfig {
	return IntentConfig{
		AcceptThreshold:  0.75,
		RejectThreshold:  0.45,
		AmbiguityMargin:  0.15,
		MinASRConfidence: 0.40,
		Fallback:         IntentFallback,
	}
}

func (c IntentConfig) validate() []string {
	var p []string
	if c.AcceptThreshold <= 0 || c.AcceptThreshold > 1 {
		p = append(p, "intent: AcceptThreshold must be in (0,1]")
	}
	if c.RejectThreshold < 0 || c.RejectThreshold >= c.AcceptThreshold {
		p = append(p, "intent: RejectThreshold must be in [0, AcceptThreshold)")
	}
	if c.AmbiguityMargin < 0 || c.AmbiguityMargin > 1 {
		p = append(p, "intent: AmbiguityMargin must be in [0,1]")
	}
	if c.MinASRConfidence < 0 || c.MinASRConfidence > 1 {
		p = append(p, "intent: MinASRConfidence must be in [0,1]")
	}
	return p
}

// IntentVerdict is the engine's assessment of a classification.
type IntentVerdict int

const (
	// IntentAccept means act on it.
	IntentAccept IntentVerdict = iota
	// IntentClarify means confidence or completeness is insufficient.
	IntentClarify
	// IntentReject means discard it.
	IntentReject
	// IntentNoise means the audio was not usable speech.
	IntentNoise
)

// String renders the verdict for logs and metric labels.
func (v IntentVerdict) String() string {
	switch v {
	case IntentClarify:
		return "clarify"
	case IntentReject:
		return "reject"
	case IntentNoise:
		return "noise"
	default:
		return "accept"
	}
}

// IntentEngine routes, validates and tracks intents.
//
// It owns lifecycle and thresholds. It owns no model, and the classifier it
// calls is supplied by the deploying service.
type IntentEngine struct {
	cfg        IntentConfig
	classifier IntentClassifier
	clock      rt.Clock
	metrics    *Metrics

	mu      sync.RWMutex
	active  *Intent
	state   IntentState
	history []Intent
}

// NewIntentEngine constructs an intent engine.
//
// classifier may be nil. An engine with no classifier resolves every utterance
// to the fallback intent, which is what a deployment running deterministic
// flows only actually wants — and it means the engine is usable in test with no
// model at all.
func NewIntentEngine(cfg IntentConfig, classifier IntentClassifier, clock rt.Clock, metrics *Metrics) (*IntentEngine, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &IntentEngine{cfg: cfg, classifier: classifier, clock: clock, metrics: metrics}, nil
}

// Resolve classifies an utterance and returns the intent with a verdict.
//
// The order of checks is the policy, and it matters:
//
//  1. Noise first. An unintelligible utterance is not a low-confidence intent,
//     and treating it as one produces a clarification about a topic the caller
//     never raised.
//  2. Constrained expectation next. When a yes/no is expected, "yes" means yes,
//     and running it through general classification is how a confirmation gets
//     misread as a new request.
//  3. Then confidence and ambiguity, then slot completeness.
func (e *IntentEngine) Resolve(u Utterance, expect Expectation) (Intent, IntentVerdict) {
	now := e.clock.Now()

	// 1 — noise
	if u.ASRConfidence < e.cfg.MinASRConfidence {
		e.metrics.IntentsRejected.Inc("noise")
		return Intent{Name: IntentUnknown, TurnID: u.TurnID, At: now}, IntentNoise
	}

	// 2 — constrained expectation short-circuits general classification
	if expect == ExpectYesNo {
		if name, ok := classifyYesNo(u); ok {
			in := Intent{Name: name, Confidence: 1.0, TurnID: u.TurnID, At: now}
			e.record(in, IntentValidated)
			e.metrics.IntentsAccepted.Inc(string(name))
			return in, IntentAccept
		}
		// A non-yes/no answer to a yes/no question is genuinely ambiguous:
		// the caller may be changing the subject or may have misheard.
		e.metrics.IntentsRejected.Inc("expected_yes_no")
		return Intent{Name: IntentUnknown, TurnID: u.TurnID, At: now}, IntentClarify
	}

	// 3 — general classification
	var (
		candidates []Candidate
		slots      []Slot
		err        error
	)
	if e.classifier != nil {
		candidates, slots, err = e.classifier.Classify(u, expect)
	}
	if err != nil || len(candidates) == 0 {
		if e.cfg.Fallback == "" {
			e.metrics.IntentsRejected.Inc("no_candidates")
			return Intent{Name: IntentUnknown, TurnID: u.TurnID, At: now}, IntentReject
		}
		e.metrics.IntentFallbacks.Inc()
		in := Intent{Name: e.cfg.Fallback, Confidence: 0, TurnID: u.TurnID, At: now}
		e.record(in, IntentValidated)
		return in, IntentAccept
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		// Deterministic tie-break. Without it two equally-scored candidates
		// resolve in whatever order the classifier happened to return, and the
		// same conversation replays differently.
		return candidates[i].Name < candidates[j].Name
	})

	top := candidates[0]
	in := Intent{
		Name:         top.Name,
		Confidence:   top.Confidence,
		Slots:        slots,
		Alternatives: candidates,
		TurnID:       u.TurnID,
		At:           now,
	}
	e.metrics.IntentsProposed.Inc(string(top.Name))
	e.metrics.IntentConfidence.Observe(top.Confidence)

	switch {
	case top.Confidence < e.cfg.RejectThreshold:
		e.metrics.IntentsRejected.Inc("low_confidence")
		return in, IntentReject
	case top.Confidence < e.cfg.AcceptThreshold:
		return in, IntentClarify
	case in.Margin() < e.cfg.AmbiguityMargin:
		// High confidence but a close runner-up. This is the case a bare
		// threshold misses entirely, and it is the most common source of
		// confident wrong answers.
		return in, IntentClarify
	case !in.Complete():
		return in, IntentClarify
	default:
		e.record(in, IntentValidated)
		e.metrics.IntentsAccepted.Inc(string(top.Name))
		return in, IntentAccept
	}
}

// classifyYesNo recognises a yes/no answer deterministically.
//
// Deliberately not a model call. A confirmation is the highest-stakes,
// lowest-complexity classification in a conversation — getting "no" wrong is how
// a system does something the caller explicitly refused — and a fixed
// vocabulary is both more accurate and immeasurably faster than inference for
// this specific job.
//
// The vocabulary is English and Hindi transliteration, matching the platform's
// launch languages. It is intentionally conservative: an unrecognised answer
// returns false and becomes a clarification rather than a guess.
//
// SWITCH, NOT MAPS. The first version built two map literals per call. That
// made the deterministic "fast path" 1,395 ns and 15 allocations — 6.6x SLOWER
// than the general classification it exists to bypass, which the benchmark
// caught. A switch on a string compiles to a length-bucketed comparison chain,
// allocates nothing, and needs no package-level state to avoid the allocation.
func classifyYesNo(u Utterance) (IntentName, bool) {
	for _, w := range tokenizeLower(u.Text) {
		switch w {
		case "yes", "yeah", "yep", "yup", "sure", "correct", "right",
			"ok", "okay", "confirm",
			"haan", "haa", "ha", "ji", "jee":
			return IntentAffirm, true
		case "no", "nope", "nah", "wrong", "incorrect", "cancel", "stop",
			"nahi", "nahin", "na":
			return IntentDeny, true
		}
	}
	return "", false
}

// tokenizeLower splits on non-letter runes and lowercases ASCII.
//
// Written by hand rather than with strings.Fields plus strings.ToLower to avoid
// allocating twice per utterance on the hot path, and to strip punctuation,
// which "yes." would otherwise carry into the lookup.
func tokenizeLower(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			cur = append(cur, r+('a'-'A'))
		case (r >= 'a' && r <= 'z') || r > 127:
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// record stores an intent and advances its lifecycle.
func (e *IntentEngine) record(in Intent, state IntentState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.active != nil && e.active.Name != in.Name {
		e.history = append(e.history, *e.active)
	}
	e.active = &in
	e.state = state
}

// Active returns the intent currently being pursued.
func (e *IntentEngine) Active() (Intent, IntentState, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.active == nil {
		return Intent{}, IntentProposed, false
	}
	return *e.active, e.state, true
}

// Advance moves the active intent's lifecycle forward.
//
// Backward moves are refused. An intent that has been fulfilled cannot become
// active again — that would be a new intent with the same name, and conflating
// them destroys the fulfilment count.
func (e *IntentEngine) Advance(to IntentState) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.active == nil {
		return ErrNoIntent
	}
	if to <= e.state && !(e.state == IntentValidated && to == IntentActive) {
		return invariant("INV-CV-5",
			"intent lifecycle cannot move backwards: %s -> %s", e.state, to)
	}
	e.state = to
	return nil
}

// Supersede replaces the active intent, recording the old one.
func (e *IntentEngine) Supersede(in Intent) {
	e.mu.Lock()
	if e.active != nil {
		old := *e.active
		e.history = append(e.history, old)
	}
	e.active = &in
	e.state = IntentValidated
	e.mu.Unlock()
}

// History returns every intent this conversation has held.
func (e *IntentEngine) History() []Intent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Intent, len(e.history))
	copy(out, e.history)
	if e.active != nil {
		out = append(out, *e.active)
	}
	return out
}
