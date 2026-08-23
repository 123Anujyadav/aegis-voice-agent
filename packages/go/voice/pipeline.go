package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	conversation "github.com/callscreen/callscreen-platform/packages/go/conversation"
	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// The streaming voice pipeline
// ---------------------------------------------------------------------------
//
// # What this file does, and the much longer list of what it does not
//
// It moves one caller's audio through the layers below it and moves the answer
// back. It performs no voice activity detection, no endpointing, no provider
// selection, no dialogue policy, no authorisation and no generation. Every one
// of those belongs to a frozen module, and the value of an orchestration layer
// is precisely that it orchestrates rather than reimplements:
//
//   - Voice activity, overlap and endpointing: audiointel.Session.Analyze.
//   - Provider selection and health: speech.ProviderRouter, reached through
//     [ProviderRegistry], which delegates to it.
//   - Dialogue decisions: conversation.Conversation.Handle, which returns a
//     Plan and never text.
//   - Authorisation: governance.Engine.Decide, on the path to speech, with no
//     branch around it.
//   - Generation: runtime.Kernel.Generate, whose Dispatcher already implements
//     the abort semantics ADR-0011 §5.1 requires.
//   - Sentence boundaries: speech.Chunker.
//
// # Streaming is structural here, not a claim
//
// The generation stage does not collect a response and then cut it up. It
// attaches a [runtime.Sink] to the dispatcher, and the dispatcher delivers each
// chunk as it arrives. That sink pushes text into the chunker and submits every
// completed clause to the synthesiser THE MOMENT the clause completes — while
// the model is still generating the rest of the sentence.
//
// The difference is audible. Buffering a whole response before speaking costs
// the caller the entire generation latency as dead air, which ADR-0007
// describes as "well over a second of dead air before the caller hears
// anything". TestPipeline_SynthesisBeginsBeforeGenerationEnds asserts the
// ordering directly, so the property cannot quietly regress into
// buffer-then-split.
//
// # Every queue is bounded, and a full queue says so
//
// An unbounded queue in front of a slow consumer does not prevent the failure;
// it converts a fast, visible one into a slow, invisible one that ends as an
// out-of-memory kill in the middle of a call. Each stage below has a fixed
// bound and reports [ErrBackpressure] rather than growing.

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------
//
// Narrow interfaces, each satisfied by the frozen type without an adapter on
// its side. The compile-time assertions below are the point: if a frozen
// signature changes, THIS PACKAGE STOPS COMPILING rather than the mismatch
// surfacing as a pipeline that silently stopped calling governance.
//
// They are not here for mocking. They are here because depending on the
// concrete types would drag their whole assemblies — a kernel with a scheduler
// and a model registry, an engine with a policy store — into the construction
// of a pipeline, and a failure anywhere in that assembly would present as a
// pipeline failure.

// Intelligence is the slice of Phase 11D this pipeline needs.
type Intelligence interface {
	Analyze(
		ctx context.Context,
		f media.Frame,
		state audiointel.ConversationState,
		controller audiointel.SpeechController,
		envelope audiointel.OutboundEnvelope,
	) (audiointel.Analysis, error)
}

// Planner is the slice of Phase 10B this pipeline needs.
//
// It returns a Plan and never text: conversation is a planner, not a generator,
// and the pipeline asking it for words would be asking it to be something it is
// not.
type Planner interface {
	Handle(e conversation.Event) (conversation.Plan, error)
}

// Governor is the slice of Phase 10E this pipeline needs.
type Governor interface {
	Decide(r governance.Request) governance.Decision
}

// Generator is the slice of Phase 10A this pipeline needs.
type Generator interface {
	Generate(ctx context.Context, spec rt.GenerateSpec, sinks ...rt.Sink) (*rt.Dispatcher, error)
}

// FrameSink is where synthesised audio goes.
//
// The media layer's inbound seam, named as a port because Phase 11B owns
// delivery and this package must not acquire a second opinion about it.
type FrameSink interface {
	// Deliver hands over one frame. It must not block indefinitely; a sink that
	// blocked would stall synthesis and, behind it, generation.
	Deliver(ctx context.Context, f media.Frame) error
}

