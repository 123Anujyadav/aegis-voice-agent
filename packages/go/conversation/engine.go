package conversation

import (
	"log/slog"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// EventKind classifies an input to the conversation.
type EventKind int

const (
	// EventStart begins the conversation.
	EventStart EventKind = iota
	// EventGreetingComplete ends the opening turn.
	EventGreetingComplete
	// EventUtterance is a completed caller contribution.
	EventUtterance
	// EventOverlap is the caller producing audio while the agent holds the
	// floor. It may be a backchannel; the turn manager decides.
	EventOverlap
	// EventSpeechComplete is the agent finishing its output.
	EventSpeechComplete
	// EventSilence is a detected silence window.
	EventSilence
	// EventInterrupt is an explicit interruption.
	EventInterrupt
	// EventToolComplete resumes from an external action.
	EventToolComplete
	// EventTimeout is an inactivity or budget expiry.
	EventTimeout
	// EventFault is an internal or upstream failure.
	EventFault
	// EventHangup is the caller ending the call.
	EventHangup
)

// String renders the event kind for logs and metric labels.
func (k EventKind) String() string {
	switch k {
	case EventGreetingComplete:
		return "greeting_complete"
	case EventUtterance:
		return "utterance"
	case EventOverlap:
		return "overlap"
	case EventSpeechComplete:
		return "speech_complete"
	case EventSilence:
		return "silence"
	case EventInterrupt:
		return "interrupt"
	case EventToolComplete:
		return "tool_complete"
	case EventTimeout:
		return "timeout"
	case EventFault:
		return "fault"
	case EventHangup:
		return "hangup"
	default:
		return "start"
	}
}

// Event is one input to a conversation.
type Event struct {
	// Kind classifies it.
	Kind EventKind
	// Utterance is populated for EventUtterance. Its Text is SENSITIVE.
	Utterance Utterance
	// Interruption is populated for EventInterrupt.
	Interruption InterruptionKind
	// Party is who produced the event.
	Party Party
	// Reason is a short machine-readable code. Never caller content.
	Reason string
	// Contradicts reports that this utterance conflicts with established
	// context. Supplied by the layer above, which owns the semantics.
	Contradicts bool
}

// Outcome is how a conversation ended.
type Outcome string

// The outcomes a conversation may reach.
const (
	OutcomeCompleted   Outcome = "completed"
	OutcomeTransferred Outcome = "transferred"
	OutcomeEscalated   Outcome = "escalated"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeAbandoned   Outcome = "abandoned"
	OutcomeFailed      Outcome = "failed"
)

// Config is the engine's complete configuration.
type Config struct {
	// Turn tunes floor control.
	Turn TurnConfig
	// Intent tunes classification handling.
	Intent IntentConfig
	// Context tunes context scoping and expiry.
	Context ContextConfig
	// Clarification tunes clarification budgets.
	Clarification ClarificationConfig
	// Latency tunes stage budgets.
	Latency LatencyConfig
	// DefaultPersona is the persona a conversation starts in.
	DefaultPersona PersonaID
	// IdleTimeout ends a conversation with no activity.
	IdleTimeout time.Duration
}

// DefaultConfig returns a fully-populated configuration.
func DefaultConfig() Config {
	return Config{
		Turn:           DefaultTurnConfig(),
		Intent:         DefaultIntentConfig(),
		Context:        DefaultContextConfig(),
		Clarification:  DefaultClarificationConfig(),
		Latency:        DefaultLatencyConfig(),
		DefaultPersona: PersonaBusinessReceptionist,
		IdleTimeout:    20 * time.Second,
	}
}

func (c Config) validate() []string {
	var p []string
	p = append(p, c.Turn.validate()...)
	p = append(p, c.Intent.validate()...)
	p = append(p, c.Context.validate()...)
	p = append(p, c.Clarification.validate()...)
	p = append(p, c.Latency.validate()...)
	if c.DefaultPersona == "" {
		p = append(p, "config: DefaultPersona is required")
	}
	if c.IdleTimeout <= 0 {
		p = append(p, "config: IdleTimeout must be positive")
	}
	return p
}

// Engine constructs and owns conversations.
//
// It holds shared configuration and the classifier, and nothing mutable that
// conversations contend on. Two engines in one process share nothing, which is
// what makes the test suite parallel-safe — the same property Phase 10A's
// kernel has, for the same reason.
type Engine struct {
	cfg        Config
	clock      rt.Clock
	logger     *slog.Logger
	metrics    *Metrics
	classifier IntentClassifier

	// personas is built once and shared by every conversation this engine
	// owns. Read-only after construction; see newPersonaRuntimeFrom for why
	// this is safe and why it matters.
	personas map[PersonaID]Persona

	active sync.Map // ConversationID -> *Conversation
}

// Option customises an Engine.
type Option func(*Engine)

// WithClock replaces the clock.
func WithClock(c rt.Clock) Option {
	return func(e *Engine) {
		if c != nil {
			e.clock = c
		}
	}
}

// WithLogger replaces the logger.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithMetrics replaces the instrument set.
func WithMetrics(m *Metrics) Option {
	return func(e *Engine) {
		if m != nil {
			e.metrics = m
		}
	}
}

// WithClassifier supplies the intent classifier.
//
// Optional. Without one every utterance resolves to the fallback intent, which
// is both a legitimate deployment (deterministic flows only) and what makes the
// engine testable with no model present.
func WithClassifier(c IntentClassifier) Option {
	return func(e *Engine) { e.classifier = c }
}

// NewEngine constructs an engine.
func NewEngine(cfg Config, opts ...Option) (*Engine, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	e := &Engine{
		cfg:      cfg,
		clock:    rt.SystemClock{},
		logger:   slog.Default(),
		metrics:  NewMetrics(),
		personas: BuiltinPersonas(),
	}
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

// Metrics returns the engine's instrument set.
func (e *Engine) Metrics() *Metrics { return e.metrics }

// Clock returns the engine's clock.
func (e *Engine) Clock() rt.Clock { return e.clock }

// ConversationID identifies a conversation.
type ConversationID string

// Begin creates a conversation.
func (e *Engine) Begin(id ConversationID, persona PersonaID) (*Conversation, error) {
	if persona == "" {
		persona = e.cfg.DefaultPersona
	}

	fsm, err := newStateMachine(e.clock)
	if err != nil {
		return nil, err
	}
	turns, err := NewTurnManager(e.cfg.Turn, e.clock, e.metrics)
	if err != nil {
		return nil, err
	}
	intents, err := NewIntentEngine(e.cfg.Intent, e.classifier, e.clock, e.metrics)
	if err != nil {
		return nil, err
	}
	ctxEngine, err := NewContextEngine(e.cfg.Context, e.clock, e.metrics)
	if err != nil {
		return nil, err
	}
	clarify, err := NewClarificationEngine(e.cfg.Clarification, e.metrics)
	if err != nil {
		return nil, err
	}
	personas, err := newPersonaRuntimeFrom(e.personas, persona, e.clock, e.metrics)
	if err != nil {
		return nil, err
	}

	c := &Conversation{
		id:          id,
		engine:      e,
		clock:       e.clock,
		metrics:     e.metrics,
		logger:      e.logger.With(slog.String("conversation_id", string(id))),
		fsm:         fsm,
		turns:       turns,
		intents:     intents,
		context:     ctxEngine,
		clarify:     clarify,
		personas:    personas,
		policy:      NewPolicyEngine(e.metrics),
		planner:     NewPlanner(e.metrics),
		interrupts:  NewInterruptionEngine(e.clock, e.metrics),
		startedAt:   e.clock.Now(),
		lastEventAt: e.clock.Now(),
	}

	// Record every transition into the trace and the metrics. Registered here
	// rather than inside transition() so the FSM remains the single writer and
	// the hook cannot be forgotten on a new call path.
	fsm.OnTransition(func(from, to State) {
		c.metrics.StateEntered.Inc(to.String())
	})

	e.active.Store(id, c)
	e.metrics.Started.Inc()
	e.metrics.Active.Add(1)
	return c, nil
}

// Get returns a live conversation.
func (e *Engine) Get(id ConversationID) (*Conversation, bool) {
	v, ok := e.active.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*Conversation), true
}

// Conversation is one dialogue, and the Dialogue Manager of the Phase 10B
// brief: it owns the lifecycle, the history, turn ownership, goals, policy
// application, context updates and recovery.
//
// Every subsystem is reachable only through a Conversation, and a Conversation
// owns its own instances. There is no shared mutable state between two
// conversations — which is what makes ten thousand of them safe in one process.
type Conversation struct {
	id      ConversationID
	engine  *Engine
	clock   rt.Clock
	metrics *Metrics
	logger  *slog.Logger

	fsm        *rt.FSM[State]
	turns      *TurnManager
	intents    *IntentEngine
	context    *ContextEngine
	clarify    *ClarificationEngine
	personas   *PersonaRuntime
	policy     *PolicyEngine
	planner    *Planner
	interrupts *InterruptionEngine

	mu          sync.RWMutex
	trace       []TransitionRecord
	startedAt   time.Time
	lastEventAt time.Time
	endedAt     time.Time
	outcome     Outcome
	lastPlan    Plan
	pendingReq  Request
	stateSince  time.Time
}

// ID returns the conversation identifier.
func (c *Conversation) ID() ConversationID { return c.id }

// State returns the current state.
func (c *Conversation) State() State { return c.fsm.State() }

// Persona returns the active persona.
func (c *Conversation) Persona() Persona { return c.personas.Active() }

// Context returns the context engine.
func (c *Conversation) Context() *ContextEngine { return c.context }

// Turns returns the turn manager.
func (c *Conversation) Turns() *TurnManager { return c.turns }

// Interruptions returns the interruption engine.
func (c *Conversation) Interruptions() *InterruptionEngine { return c.interrupts }

// Intents returns the intent engine.
func (c *Conversation) Intents() *IntentEngine { return c.intents }

// Trace returns a copy of the state trace.
func (c *Conversation) Trace() []TransitionRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]TransitionRecord, len(c.trace))
	copy(out, c.trace)
	return out
}

