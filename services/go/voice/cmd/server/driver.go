package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// PHASE 16 T7 — THE INTERNAL CALL DRIVER.
//
// # What this is
//
// The component that was missing. Phase 16 T2 audited who owns each step of a
// call's life and found the same answer nine times: nobody. Nobody minted a
// ConversationID, nobody constructed a Pipeline, nobody sent EventStart, and
// after T6 gave the greeting a route into the speaking path, nobody called it.
// Every conversation in this repository was opened by a test.
//
// This file is the owner. It orchestrates components that already exist and
// adds no dialogue behaviour of its own: no second engine, no second planner,
// no second classifier, no session registry, no package-level state.
//
// # What this is NOT
//
//	DETERMINISTIC INTERNAL CALL DRIVER  ≠  REAL TELEPHONY
//
// There is no SIP, no PSTN, no WebRTC, no RTP, no WebSocket, no HTTP media
// ingestion and no microphone. Inbound audio arrives as media.Frame values
// handed to [Call.Audio] by whoever placed the call. What this proves is that
// the production orchestration of the existing components is correct — nothing
// about carrying a real phone call.
//
// # Why it is not registered by the production build
//
// A call needs audio-bearing ports: a provider registry with a recogniser and a
// voice, an audio-intelligence analyser, a generator, a governor and a media
// sink. Every shipped provider shells out to an external binary or a model, and
// this phase remains no-model/no-network (ADR-0017), so run() supplies none and
// registers no driver. [withCallDriver] is how a deployment that HAS providers
// registers one, and it is how the deterministic suite drives real calls.

// CallDriverConfig is everything a call needs that is not the call itself.
type CallDriverConfig struct {
	// Pipeline is the per-call pipeline configuration, minus the four fields
	// that belong to one call: Session, Call, FSM and Planner.
	//
	// A TEMPLATE RATHER THAN TWENTY FIELDS. Re-declaring the pipeline's
	// configuration here would be a second copy of it, and the first
	// divergence would be silent — a bound tightened in one place and not the
	// other. Whatever the deployment configures, the call gets.
	Pipeline voice.PipelineConfig

	// Persona selects the conversation persona. Empty means the engine's
	// default, which is the business receptionist.
	Persona conversation.PersonaID

	// NewCallPorts produces the ports that belong to ONE call. Required.
	//
	// # Why these three are not in the template
	//
	// The registry, the generator and the governor are process-wide: one
	// provider pool, one kernel, one policy engine, shared by every call. The
	// three below are not. Audio intelligence holds per-session detector state,
	// a frame sink points at one caller's outbound media path, and an observer
	// watches one call. Sharing any of them across calls would put two callers'
	// audio into one sink and one caller's speech onset into another's turn —
	// the exact cross-session leak the isolation tests exist to catch.
	NewCallPorts func(voice.SessionID) (CallPorts, error)
}

// CallPorts are the ports that belong to one call.
type CallPorts struct {
	// Intel analyses this call's inbound audio. Required.
	Intel voice.Intelligence

	// Output receives this call's synthesised audio. Required.
	Output voice.FrameSink

	// Observer watches this call. Optional.
	Observer voice.PipelineObserver
}

// CallDriver places internal calls.
//
// One driver per process, owned by the service lifecycle. It holds no map of
// live calls: a call is reachable only through the value [CallDriver.Place]
// returned, and every call's pipeline is bound to the driver's run context, so
// shutdown reaches all of them without anything having to remember them.
type CallDriver struct {
	bridge *voiceintel.Bridge
	cfg    CallDriverConfig
	log    *slog.Logger

	// ready is closed when Run starts, and runCtx is valid from that moment.
	// Place before Run is refused rather than silently using a background
	// context, which would produce calls the service could not shut down.
	ready   chan struct{}
	runCtx  context.Context
	stopped atomic.Bool

	startOnce sync.Once
}

// Compile-time proof that the driver is a lifecycle component like any other.
// It is registered through platform.Service.Register and supervised by the
// service's own goroutine group; there is no second supervisor here.
var _ platform.Runner = (*CallDriver)(nil)

// ErrDriverNotRunning is returned by a driver that has not started or has
// stopped.
var ErrDriverNotRunning = errors.New("voice: the call driver is not running")

// ErrNotTheOpeningTurn is returned when EventStart does not produce the opening
// turn, which would mean the frozen conversation contract had changed
// underneath this driver.
var ErrNotTheOpeningTurn = errors.New("voice: EventStart did not produce the opening turn")

// NewCallDriver builds a driver over the process's one bridge.
func NewCallDriver(
	bridge *voiceintel.Bridge, cfg CallDriverConfig, log *slog.Logger,
) (*CallDriver, error) {
	if bridge == nil {
		return nil, voiceintel.ErrNoBridge
	}
	if log == nil {
		log = slog.Default()
	}
	return &CallDriver{
		bridge: bridge,
		cfg:    cfg,
		log:    log,
		ready:  make(chan struct{}),
	}, nil
}