// Compile-time proof that the frozen types satisfy these ports unadapted.
var (
	_ Intelligence = (*audiointel.Session)(nil)
	_ Planner      = (*conversation.Conversation)(nil)
	_ Governor     = (*governance.Engine)(nil)
	_ Generator    = (*rt.Kernel)(nil)
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// PipelineConfig configures one session's pipeline.
type PipelineConfig struct {
	// Session and Call identify the work.
	Session SessionID
	Call    CallID

	// Language selects providers through the registry.
	Language speech.Language

	// Format is the audio format on both directions of the call.
	Format media.AudioFormat

	// Registry selects providers. Required, and the ONLY way this pipeline
	// reaches one: it delegates to speech.ProviderRouter, so there is no second
	// routing engine here.
	Registry *ProviderRegistry

	// Intel analyses inbound audio. Required.
	Intel Intelligence

	// Planner decides what the dialogue does. Required.
	Planner Planner

	// Governor authorises speech. Required — there is no configuration in which
	// this pipeline speaks without a decision.
	Governor Governor

	// Generator produces the response. Required.
	Generator Generator

	// Output receives synthesised audio. Required.
	Output FrameSink

	// Tools is the governed-action gateway. Optional: a deployment with no
	// tools simply never calls one.
	//
	// It is the ONLY way an action leaves this layer, and it cannot be reached
	// without a governance decision — see governed.go.
	Tools *ToolGateway

	// Controller is Phase 11C's interruption seam, normally an
	// *audiobridge.Adapter over a speech.SpeechSession.
	//
	// OPTIONAL, AND ITS ABSENCE IS A REAL DEPLOYMENT. This pipeline drives
	// providers directly and owns its own generation guard, so it can interrupt
	// itself without one. What it cannot do without one is cancel a Phase 11C
	// session's synthesis, so a deployment that has one must wire it — and
	// audiointel already counts a missing controller as a configuration fault
	// rather than silence.
	Controller audiointel.SpeechController

	// FSM is the session's lifecycle state. Required.
	FSM *SessionFSM

	// Metrics and Publisher are optional observability.
	Metrics   *VoiceMetrics
	Publisher EventPublisher

	// Clock is injected for measurement. Nil means the system clock.
	Clock rt.Clock

	// Chunking configures sentence boundaries. Zero uses the telephony
	// baseline.
	Chunking speech.ChunkConfig

	// MaxPendingFrames bounds inbound audio awaiting analysis.
	MaxPendingFrames int

	// MaxPendingSegments bounds transcript segments awaiting forwarding.
	MaxPendingSegments int

	// MaxPendingAudio bounds synthesised frames awaiting delivery.
	//
	// THE SLOW-CONSUMER BOUND. A media sink that stalls must not be able to
	// accumulate a call's worth of audio in memory.
	MaxPendingAudio int

	// TurnTimeout bounds one whole turn.
	TurnTimeout time.Duration

	// Governance identity. Carried onto every decision.
	Actor    governance.ActorID
	Subject  governance.SubjectID
	Org      governance.OrgID
	Business governance.BusinessID

	// Tier names the model capability required. Required by the kernel.
	Tier rt.ModelTier

	// System is the system instruction for generation.
	System string

	// GreetingSystem is the system instruction used for the opening turn.
	//
	// # THE DEPLOYMENT'S WORDS, NOT THIS PACKAGE'S
	//
	// The greeting is the one response with no caller transcript to generate
	// from, and on this platform it carries the announcement that is the
	// caller's lawful basis. Nothing in this repository holds greeting text:
	// the plan carries "greeting" as a MARKER, and the persona carries
	// behavioural policy — budgets, capabilities, limits — not content. A
	// default here would be this package writing the product's opening line,
	// and a wrong opening line is a compliance defect rather than a bug.
	//
	// So it is configuration, it is optional, and it has no default. Left
	// empty, a greeting is generated from [PipelineConfig.System] and the
	// session context like any other response — which is legal and probably not
	// what a deployment wants.
	GreetingSystem string

	// MaxOutputTokens bounds a response.
	MaxOutputTokens int

	// Observer receives transcript and lifecycle notifications. Optional.
	Observer PipelineObserver
}

// PipelineObserver watches the pipeline without being able to steer it.
//
// # Lengths and codes, never content
//
// A transcript is the caller's words and a response is the model's. Neither may
// reach a log, a metric label or an event — §17 and the event contract both say
// so. The observer receives the SEGMENT for partial forwarding because a
// consumer genuinely needs the text to display live captions, and that consumer
// is inside the process; nothing here writes it anywhere.
type PipelineObserver interface {
	// OnTranscript is called for every segment, partial and final, in arrival
	// order.
	OnTranscript(seg speech.TranscriptSegment)

	// OnResponseChunk is called for each chunk of generated text as it is
	// submitted for synthesis.
	OnResponseChunk(c speech.Chunk)

	// OnTurnComplete reports how a turn ended.
	OnTurnComplete(outcome TurnOutcome, reason string)
}

func (c *PipelineConfig) validate() []string {
	var problems []string

	if !c.Session.Valid() {
		problems = append(problems, fmt.Sprintf("session %q is not a valid label", c.Session))
	}
	if c.Language == "" {
		problems = append(problems, "Language must be set; it selects the provider")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Registry == nil {
		problems = append(problems, "Registry is required: it is how this pipeline "+
			"reaches the speech router, and there is no second routing engine here")
	}
	if c.Intel == nil {
		problems = append(problems, "Intel is required: voice activity and endpointing "+
			"are Phase 11D's and this package does not reimplement them")
	}
	if c.Planner == nil {
		problems = append(problems, "Planner is required: the dialogue decision is "+
			"Phase 10B's and must not be bypassed")
	}
	if c.Governor == nil {
		problems = append(problems, "Governor is required: there is no configuration "+
			"in which this pipeline speaks without a governance decision")
	}
	if c.Generator == nil {
		problems = append(problems, "Generator is required")
	}
	if c.Output == nil {
		problems = append(problems, "Output is required")
	}
	if c.FSM == nil {
		problems = append(problems, "FSM is required: pipeline state and session state "+
			"must not be able to disagree")
	}
	if c.MaxPendingFrames <= 0 {
		problems = append(problems, "MaxPendingFrames must be positive")
	}
	if c.MaxPendingSegments <= 0 {
		problems = append(problems, "MaxPendingSegments must be positive")
	}
	if c.MaxPendingAudio <= 0 {
		problems = append(problems, "MaxPendingAudio must be positive; an unbounded "+
			"queue in front of a slow media sink grows for the length of the call")
	}
	if c.TurnTimeout <= 0 {
		problems = append(problems, "TurnTimeout must be positive")
	}
	if !c.Tier.Valid() || c.Tier == rt.TierNone {
		problems = append(problems, "Tier must name a tier that invokes a model")
	}
	return problems
}

// ---------------------------------------------------------------------------
// The pipeline
// ---------------------------------------------------------------------------

// Pipeline is one session's live voice loop.
//
// Safe for concurrent use: media writes frames from the transport goroutine, a
// supervisor may cancel from another, and the turn machinery runs on its own.
type Pipeline struct {
	cfg   PipelineConfig
	clock rt.Clock
	m     *VoiceMetrics

	// generation is bumped on every cancellation and every turn boundary.
	//
	// THE STALE-OUTPUT GUARD. Audio synthesised for a turn the caller has
	// already talked over must never reach them, and the check cannot be "is
	// the stream closed" because a frame already in flight has passed that
	// point. Every outbound frame carries the generation it was produced for
	// and is dropped if that generation is no longer current.
	generation atomic.Uint64

	frames chan media.Frame

	ctx    context.Context
	cancel context.CancelCauseFunc

	wg   sync.WaitGroup
	done chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	// speaking is held for the whole of a [Pipeline.Speak] call.
	//
	// The pipeline has exactly one active synthesis stream and one active
	// dispatcher, so two submitted responses would overwrite each other's. A
	// flag rather than a queue: a response that waited its turn would be spoken
	// into a conversation that had already moved past it.
	speaking atomic.Bool

	// interrupts is this pipeline's audiointel.SpeechController.
	interrupts *interruptController

	mu      sync.Mutex
	started bool
	failure error
	turnSeq uint64
	sttOpen speech.STTStream
	// sttSendClosed records that this turn's recogniser has already been told
	// the caller stopped speaking.
	//
	// AUDIO DOES NOT STOP WHEN THE UTTERANCE DOES. The carrier keeps
	// delivering frames — silence, breath, the next caller turn beginning —
	// and every state but a terminal one accepts them. Without this flag those
	// frames were written to a stream whose send side this pipeline had
	// already closed, and a provider entitled to refuse them ended the CALL.
	//
	// Not observable with an in-process fake, which tolerates a late write.
	// Found in Phase 16 T10 the first time a real subprocess recogniser was
	// wired in, and it failed the session on the very first turn.
	sttSendClosed bool
	stale         uint64
	observer      PipelineObserver

	// The live turn's cancellable parts, so a barge-in can abandon them.
	// Held here rather than passed around because the interruption arrives on
	// the ingest goroutine, which has no other way to reach them.
	activeDispatcher *rt.Dispatcher
	activeTTS        speech.TTSStream
	activeTurnCancel context.CancelFunc
}

// NewPipeline builds a pipeline. Nothing runs until Start.
func NewPipeline(cfg PipelineConfig) (*Pipeline, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	clock := cfg.Clock
	if clock == nil {
		clock = rt.SystemClock{}
	}
	m := cfg.Metrics
	if m == nil {
		m = NewVoiceMetrics()
	}
	// A ChunkConfig holds a slice, so it cannot be compared to its zero value.
	// MaxChars is the field that must be set for the chunker to work at all.
	if cfg.Chunking.MaxChars == 0 {
		cfg.Chunking = speech.DefaultChunkConfig()
	}

	p := &Pipeline{
		cfg:      cfg,
		clock:    clock,
		m:        m,
		frames:   make(chan media.Frame, cfg.MaxPendingFrames),
		done:     make(chan struct{}),
		observer: cfg.Observer,
	}
	p.interrupts = &interruptController{p: p}
	return p, nil
}

// Start begins the pipeline. Idempotent.
func (p *Pipeline) Start(ctx context.Context) error {
	var err error
	p.startOnce.Do(func() {
		p.ctx, p.cancel = context.WithCancelCause(ctx)

		p.mu.Lock()
		p.started = true
		p.mu.Unlock()

		if terr := p.cfg.FSM.To(p.ctx, StateListening, ReasonRequested); terr != nil {
			err = terr
			p.cancel(terr)
			close(p.done)
			return
		}

		p.wg.Add(1)
		go p.ingest()

		go func() {
			p.wg.Wait()
			close(p.done)
		}()
	})
	return err
}

// WriteFrame submits one inbound frame.
//
// # It reports backpressure rather than blocking
//
// This is called from the transport goroutine. Blocking here would stall the
// media reader, and a stalled media reader backs up into the carrier — the same
// reasoning speech.STTStream.Write documents. A dropped frame is a small,
// visible loss; a stalled carrier is a dropped call.
func (p *Pipeline) WriteFrame(f media.Frame) error {
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()

	if !started {
		return fmt.Errorf("%w: the pipeline has not been started", ErrSessionClosed)
	}
	if p.cfg.FSM.Terminal() {
		return ErrSessionClosed
	}

	select {
	case <-p.ctx.Done():
		return ErrSessionClosed
	default:
	}

	select {
	case p.frames <- f:
		return nil
	case <-p.ctx.Done():
		return ErrSessionClosed
	default:
		p.m.Backpressure.Inc(string(StageAudioIn))
		p.m.DroppedChunks.Inc(string(StageAudioIn), ReasonQueueFull)
		return fmt.Errorf("%w: %d inbound frames queued", ErrBackpressure, p.cfg.MaxPendingFrames)
	}
}

// InvokeTool performs a governed action on behalf of the live turn.
//
// # It adds no authorisation of its own, and removes none
//
// Everything it does is bind the session's context and the current turn to the
// request; the decision is [ToolGateway.Invoke]'s, which is Phase 10E's. A
// pipeline that pre-screened intents here would be the second policy engine
// this phase is forbidden to build.
//
// The session's context governs, so a caller who hangs up or is barged in on
// cancels an action that has not yet been decided.
func (p *Pipeline) InvokeTool(ctx context.Context, intent ToolIntent) (ToolResult, error) {
	if p.cfg.Tools == nil {
		return ToolResult{}, fmt.Errorf(
			"%w: no governed-action gateway is wired for this session",
			ErrProviderUnavailable)
	}

	p.mu.Lock()
	started := p.started
	p.mu.Unlock()

	if !started {
		return ToolResult{}, fmt.Errorf("%w: the pipeline has not been started",
			ErrSessionClosed)
	}
	if p.cfg.FSM.Terminal() {
		return ToolResult{}, ErrSessionClosed
	}

	// Bound to the SESSION as well as the caller's context: an action must not
	// outlive the call it was requested for.
	bound, cancel := mergeCancel(ctx, p.ctx)
	defer cancel()

	return p.cfg.Tools.Invoke(bound, p.cfg.FSM.Turn(), intent)
}

// mergeCancel returns a context cancelled when either parent is.
//
// context.WithCancel takes one parent, and the two here are genuinely
// independent: the caller's own deadline, and the session ending underneath it.
func mergeCancel(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)

	stop := make(chan struct{})
	go func() {
		select {
		case <-b.Done():
			cancel()
		case <-ctx.Done():
		case <-stop:
		}
	}()

	return ctx, func() {
		close(stop)
		cancel()
	}
}

// Cancel stops the pipeline by request.
func (p *Pipeline) Cancel(reason string) error { return p.stop(StateCancelled, reason) }

// Disconnect stops the pipeline because the call ended.
//
// Distinct from Cancel because the session ends differently: a caller hanging
// up completed their call, and recording it as cancelled would make an ordinary
// ending look like an abort in every report that counts them.
func (p *Pipeline) Disconnect() error { return p.stop(StateCompleted, ReasonCallEnded) }

// Fail stops the pipeline on an unrecoverable fault.
func (p *Pipeline) Fail(reason string, cause error) error {
	p.note(cause)
	return p.stop(StateFailed, reason)
}

// stop moves the session to a terminal state and tears everything down.
func (p *Pipeline) stop(end SessionState, reason string) error {
	var err error
	p.stopOnce.Do(func() {
		// The generation bump comes FIRST. Everything already synthesised for
		// the current turn becomes stale at this instant, so a frame in flight
		// cannot overtake the teardown and reach the caller after the session
		// ended.
		p.generation.Add(1)

		err = p.cfg.FSM.To(context.Background(), end, reason)

		if p.cancel != nil {
			p.cancel(fmt.Errorf("%w: %s", ErrSessionClosed, reason))
		}

		p.mu.Lock()
		stream := p.sttOpen
		p.sttOpen = nil
		p.mu.Unlock()
		if stream != nil {
			_ = stream.Close()
		}

		p.m.SessionsClosed.Inc(reason)
	})
	return err
}

// Done is closed when every goroutine has finished.
func (p *Pipeline) Done() <-chan struct{} { return p.done }

// Wait blocks until the pipeline has finished.
func (p *Pipeline) Wait() { <-p.done }

// Err returns the first fault the pipeline recorded, or nil.
func (p *Pipeline) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

// StaleFramesBlocked returns how many synthesised frames were withheld because
// their generation had been superseded.
func (p *Pipeline) StaleFramesBlocked() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stale
}