// Elapsed returns conversation wall time.
func (c *Conversation) Elapsed() time.Duration {
	c.mu.RLock()
	started, ended := c.startedAt, c.endedAt
	c.mu.RUnlock()
	if !ended.IsZero() {
		return ended.Sub(started)
	}
	return c.clock.Now().Sub(started)
}

// Outcome returns how the conversation ended, empty while live.
func (c *Conversation) Outcome() Outcome {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.outcome
}

// transition is the ONLY writer of conversation state.
//
// Every state change in this package goes through here. That is what makes
// "no implicit state changes" true rather than aspirational: there is one
// function, it records a trace entry, it emits a metric, and an undeclared
// transition returns an error instead of happening.
func (c *Conversation) transition(to State, trigger Trigger, note string) error {
	from, err := c.fsm.To(to)
	if err != nil {
		c.metrics.InvalidAttempts.Inc(from.String(), to.String())
		return err
	}

	now := c.clock.Now()
	c.mu.Lock()
	if !c.stateSince.IsZero() {
		c.metrics.StateDuration.Observe(now.Sub(c.stateSince).Seconds(), from.String())
	}
	c.stateSince = now
	c.trace = append(c.trace, TransitionRecord{
		From: from, To: to, Trigger: trigger, At: now.UnixNano(), Note: note,
	})
	c.mu.Unlock()

	c.metrics.Transitions.Inc(from.String(), to.String(), string(trigger))

	if to.IsTerminal() {
		c.finish(outcomeForState(to))
	}
	return nil
}