// Name identifies the component in lifecycle logs.
func (d *CallDriver) Name() string { return "voice-call-driver" }

// Run publishes the driver's context and blocks until the service stops.
//
// The context is the point. Every call placed while it is live derives from it,
// so cancelling it — which is what the service does on shutdown — tears down
// every pipeline without this driver keeping a list of them. A registry would
// be a second place session identity lives, and would have to be pruned
// correctly on every exit path.
func (d *CallDriver) Run(ctx context.Context) error {
	// Logged BEFORE readiness is published, not after. Anything that observes
	// the driver as ready — an operator reading the log, a supervisor, a test —
	// must be able to see the record that says so; publishing first leaves a
	// window in which the driver is placing calls and has announced nothing.
	d.log.Info("call driver ready", slog.String("component", d.Name()))

	d.startOnce.Do(func() {
		d.runCtx = ctx
		close(d.ready)
	})

	<-ctx.Done()
	d.stopped.Store(true)
	return nil
}

// Shutdown releases the component.
//
// Nothing to close. Calls are bound to the run context, which the service has
// already cancelled by the time this is called; each pipeline tears itself down
// on that cancellation and closes its own Done channel. Sending a terminal
// event to conversations here would be inventing a shutdown policy for frozen
// code that already discards a conversation when it reaches a terminal state.
func (d *CallDriver) Shutdown(_ context.Context) error {
	d.stopped.Store(true)
	d.log.Info("call driver stopped", slog.String("component", d.Name()))
	return nil
}

// Place opens one call: a conversation, a session and a live pipeline.
//
// It does NOT greet. Greeting is a separate step because it produces audio and
// can fail on its own, and a caller that could not tell "the call did not open"
// from "the greeting did not play" would have no way to react differently.
func (d *CallDriver) Place(ctx context.Context) (*Call, error) {
	select {
	case <-d.ready:
	default:
		return nil, fmt.Errorf("%w: Place before Run", ErrDriverNotRunning)
	}
	if d.stopped.Load() || d.runCtx.Err() != nil {
		return nil, fmt.Errorf("%w: the service is shutting down", ErrDriverNotRunning)
	}

	// ONE IDENTITY, MINTED HERE, PER CALL.
	//
	// voice.NewSessionID is the existing production minter: a sortable
	// timestamp prefix and ten bytes of crypto/rand, bounded and label-safe.
	// The conversation takes the same value so a log line, a metric and a
	// conversation trace can be joined without a lookup table — and because two
	// identifiers for one call is two things to get out of step.
	session := voice.NewSessionID()
	id := conversation.ConversationID(session)

	// The Planner comes from the Bridge, which is the only production route to
	// a conversation. Constructing a conversation.Engine here would be a second
	// engine in the process and would put dialogue configuration in a service.
	planner, err := d.bridge.Planner(id, d.cfg.Persona)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", id, err)
	}
	conv, ok := d.bridge.Conversation(id)
	if !ok {
		return nil, fmt.Errorf("call %s: the bridge did not retain the conversation", id)
	}

	if d.cfg.NewCallPorts == nil {
		return nil, fmt.Errorf("call %s: no per-call ports are configured", id)
	}
	ports, err := d.cfg.NewCallPorts(session)
	if err != nil {
		return nil, fmt.Errorf("call %s: per-call ports: %w", id, err)
	}

	cfg := d.cfg.Pipeline
	cfg.Session = session
	cfg.Call = voice.CallID(session)
	cfg.Planner = planner
	cfg.Intel = ports.Intel
	cfg.Output = ports.Output
	cfg.Observer = ports.Observer

	fsm, err := voice.NewSessionFSM(voice.FSMConfig{
		Session:   session,
		Call:      cfg.Call,
		Metrics:   cfg.Metrics,
		Publisher: cfg.Publisher,
		Clock:     cfg.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("call %s: session state: %w", id, err)
	}
	cfg.FSM = fsm

	pipeline, err := voice.NewPipeline(cfg)
	if err != nil {
		return nil, fmt.Errorf("call %s: pipeline: %w", id, err)
	}

	// Bound to BOTH the caller's context and the driver's, so a call ends when
	// the placer says so OR when the service shuts down, and cannot outlive
	// either.
	callCtx, cancel := context.WithCancel(d.runCtx)
	stop := context.AfterFunc(ctx, cancel)

	if err := pipeline.Start(callCtx); err != nil {
		stop()
		cancel()
		return nil, fmt.Errorf("call %s: starting the session: %w", id, err)
	}

	d.log.Info("call placed",
		slog.String("component", d.Name()),
		slog.String("conversation_id", string(id)))

	call := &Call{
		id: id, session: session,
		planner: planner, conv: conv, pipeline: pipeline, fsm: fsm,
		cancel: func() { stop(); cancel() },
		log:    d.log,
		closed: make(chan struct{}),
	}

	// The session can die without anyone asking it to — a recogniser that will
	// not start ends it from the inside. Nothing else would then close the
	// conversation, so the call watches its own session for exactly that.
	go call.watch()

	return call, nil
}