// Generation returns the current output generation.
func (p *Pipeline) Generation() uint64 { return p.generation.Load() }

func (p *Pipeline) note(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		p.failure = err
	}
}

// ---------------------------------------------------------------------------
// Stage 1 — inbound audio and audio intelligence
// ---------------------------------------------------------------------------

// ingest analyses every inbound frame and drives recognition from what Phase
// 11D concludes.
//
// It makes no acoustic judgement of its own. Onset and endpoint both come from
// the Analysis, which is what keeps a second, divergent VAD out of this
// package.
func (p *Pipeline) ingest() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			p.closeRecognition()
			return

		case f, ok := <-p.frames:
			if !ok {
				p.closeRecognition()
				return
			}
			if err := p.onFrame(f); err != nil {
				p.note(err)
				_ = p.stop(StateFailed, failureReason(err))
				return
			}
		}
	}
}

// failureReason maps a fatal ingest error onto the reason the session records.
//
// # "crashed" and "unavailable" send an operator to different places
//
// It is the same distinction errors.go draws between ErrProviderUnavailable and
// ErrProviderFailed, carried through to the session's terminal reason — which
// is what reaches metrics, events and the operator's dashboard. Reporting "the
// recogniser is not installed" as "crashed" sends somebody to look at the audio
// for a problem that is fixed by a path in a configuration file.
//
// The typed error itself is preserved separately by [Pipeline.note] and is
// available from [Pipeline.Err]; this is the label, not the cause.
func failureReason(err error) string {
	switch {
	case errors.Is(err, ErrProviderUnavailable):
		return ReasonUnavailable
	case errors.Is(err, ErrProviderTimeout):
		return ReasonTimeout
	default:
		return ReasonCrashed
	}
}

// onFrame handles one analysed frame.
func (p *Pipeline) onFrame(f media.Frame) error {
	state := audiointel.ConversationState{
		AgentSpeaking: p.cfg.FSM.State().AgentHoldsFloor(),
		Turn:          audiointel.TurnID(p.cfg.FSM.Turn()),
	}

	// Phase 11D's verdict, not ours — and the controller it may call is this
	// pipeline's orchestration, which runs INSIDE this Analyze call. By the time
	// it returns, an interruption has already invalidated the old generation and
	// returned the floor to the caller.
	//
	// The envelope stays nil: it is optional echo evidence and this package
	// does not instrument the outbound level.
	analysis, err := p.cfg.Intel.Analyze(p.ctx, f, state, p.interrupts, nil)
	if err != nil {
		if errors.Is(err, audiointel.ErrSessionClosed) {
			return nil
		}
		return fmt.Errorf("%w: audio intelligence: %v", ErrProviderFailed, err)
	}

	// A speech onset opens a turn.
	if analysis.VAD.OnsetConfirmed && p.cfg.FSM.State() == StateListening {
		if err := p.beginTurn(); err != nil {
			if !errors.Is(err, errTurnRaceLost) {
				return err
			}
			// The session moved between the check above and the transition
			// inside beginTurn. No turn was opened; the next onset opens one.
			p.m.DroppedChunks.Inc(string(StageRecognise), ReasonStaleChunk)
		}
	}

	// Audio flows to the recogniser whenever one is open.
	p.mu.Lock()
	stream := p.sttOpen
	sendClosed := p.sttSendClosed
	p.mu.Unlock()

	// Audio flows to the recogniser only while it is still listening for this
	// turn. Once the endpoint has been signalled the send side is closed, and
	// further frames belong to nothing: the next turn opens its own stream.
	if stream != nil && !sendClosed {
		switch err := stream.Write(f); {
		case err == nil:

		case errors.Is(err, speech.ErrBackpressure):
			// The recogniser is behind. Reported and counted; dropping one
			// frame degrades recognition slightly, whereas blocking here
			// would stall the carrier.
			p.m.Backpressure.Inc(string(StageRecognise))
			p.m.DroppedChunks.Inc(string(StageRecognise), ReasonQueueFull)

		case errors.Is(err, speech.ErrSpeechSessionClosed):
			// THE TURN THAT OWNED THIS RECOGNISER HAS ENDED, and this frame
			// arrived a moment too late.
			//
			// p.sttOpen is read under the mutex and written to outside it, so
			// turn teardown — which runs constantly during repeated barge-ins —
			// can close the stream in between. Losing that race costs one frame
			// of audio.
			//
			// It used to cost the CALL: every error that was not backpressure
			// failed the session, and Gate 4 caught it as "repeated barge-ins
			// ended the call". A stream closing underneath a frame is ordinary
			// during an interruption, not a fault.
			p.m.DroppedChunks.Inc(string(StageRecognise), ReasonStaleChunk)

		default:
			return fmt.Errorf("%w: writing audio: %v", ErrProviderFailed, err)
		}
	}

	// The end of the caller's turn is Phase 11D's call too.
	//
	// Issued ONCE. A second CloseSend is as refusable as a late Write, and the
	// endpoint can be confirmed on more than one frame.
	if analysis.Endpoint.Confirmed && stream != nil && !sendClosed {
		p.mu.Lock()
		p.sttSendClosed = true
		p.mu.Unlock()

		// THE ERROR IS NOT DISCARDED, and it used to be.
		//
		// For a batch recogniser this call is where inference STARTS — it is
		// the moment the process is spawned — so a failure here means the
		// provider never ran at all. Ignoring it left the turn waiting on a
		// results channel that would never close and never carry anything, and
		// the call hung until something else ended it.
		//
		// Failing the session is the honest response: a recogniser that cannot
		// be told the caller stopped speaking will fail identically on every
		// later turn. The T9 policy then closes the conversation with a fault.
		if err := stream.CloseSend(); err != nil {
			return fmt.Errorf("%w: ending recognition: %v", ErrProviderFailed, err)
		}
	}
	return nil
}