// outcomeForState maps a terminal state to a conversation outcome.
func outcomeForState(s State) Outcome {
	switch s {
	case StateTransferred:
		return OutcomeTransferred
	case StateEscalated:
		return OutcomeEscalated
	case StateTimeout:
		return OutcomeTimeout
	default:
		return OutcomeCompleted
	}
}

// finish records terminal accounting exactly once.
func (c *Conversation) finish(o Outcome) {
	c.mu.Lock()
	if !c.endedAt.IsZero() {
		c.mu.Unlock()
		return
	}
	c.endedAt = c.clock.Now()
	c.outcome = o
	elapsed := c.endedAt.Sub(c.startedAt)
	c.mu.Unlock()

	c.clarify.Complete()
	c.metrics.Completed.Inc(string(o))
	c.metrics.Duration.Observe(elapsed.Seconds())
	c.metrics.TurnsPerConv.Observe(float64(c.turns.Count()))
	c.metrics.Active.Add(-1)
	c.engine.active.Delete(c.id)
}

// Handle processes one event and returns the resulting plan.
//
// This is the decision cycle. Its shape is fixed: budget the cycle, update the
// floor, resolve meaning, consult policy, plan, transition. Every stage is
// measured, and policy is the one stage that can never be skipped.
func (c *Conversation) Handle(e Event) (Plan, error) {
	if c.fsm.IsTerminal() {
		return Plan{}, ErrTerminal
	}

	persona := c.personas.Active()
	latency, err := NewLatencyController(
		c.engine.cfg.Latency.scaleBudgets(personaLatencyFactor(persona)),
		c.clock, c.metrics)
	if err != nil {
		return Plan{}, err
	}
	latency.Begin()
	defer latency.End()

	c.mu.Lock()
	c.lastEventAt = c.clock.Now()
	c.mu.Unlock()

	switch e.Kind {
	case EventStart:
		return c.handleStart()
	case EventGreetingComplete:
		return c.handleGreetingComplete()
	case EventOverlap:
		return c.handleOverlap(e)
	case EventSpeechComplete:
		return c.handleSpeechComplete()
	case EventInterrupt:
		return c.handleInterrupt(e)
	case EventHangup:
		return Plan{Action: ActionEnd, Reason: "hangup", NextState: StateEnded},
			c.transition(StateEnded, TriggerHangup, e.Reason)
	case EventTimeout:
		return Plan{Action: ActionEnd, Reason: "timeout", NextState: StateTimeout},
			c.transition(StateTimeout, TriggerTimeout, e.Reason)
	case EventFault:
		return Plan{Action: ActionWait, Reason: "fault", NextState: StateError},
			c.transition(StateError, TriggerFault, e.Reason)
	case EventToolComplete:
		return Plan{Action: ActionRespond, Reason: "tool_complete", NextState: StateSpeaking},
			c.transition(StateThinking, TriggerToolComplete, e.Reason)
	case EventSilence:
		return c.handleSilence()
	case EventUtterance:
		return c.handleUtterance(e, latency)
	default:
		return Plan{}, invariant("INV-CV-1", "unhandled event kind %d", e.Kind)
	}
}

