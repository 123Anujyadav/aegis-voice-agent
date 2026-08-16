package conversation

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires an engine for test with a controllable clock and a scriptable
// classifier.
//
// Exported rather than test-only because every service embedding this engine
// needs it: a service testing its own dialogue flows should not have to
// reimplement a fake classifier and a deterministic clock. A harness only this
// package's tests can use pushes every consumer into real time and real models.
type Harness struct {
	// Clock is the controllable clock. Advance it to drive timeouts and TTLs
	// without sleeping.
	Clock *rt.FakeClock
	// Metrics is the engine's instrument set, exposed for assertions.
	Metrics *Metrics
	// Engine is the engine under test.
	Engine *Engine
	// Classifier is the scripted classifier.
	Classifier *ScriptedClassifier
}

// HarnessOption customises a Harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg        *Config
	classifier *ScriptedClassifier
	logger     *slog.Logger
}

// WithHarnessConfig overrides the engine configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = &c }
}

// WithHarnessLogger sets the logger. Defaults to discarding, so a passing test
// is silent and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// NewHarness builds an engine wired for test.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(rt.SystemClock{}.Now().Truncate(0))
	metrics := NewMetrics()
	classifier := NewScriptedClassifier()

	cfg := DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}

	e, err := NewEngine(cfg,
		WithClock(clock), WithMetrics(metrics),
		WithLogger(o.logger), WithClassifier(classifier))
	if err != nil {
		return nil, err
	}
	return &Harness{Clock: clock, Metrics: metrics, Engine: e, Classifier: classifier}, nil
}

// Begin starts a conversation with the default persona.
func (h *Harness) Begin(id ConversationID) (*Conversation, error) {
	return h.Engine.Begin(id, "")
}

// BeginAs starts a conversation with a named persona.
func (h *Harness) BeginAs(id ConversationID, p PersonaID) (*Conversation, error) {
	return h.Engine.Begin(id, p)
}

// ---------------------------------------------------------------------------
// Scripted classifier
// ---------------------------------------------------------------------------

// ScriptedClassifier is a deterministic IntentClassifier for test.
//
// It is the ONLY implementation of [IntentClassifier] in this module, and it
// exists solely so the engine can be tested without a model. The production
// classifier is supplied by the deploying service — the Phase 10B brief makes
// the intent engine framework-only, and shipping a real classifier here would
// breach that.
type ScriptedClassifier struct {
	mu       sync.Mutex
	rules    []scriptRule
	fallback []Candidate
	slots    map[IntentName][]Slot
	err      error
	calls    int
}

type scriptRule struct {
	contains   string
	candidates []Candidate
}

// NewScriptedClassifier constructs an empty classifier.
func NewScriptedClassifier() *ScriptedClassifier {
	return &ScriptedClassifier{slots: make(map[IntentName][]Slot)}
}

// On maps an utterance substring to candidate intents.
func (s *ScriptedClassifier) On(contains string, candidates ...Candidate) *ScriptedClassifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, scriptRule{contains: strings.ToLower(contains), candidates: candidates})
	return s
}

// Fallback sets the candidates returned when no rule matches.
func (s *ScriptedClassifier) Fallback(candidates ...Candidate) *ScriptedClassifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = candidates
	return s
}

// WithSlots attaches slots to an intent.
func (s *ScriptedClassifier) WithSlots(name IntentName, slots ...Slot) *ScriptedClassifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slots[name] = slots
	return s
}

// FailWith makes Classify return an error.
func (s *ScriptedClassifier) FailWith(err error) *ScriptedClassifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	return s
}

// Calls returns how many times Classify has been called.
func (s *ScriptedClassifier) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Classify implements IntentClassifier.
func (s *ScriptedClassifier) Classify(u Utterance, _ Expectation) ([]Candidate, []Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++

	if s.err != nil {
		return nil, nil, s.err
	}
	text := strings.ToLower(u.Text)
	for _, r := range s.rules {
		if strings.Contains(text, r.contains) {
			var slots []Slot
			if len(r.candidates) > 0 {
				slots = s.slots[r.candidates[0].Name]
			}
			return r.candidates, slots, nil
		}
	}
	if len(s.fallback) > 0 {
		return s.fallback, s.slots[s.fallback[0].Name], nil
	}
	return nil, nil, nil
}