// beginTurn opens recognition for a new caller turn.
func (p *Pipeline) beginTurn() error {
	provider, err := p.cfg.Registry.PickSTT(p.cfg.Language)
	if err != nil {
		// Unsupported language and no healthy provider are different facts and
		// the router already distinguishes them; the error is forwarded rather
		// than flattened.
		return fmt.Errorf("%w: recognition: %v", ErrProviderUnavailable, err)
	}

	p.mu.Lock()
	p.turnSeq++
	turn := TurnID(fmt.Sprintf("%s-t%d", p.cfg.Session, p.turnSeq))
	p.mu.Unlock()

	p.cfg.FSM.SetTurn(turn)
	if err := p.cfg.FSM.To(p.ctx, StateSpeakingDetected, ReasonOK); err != nil {
		return p.classifyTransitionRace(err, StateListening)
	}

	stream, err := provider.OpenSTT(p.ctx, speech.STTConfig{
		Session:  speech.SessionID(p.cfg.Session),
		Turn:     speech.TurnID(turn),
		Language: p.cfg.Language,
		Format:   p.cfg.Format,
	})
	if err != nil {
		p.m.ProviderFailures.Inc(string(provider.ID()), string(KindSTT), ReasonStartFailed)
		p.cfg.Registry.Report(ProviderID(provider.ID()), speech.OutcomeFailure)
		return fmt.Errorf("%w: opening recognition: %v", ErrProviderUnavailable, err)
	}

	p.mu.Lock()
	p.sttOpen = stream
	p.sttSendClosed = false
	p.mu.Unlock()

	if err := p.cfg.FSM.To(p.ctx, StateTranscribing, ReasonOK); err != nil {
		_ = stream.Close()
		p.mu.Lock()
		if p.sttOpen == stream {
			p.sttOpen = nil
		}
		p.mu.Unlock()
		return p.classifyTransitionRace(err, StateSpeakingDetected)
	}

	p.m.TurnsStarted.Inc()

	p.wg.Add(1)
	go p.recognize(turn, provider.ID(), stream)
	return nil
}

// errTurnRaceLost reports that the session moved on between observing its
// state and acting on it.
//
// Not exported and never returned to a caller: it exists so onFrame can tell a
// lost race from a fault.
var errTurnRaceLost = errors.New("voice: the session moved on before the turn opened")

// classifyTransitionRace decides whether a refused transition is a lost race or
// a real invariant violation.
//
// # Why this is not "ignore ErrInvalidTransition"
//
// Swallowing every refused transition would hide a genuinely wrong state table,
// which is the one class of bug the Task 10 machine exists to make loud. The
// distinction is EVIDENCE: if the session is no longer in the state this
// transition was predicated on, something else legitimately moved it and this
// goroutine simply lost — benign. If it IS still in that state and the
// transition was still refused, the table and the code disagree, and that is a
// fault worth failing on.
func (p *Pipeline) classifyTransitionRace(err error, expected SessionState) error {
	if !errors.Is(err, ErrInvalidTransition) {
		return err
	}

	switch got := p.cfg.FSM.State(); {
	case got == expected:
		// Nothing moved, and the move was still refused. A real violation.
		return err

	case got.Terminal():
		// The call ended underneath this turn. Ordinary at hang-up.
		return fmt.Errorf("%w: the session ended (%s)", errTurnRaceLost, got)

	default:
		return fmt.Errorf("%w: expected %s, found %s", errTurnRaceLost, expected, got)
	}
}

// closeRecognition ends any open recognition stream.
func (p *Pipeline) closeRecognition() {
	p.mu.Lock()
	stream := p.sttOpen
	p.sttOpen = nil
	p.sttSendClosed = false
	p.mu.Unlock()

	if stream != nil {
		_ = stream.Close()
	}
}

// ---------------------------------------------------------------------------
// Stage 2 — transcripts
// ---------------------------------------------------------------------------

// recognize forwards segments as they arrive and runs the turn on the final.
//
// # Partials are forwarded the moment they exist
//
// Not accumulated and released at the end. A partial's whole value is that it
// is early — it drives live captions and lets the layer above see that
// recognition is working — and a partial delivered alongside the final is a
// partial that did nothing.
func (p *Pipeline) recognize(turn TurnID, provider speech.ProviderID, stream speech.STTStream) {
	defer p.wg.Done()

	started := p.clock.Now()
	var (
		final       speech.TranscriptSegment
		haveFinal   bool
		firstAt     time.Time
		partials    int
		firstIsSeen bool
	)

	for {
		select {
		case <-p.ctx.Done():
			_ = stream.Close()
			return

		case seg, ok := <-stream.Results():
			if !ok {
				// The recogniser finished. Whether it produced anything is what
				// decides the rest of the turn.
				// A FINAL THAT SAYS NOTHING IS NOT A TRANSCRIPT.
				//
				// A recogniser can end a stream with a final segment whose text
				// is blank — it believes it succeeded, which is why nothing
				// upstream reports an error. Treating that as a caller
				// utterance sends an empty question to the model, which answers
				// it, and the agent speaks unprompted at somebody who said
				// nothing. Indistinguishable from silence, and handled as
				// silence.
				if !haveFinal || strings.TrimSpace(final.Text) == "" {
					p.emptyTurn(ReasonEmptyTranscript)
					return
				}
				p.m.STTFinal.Observe(p.clock.Since(started).Seconds(), string(provider))
				p.cfg.Registry.Report(ProviderID(provider), speech.OutcomeSuccess)
				p.runTurn(turn, final)
				return
			}

			if !firstIsSeen {
				firstIsSeen = true
				firstAt = p.clock.Now()
				p.m.STTFirstPartial.Observe(firstAt.Sub(started).Seconds(), string(provider))
			}

			// FORWARDED IMMEDIATELY, partial or final.
			if p.observer != nil {
				p.observer.OnTranscript(seg)
			}
			segType := EventTranscriptPartial
			if seg.IsFinal {
				segType = EventTranscriptFinal
			}
			p.publish(VoiceEvent{
				Type:      segType,
				Session:   p.cfg.Session,
				Turn:      turn,
				Call:      p.cfg.Call,
				Provider:  ProviderID(provider),
				CharCount: len([]rune(seg.Text)),
				// The LENGTH, never the words.
				Confidence:    seg.Confidence,
				LatencyMicros: p.clock.Since(started).Microseconds(),
				At:            p.clock.Now(),
			})

			if seg.IsFinal {
				final, haveFinal = seg, true
			} else {
				partials++
			}
		}
	}
}