// handleStart opens the conversation with the non-yielding greeting turn.
func (c *Conversation) handleStart() (Plan, error) {
	// INVARIANT INV-CV-2. The opening turn is non-yielding. On this platform
	// the greeting carries the announcement that is the caller's lawful basis
	// (frozen invariant I1), so it must complete. A barge-in during it is
	// queued by the turn manager rather than discarded, so the caller does not
	// have to repeat themselves.
	c.turns.Acquire(PartyAgent, true)
	if err := c.transition(StateGreeting, TriggerStart, ""); err != nil {
		return Plan{}, err
	}
	return Plan{Action: ActionRespond, Reason: "greeting", NextState: StateGreeting,
		Confidence: 1.0}, nil
}

// handleGreetingComplete releases the opening turn.
func (c *Conversation) handleGreetingComplete() (Plan, error) {
	c.turns.Release(PartyAgent, ExpectNothing)
	if err := c.transition(StateListening, TriggerGreetingComplete, ""); err != nil {
		return Plan{}, err
	}
	return Plan{Action: ActionWait, Reason: "awaiting_caller", NextState: StateListening}, nil
}

// handleOverlap classifies simultaneous speech.
func (c *Conversation) handleOverlap(e Event) (Plan, error) {
	decision := c.turns.NoteOverlap(e.Party)
	switch decision {
	case FloorBackchannel:
		// The caller agreed with us. Continuing to speak is correct, and
		// stopping would make the agent seem unable to hold a thought.
		c.turns.ClearOverlap()
		return Plan{Action: ActionIgnore, Reason: "backchannel",
			NextState: c.fsm.State()}, nil
	case FloorQueued:
		return Plan{Action: ActionIgnore, Reason: "floor_queued",
			NextState: c.fsm.State()}, nil
	case FloorGranted:
		return c.handleInterrupt(Event{
			Kind: EventInterrupt, Interruption: InterruptionUser,
			Party: e.Party, Reason: "barge_in",
		})
	default:
		return Plan{Action: ActionIgnore, Reason: "overlap_denied",
			NextState: c.fsm.State()}, nil
	}
}

