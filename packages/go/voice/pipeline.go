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

	// interrupts is this pipeline's audiointel.SpeechController.
	interrupts *interruptController

	mu       sync.Mutex
	started  bool
	failure  error
	turnSeq  uint64
	sttOpen  speech.STTStream
	stale    uint64
	observer PipelineObserver

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
				_ = p.stop(StateFailed, ReasonCrashed)
				return
			}
		}
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
	p.mu.Unlock()

	if stream != nil {
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
	if analysis.Endpoint.Confirmed && stream != nil {
		_ = stream.CloseSend()
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
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: final.Text},
		Party:     conversation.PartyCaller,
	})
	if err != nil {
		p.note(fmt.Errorf("%w: conversation: %v", ErrProviderFailed, err))
		p.failTurn(ReasonInvalidOutput)
		return
	}

	if plan.Action != conversation.ActionRespond {
		// A plan that is not "respond" produces no speech on this path. Ending
		// the turn quietly is right: transfer, escalation and hangup are the
		// layer above's to execute, and this pipeline inventing words for them
		// would be it deciding policy.
		p.emptyTurn(ReasonOK)
		return
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
		p.m.TurnsCompleted.Inc(string(OutcomeDenied))
		if p.observer != nil {
			p.observer.OnTurnComplete(OutcomeDenied, decision.Reason)
		}
		return
	}

	// ---- synthesis opened BEFORE generation starts ------------------------
	//
	// So the first completed clause has somewhere to go the instant it exists.
	// Opening the voice after the first token arrives would put provider
	// startup on the critical path of every response.
	voice, err := p.cfg.Registry.PickTTS(p.cfg.Language)
	if err != nil {
		p.note(fmt.Errorf("%w: synthesis: %v", ErrProviderUnavailable, err))
		p.failTurn(ReasonUnavailable)
		return
	}

	if err := p.cfg.FSM.To(p.ctx, StateSynthesizing, ReasonOK); err != nil {
		p.note(err)
		return
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
		p.note(fmt.Errorf("%w: opening synthesis: %v", ErrProviderUnavailable, err))
		p.failTurn(ReasonStartFailed)
		return
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
		p.failTurn(ReasonInvalidOutput)
		return
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
		System:          p.cfg.System,
		Messages:        []rt.Message{{Role: rt.RoleUser, Content: final.Text}},
		MaxOutputTokens: p.cfg.MaxOutputTokens,
	}, sink)
	if err != nil {
		p.note(fmt.Errorf("%w: generation: %v", ErrProviderFailed, err))
		_ = ttsStream.Close()
		<-audioDone
		p.failTurn(ReasonUnavailable)
		return
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
		return // the session ended; the terminal transition already happened
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
		return
	}

	if err := p.cfg.FSM.To(p.ctx, StateListening, ReasonOK); err != nil {
		p.note(err)
		return
	}
	p.m.TurnsCompleted.Inc(string(OutcomeCompleted))
	if p.observer != nil {
		p.observer.OnTurnComplete(OutcomeCompleted, ReasonOK)
	}
}

// failTurn ends a turn on a fault without ending the session.
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