// emptyTurn ends a turn that produced nothing to answer.
//
// Silence is not a failure. Routing it through StateFailed would make an
// ordinary pause look like an outage in every dashboard that counts them.
// planSpeaks reports whether a plan requires the agent to say something.
//
// FOUR ACTIONS SPEAK, not one. ActionRespond answers; ActionClarify asks a
// disambiguating question; ActionConfirm asks for a yes/no; ActionAsk requests a
// missing slot (policy.go:12-20). All four set NextState=StateSpeaking, and the
// three questions additionally require the persona capability
// CapAskClarification (policy.go:64) — a capability is demanded precisely
// because the agent is about to speak.
//
// Before this, only ActionRespond reached the speaking path, so every
// clarification, confirmation and slot request the planner decided on was
// silently discarded: the question was never asked, the agent kept the floor,
// and the caller's answer was classified as a backchannel and ignored.
//
// The remainder — transfer, escalation, rejection, ignore, wait and end — are
// genuinely the layer above's, and this pipeline does not speak for them.
//
// ActionReject is deliberately EXCLUDED. Its documentation says it declines
// "politely", which hints at speech, but Phase 16 T1 was scoped to the three
// question actions and widening further would be deciding policy unasked.
func planSpeaks(a conversation.Action) bool {
	switch a {
	case conversation.ActionRespond, conversation.ActionClarify,
		conversation.ActionConfirm, conversation.ActionAsk:
		return true
	default:
		return false
	}
}

func (p *Pipeline) emptyTurn(reason string) {
	p.closeRecognition()
	if err := p.cfg.FSM.To(p.ctx, StateListening, reason); err != nil {
		p.note(err)
	}
	p.m.TurnsCompleted.Inc(string(OutcomeCompleted))
	if p.observer != nil {
		p.observer.OnTurnComplete(OutcomeCompleted, reason)
	}
}

// ---------------------------------------------------------------------------
// Stage 3 — plan, authorise, generate, synthesise
// ---------------------------------------------------------------------------

// runTurn takes a final transcript through the rest of the loop.
func (p *Pipeline) runTurn(turn TurnID, final speech.TranscriptSegment) {
	p.closeRecognition()

	if err := p.cfg.FSM.To(p.ctx, StateThinking, ReasonOK); err != nil {
		p.note(err)
		return
	}

	ctx, cancel := context.WithTimeout(p.ctx, p.cfg.TurnTimeout)
	defer cancel()

	// A barge-in reaches this turn through here. Recorded so the interruption,
	// which arrives on the ingest goroutine, can end the turn without ending
	// the session.
	p.mu.Lock()
	p.activeTurnCancel = cancel
	p.mu.Unlock()

	// ---- the dialogue decision, which is not ours to make -----------------
	plan, err := p.cfg.Planner.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		// ASRConfidence is the recogniser's own acoustic confidence, carried
		// through unchanged. The frozen intent engine gates on it
		// (conversation/intent.go:308): an utterance below MinASRConfidence is
		// classified as noise. Leaving it at its zero value made EVERY
		// utterance noise, so no intent could ever resolve and generation,
		// synthesis and audio output were unreachable.
		//
		// This is acoustic confidence, not intent confidence and not the
		// planner's decision confidence. It is only forwarded here; no
		// threshold is applied by this package.
		Utterance: conversation.Utterance{
			Text:          final.Text,
			ASRConfidence: final.Confidence,
		},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		p.note(fmt.Errorf("%w: conversation: %v", ErrProviderFailed, err))
		p.failTurn(ReasonInvalidOutput)
		return
	}

	if !planSpeaks(plan.Action) {
		// A plan that does not speak produces no audio on this path. Ending the
		// turn quietly is right: transfer, escalation, rejection and hangup are
		// the layer above's to execute, and this pipeline inventing words for
		// them would be it deciding policy.
		p.emptyTurn(ReasonOK)
		return
	}

	_ = p.deliverPlan(ctx, speakRequest{
		turn:   turn,
		plan:   plan,
		system: p.cfg.System,
		// The caller's words, unchanged. This is the ONE field that differs
		// between a turn the caller started and a turn the agent started.
		messages: []rt.Message{{Role: rt.RoleUser, Content: final.Text}},
		// The session itself. A caller turn has no external canceller, so the
		// guards below behave exactly as they did before this path was shared.
		abort: p.ctx,
	})
}

// Speak submits a plan the conversation has already decided on.
//
// # What it is for
//
// One response in this system is not an answer to anything the caller said: the
// greeting. conversation.EventStart returns it as a plan, and until this method
// existed there was nowhere to put that plan — the speaking path could only be
// entered by a final transcript, so a call driver could open a conversation and
// then never deliver the opening announcement it produced. Phase 16 T4 measured
// what that costs: the caller's first words come back as "floor_queued", queued
// behind a greeting that will never finish.
//
// # What it is NOT
//
// It is not a second response engine. Everything past this method is the same
// governance -> generation -> chunking -> synthesis -> delivery sequence a
// caller turn runs, and it is not a way around the dialogue policy either: the
// plan must already be one [planSpeaks] admits, and this pipeline neither makes
// nor revises the decision it carries.
//
// # Contract
//
// Blocks until the response has been delivered or has failed. One at a time per
// pipeline: a second call while one is in flight is REFUSED, not queued, because
// the pipeline holds exactly one active synthesis stream and one active
// dispatcher and a queued response would play into a conversation that had moved
// on. Cancelling ctx stops the response, and a response stopped that way reports
// no completion and no abandonment — there is nobody left to tell.
//
// Errors are the package's existing vocabulary: ErrPlanNotSpeakable for a plan
// that produces no speech, ErrSessionClosed before Start and after the call has
// ended, ErrInvalidTransition when the session cannot begin a turn, and the
// provider and governance errors from the response path itself.
func (p *Pipeline) Speak(ctx context.Context, plan conversation.Plan) error {
	// The policy question first, and it is answered by the SAME predicate the
	// transcript path uses. A seam with its own idea of which actions speak
	// would be a second dialogue policy, and the first divergence would be
	// invisible: a caller hearing the pipeline's words for a decision the
	// conversation made silently.
	if !planSpeaks(plan.Action) {
		return fmt.Errorf("%w: %s", ErrPlanNotSpeakable, plan.Action)
	}

	p.mu.Lock()
	started := p.started
	p.mu.Unlock()

	if !started {
		return fmt.Errorf("%w: the pipeline has not been started", ErrSessionClosed)
	}
	if p.cfg.FSM.Terminal() {
		return ErrSessionClosed
	}
	if err := p.ctx.Err(); err != nil {
		return fmt.Errorf("%w: the session has ended", ErrSessionClosed)
	}

	// ONE RESPONSE AT A TIME, decided before anything is allocated.
	//
	// The state machine would refuse the second call too, but not reliably: two
	// calls arriving together could both observe listening before either had
	// moved. This is the deterministic answer, and the loser is told the same
	// thing the machine would have told it.
	if !p.speaking.CompareAndSwap(false, true) {
		return &TransitionError{
			Session: p.cfg.Session,
			From:    p.cfg.FSM.State(),
			To:      StateThinking,
			Reason:  "a response is already being spoken on this session",
		}
	}
	defer p.speaking.Store(false)

	p.mu.Lock()
	p.turnSeq++
	turn := TurnID(fmt.Sprintf("%s-t%d", p.cfg.Session, p.turnSeq))
	p.mu.Unlock()

	p.cfg.FSM.SetTurn(turn)

	// The agent's own turn begins here. A caller turn enters thinking from
	// transcribing; this one enters from listening, which is the edge
	// [sessionTransitions] declares for exactly this case. A refusal means the
	// session is busy or gone, and it is returned unchanged.
	if err := p.cfg.FSM.To(p.ctx, StateThinking, ReasonOK); err != nil {
		return err
	}
	p.m.TurnsStarted.Inc()

	// Bound to BOTH contexts: the submitter's, so cancelling stops the
	// response, and the session's, so a response cannot outlive the call.
	base, release := mergeCancel(ctx, p.ctx)
	defer release()

	turnCtx, cancel := context.WithTimeout(base, p.cfg.TurnTimeout)
	defer cancel()

	// Recorded so a barge-in can abandon this response too. The opening
	// announcement is non-yielding by INV-CV-2 and the frozen turn manager
	// QUEUES an interruption during it rather than granting the floor — but
	// that is the conversation's decision, and this pipeline does not
	// second-guess it by making its own audio uninterruptible.
	p.mu.Lock()
	p.activeTurnCancel = cancel
	p.mu.Unlock()

	return p.deliverPlan(turnCtx, speakRequest{
		turn:   turn,
		plan:   plan,
		system: p.instructionFor(plan),
		// NO MESSAGE, and specifically not an empty one. There is no caller
		// utterance behind this response, and inventing a blank user turn would
		// be a fabricated transcript. The frozen kernel assembles from the
		// session's own context when Messages is empty (runtime/kernel.go:573),
		// which is the contract's own way of saying "no input this turn".
		messages: nil,
		abort:    ctx,
	})
}