// handleSpeechComplete ends an agent turn and enters the awaiting state the
// plan established.
func (c *Conversation) handleSpeechComplete() (Plan, error) {
	c.mu.RLock()
	last := c.lastPlan
	c.mu.RUnlock()

	c.turns.Release(PartyAgent, last.Expectation)

	next := stateForExpectation(last.Expectation)
	// A plan whose action was terminal overrides the expectation: an agent
	// that finished saying goodbye is not awaiting an answer.
	switch last.Action {
	case ActionEnd:
		next = StateEnded
	case ActionEscalate:
		next = StateEscalated
	case ActionTransfer:
		next = StateTransferred
	}

	if err := c.transition(next, TriggerSpeechComplete, last.Reason); err != nil {
		return Plan{}, err
	}
	c.turns.Acquire(PartyCaller, false)
	return Plan{Action: ActionWait, Reason: "awaiting_caller", NextState: next}, nil
}

// handleInterrupt applies an interruption.
func (c *Conversation) handleInterrupt(e Event) (Plan, error) {
	current, _ := c.turns.Current()
	i := c.interrupts.Raise(e.Interruption, e.Party, e.Reason, current.ID)

	// An emergency permanently narrows the persona. The switch is one-way and
	// is what makes every subsequent policy evaluation refuse to converse.
	if e.Interruption == InterruptionEmergency {
		_ = c.personas.Switch(PersonaEmergencyAssistant, "emergency_interrupt")
	}

	if e.Interruption.Preemptive() || e.Interruption == InterruptionUser {
		c.turns.ForceYield(interruptBeneficiary(e.Interruption), e.Interruption)
	}

	next := stateAfterInterruption(e.Interruption)

	// Interrupted is a real state, entered before arbitration resolves, so a
	// trace never shows the floor changing hands without a contested moment.
	if c.fsm.CanGo(StateInterrupted) {
		if err := c.transition(StateInterrupted, TriggerInterruption, i.Kind.String()); err != nil {
			return Plan{}, err
		}
	}
	if err := c.transition(next, TriggerArbitrated, i.Resume.String()); err != nil {
		return Plan{}, err
	}

	plan := Plan{
		Action: ActionIgnore, Reason: "interrupted_" + i.Kind.String(),
		NextState: next,
	}
	switch next {
	case StateEscalated:
		plan.Action = ActionEscalate
		plan.Escalation = i.Kind.String()
	case StateTransferred:
		plan.Action = ActionTransfer
	}
	c.mu.Lock()
	c.lastPlan = plan
	c.mu.Unlock()
	return plan, nil
}