// ---------------------------------------------------------------------------
// Simulation
// ---------------------------------------------------------------------------

// Simulator replays a scripted conversation and collects what happened.
//
// It is what makes "conversation simulation tests" possible as a distinct test
// class: a whole dialogue is expressed as data, run deterministically, and
// asserted on as a trace rather than as a sequence of hand-wired calls.
type Simulator struct {
	conv  *Conversation
	clock *rt.FakeClock

	Plans  []Plan
	Errors []error
}

// NewSimulator constructs a simulator over a conversation.
func NewSimulator(c *Conversation, clock *rt.FakeClock) *Simulator {
	return &Simulator{conv: c, clock: clock}
}

// Say feeds a caller utterance with full ASR confidence.
func (s *Simulator) Say(text string) *Simulator {
	return s.SayWith(text, 1.0, false)
}

// SayWith feeds a caller utterance with explicit recognition quality.
func (s *Simulator) SayWith(text string, asrConfidence float64, truncated bool) *Simulator {
	return s.Do(Event{
		Kind:  EventUtterance,
		Party: PartyCaller,
		Utterance: Utterance{
			Text: text, ASRConfidence: asrConfidence,
			Truncated: truncated, DurationMS: 1200,
		},
	})
}

// Do feeds an arbitrary event.
func (s *Simulator) Do(e Event) *Simulator {
	plan, err := s.conv.Handle(e)
	s.Plans = append(s.Plans, plan)
	if err != nil {
		s.Errors = append(s.Errors, err)
	}
	return s
}

// Start begins the conversation and completes the greeting.
//
// Combined because a conversation is never usefully left mid-greeting, and
// every simulation would otherwise open with the same two lines.
func (s *Simulator) Start() *Simulator {
	return s.Do(Event{Kind: EventStart}).
		Do(Event{Kind: EventGreetingComplete})
}

// Reply completes the agent's turn, moving into the awaiting state the last
// plan established.
func (s *Simulator) Reply() *Simulator {
	return s.Do(Event{Kind: EventSpeechComplete, Party: PartyAgent})
}

// Exchange is Say followed by Reply — one full round trip.
func (s *Simulator) Exchange(text string) *Simulator {
	return s.Say(text).Reply()
}

// Interrupt raises an interruption.
func (s *Simulator) Interrupt(kind InterruptionKind, reason string) *Simulator {
	return s.Do(Event{Kind: EventInterrupt, Interruption: kind,
		Party: PartyCaller, Reason: reason})
}

// Advance moves the fake clock, driving timeouts, TTLs and turn-overlap
// classification deterministically.
func (s *Simulator) Advance(d time.Duration) *Simulator {
	s.clock.Advance(d)
	return s
}

// LastPlan returns the most recent plan.
func (s *Simulator) LastPlan() Plan {
	if len(s.Plans) == 0 {
		return Plan{}
	}
	return s.Plans[len(s.Plans)-1]
}

// Actions returns every planned action in order, for trace assertions.
func (s *Simulator) Actions() []Action {
	out := make([]Action, 0, len(s.Plans))
	for _, p := range s.Plans {
		out = append(out, p.Action)
	}
	return out
}

// States returns the state trace as a slice of States entered.
func (s *Simulator) States() []State {
	trace := s.conv.Trace()
	out := make([]State, 0, len(trace)+1)
	if len(trace) > 0 {
		out = append(out, trace[0].From)
	}
	for _, t := range trace {
		out = append(out, t.To)
	}
	return out
}

// TraceString renders the state trace for a failure message.
func (s *Simulator) TraceString() string {
	var b strings.Builder
	for i, t := range s.conv.Trace() {
		if i > 0 {
			b.WriteString(" -> ")
		}
		fmt.Fprintf(&b, "%s[%s]", t.To, t.Trigger)
	}
	return b.String()
}

// CountAction returns how many plans chose an action.
func (s *Simulator) CountAction(a Action) int {
	n := 0
	for _, p := range s.Plans {
		if p.Action == a {
			n++
		}
	}
	return n
}
