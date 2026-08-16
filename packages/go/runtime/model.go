package runtime

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ModelTier names a rung on the four-tier ladder frozen in ADR-0006.
//
// This is the one domain concept the runtime knows, and it is here because tier
// is a routing input the runtime must act on — not because the runtime
// understands why a tier was chosen. Deciding the tier is the orchestration
// layer's job; honouring it is this package's.
type ModelTier int

const (
	// TierNone is deterministic handling with no model involved. It exists on
	// the ladder because "do not call a model" is a legitimate and frequently
	// correct routing outcome, and modelling it as the absence of a tier would
	// make the cheapest path the one the runtime cannot express.
	TierNone ModelTier = iota

	// TierFast is the low-latency, low-cost rung.
	TierFast

	// TierBalanced is the default reasoning rung.
	TierBalanced

	// TierDeep is the highest-capability rung.
	TierDeep
)

// String renders the tier for logs and metric labels.
func (t ModelTier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierFast:
		return "fast"
	case TierBalanced:
		return "balanced"
	case TierDeep:
		return "deep"
	default:
		return "invalid"
	}
}

// Valid reports whether t is a defined tier.
func (t ModelTier) Valid() bool { return t >= TierNone && t <= TierDeep }

// Escalate returns the next tier up, or t if already at the top.
//
// Escalation is monotonic and single-step by design. A runtime that could jump
// from fast to deep in one hop would make cost spikes invisible in the tier
// transition record, and ADR-0002 §11 makes per-call cost a first-class
// concern.
func (t ModelTier) Escalate() ModelTier {
	if t >= TierDeep {
		return TierDeep
	}
	return t + 1
}

// Downgrade returns the next tier down, or t if already at the bottom.
//
// Used under load shedding: Invariant I11 permits downgrading a tier and
// forbids skipping the safety layer. Downgrading below TierFast means not
// calling a model at all, which is TierNone and is a legitimate destination.
func (t ModelTier) Downgrade() ModelTier {
	if t <= TierNone {
		return TierNone
	}
	return t - 1
}

// ModelSpec describes one registered model.
//
// It is a value type and is copied on read. Registry entries are therefore
// immutable from a caller's perspective, which removes a whole class of bug in
// which a caller mutates a shared spec and changes routing for everyone.
type ModelSpec struct {
	// ID is the model's stable identifier, for example "claude-haiku-4-5".
	ID ModelID

	// Provider binds the model to a registered provider.
	Provider ProviderID

	// Tier places the model on the ladder. Several models may share a tier;
	// the registry picks among them by Priority.
	Tier ModelTier

	// Priority orders models within a tier. Lower is preferred. Ties are broken
	// by ID for determinism — a registry that returns a different model on each
	// call for the same input makes an incident impossible to reason about.
	Priority int

	// MaxContextTokens is the model's context ceiling.
	MaxContextTokens int

	// MaxOutputTokens is the model's completion ceiling.
	MaxOutputTokens int

	// SupportsThinking reports whether the model supports extended thinking.
	SupportsThinking bool

	// SupportsToolCalling reports whether the model can emit tool calls.
	//
	// INVARIANT I3. A model with this set must also have SupportsThinking, and
	// [ModelRegistry.Register] refuses the registration otherwise. The
	// invariant exists because disabling thinking on a tool-calling tier
	// silently drops tool calls with no error, and an invisible failure cannot
	// be caught downstream.
	SupportsToolCalling bool

	// DefaultMaxOutputTokens bounds a completion when the caller does not
	// specify one.
	DefaultMaxOutputTokens int

	// TypicalLatency is the observed p50 for a short completion. Used by the
	// scheduler to decide whether a request can plausibly finish inside its
	// remaining budget before it is admitted — starting work that cannot
	// finish in time wastes the capacity that would have served the next
	// request.
	TypicalLatency time.Duration

	// Enabled allows a model to be registered but withheld from routing. Used
	// for staged rollout and for taking a model out of rotation without a
	// deploy.
	Enabled bool
}