// interruptBeneficiary returns the party that gains the floor.
func interruptBeneficiary(k InterruptionKind) Party {
	switch k {
	case InterruptionEmergency, InterruptionTransfer, InterruptionProvider:
		return PartySystem
	default:
		return PartyCaller
	}
}

// handleSilence treats a silence window as the caller releasing the floor.
func (c *Conversation) handleSilence() (Plan, error) {
	state := c.fsm.State()
	if !state.IsAwaiting() {
		return Plan{Action: ActionIgnore, Reason: "silence_ignored", NextState: state}, nil
	}
	if c.fsm.CanGo(StateWaiting) {
		if err := c.transition(StateWaiting, TriggerSilence, ""); err != nil {
			return Plan{}, err
		}
	}
	return Plan{Action: ActionWait, Reason: "silence", NextState: StateWaiting}, nil
}

// handleUtterance is the main decision path.
func (c *Conversation) handleUtterance(e Event, latency *LatencyController) (Plan, error) {
	state := c.fsm.State()
	if !state.IsAwaiting() {
		// An utterance arriving while the agent holds the floor is an overlap,
		// not a turn. Routing it here rather than treating it as a turn is what
		// prevents the engine from answering something the caller said over it.
		return c.handleOverlap(Event{Kind: EventOverlap, Party: PartyCaller})
	}

	expect := c.turns.LastExpectation()

	// --- intent ---
	var (
		intent  Intent
		verdict IntentVerdict
	)
	if end, run := latency.Enter(StageIntent); run {
		intent, verdict = c.intents.Resolve(e.Utterance, expect)
		end()
	} else {
		// The intent stage was skipped under budget pressure. Falling back to
		// the fallback intent is honest: we did not classify, and pretending
		// otherwise would attach a confidence we never computed.
		intent = Intent{Name: c.engine.cfg.Intent.Fallback, TurnID: e.Utterance.TurnID}
		verdict = IntentAccept
	}

	// --- context ---
	if end, run := latency.Enter(StageContext); run {
		_ = c.context.Set(Entry{
			Key: "last_intent", Value: string(intent.Name),
			Scope: ScopeConversation, Sensitivity: Internal, Source: "intent_engine",
		})
		end()
	}

	// --- clarification assessment ---
	req := c.clarify.Assess(e.Utterance, intent, verdict, e.Contradicts)
	req, allowed := c.clarify.Reserve(req, c.personas.Active().ClarificationBudget)
	if req.Kind == ClarifyNone {
		// A resolved subject frees its per-subject budget for later reuse.
		c.mu.RLock()
		prev := c.pendingReq
		c.mu.RUnlock()
		if prev.Kind != ClarifyNone {
			c.clarify.Resolved(prev)
			c.mu.Lock()
			c.pendingReq = Request{}
			c.mu.Unlock()
		}
	} else {
		c.mu.Lock()
		c.pendingReq = req
		c.mu.Unlock()
	}

	// --- transition into Thinking, so the state reflects that a decision is
	// genuinely in flight (INV-CV-4) ---
	if err := c.transition(StateThinking, TriggerUtterance, verdict.String()); err != nil {
		return Plan{}, err
	}

	persona := c.personas.Active()
	planInput := PlanInput{
		State: StateThinking, Persona: persona, Intent: intent, Verdict: verdict,
		Clarification: req, ClarificationAllowed: allowed, Expectation: expect,
		EmergencyRaised:   c.interrupts.EmergencyRaised(),
		TurnCount:         c.turns.Count(),
		Elapsed:           c.Elapsed(),
		InterruptionCount: c.interrupts.Count(InterruptionNone),
		Deadline:          c.clock.Now().Add(c.engine.cfg.Latency.Total),
		PolicyDenied:      map[Action]bool{},
	}

	// --- plan, then policy, then re-plan on denial ---
	//
	// Planning first and checking policy second, rather than the reverse, is
	// deliberate: policy is a veto over a concrete proposal, and evaluating
	// every possible action against policy before choosing would be both slower
	// and less explicable. The bounded re-plan loop is what turns a veto into a
	// different decision rather than a failure.
	var plan Plan
	const maxReplans = 3
	for attempt := 0; attempt < maxReplans; attempt++ {
		if end, run := latency.Enter(StagePlanning); run {
			plan = c.planner.Plan(planInput)
			end()
		} else {
			plan = c.planner.Plan(planInput)
		}

		// StagePolicy is NOT skippable — Stage.Skippable() refuses it — so this
		// always runs, whatever the budget says. That is invariant I11's shape
		// inside this engine.
		end, _ := latency.Enter(StagePolicy)
		verdictPolicy := c.policy.Evaluate(PolicyInput{
			Action: plan.Action, State: c.fsm.State(), Persona: persona,
			Intent: intent, HasIntent: verdict == IntentAccept,
			TurnCount: planInput.TurnCount, Elapsed: planInput.Elapsed,
			ClarificationsUsed: c.clarify.Used(),
			EmergencyRaised:    planInput.EmergencyRaised,
			InterruptionCount:  planInput.InterruptionCount,
		})
		end()

		if verdictPolicy.Allowed() {
			break
		}
		planInput.PolicyDenied[plan.Action] = true
		plan.Reason = "denied_" + verdictPolicy.Reason

		if attempt == maxReplans-1 {
			// Every proposal was refused. Escalating is the only honest exit —
			// looping would spend the caller's time producing nothing.
			plan = Plan{Action: ActionEscalate, Reason: "policy_exhausted",
				NextState: StateEscalated, Confidence: 0.5, Escalation: "policy"}
		}
	}

	c.mu.Lock()
	c.lastPlan = plan
	c.mu.Unlock()

	// --- apply ---
	if end, run := latency.Enter(StageTransition); run {
		defer end()
	}
	c.turns.Release(PartyCaller, ExpectNothing)

	next := plan.NextState
	if next == StateThinking || next == 0 {
		next = StateSpeaking
	}
	if err := c.transition(next, TriggerPlanned, plan.Reason); err != nil {
		return plan, err
	}
	if next == StateSpeaking {
		c.turns.Acquire(PartyAgent, false)
	}
	return plan, nil
}