// Call is one live internal call.
//
// Ownership is structural: the value IS the call. There is no lookup, no
// registry and no identifier-to-session map anywhere in this service, so two
// calls cannot reach each other's state and a stale identifier cannot resolve
// to a live session.
type Call struct {
	id      conversation.ConversationID
	session voice.SessionID

	planner  voice.Planner
	conv     *conversation.Conversation
	pipeline *voice.Pipeline
	fsm      *voice.SessionFSM

	cancel context.CancelFunc
	log    *slog.Logger

	greeted atomic.Bool
	ended   atomic.Bool

	// closed is closed once the call has finished terminating — after every
	// lifecycle event has been delivered, not merely when the pipeline stopped.
	//
	// THE TWO ARE DIFFERENT, and the difference is observable. Done() reports
	// that the voice session's goroutines have returned; a session that died on
	// its own is closed by [Call.watch] AFTER that, so anything waiting on
	// Done() alone can see the conversation mid-close.
	closed    chan struct{}
	closeOnce sync.Once
}

// Closed is closed when the call has fully terminated: the conversation has
// been told, and the session is gone.
func (c *Call) Closed() <-chan struct{} { return c.closed }

func (c *Call) markClosed() { c.closeOnce.Do(func() { close(c.closed) }) }

// ID returns the conversation identifier, which is also the session identifier.
func (c *Call) ID() conversation.ConversationID { return c.id }

// Session returns the voice session identifier.
func (c *Call) Session() voice.SessionID { return c.session }

// Conversation exposes the dialogue for inspection: state, floor, context,
// trace. Read-only in practice — the driver drives the call through events and
// audio, never by reaching in and setting state.
func (c *Call) Conversation() *conversation.Conversation { return c.conv }

// Pipeline exposes the live voice session.
func (c *Call) Pipeline() *voice.Pipeline { return c.pipeline }

// State returns the voice session state.
func (c *Call) State() voice.SessionState { return c.fsm.State() }

// Done is closed when the call's pipeline has finished.
func (c *Call) Done() <-chan struct{} { return c.pipeline.Done() }

// Greet performs the opening turn: EventStart, then the plan it returns is
// spoken through the pipeline.
//
// # What this deliberately does not do
//
// It does not send EventGreetingComplete. That event means "the greeting audio
// finished", and only the component that delivered the audio knows when that
// is; [voice.Pipeline.Speak] emits it at the delivery boundary. A driver that
// sent it here would be asserting completion it cannot observe — and would send
// it twice on every call.
//
// It also writes no greeting text. The words come from the deployment's
// GreetingSystem instruction; this driver would be authoring the caller's
// lawful-basis announcement, which is a compliance decision and not a service's
// to make.
//
// Blocks until the greeting has been delivered or has failed.
func (c *Call) Greet(ctx context.Context) error {
	if c.ended.Load() {
		return fmt.Errorf("call %s: %w", c.id, voice.ErrSessionClosed)
	}
	if !c.greeted.CompareAndSwap(false, true) {
		// A second greeting would be a second opening turn. The frozen engine
		// refuses the duplicate EventStart anyway; refusing here means the
		// refusal is the driver's own contract rather than a rejected event
		// whose plan a caller might use.
		return fmt.Errorf("call %s: the opening turn has already been taken", c.id)
	}

	plan, err := c.planner.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		return fmt.Errorf("call %s: starting the conversation: %w", c.id, err)
	}

	// THE PLAN IS CHECKED, NOT ASSUMED. Speak chooses the completion event from
	// plan.NextState, so a plan that is not the opening turn would be completed
	// as ordinary speech and the conversation would be left in its greeting
	// phase with the caller queued behind it.
	if plan.NextState != conversation.StateGreeting {
		return fmt.Errorf("call %s: %w: got %v/%q",
			c.id, ErrNotTheOpeningTurn, plan.Action, plan.Reason)
	}

	if err := c.pipeline.Speak(ctx, plan); err != nil {
		return fmt.Errorf("call %s: speaking the greeting: %w", c.id, err)
	}

	// The conversation must have left its opening turn. The floor is NOT the
	// caller's at this point and must not be asserted to be: the frozen
	// handleGreetingComplete RELEASES the agent's turn to nobody rather than
	// handing it over, which is what an announcement is. The caller acquires it
	// by speaking.
	if got := c.conv.State(); got != conversation.StateListening {
		return fmt.Errorf("call %s: after the greeting the conversation is %v, "+
			"not listening", c.id, got)
	}
	return nil
}

