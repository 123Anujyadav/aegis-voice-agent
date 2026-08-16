package toolruntime

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Tool is what an adapter implements.
//
// ONE METHOD. The interface is this small on purpose: everything a tool
// integration might be tempted to own — retry, timeout, permission, budget,
// idempotency, compensation, audit — is the runtime's job, and an interface
// with hooks for those would invite every adapter to reimplement them
// differently. A tool does the thing and returns. That is the whole contract.
//
// Implementations MUST honour ctx cancellation. The runtime enforces deadlines
// by cancelling the context and abandoning the call; a tool that ignores
// cancellation leaks a goroutine per timed-out execution, and the runtime
// counts those (see ExecutionSupervisor.Abandoned).
//
// There is not one real implementation in this module. Real adapters — carrier,
// CRM, calendar, payments — are explicitly out of scope for Phase 10D.
type Tool interface {
	// Invoke performs the tool's work.
	Invoke(ctx context.Context, in Invocation) (Result, error)
}

// StreamingTool is optionally implemented by tools that produce partial results.
//
// Separate from [Tool] rather than a second method on it, so a tool that has
// nothing to stream does not have to write a stub that nobody reads. The
// executor type-asserts, and a tool that implements both gets the streaming
// path.
type StreamingTool interface {
	Tool
	// InvokeStream performs the work, emitting partial results through the
	// sink. The final Result is still returned: a stream is an early view of
	// the answer, never the answer itself, so a consumer that ignored the
	// stream still receives everything.
	InvokeStream(ctx context.Context, in Invocation, sink StreamSink) (Result, error)
}

// CompensatingTool is optionally implemented by tools whose effects can be
// undone.
//
// A tool that changes the world and cannot implement this is not refused — some
// actions genuinely cannot be undone, and pretending otherwise is worse than
// admitting it. It is recorded: the plan carries whether each step is
// compensable, and [Plan.FullyCompensable] tells a caller before anything runs
// whether a partial failure can be cleaned up.
type CompensatingTool interface {
	Tool
	// Compensate undoes a completed invocation. It receives the original
	// invocation and the result that must be undone, because undoing a booking
	// requires the booking reference the tool itself returned.
	Compensate(ctx context.Context, in Invocation, produced Result) error
}

// Invocation is everything a tool receives.
//
// It carries identity and deadline alongside the arguments so a tool can log,
// trace, and make its own idempotent call downstream using the same key the
// runtime is using. A tool that invents its own key downstream defeats the
// runtime's deduplication at the first hop.
type Invocation struct {
	// Execution identifies this attempt.
	Execution ExecutionID
	// Step names the plan step.
	Step StepID
	// Attempt is 1-based.
	Attempt int
	// Descriptor is the tool and pinned version being invoked.
	Descriptor Descriptor
	// Args are the validated arguments.
	Args Arguments
	// Idempotency is the key for this execution. Tools making a downstream
	// call SHOULD pass it through.
	Idempotency IdempotencyKey
	// Correlation ties this to the originating conversation turn.
	Correlation CorrelationID
	// Session is the conversation session, for audit only.
	Session SessionID
	// Actor is on whose behalf this runs.
	Actor ActorID
	// Deadline is when the runtime will abandon the call.
	Deadline time.Time
	// Budget states the limits this invocation runs under.
	Budget Budget
}

// FieldSpec describes one input or output field.
type FieldSpec struct {
	// Name is the field's key.
	Name string
	// Kind is the required kind.
	Kind ValueKind
	// Required refuses an absent field. A field that is Required and has a
	// Default is a configuration error, not a convenience: a default IS the
	// value for absence.
	Required bool
	// Default supplies a value when the field is absent and not required.
	Default Value
	// HasDefault distinguishes "default is null" from "no default", which are
	// different instructions and would otherwise be the same zero value.
	HasDefault bool
	// MaxLen bounds strings, bytes, lists and maps. Zero means unbounded.
	MaxLen int
	// MinNum and MaxNum bound numeric fields when Bounded is set.
	MinNum, MaxNum float64
	// Bounded enables the numeric range check. Explicit rather than inferred
	// from non-zero bounds, because 0 is a legitimate bound.
	Bounded bool
	// Enum restricts a string field to a fixed set. Empty means unrestricted.
	Enum []string
	// Sensitive marks a field whose value must never appear in a log, an event
	// or an audit record. The runtime fingerprints it instead.
	Sensitive bool
	// Description documents the field for operators and discovery.
	Description string
}

