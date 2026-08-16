package conversation

import (
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// PersonaID identifies a conversation persona.
type PersonaID string

// The four personas the platform ships.
const (
	// PersonaBusinessReceptionist answers for an organisation: routing,
	// hours, message-taking.
	PersonaBusinessReceptionist PersonaID = "business_receptionist"

	// PersonaPersonalAssistant answers for an individual subscriber.
	PersonaPersonalAssistant PersonaID = "personal_assistant"

	// PersonaFraudShield engages a caller already assessed as likely hostile.
	// Deliberately narrow: it discloses nothing and asks verifying questions.
	PersonaFraudShield PersonaID = "fraud_shield"

	// PersonaEmergencyAssistant handles a call carrying emergency intent. Its
	// only job is to stop being in the way.
	PersonaEmergencyAssistant PersonaID = "emergency_assistant"
)

// Capability is something a persona is permitted to attempt.
type Capability string

// The capability vocabulary. Closed, because a capability that can be invented
// at a call site is a capability nobody reviewed.
const (
	// CapAnswerQuestion responds substantively to a caller.
	CapAnswerQuestion Capability = "answer_question"
	// CapAskClarification asks a disambiguating question.
	CapAskClarification Capability = "ask_clarification"
	// CapTakeMessage records a message for the subscriber.
	CapTakeMessage Capability = "take_message"
	// CapTransfer routes the call to another destination.
	CapTransfer Capability = "transfer"
	// CapEscalate hands the call to a human.
	CapEscalate Capability = "escalate"
	// CapDiscloseAvailability reveals whether the subscriber is free.
	CapDiscloseAvailability Capability = "disclose_availability"
	// CapDiscloseIdentity reveals the subscriber's name.
	CapDiscloseIdentity Capability = "disclose_identity"
	// CapCollectCallback records a callback preference.
	CapCollectCallback Capability = "collect_callback"
	// CapVerifyCaller asks verifying questions of a suspicious caller.
	CapVerifyCaller Capability = "verify_caller"
	// CapEndCall terminates the conversation.
	CapEndCall Capability = "end_call"
	// CapHandOverDialer yields control to the subscriber's dialer.
	CapHandOverDialer Capability = "hand_over_dialer"
)

// Persona is a bounded conversational role.
//
// It is capabilities and constraints, not personality. There is no name, no
// voice, no tone and no prompt here — the Phase 10B brief excludes prompt
// writing, and the frozen UX record separately forbids an anthropomorphic
// persona in the product. What a persona actually is, in this engine, is the
// answer to "what may this role do, and what must it never do".
type Persona struct {
	// ID identifies the persona.
	ID PersonaID

	// Capabilities are what it may attempt. Deny-by-default: anything absent
	// is forbidden.
	Capabilities map[Capability]bool

	// Forbidden are capabilities explicitly denied even if a policy would
	// otherwise allow them. Distinct from mere absence so that a deliberate
	// prohibition is visible rather than inferred.
	Forbidden map[Capability]bool

	// ClarificationBudget bounds clarifying questions before escalation.
	ClarificationBudget int

	// MaxTurns bounds conversation length. Zero means unbounded.
	MaxTurns int

	// MaxDuration bounds conversation wall time. Zero means unbounded.
	MaxDuration time.Duration

	// EscalateOnUncertainty makes a low-confidence intent escalate rather than
	// clarify. Correct for emergency handling, where asking a clarifying
	// question wastes the only thing that matters.
	EscalateOnUncertainty bool

	// LatencyProfile scales the stage budgets. A fraud interaction may take
	// longer than a receptionist greeting; an emergency may take none.
	LatencyProfile float64
}

// Allows reports whether the persona permits a capability.
func (p Persona) Allows(c Capability) bool {
	if p.Forbidden[c] {
		return false
	}
	return p.Capabilities[c]
}

// BuiltinPersonas returns the four shipped personas.
//
// Each is a set of deliberate answers, and the interesting ones are the
// prohibitions:
//
//   - The receptionist may not disclose the subscriber's identity. A caller
//     asking "is this Dr Nair's personal number" gets nothing, because
//     answering is how a receptionist becomes a directory for a social
//     engineer.
//   - Fraud Shield may disclose NOTHING and may not take a message. Its entire
//     purpose is to give a hostile caller no surface, and a message field is a
//     surface.
//   - Emergency Assistant may do exactly two things: escalate, and hand over
//     the dialer. It cannot answer, cannot clarify, cannot take a message. U7
//     says the product's job in an emergency is to get out of the way, and a
//     persona that could do anything else would eventually do it.
func BuiltinPersonas() map[PersonaID]Persona {
	return map[PersonaID]Persona{
		PersonaBusinessReceptionist: {
			ID: PersonaBusinessReceptionist,
			Capabilities: map[Capability]bool{
				CapAnswerQuestion: true, CapAskClarification: true,
				CapTakeMessage: true, CapTransfer: true, CapEscalate: true,
				CapDiscloseAvailability: true, CapCollectCallback: true,
				CapEndCall: true,
			},
			Forbidden: map[Capability]bool{
				CapDiscloseIdentity: true,
			},
			ClarificationBudget: 3,
			MaxTurns:            40,
			MaxDuration:         5 * time.Minute,
			LatencyProfile:      1.0,
		},

		PersonaPersonalAssistant: {
			ID: PersonaPersonalAssistant,
			Capabilities: map[Capability]bool{
				CapAnswerQuestion: true, CapAskClarification: true,
				CapTakeMessage: true, CapEscalate: true,
				CapCollectCallback: true, CapEndCall: true,
			},
			Forbidden: map[Capability]bool{
				// Availability and identity are the two things a stranger most
				// wants and most easily misuses. Both are withheld unless the
				// subscriber has widened disclosure scope, which is enforced
				// above this engine.
				CapDiscloseIdentity:     true,
				CapDiscloseAvailability: true,
				CapTransfer:             true,
			},
			ClarificationBudget: 2,
			MaxTurns:            30,
			MaxDuration:         4 * time.Minute,
			LatencyProfile:      1.0,
		},

		PersonaFraudShield: {
			ID: PersonaFraudShield,
			Capabilities: map[Capability]bool{
				CapVerifyCaller: true, CapAskClarification: true,
				CapEndCall: true, CapEscalate: true,
			},
			Forbidden: map[Capability]bool{
				CapAnswerQuestion: true, CapTakeMessage: true,
				CapDiscloseIdentity: true, CapDiscloseAvailability: true,
				CapCollectCallback: true, CapTransfer: true,
			},
			ClarificationBudget: 4, // verification IS clarification here
			MaxTurns:            20,
			MaxDuration:         3 * time.Minute,
			LatencyProfile:      1.2,
		},

		PersonaEmergencyAssistant: {
			ID: PersonaEmergencyAssistant,
			Capabilities: map[Capability]bool{
				CapEscalate: true, CapHandOverDialer: true, CapEndCall: true,
			},
			Forbidden: map[Capability]bool{
				CapAnswerQuestion: true, CapAskClarification: true,
				CapTakeMessage: true, CapTransfer: true,
				CapDiscloseIdentity: true, CapDiscloseAvailability: true,
				CapCollectCallback: true, CapVerifyCaller: true,
			},
			ClarificationBudget:   0,
			MaxTurns:              3,
			MaxDuration:           30 * time.Second,
			EscalateOnUncertainty: true,
			LatencyProfile:        0.5,
		},
	}
}

// PersonaRuntime holds the active persona and governs switching.
type PersonaRuntime struct {
	clock   rt.Clock
	metrics *Metrics

	mu       sync.RWMutex
	registry map[PersonaID]Persona
	active   PersonaID
	history  []personaSwitch
	locked   bool
}

type personaSwitch struct {
	From, To PersonaID
	At       time.Time
	Reason   string
}

// NewPersonaRuntime constructs a persona runtime with the built-in personas.
//
// It builds a fresh registry, so a caller may Register over it without
// affecting anyone else. Engines use [newPersonaRuntimeFrom] instead, sharing
// one registry across conversations — see the note there.
func NewPersonaRuntime(initial PersonaID, clock rt.Clock, metrics *Metrics) (*PersonaRuntime, error) {
	return newPersonaRuntimeFrom(BuiltinPersonas(), initial, clock, metrics)
}

// newPersonaRuntimeFrom constructs a runtime over an existing registry.
//
// WHY THIS EXISTS. BuiltinPersonas builds four personas, each with two maps, on
// every call. Constructing it per conversation cost 139 allocations — roughly
// 60% of a whole conversation's allocation budget — to rebuild a value that is
// identical every time. The benchmark made that visible.
//
// The registry is now built once per [Engine] and shared. It is shared state,
// and it is safe because it is written once at engine construction and only
// read thereafter: Persona is a value type, and the maps inside it are never
// mutated after BuiltinPersonas returns. It is engine-scoped rather than
// package-scoped, so two engines still share nothing and the no-global-mutable-
// state property holds.
//
// A caller that needs a bespoke persona uses NewPersonaRuntime, which gets its
// own registry and may safely Register into it.
func newPersonaRuntimeFrom(reg map[PersonaID]Persona, initial PersonaID, clock rt.Clock, metrics *Metrics) (*PersonaRuntime, error) {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	if _, ok := reg[initial]; !ok {
		return nil, &ConfigError{Problems: []string{
			"persona: initial persona " + string(initial) + " is not registered"}}
	}
	return &PersonaRuntime{clock: clock, metrics: metrics, registry: reg, active: initial}, nil
}

// Register adds or replaces a persona. Used by a deployment with a bespoke
// role; the four built-ins cover the platform's own product.
//
// COPY-ON-WRITE, because the registry may be shared with an [Engine] and every
// other conversation it owns. Writing in place would let one conversation
// silently redefine a persona for every concurrent call — a data race and a
// correctness failure at once. The copy is paid only by the rare caller that
// registers, never on the conversation path.
func (p *PersonaRuntime) Register(persona Persona) error {
	if persona.ID == "" {
		return &ConfigError{Problems: []string{"persona: ID is required"}}
	}
	if persona.ClarificationBudget < 0 {
		return &ConfigError{Problems: []string{"persona: ClarificationBudget cannot be negative"}}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	next := make(map[PersonaID]Persona, len(p.registry)+1)
	for k, v := range p.registry {
		next[k] = v
	}
	next[persona.ID] = persona
	p.registry = next
	return nil
}

// Active returns the current persona.
func (p *PersonaRuntime) Active() Persona {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.registry[p.active]
}

// ActiveID returns the current persona's identifier.
func (p *PersonaRuntime) ActiveID() PersonaID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.active
}

// switchAllowed decides whether a persona transition is permitted.
//
// The rules, and why:
//
//  1. Any persona may switch to Emergency, always, from any state. U7 makes
//     emergency handling unconditional, and a rule that could block it would
//     eventually block it.
//  2. Emergency never switches away. Once a call is an emergency it stays one;
//     "we decided it wasn't an emergency after all" is not a judgement this
//     engine gets to make mid-call.
//  3. Any persona may switch to Fraud Shield. Narrowing capability is always
//     safe.
//  4. Fraud Shield never switches to a broader persona. A caller who talked
//     their way out of fraud screening is exactly the attack, and the
//     narrowing is therefore one-way.
func switchAllowed(from, to PersonaID) bool {
	if from == to {
		return false
	}
	if to == PersonaEmergencyAssistant {
		return true
	}
	if from == PersonaEmergencyAssistant {
		return false
	}
	if to == PersonaFraudShield {
		return true
	}
	if from == PersonaFraudShield {
		return false
	}
	return true
}

// Switch changes the active persona.
func (p *PersonaRuntime) Switch(to PersonaID, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.registry[to]; !ok {
		return &ConfigError{Problems: []string{"persona: " + string(to) + " is not registered"}}
	}
	from := p.active

	if p.locked && to != PersonaEmergencyAssistant {
		p.metrics.PersonaDenied.Inc(string(from), string(to))
		return ErrPersonaSwitchDenied
	}
	if !switchAllowed(from, to) {
		p.metrics.PersonaDenied.Inc(string(from), string(to))
		return ErrPersonaSwitchDenied
	}

	p.active = to
	p.history = append(p.history, personaSwitch{From: from, To: to, At: p.clock.Now(), Reason: reason})
	p.metrics.PersonaSwitches.Inc(string(from), string(to))

	// Emergency and Fraud Shield are one-way. Locking here rather than relying
	// on switchAllowed alone means a future registered persona cannot open a
	// path out by accident.
	if to == PersonaEmergencyAssistant || to == PersonaFraudShield {
		p.locked = true
	}
	return nil
}

// Locked reports whether the persona can no longer broaden.
func (p *PersonaRuntime) Locked() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.locked
}

// SwitchCount returns how many persona changes have occurred.
func (p *PersonaRuntime) SwitchCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.history)
}