// Recover attempts to return an errored conversation to a usable state.
//
// It restores the most recent context snapshot and re-enters listening. If no
// snapshot exists the conversation escalates rather than continuing on
// context that may be half-written — a recovery that leaves the agent with
// partial state is worse than a handover.
func (c *Conversation) Recover() error {
	if err := c.transition(StateRecovery, TriggerFault, "recover"); err != nil {
		return err
	}
	snap, ok := c.context.LatestSnapshot()
	if !ok {
		return c.transition(StateEscalated, TriggerEscalate, "no_snapshot")
	}
	if err := c.context.Restore(snap.ID); err != nil {
		return c.transition(StateEscalated, TriggerEscalate, "restore_failed")
	}
	return c.transition(StateListening, TriggerRecovered, snap.Label)
}

// Escalate ends the conversation by handing it to a human.
func (c *Conversation) Escalate(reason string) error {
	return c.transition(StateEscalated, TriggerEscalate, reason)
}

// Transfer ends the conversation by routing it elsewhere.
func (c *Conversation) Transfer(reason string) error {
	return c.transition(StateTransferred, TriggerTransfer, reason)
}

// End ends the conversation normally.
func (c *Conversation) End(reason string) error {
	return c.transition(StateEnded, TriggerHangup, reason)
}

// Idle reports whether no event has arrived within the idle timeout.
func (c *Conversation) Idle() bool {
	c.mu.RLock()
	last := c.lastEventAt
	c.mu.RUnlock()
	return c.clock.Now().Sub(last) > c.engine.cfg.IdleTimeout
}

// personaLatencyFactor returns a persona's budget multiplier, defaulting to 1.
func personaLatencyFactor(p Persona) float64 {
	if p.LatencyProfile <= 0 {
		return 1
	}
	return p.LatencyProfile
}