// instructionFor returns the generation instruction for a submitted plan.
//
// The greeting gets its own because it is the only response generated with
// nothing to respond to: the session instruction describes how to answer a
// caller, and answering nobody with it produces silence or a non-sequitur. If
// the deployment configured none, the session instruction is used unchanged —
// this package supplies no words of its own.
func (p *Pipeline) instructionFor(plan conversation.Plan) string {
	if plan.NextState == conversation.StateGreeting && p.cfg.GreetingSystem != "" {
		return p.cfg.GreetingSystem
	}
	return p.cfg.System
}

// speakRequest is one accepted plan on its way to the caller's ear.
//
// The two producers differ in three fields and nothing else: a caller turn
// carries the transcript as its message and the session as its abort source; a
// plan submitted through [Pipeline.Speak] carries no message, possibly a
// different instruction, and the submitter's context.
type speakRequest struct {
	turn TurnID

	// plan is the decision the conversation already made. It is not
	// re-litigated here; it selects the completion event and nothing else.
	plan conversation.Plan

	// system is the generation instruction for THIS response.
	system string

	// messages is the generation input. EMPTY IS MEANINGFUL: the frozen kernel
	// assembles from the session's own context when no message is supplied
	// (runtime/kernel.go:573), which is how a response with no caller
	// utterance behind it is expressed without inventing one.
	messages []rt.Message

	// abort is the external cancellation source, checked before any lifecycle
	// event is sent. For a caller turn it is the session context, which the
	// completion path already checked; for Speak it is the submitter's context,
	// so a cancelled greeting reports neither completion nor abandonment.
	abort context.Context
}

// completionEvent chooses which lifecycle event ends this response.
//
// # Both are legal here, and that is the danger
//
// The frozen engine accepts EventSpeechComplete AND EventGreetingComplete from
// StateGreeting, and both leave the conversation in listening. They are not
// interchangeable: greeting-complete releases the floor to nobody, which is what
// the opening announcement means, while speech-complete hands the floor to the
// caller as the answer to something they said. And EventGreetingComplete is
// accepted mid-conversation too, so sending it everywhere would "work" while
// recording an opening turn in the middle of a call.
//
// Plan.NextState is the typed discriminator the frozen contract already
// provides: handleStart returns StateGreeting, every ordinary speaking plan
// returns StateSpeaking. No string is compared, and nothing is inferred.
func completionEvent(plan conversation.Plan) conversation.EventKind {
	if plan.NextState == conversation.StateGreeting {
		return conversation.EventGreetingComplete
	}
	return conversation.EventSpeechComplete
}