func (f FieldSpec) validate() []string {
	var problems []string
	if f.Name == "" {
		problems = append(problems, "field: name is required")
	}
	if f.Required && f.HasDefault {
		problems = append(problems, fmt.Sprintf(
			"field %s: required fields cannot have a default; a default IS the "+
				"value for absence", f.Name))
	}
	if f.MaxLen < 0 {
		problems = append(problems, fmt.Sprintf("field %s: MaxLen must not be negative", f.Name))
	}
	if f.Bounded && f.MinNum > f.MaxNum {
		problems = append(problems, fmt.Sprintf(
			"field %s: MinNum %g exceeds MaxNum %g", f.Name, f.MinNum, f.MaxNum))
	}
	if len(f.Enum) > 0 && f.Kind != ValueString {
		problems = append(problems, fmt.Sprintf(
			"field %s: Enum applies to string fields only, got %s", f.Name, f.Kind))
	}
	if f.HasDefault && !f.Default.IsNull() && f.Default.Kind() != f.Kind {
		problems = append(problems, fmt.Sprintf(
			"field %s: default is %s but field is %s", f.Name, f.Default.Kind(), f.Kind))
	}
	return problems
}

// check validates one present value against the spec.
func (f FieldSpec) check(v Value) error {
	if v.IsNull() {
		if f.Required {
			return fmt.Errorf("%w: field %s is required and was null", ErrInvalidInput, f.Name)
		}
		return nil
	}
	if v.Kind() != f.Kind {
		// Int where Float is wanted is accepted; the reverse is not. Widening
		// is lossless, narrowing is a silent truncation waiting to be a bug.
		if !(f.Kind == ValueFloat && v.Kind() == ValueInt) {
			return fmt.Errorf("%w: field %s expects %s, got %s",
				ErrInvalidInput, f.Name, f.Kind, v.Kind())
		}
	}
	if f.MaxLen > 0 && v.Len() > f.MaxLen {
		return fmt.Errorf("%w: field %s is %d long, limit is %d",
			ErrInvalidInput, f.Name, v.Len(), f.MaxLen)
	}
	if f.Bounded {
		n, ok := v.Flt()
		if !ok {
			return fmt.Errorf("%w: field %s has numeric bounds but is %s",
				ErrInvalidInput, f.Name, v.Kind())
		}
		if n < f.MinNum || n > f.MaxNum {
			return fmt.Errorf("%w: field %s is %g, outside [%g, %g]",
				ErrInvalidInput, f.Name, n, f.MinNum, f.MaxNum)
		}
	}
	if len(f.Enum) > 0 {
		s, _ := v.Str()
		found := false
		for _, allowed := range f.Enum {
			if s == allowed {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: field %s value is not one of %v", ErrInvalidInput, f.Name, f.Enum)
		}
	}
	return nil
}

// Effect classifies what invoking a tool does to the world.
//
// This is the most consequential field on a contract and the one an operator
// should read first. It drives retry safety, idempotency requirements and
// permission strictness — three decisions that would otherwise each be
// configured separately and inconsistently.
type Effect uint8

// The effect classes.
const (
	// EffectRead changes nothing. Safe to retry, safe to run twice, safe to
	// run speculatively.
	EffectRead Effect = iota

	// EffectIdempotentWrite changes the world, but running it twice with the
	// same key has the same outcome as running it once. Safe to retry.
	EffectIdempotentWrite

	// EffectWrite changes the world and running it twice does it twice.
	// Retried only under an idempotency key, and never speculatively.
	EffectWrite

	// EffectIrreversible changes the world in a way no compensation can undo:
	// a sent message, a placed call, a released payment. Never retried
	// automatically, never run in parallel with a fallback, and permitted only
	// with an explicit grant.
	EffectIrreversible
)

// String renders the effect.
func (e Effect) String() string {
	switch e {
	case EffectIdempotentWrite:
		return "idempotent_write"
	case EffectWrite:
		return "write"
	case EffectIrreversible:
		return "irreversible"
	default:
		return "read"
	}
}

// Mutating reports whether the effect changes the world.
func (e Effect) Mutating() bool { return e != EffectRead }

// AutoRetryable reports whether the runtime may retry this effect on its own.
//
// EffectWrite is retryable only because every mutating execution carries an
// idempotency key (INV-TOOL-3). EffectIrreversible is not, under any
// circumstances: an unanswered call to "send the message" might have sent it,
// and the only safe assumption is that it did.
func (e Effect) AutoRetryable() bool { return e != EffectIrreversible }

// Contract describes a tool completely enough that the runtime never has to ask
// the tool anything.
//
// The runtime holds contracts, not tools, wherever it can: planning,
// permission, validation and audit all work from the contract alone. That is
// what lets a plan be built and reviewed without a tool being reachable.
type Contract struct {
	// Descriptor is the tool identity and version.
	Descriptor Descriptor

	// Capabilities are what this tool provides. A tool may provide several;
	// intents reference capabilities, never tools.
	Capabilities []CapabilityID

	// Title and Description document the tool for operators and discovery.
	Title       string
	Description string

	// Owner names the team accountable for the tool. Required, because a tool
	// with no owner is a tool nobody fixes at 3 a.m.
	Owner string

	// Effect classifies what invoking it does.
	Effect Effect

	// Input and Output specify the fields.
	Input  []FieldSpec
	Output []FieldSpec

	// AllowExtraOutput permits result fields the contract does not declare.
	// Off by default: an undeclared output field is a tool that has drifted
	// from its contract, and finding out at the boundary is cheaper than
	// finding out downstream.
	AllowExtraOutput bool

	// Timeout bounds one attempt. Required and capped by the runtime; see
	// Config.MaxToolTimeout.
	Timeout time.Duration

	// Retry policy for this tool. Zero means the runtime default.
	Retry RetrySpec

	// Budget bounds resources for one execution. Zero fields take the
	// runtime default.
	Budget Budget

	// RequiredPermissions must all be granted before invocation.
	RequiredPermissions []Permission

	// RequiresConsent names a consent basis the caller must present. Empty
	// means none required.
	RequiresConsent string

	// Concurrency caps simultaneous executions of this tool. Zero means the
	// runtime default. This is the tool's own protection: a downstream that
	// tolerates ten concurrent calls should say ten, not discover eleven.
	Concurrency int

	// Streaming declares that the tool can emit partial results.
	Streaming bool

	// Compensable declares that the tool implements [CompensatingTool].
	// Declared as well as type-asserted, so a plan can report compensability
	// before any tool instance is looked up.
	Compensable bool

	// Tags carry operator metadata. Never used for routing — a tag that
	// affected behaviour would be an untyped configuration channel.
	Tags map[string]string
}

func (c Contract) validate(maxTimeout time.Duration) []string {
	problems := c.Descriptor.validate()

	if len(c.Capabilities) == 0 {
		problems = append(problems, fmt.Sprintf(
			"%s: at least one capability is required; a tool no intent can "+
				"reference is unreachable", c.Descriptor))
	}
	for _, cap := range c.Capabilities {
		if cap == "" {
			problems = append(problems, fmt.Sprintf("%s: empty capability", c.Descriptor))
		}
	}
	if c.Owner == "" {
		problems = append(problems, fmt.Sprintf(
			"%s: owner is required; an unowned tool is one nobody fixes", c.Descriptor))
	}
	if c.Timeout <= 0 {
		problems = append(problems, fmt.Sprintf(
			"%s: timeout is required and must be positive; an unbounded tool "+
				"call is an unbounded conversation", c.Descriptor))
	}
	if maxTimeout > 0 && c.Timeout > maxTimeout {
		problems = append(problems, fmt.Sprintf(
			"%s: timeout %s exceeds the runtime maximum %s", c.Descriptor, c.Timeout, maxTimeout))
	}
	if c.Concurrency < 0 {
		problems = append(problems, fmt.Sprintf("%s: concurrency must not be negative", c.Descriptor))
	}

	seen := make(map[string]bool, len(c.Input)+len(c.Output))
	for _, f := range c.Input {
		problems = append(problems, prefix(c.Descriptor, "input", f.validate())...)
		if seen["in:"+f.Name] {
			problems = append(problems, fmt.Sprintf("%s: duplicate input field %s", c.Descriptor, f.Name))
		}
		seen["in:"+f.Name] = true
	}
	for _, f := range c.Output {
		problems = append(problems, prefix(c.Descriptor, "output", f.validate())...)
		if seen["out:"+f.Name] {
			problems = append(problems, fmt.Sprintf("%s: duplicate output field %s", c.Descriptor, f.Name))
		}
		seen["out:"+f.Name] = true
	}

	if c.Effect == EffectIrreversible && c.Compensable {
		problems = append(problems, fmt.Sprintf(
			"%s: irreversible effects cannot be compensable; if it can be undone "+
				"it is not irreversible", c.Descriptor))
	}
	problems = append(problems, c.Retry.validate(c.Descriptor, c.Effect)...)
	problems = append(problems, c.Budget.validate(c.Descriptor)...)
	return problems
}

func prefix(d Descriptor, where string, problems []string) []string {
	if len(problems) == 0 {
		return nil
	}
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, fmt.Sprintf("%s %s %s", d, where, p))
	}
	return out
}