// validate reports every problem with a spec, rather than the first.
func (s ModelSpec) validate() []string {
	var problems []string
	if s.ID == "" {
		problems = append(problems, "model ID is required")
	}
	if s.Provider == "" {
		problems = append(problems, fmt.Sprintf("model %s: provider is required", s.ID))
	}
	if !s.Tier.Valid() {
		problems = append(problems, fmt.Sprintf("model %s: tier %d is not valid", s.ID, s.Tier))
	}
	if s.Tier == TierNone {
		problems = append(problems, fmt.Sprintf("model %s: TierNone means no model is invoked and cannot have a model bound to it", s.ID))
	}
	if s.MaxContextTokens <= 0 {
		problems = append(problems, fmt.Sprintf("model %s: MaxContextTokens must be positive", s.ID))
	}
	if s.MaxOutputTokens <= 0 {
		problems = append(problems, fmt.Sprintf("model %s: MaxOutputTokens must be positive", s.ID))
	}
	if s.DefaultMaxOutputTokens > s.MaxOutputTokens {
		problems = append(problems, fmt.Sprintf("model %s: DefaultMaxOutputTokens exceeds MaxOutputTokens", s.ID))
	}
	// INVARIANT I3, enforced at registration rather than at request time. A
	// model that could be registered in this state would be a latent invariant
	// violation waiting for the first tool call.
	if s.SupportsToolCalling && !s.SupportsThinking {
		problems = append(problems, fmt.Sprintf(
			"model %s: I3 — a tool-calling model must support thinking; "+
				"disabling it silently drops tool calls with no error", s.ID))
	}
	return problems
}

// ModelRegistry holds the model catalogue and resolves tiers to models.
//
// It is read-heavy and written rarely — typically once at boot and thereafter
// only by a rollout — so it uses an RWMutex and copies on read rather than
// handing out pointers into shared state.
type ModelRegistry struct {
	mu       sync.RWMutex
	byID     map[ModelID]ModelSpec
	byTier   map[ModelTier][]ModelID // maintained in resolution order
	fallback map[ModelID]ModelID     // model -> explicit fallback
}

// NewModelRegistry returns an empty registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		byID:     make(map[ModelID]ModelSpec),
		byTier:   make(map[ModelTier][]ModelID),
		fallback: make(map[ModelID]ModelID),
	}
}

// Register adds or replaces a model.
//
// It validates fully and returns every problem at once. A registry that accepts
// a half-valid spec produces a failure at request time, far from the
// configuration that caused it.
func (r *ModelRegistry) Register(spec ModelSpec) error {
	if problems := spec.validate(); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.byID[spec.ID] = spec
	r.reindexLocked()
	return nil
}

// SetFallback records an explicit fallback model, used when the preferred model
// is circuit-broken or its provider is unavailable.
//
// An explicit fallback overrides tier-order resolution because the two express
// different intents: tier order says "any model of this capability will do",
// while a fallback says "when this specific model fails, use this specific
// other one". Conflating them loses the second.
func (r *ModelRegistry) SetFallback(from, to ModelID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byID[from]; !ok {
		return fmt.Errorf("%w: model %s", ErrNotFound, from)
	}
	if _, ok := r.byID[to]; !ok {
		return fmt.Errorf("%w: fallback model %s", ErrNotFound, to)
	}
	if from == to {
		return &ConfigError{Problems: []string{
			fmt.Sprintf("model %s: fallback cannot be itself", from)}}
	}
	// Reject a cycle at configuration time. A fallback loop is a hang, and a
	// hang on the screening path is a dropped call.
	seen := map[ModelID]bool{from: true}
	for cur := to; ; {
		if seen[cur] {
			return &ConfigError{Problems: []string{
				fmt.Sprintf("model %s: fallback chain forms a cycle at %s", from, cur)}}
		}
		seen[cur] = true
		next, ok := r.fallback[cur]
		if !ok {
			break
		}
		cur = next
	}

	r.fallback[from] = to
	return nil
}

// reindexLocked rebuilds the tier index. Caller must hold r.mu for writing.
func (r *ModelRegistry) reindexLocked() {
	byTier := make(map[ModelTier][]ModelID, len(r.byTier))
	for id, spec := range r.byID {
		byTier[spec.Tier] = append(byTier[spec.Tier], id)
	}
	for tier, ids := range byTier {
		specs := r.byID
		sort.Slice(ids, func(i, j int) bool {
			a, b := specs[ids[i]], specs[ids[j]]
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			// Deterministic tie-break. Without it, two equal-priority models
			// resolve in map-iteration order, which differs per process and
			// makes an incident unreproducible.
			return a.ID < b.ID
		})
		byTier[tier] = ids
	}
	r.byTier = byTier
}

// Get returns a model spec by ID.
func (r *ModelRegistry) Get(id ModelID) (ModelSpec, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.byID[id]
	if !ok {
		return ModelSpec{}, fmt.Errorf("%w: model %s", ErrNotFound, id)
	}
	return spec, nil
}