// deliverPlan authorises, generates, synthesises and delivers one accepted
// plan, then tells the conversation what became of it.
//
// # One implementation, two producers
//
// This is the response engine, and there is exactly one of it. The transcript
// loop reaches it with the caller's words; [Pipeline.Speak] reaches it with a
// plan the conversation decided without one. Everything between authorisation
// and delivery is identical for both, because it was already independent of the
// transcript — the only thing a response needs from the caller is the message
// that goes into the generation request.
//
// The returned error is for a caller that is waiting; the transcript loop has
// nobody to report to and discards it. Failures are recorded and the turn is
// ended either way.
func (p *Pipeline) deliverPlan(ctx context.Context, req speakRequest) error {
	turn := req.turn

	// abandon reports that an accepted response will not be spoken, unless the
	// submitter has already walked away. A cancelled response was not
	// abandoned mid-flight — the conversation it belonged to is going away.
	abandon := func(reason string) {
		if req.abort.Err() != nil {
			return
		}
		p.abandonResponse(reason)
	}

	// ---- authorisation, with no branch around it --------------------------
	decision := p.cfg.Governor.Decide(governance.Request{
		Action: governance.Action{
			Kind:      governance.ActionConversation,
			Operation: "speak",
			Resource:  "voice.response",
			// Speech to a caller cannot be unsaid.
			Reversibility: governance.ReversibleNever,
			// The caller's words are in the context this response is derived
			// from. The ATTRIBUTES carry no content — a classification, never
			// the sentence.
			Classification: governance.ClassPersonal,
		},
		Actor:    p.cfg.Actor,
		Subject:  p.cfg.Subject,
		Org:      p.cfg.Org,
		Business: p.cfg.Business,
		Session:  governance.SessionID(p.cfg.Session),
	})

	p.m.GovernanceDecisions.Inc(decision.Outcome.String())

	if !decision.Outcome.Permits() {
		p.publish(VoiceEvent{
			Type:    EventGovernanceDenied,
			Session: p.cfg.Session, Turn: turn, Call: p.cfg.Call,
			Reason: ReasonDenied,
			At:     p.clock.Now(),
		})
		if err := p.cfg.FSM.To(p.ctx, StateListening, ReasonGovernanceDeny); err != nil {
			p.note(err)
		}
		// A denial is a domain outcome, not a fault, so it is NOT reported as
		// one. The response simply will not be spoken, and the caller must be
		// able to speak again.
		abandon(ReasonGovernanceDeny)
		p.m.TurnsCompleted.Inc(string(OutcomeDenied))
		if p.observer != nil {
			p.observer.OnTurnComplete(OutcomeDenied, decision.Reason)
		}
		return fmt.Errorf("%w: %s", ErrGovernanceDenied, decision.Outcome)
	}

	// ---- synthesis opened BEFORE generation starts ------------------------
	//
	// So the first completed clause has somewhere to go the instant it exists.
	// Opening the voice after the first token arrives would put provider
	// startup on the critical path of every response.
	voice, err := p.cfg.Registry.PickTTS(p.cfg.Language)
	if err != nil {
		failure := fmt.Errorf("%w: synthesis: %v", ErrProviderUnavailable, err)
		p.note(failure)
		abandon(ReasonUnavailable)
		p.failTurn(ReasonUnavailable)
		return failure
	}

	if err := p.cfg.FSM.To(p.ctx, StateSynthesizing, ReasonOK); err != nil {
		p.note(err)
		return err
	}

	generation := p.generation.Load()

	ttsStream, err := voice.OpenTTS(ctx, speech.TTSConfig{
		Session:  speech.SessionID(p.cfg.Session),
		Turn:     speech.TurnID(turn),
		Language: p.cfg.Language,
		Format:   p.cfg.Format,
		Prosody:  speech.DefaultProsody(),
	})
	if err != nil {
		p.m.ProviderFailures.Inc(string(voice.ID()), string(KindTTS), ReasonStartFailed)
		p.cfg.Registry.Report(ProviderID(voice.ID()), speech.OutcomeFailure)
		failure := fmt.Errorf("%w: opening synthesis: %v", ErrProviderUnavailable, err)
		p.note(failure)
		abandon(ReasonStartFailed)
		p.failTurn(ReasonStartFailed)
		return failure
	}

	p.mu.Lock()
	p.activeTTS = ttsStream
	p.mu.Unlock()

	// The audio pump runs while generation is still going.
	audioDone := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(audioDone)
		p.pumpAudio(ctx, turn, ttsStream, generation)
	}()

	// ---- generation, streamed into synthesis ------------------------------
	chunker, err := speech.NewChunker(p.cfg.Chunking)
	if err != nil {
		p.note(err)
		_ = ttsStream.Close()
		<-audioDone
		abandon(ReasonInvalidOutput)
		p.failTurn(ReasonInvalidOutput)
		return err
	}

	sink := &synthesisSink{
		pipeline: p,
		chunker:  chunker,
		stream:   ttsStream,
		turn:     turn,
		started:  p.clock.Now(),
	}

	dispatcher, err := p.cfg.Generator.Generate(ctx, rt.GenerateSpec{
		SessionID:       rt.SessionID(p.cfg.Session),
		Tier:            p.cfg.Tier,
		System:          req.system,
		Messages:        req.messages,
		MaxOutputTokens: p.cfg.MaxOutputTokens,
	}, sink)
	if err != nil {
		failure := fmt.Errorf("%w: generation: %v", ErrProviderFailed, err)
		p.note(failure)
		_ = ttsStream.Close()
		<-audioDone
		abandon(ReasonUnavailable)
		p.failTurn(ReasonUnavailable)
		return failure
	}

	p.mu.Lock()
	p.activeDispatcher = dispatcher
	p.mu.Unlock()

	select {
	case <-dispatcher.Done():
	case <-ctx.Done():
		// A turn that ran out of time, was cancelled, or was barged in on.
		// Abort is the frozen mechanism and it is what the 20 ms budget is
		// measured against.
		dispatcher.Abort()
		<-dispatcher.Done()
	}

	// The sink closes the text side; audio may still be arriving.
	<-audioDone

	p.mu.Lock()
	if p.activeDispatcher == dispatcher {
		p.activeDispatcher = nil
	}
	if p.activeTTS == ttsStream {
		p.activeTTS = nil
	}
	p.mu.Unlock()

	if p.ctx.Err() != nil {
		// The session ended; the terminal transition already happened.
		return p.ctx.Err()
	}

	// THE SUBMITTER WALKED AWAY.
	//
	// A response whose caller cancelled it is neither complete nor abandoned
	// mid-flight: there is nobody left to tell. Sending a lifecycle event here
	// would put a record of a finished greeting on a call that is going away.
	//
	// For a turn the caller started this is the session context, already
	// checked immediately above, so nothing about that path changes.
	if err := req.abort.Err(); err != nil {
		// The SESSION, however, is still alive, and it is currently holding the
		// floor for a response that will not finish. Left there it could never
		// take another turn — speaking is not a state inbound audio can start
		// from. The machine goes back to listening; the conversation is told
		// nothing, because the thing that would have been told is gone.
		if p.cfg.FSM.State().AgentHoldsFloor() {
			if terr := p.cfg.FSM.To(p.ctx, StateListening, ReasonCancelled); terr != nil {
				p.note(terr)
			}
		}
		p.m.TurnsCompleted.Inc(string(OutcomeCancelled))
		if p.observer != nil {
			p.observer.OnTurnComplete(OutcomeCancelled, ReasonCancelled)
		}
		return err
	}

	// A BARGE-IN ALREADY ENDED THIS TURN.
	//
	// The interruption bumped the generation and returned the floor to the
	// caller, synchronously, on the ingest goroutine. This turn must not now
	// announce itself complete or move a state machine that has already moved
	// on — listening -> listening is not a declared edge, and attempting it
	// would record an invalid transition for an interruption that worked.
	if p.generation.Load() != generation {
		p.m.TurnsCompleted.Inc(string(OutcomeInterrupted))
		if p.observer != nil {
			p.observer.OnTurnComplete(OutcomeInterrupted, ReasonBargeIn)
		}
		// Not nil: a submitter waiting on this response must not be told it
		// finished. It did not — the caller talked over it, and EventInterrupt
		// has already moved the floor.
		return fmt.Errorf("voice: the response was superseded (%s)", ReasonBargeIn)
	}

	if err := p.cfg.FSM.To(p.ctx, StateListening, ReasonOK); err != nil {
		p.note(err)
		return err
	}

	// THE AGENT HAS FINISHED ITS OUTPUT, so the floor is no longer its own.
	//
	// WHICH event says so is [completionEvent]'s decision, taken from the plan
	// that was spoken: the opening announcement ends with EventGreetingComplete,
	// every other response with EventSpeechComplete. Both are accepted by the
	// frozen engine in both situations and both reach listening, so choosing
	// wrongly is invisible in the state and visible only in the turn record —
	// which is why the choice is made from a typed field rather than guessed.
	//
	// EventSpeechComplete is defined as "the agent finishing its output"
	// (conversation/engine.go:24), and handleSpeechComplete is the ONLY thing
	// that performs the handoff: Release(PartyAgent) -> transition ->
	// Acquire(PartyCaller). Without it the conversation stays in speaking with
	// the agent holding the floor, and every later caller turn is planned as a
	// no-op — measured as one spoken response followed by silence.
	//
	// THIS IS THE ONLY PLACE IT MAY BE SENT. Every path where the agent did not
	// actually finish speaking has already returned above:
	//
	//   - session ended or cancelled      -> p.ctx.Err() != nil
	//   - barge-in superseded this turn   -> p.generation.Load() != generation,
	//                                        where EventInterrupt owns the floor
	//   - governance denied               -> returned at the decision
	//   - generation or synthesis failed  -> failTurn
	//
	// Sent BEFORE OnTurnComplete so that an observer told the turn is complete
	// can rely on the floor having already moved.
	//
	// A failure here is recorded and does not undo the turn: the audio has
	// already reached the caller, so there is nothing to roll back. ErrTerminal
	// is expected and ignored — a conversation that ended during this turn is
	// not a fault, and the frozen engine refuses the event by design.
	kind := completionEvent(req.plan)
	if _, err := p.cfg.Planner.Handle(conversation.Event{
		Kind:  kind,
		Party: conversation.PartyAgent,
	}); err != nil && !errors.Is(err, conversation.ErrTerminal) {
		p.note(fmt.Errorf("%w: conversation: %s: %v", ErrProviderFailed, kind, err))
	}

	p.m.TurnsCompleted.Inc(string(OutcomeCompleted))
	if p.observer != nil {
		p.observer.OnTurnComplete(OutcomeCompleted, ReasonOK)
	}
	return nil
}

