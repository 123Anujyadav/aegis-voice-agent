// Package audiobridge adapts the Phase 11D audio intelligence engine to the
// Phase 11C speech pipeline.
//
// # Why this module exists at all
//
// Phase 11D's brief requires barge-in to cancel synthesis through the existing
// Phase 11C contract (§8) and simultaneously requires packages/go/audiointel to
// have a dependency closure of the standard library plus first-party modules,
// not sitting above packages/go/speech (§26, §29).
//
// Those cannot both hold inside one module. The Phase 11C contract is
// [speech.SpeechSession.Interrupt] and [speech.SpeechSession.EndOfSpeech];
// naming either inside audiointel means importing packages/go/speech, and
// packages/go/speech is FROZEN so it cannot be inverted to depend on audiointel
// instead.
//
// The resolution is a port. audiointel declares [audiointel.SpeechController]
// and calls it; this module implements that interface over a real speech
// session. audiointel's dependency graph stays provably clean, and the
// integration is compiled, tested code rather than two signatures that happen
// to line up.
//
// # What this module deliberately does not do
//
// It holds no policy. Every decision about WHETHER to interrupt — debouncing,
// staleness, whether the agent holds the floor — belongs to
// audiointel.BargeInDetector and stays there. This adapter translates one call
// into another and reports what happened.
package audiobridge

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// SpeechSession is the slice of Phase 11C this adapter needs.
//
// # An interface rather than *speech.SpeechSession, and not for mocking
//
// speech.SpeechSession's constructor is reached through a SpeechRuntime that
// owns provider routing, a turn manager and two orchestrators. Depending on the
// concrete type would make every test of this adapter a test of that whole
// assembly, and a failure anywhere in it would present as a bridge failure.
//
// The interface is satisfied by *speech.SpeechSession without any adapter on
// the speech side — TestBridge_RealSpeechSessionSatisfiesTheInterface asserts
// that with a compile-time assignment, so a Phase 11C signature change breaks
// this module's build rather than being discovered at runtime.
type SpeechSession interface {
	// Interrupt is Phase 11C's barge-in contract.
	Interrupt(reason string) (speech.InterruptResult, error)

	// EndOfSpeech is Phase 11C's endpoint seam — the method whose own
	// documentation calls itself "the VAD boundary, not a VAD".
	EndOfSpeech() error
}

// Compile-time proof that the frozen Phase 11C session satisfies the interface
// above with no adapter of its own.
//
// If Phase 11C ever changes either signature, THIS LINE FAILS TO COMPILE. That
// is the point: a contract mismatch should stop a build, not surface as a
// barge-in that silently stopped working in production.
var _ SpeechSession = (*speech.SpeechSession)(nil)

// Adapter implements [audiointel.SpeechController] over a Phase 11C session.
//
// Safe for concurrent use. audiointel calls it from whichever goroutine drives
// the session's frames, and a supervising goroutine may read the counters at
// the same time.
type Adapter struct {
	session SpeechSession

	interrupts atomic.Uint64
	refusals   atomic.Uint64
	endpoints  atomic.Uint64

	// lastLatencyMicros is Phase 11C's own measurement of its cancellation,
	// which is a DIFFERENT number from the one audiointel measures.
	//
	// audiointel.BargeInDecision.Latency covers detection through to this
	// adapter returning. speech.InterruptResult.Latency covers Phase 11C's
	// internal work: the generation bump, the provider stream close and the
	// turn transition. Both are inside ADR-0004 §12's 20 ms, and keeping them
	// separate is what lets an operator tell which side of the port a
	// regression is on.
	lastLatencyMicros atomic.Int64
}

// Compile-time proof that this adapter satisfies the Phase 11D port.
var _ audiointel.SpeechController = (*Adapter)(nil)

// New builds an adapter over a speech session.
//
// A nil session is refused rather than tolerated. audiointel already
// distinguishes "no controller wired" as [audiointel.BargeInNoController] and
// counts it as a configuration fault; an adapter wrapping nothing would hide
// that behind a successful-looking object.
func New(session SpeechSession) (*Adapter, error) {
	if session == nil {
		return nil, errors.New("audiobridge: a nil speech session cannot be adapted; " +
			"leave the controller unset instead, which audiointel counts as " +
			"no_controller rather than silently succeeding")
	}
	return &Adapter{session: session}, nil
}

// Interrupt cancels agent speech through Phase 11C.
//
// # Phase 11C refusing is a normal outcome, not an error to paper over
//
// speech.SpeechSession.Interrupt refuses unless the turn is responding or
// speaking, because a caller talking while the agent is listening is not
// interrupting — their audio already belongs to the live turn, and cancelling
// would throw away a transcript in progress.
//
// audiointel applies the same rule before calling here, through
// BargeInPolicy.RequireAgentSpeaking, so the two agree in the common case. They
// can still disagree in the window where the turn moved on between the
// detection and this call, and when that happens the refusal is returned
// unchanged: audiointel counts it as [audiointel.BargeInRefused] and does not
// retry, because a refusal means the thing to interrupt is already gone.
func (a *Adapter) Interrupt(_ context.Context, reason string) error {
	result, err := a.session.Interrupt(reason)
	if err != nil {
		a.refusals.Add(1)
		return fmt.Errorf("audiobridge: phase 11C refused the interruption: %w", err)
	}

	a.interrupts.Add(1)
	a.lastLatencyMicros.Store(result.Latency.Microseconds())
	return nil
}

// EndOfSpeech signals the endpoint through Phase 11C.
//
// This is the seam ADR-0005 C6 describes when it records that endpointing is
// ours and vendor endpointing is disabled or ignored. The 250 ms window that
// decided to call this lives in audiointel.EndpointPolicy; this method just
// delivers the verdict.
func (a *Adapter) EndOfSpeech(_ context.Context) error {
	if err := a.session.EndOfSpeech(); err != nil {
		return fmt.Errorf("audiobridge: phase 11C refused the endpoint: %w", err)
	}
	a.endpoints.Add(1)
	return nil
}

// Interrupts returns how many interruptions Phase 11C accepted.
func (a *Adapter) Interrupts() uint64 { return a.interrupts.Load() }

// Refusals returns how many Phase 11C declined.
func (a *Adapter) Refusals() uint64 { return a.refusals.Load() }

// Endpoints returns how many endpoints were delivered.
func (a *Adapter) Endpoints() uint64 { return a.endpoints.Load() }

// LastInterruptLatencyMicros returns Phase 11C's own measurement of its most
// recent cancellation.
//
// NOT the same number audiointel reports. See [Adapter.lastLatencyMicros].
func (a *Adapter) LastInterruptLatencyMicros() int64 {
	return a.lastLatencyMicros.Load()
}