// ResolveTier returns the preferred enabled model for a tier.
//
// avoid lists models already tried and found unavailable, so a caller retrying
// after a provider failure gets a genuinely different model rather than the
// same one again.
func (r *ModelRegistry) ResolveTier(tier ModelTier, avoid ...ModelID) (ModelSpec, error) {
	if tier == TierNone {
		return ModelSpec{}, fmt.Errorf("%w: TierNone invokes no model", ErrNotFound)
	}

	skip := make(map[ModelID]bool, len(avoid))
	for _, id := range avoid {
		skip[id] = true
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, id := range r.byTier[tier] {
		if skip[id] {
			continue
		}
		spec := r.byID[id]
		if !spec.Enabled {
			continue
		}
		return spec, nil
	}
	return ModelSpec{}, fmt.Errorf("%w: no enabled model for tier %s", ErrNotFound, tier)
}

// Fallback returns the explicit fallback for a model, if one is configured.
func (r *ModelRegistry) Fallback(id ModelID) (ModelSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	to, ok := r.fallback[id]
	if !ok {
		return ModelSpec{}, false
	}
	spec, ok := r.byID[to]
	if !ok || !spec.Enabled {
		return ModelSpec{}, false
	}
	return spec, true
}

// SetEnabled turns a model in or out of rotation without a deploy.
func (r *ModelRegistry) SetEnabled(id ModelID, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("%w: model %s", ErrNotFound, id)
	}
	spec.Enabled = enabled
	r.byID[id] = spec
	return nil
}

// List returns every registered spec, ordered by tier then priority.
func (r *ModelRegistry) List() []ModelSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ModelSpec, 0, len(r.byID))
	for tier := TierFast; tier <= TierDeep; tier++ {
		for _, id := range r.byTier[tier] {
			out = append(out, r.byID[id])
		}
	}
	return out
}

// BuildRequest assembles a provider request for a model, applying defaults and
// enforcing invariants.
//
// This is the single place a GenerateRequest may be constructed for dispatch.
// Constructing one by hand and handing it to a provider bypasses the invariant
// checks below, which is why [Kernel.Generate] takes a [GenerateSpec] rather
// than a GenerateRequest.
func (r *ModelRegistry) BuildRequest(spec GenerateSpec, model ModelSpec) (GenerateRequest, error) {
	maxOut := spec.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = model.DefaultMaxOutputTokens
	}
	if maxOut <= 0 || maxOut > model.MaxOutputTokens {
		maxOut = model.MaxOutputTokens
	}

	thinking := spec.Thinking

	// INVARIANT I3, enforced at the last point before dispatch.
	//
	// Registration already refuses a tool-calling model without thinking
	// support, so this catches the other direction: a caller explicitly asking
	// to disable thinking on a model that can call tools. That request is
	// refused rather than silently corrected, because silently correcting it
	// would hide a caller bug that will recur.
	if model.SupportsToolCalling {
		if !model.SupportsThinking {
			return GenerateRequest{}, invariant("I3",
				"model %s advertises tool calling without thinking support", model.ID)
		}
		if spec.ThinkingExplicitlySet && !spec.Thinking {
			return GenerateRequest{}, invariant("I3",
				"request for model %s disabled thinking on a tool-calling model; "+
					"this silently drops tool calls", model.ID)
		}
		thinking = true
	}

	if thinking && !model.SupportsThinking {
		return GenerateRequest{}, fmt.Errorf(
			"%w: model %s does not support thinking", ErrInvalidTransition, model.ID)
	}

	return GenerateRequest{
		RequestID:       spec.RequestID,
		SessionID:       spec.SessionID,
		Model:           model.ID,
		Messages:        spec.Messages,
		System:          spec.System,
		MaxOutputTokens: maxOut,
		Temperature:     spec.Temperature,
		Thinking:        thinking,
		Deadline:        spec.Deadline,
		Metadata:        spec.Metadata,
	}, nil
}

// GenerateSpec is what a caller asks the runtime for.
//
// It names a TIER, not a model. The caller states the capability it needs; the
// runtime decides which model provides it. That is the whole point of the
// ladder, and accepting a model ID here would let a caller pin a model and
// silently defeat failover.
type GenerateSpec struct {
	// SessionID identifies the owning session. Required.
	SessionID SessionID

	// RequestID correlates the request. Generated if empty.
	RequestID RequestID

	// Tier names the capability required. Required.
	Tier ModelTier

	// Messages is the context to send. The context manager may trim it.
	Messages []Message

	// System carries system-level instruction.
	System string

	// MaxOutputTokens bounds the completion. Zero means the model's default.
	MaxOutputTokens int

	// Temperature controls sampling. Nil means the provider default.
	Temperature *float64

	// Thinking requests extended thinking. Ignored — and forced true — for a
	// tool-calling model, per Invariant I3.
	Thinking bool

	// ThinkingExplicitlySet distinguishes "caller did not say" from "caller
	// said false". Without it, the zero value of Thinking is indistinguishable
	// from an explicit request to disable it, and I3 could not tell a default
	// from a violation.
	ThinkingExplicitlySet bool

	// Class assigns a scheduling class. Zero value is ClassStandard.
	Class Class

	// Deadline is the absolute completion deadline. Zero means the runtime
	// derives one from configuration.
	Deadline time.Time

	// Metadata is opaque provider-specific data.
	Metadata map[string]string
}