// failTurn ends a turn on a fault without ending the session.
// abandonResponse tells the conversation that an accepted response plan will
// not be spoken, then lets the caller talk again.
//
// Called only from paths where the planner returned ActionRespond and no audio
// reached the caller. Without it the conversation keeps the agent's floor and
// every later caller utterance is classified as a backchannel and ignored — a
// silent stuck call.
//
// It is NOT EventSpeechComplete: nothing was said. It is NOT EventInterrupt:
// nothing interrupted. It repairs lifecycle state only — no retry, no apology,
// no escalation — and the original failure or governance decision is left
// untouched for observability.
//
// ErrTerminal is expected and ignored: a conversation that ended during the
// turn needs no floor returned, and the frozen engine refuses the event by
// design.
func (p *Pipeline) abandonResponse(reason string) {
	if p.ctx.Err() != nil {
		return
	}
	if _, err := p.cfg.Planner.Handle(conversation.Event{
		Kind:   conversation.EventResponseAbandoned,
		Party:  conversation.PartyAgent,
		Reason: reason,
	}); err != nil && !errors.Is(err, conversation.ErrTerminal) {
		p.note(fmt.Errorf("%w: conversation: response abandoned: %v",
			ErrProviderFailed, err))
	}
}

func (p *Pipeline) failTurn(reason string) {
	if p.ctx.Err() != nil {
		return
	}
	// A failed turn returns the floor rather than killing the call: one
	// provider hiccup should not end a conversation the caller is still in.
	if err := p.cfg.FSM.To(p.ctx, StateListening, reason); err != nil {
		p.note(err)
	}
	p.m.TurnsCompleted.Inc(string(OutcomeFailed))
	if p.observer != nil {
		p.observer.OnTurnComplete(OutcomeFailed, reason)
	}
}

// ---------------------------------------------------------------------------
// The streaming seam
// ---------------------------------------------------------------------------

// synthesisSink turns a live token stream into synthesis requests.
//
// # This is where "streaming" is either true or a lie
//
// runtime.Dispatcher calls Write for every chunk AS IT ARRIVES. Each call
// pushes text into the chunker, and the chunker returns a clause the moment one
// is provably complete — at which point it goes to the synthesiser, while the
// model is still producing the rest of the response.
//
// The alternative, collecting the response in Close and cutting it up there,
// would pass every test that only checks the audio came out right, and would
// cost the caller the whole generation latency in dead air.
type synthesisSink struct {
	pipeline *Pipeline
	chunker  *speech.Chunker
	stream   speech.TTSStream
	turn     TurnID
	started  time.Time

	mu        sync.Mutex
	submitted int
	closed    bool
	firstAt   time.Time
}

// Compile-time proof that the frozen sink port is satisfied.
var _ rt.Sink = (*synthesisSink)(nil)

// Write receives one chunk of generated text.
func (s *synthesisSink) Write(c rt.Chunk) error {
	if c.Kind != rt.ChunkText || c.Text == "" {
		return nil
	}
	return s.submit(s.chunker.Push(c.Text))
}

// AcceptsThinking is false: INV-AI-10 forbids chain-of-thought reaching any
// user, and this sink's output is spoken to one.
func (s *synthesisSink) AcceptsThinking() bool { return false }

// Close flushes whatever the chunker still holds and ends the text stream.
func (s *synthesisSink) Close(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	if err == nil || errors.Is(err, rt.ErrAborted) {
		// The tail of the response is a real clause even though no terminator
		// ended it. Dropping it would clip the last words off every answer.
		if err == nil {
			_ = s.submit(s.chunker.Flush())
		}
	}
	_ = s.stream.CloseSend()
}

// submit sends completed clauses to the synthesiser.
func (s *synthesisSink) submit(chunks []speech.Chunk) error {
	for _, c := range chunks {
		if err := s.stream.Synthesize(c); err != nil {
			if errors.Is(err, speech.ErrBackpressure) {
				s.pipeline.m.Backpressure.Inc(string(StageSynthesise))
				s.pipeline.m.DroppedChunks.Inc(string(StageSynthesise), ReasonQueueFull)
				continue
			}
			// Detaching this sink does not fail the stream — one slow or broken
			// consumer must not kill a live call for everything else.
			return fmt.Errorf("%w: synthesising: %v", ErrProviderFailed, err)
		}

		s.mu.Lock()
		if s.submitted == 0 {
			s.firstAt = s.pipeline.clock.Now()
			s.pipeline.m.TTSFirstAudio.Observe(
				s.firstAt.Sub(s.started).Seconds(), "chunk_submitted")
		}
		s.submitted++
		s.mu.Unlock()

		if s.pipeline.observer != nil {
			s.pipeline.observer.OnResponseChunk(c)
		}
	}
	return nil
}

// Submitted returns how many clauses reached the synthesiser.
func (s *synthesisSink) Submitted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted
}

// ---------------------------------------------------------------------------
// Stage 4 — outbound audio
// ---------------------------------------------------------------------------

// pumpAudio moves synthesised frames to media, dropping superseded ones.
//
// # The stale-output guard lives here, and it is two guards
//
// A frame produced for a turn the caller has already talked over must never be
// delivered. Checking whether the stream is closed is not enough: a frame
// already read from it has passed that check.
//
// The first guard compares the generation the frame was produced for against
// the current one. That alone leaves a window — between the comparison and the
// sink accepting the frame, an interruption can land — and under load that
// window is real: it leaked exactly one frame in a full-package test run.
//
// The second guard closes it. Delivery carries the TURN's context, which
// [Pipeline.abortActiveTurn] cancels synchronously before the interruption
// returns, so a frame that wins the race against the generation check loses it
// against the context. The FrameSink contract says Deliver takes a context; a
// sink that ignores its cancellation can still play the one frame already
// handed to it, and that is the residual this design accepts rather than
// blocking the abort behind a slow media write.
func (p *Pipeline) pumpAudio(
	turnCtx context.Context, turn TurnID, stream speech.TTSStream, generation uint64,
) {
	defer func() { _ = stream.Close() }()

	spoke := false

	for {
		select {
		case <-p.ctx.Done():
			return

		case <-turnCtx.Done():
			// THE TURN'S OWN DEADLINE, not just the session's.
			//
			// A synthesiser that accepts text and produces nothing would
			// otherwise hold this goroutine — and the turn behind it — until the
			// whole CALL ended, because the audio channel never closes and the
			// session context is still live. The turn timeout exists precisely
			// so one wedged provider costs one answer rather than the call.
			return

		case frame, ok := <-stream.Audio():
			if !ok {
				return
			}

			// Both guards, in the order that makes the window smallest: the
			// cheap comparison first, the context immediately before handover.
			if p.generation.Load() != generation || turnCtx.Err() != nil {
				p.mu.Lock()
				p.stale++
				p.mu.Unlock()
				p.m.StaleChunksBlocked.Inc()
				p.m.DroppedChunks.Inc(string(StageAudioOut), ReasonStaleChunk)
				continue
			}

			if !spoke {
				spoke = true
				if err := p.cfg.FSM.To(p.ctx, StateSpeaking, ReasonOK); err != nil {
					p.note(err)
					return
				}
			}

			if err := p.cfg.Output.Deliver(turnCtx, frame); err != nil {
				if turnCtx.Err() != nil {
					// The turn was abandoned mid-handover. Not a delivery
					// failure — the frame was correctly refused.
					p.mu.Lock()
					p.stale++
					p.mu.Unlock()
					p.m.StaleChunksBlocked.Inc()
					return
				}
				if errors.Is(err, ErrBackpressure) {
					// The media sink is behind. Counted and dropped rather than
					// queued: audio that arrives late is worse than audio that
					// does not arrive, because it plays over what came after it.
					p.m.Backpressure.Inc(string(StageAudioOut))
					p.m.DroppedChunks.Inc(string(StageAudioOut), ReasonQueueFull)
					continue
				}
				p.note(fmt.Errorf("%w: delivering audio: %v", ErrProviderFailed, err))
				return
			}
		}
	}
}

// publish emits an event without letting a broker failure affect the call.
func (p *Pipeline) publish(e VoiceEvent) {
	if p.cfg.Publisher == nil {
		return
	}
	_ = p.cfg.Publisher.Publish(p.ctx, e)
}