// Audio submits one inbound frame.
//
// The transport boundary. In a deployment with real telephony this is where
// carrier audio would arrive; here it is whatever the placer hands over, and
// the payload is borrowed exactly as media.Frame requires.
func (c *Call) Audio(f media.Frame) error {
	if c.ended.Load() {
		return voice.ErrSessionClosed
	}
	return c.pipeline.WriteFrame(f)
}

// End terminates the call. Idempotent.
//
// The conversation is told the call ended with the frozen vocabulary's own
// EventHangup, and the session is disconnected rather than cancelled: a call
// that finished is not a call that was aborted, and recording one as the other
// makes an ordinary ending look like a fault in every report that counts them.
func (c *Call) End(reason string) error {
	return c.terminate(reason, nil)
}

// terminate closes the call. Idempotent; the first caller wins.
//
// # Why a fault is two events and not one
//
// A provider failure is two facts — WHY the call died, and THAT it is over —
// and the frozen vocabulary already has both. EventFault records the cause and
// moves the conversation to StateError; EventHangup then closes it. Sending
// only the fault would not be enough: StateError is deliberately NOT terminal
// ("that is the difference between an error and a failure", conversation
// state.go:111), so the conversation would sit open, still accepting events,
// beside a voice session that no longer exists — which is precisely the defect
// Phase 16 T8 found.
//
// Neither event is new and neither is repurposed: this is the vocabulary used
// as written.
func (c *Call) terminate(reason string, fault error) error {
	if !c.ended.CompareAndSwap(false, true) {
		return nil
	}

	// Told BEFORE the pipeline goes away, while the events can still be
	// delivered. ErrTerminal is expected throughout: a conversation that
	// already ended needs neither, and the frozen engine refuses them by
	// design.
	if fault != nil {
		// The cause, recorded first. The REASON is a code this service
		// authored; the error itself is deliberately not forwarded into the
		// conversation, because a provider's error text is not dialogue and has
		// no business in a transcript.
		c.emit(conversation.Event{
			Kind:   conversation.EventFault,
			Party:  conversation.PartyNone,
			Reason: reason,
		}, "fault")
	}

	party := conversation.PartyCaller
	if fault != nil {
		// NOT attributed to the caller. Nobody hung up; the platform closed a
		// call it could no longer serve, and recording that as a caller hangup
		// would misreport every provider outage as customers dropping off.
		party = conversation.PartyNone
	}
	c.emit(conversation.Event{
		Kind:   conversation.EventHangup,
		Party:  party,
		Reason: reason,
	}, "hangup")

	err := c.pipeline.Disconnect()
	c.cancel()

	// LAST. Everything a closing call promises has now happened: the
	// conversation has been told and the session has been stopped.
	c.markClosed()
	return err
}

// emit sends one lifecycle event, tolerating a conversation that has already
// ended.
func (c *Call) emit(e conversation.Event, what string) {
	if _, err := c.planner.Handle(e); err != nil &&
		!errors.Is(err, conversation.ErrTerminal) {
		// The identifier and the failure, never the caller's words.
		c.log.Warn("conversation refused a lifecycle event",
			slog.String("conversation_id", string(c.id)),
			slog.String("event", what),
			slog.String("error", err.Error()))
	}
}

// watch closes the conversation if the voice session dies on its own.
//
// # The gap this fills
//
// A provider failure inside the pipeline ends the SESSION — it has no other
// option, and its typed error is preserved on [voice.Pipeline.Err]. What it
// cannot do is close the CONVERSATION: the pipeline is below the dialogue layer
// and does not own termination. Before this, a recogniser that would not start
// left a dead session next to a live conversation, and nothing ever closed it.
//
// # Why only on a fault
//
// A session that finishes because somebody called End, or because the service
// is shutting down, has already been handled: End closed the conversation, and
// on shutdown the conversations die with the engine that owns them. Terminating
// here as well would record a spurious hangup on every clean exit.
//
// The goroutine's lifetime is the pipeline's: it waits on a channel that always
// closes, so there is nothing to leak and nothing to cancel.
func (c *Call) watch() {
	// Marked on EVERY exit, including the ones that do nothing: a session that
	// ended cleanly without End — the service shutting down — still has to
	// report that the call is over, or a caller waiting on Closed waits for
	// something that will never happen.
	defer c.markClosed()

	<-c.pipeline.Done()

	fault := c.pipeline.Err()
	if fault == nil || c.ended.Load() {
		return
	}

	c.log.Error("closing a call whose voice session failed",
		slog.String("conversation_id", string(c.id)),
		slog.String("error", fault.Error()))

	_ = c.terminate(voice.ReasonCrashed, fault)
}