// Provides reports whether the contract offers a capability.
func (c Contract) Provides(cap CapabilityID) bool {
	for _, have := range c.Capabilities {
		if have == cap {
			return true
		}
	}
	return false
}

// InputSpec returns the spec for a named input field.
func (c Contract) InputSpec(name string) (FieldSpec, bool) {
	for _, f := range c.Input {
		if f.Name == name {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// ValidateInput checks arguments against the contract, returning the
// normalised arguments with defaults applied.
//
// It returns a COPY. Applying defaults in place would mutate a caller's map,
// and a plan built from those arguments would then differ from the plan the
// caller thought it built.
func (c Contract) ValidateInput(args Arguments) (Arguments, error) {
	out := make(Arguments, len(args)+len(c.Input))
	for k, v := range args {
		out[k] = v
	}

	declared := make(map[string]bool, len(c.Input))
	for _, f := range c.Input {
		declared[f.Name] = true
		v, present := out[f.Name]
		if !present {
			if f.Required {
				return nil, fmt.Errorf("%w: %s requires input %s",
					ErrInvalidInput, c.Descriptor, f.Name)
			}
			if f.HasDefault {
				out[f.Name] = f.Default
			}
			continue
		}
		if err := f.check(v); err != nil {
			return nil, fmt.Errorf("%s: %w", c.Descriptor, err)
		}
	}

	// Undeclared INPUT is always refused, with no opt-out, unlike undeclared
	// output. An unexpected argument is a caller sending something the tool
	// will silently ignore — a bug that presents as "the tool ignored my
	// request", which is among the hardest classes of bug to find from a
	// support ticket.
	for _, name := range out.Keys() {
		if !declared[name] {
			return nil, fmt.Errorf("%w: %s has no input field %s",
				ErrInvalidInput, c.Descriptor, name)
		}
	}
	return out, nil
}

// ValidateOutput checks a result against the contract.
//
// Output validation exists because a tool that returns garbage is a tool that
// poisons everything downstream of it, and the boundary is the only place the
// runtime can still tell truth from garbage cheaply.
func (c Contract) ValidateOutput(res Result) error {
	declared := make(map[string]bool, len(c.Output))
	for _, f := range c.Output {
		declared[f.Name] = true
		v, present := res[f.Name]
		if !present {
			if f.Required {
				return fmt.Errorf("%w: %s omitted required output %s",
					ErrInvalidOutput, c.Descriptor, f.Name)
			}
			continue
		}
		if err := f.check(v); err != nil {
			return fmt.Errorf("%w: %s output %s: %s",
				ErrInvalidOutput, c.Descriptor, f.Name, err.Error())
		}
	}
	if !c.AllowExtraOutput {
		for _, name := range res.Keys() {
			if !declared[name] {
				return fmt.Errorf("%w: %s returned undeclared output %s",
					ErrInvalidOutput, c.Descriptor, name)
			}
		}
	}
	return nil
}

// SensitiveFields returns the names of input fields marked sensitive, sorted.
func (c Contract) SensitiveFields() []string {
	var out []string
	for _, f := range c.Input {
		if f.Sensitive {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}
