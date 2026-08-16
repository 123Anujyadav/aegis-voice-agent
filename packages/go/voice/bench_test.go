package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// What these benchmarks measure, and what they cannot
// ---------------------------------------------------------------------------
//
// # Orchestration only
//
// Every provider below is a deterministic stand-in. There is no model, no
// recogniser and no synthesiser inference in any number this file produces.
// What is left when inference is removed is exactly what this phase owns:
// channel handovers, the state machine, the chunker, the governance call, the
// dispatcher fan-out and the generation guard.
//
// That separation is not a caveat bolted on afterwards — it is the reason the
// numbers are worth anything. An end-to-end figure containing a local CPU model
// would tell you about the model and nothing about the orchestration, and it
// would change on a machine with a different GPU while this code stayed
// identical.
//
// # The three quantities that must never be mixed
//
//	A. Aegis orchestration overhead      — measured here.
//	B. Provider inference latency        — NOT measured; no runtime installed.
//	C. Frozen architectural budgets      — ADR references, asserted nowhere here.
//
// ADR-0004 §12's 20 ms is the PROVIDER CANCELLATION budget. ADR-0006 and
// ADR-0011's 250 ms p50 / 550 ms p95 are a production time-to-first-token
// reference for a hosted frontier model over a network. Neither is a target for
// anything in this file, and BenchmarkZZZ_MeasurementResolution exists so a
// reader can see which of these figures the machine can even resolve.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func benchFormat() media.AudioFormat { return media.PCM16Mono16k() }

func benchFrame(seq uint64) media.Frame {
	f := benchFormat()
	return media.Frame{
		Sequence: seq, Format: f,
		Payload: make([]byte, f.BytesFor(20*time.Millisecond)),
	}
}

// benchRegistry builds a registry with one healthy provider of each kind.
func benchRegistry(b *testing.B) *ProviderRegistry {
	b.Helper()

	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		b.Fatalf("router: %v", err)
	}
	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		b.Fatalf("registry: %v", err)
	}

	stt := &benchSTT{id: speech.ProviderID("stt-bench")}
	if err := reg.RegisterSTT(stt, benchSpec("whisper.cpp")); err != nil {
		b.Fatalf("RegisterSTT: %v", err)
	}
	tts := &benchTTS{id: speech.ProviderID("tts-bench")}
	if err := reg.RegisterTTS(tts, benchSpec("piper")); err != nil {
		b.Fatalf("RegisterTTS: %v", err)
	}
	return reg
}

func benchSpec(engine string) ProviderSpec {
	return ProviderSpec{
		Class: ClassProduction, Tier: speech.TierPrimary,
		Engine: engine, Version: "bench",
		Model:    ModelIdentity{Model: ModelID("bench-model")},
		Locality: LocalityProcess,
	}
}

type benchSTT struct{ id speech.ProviderID }

func (s *benchSTT) ID() speech.ProviderID { return s.id }
func (s *benchSTT) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: true, PartialResults: true,
		Languages:   []speech.Language{langEN},
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}
func (s *benchSTT) OpenSTT(context.Context, speech.STTConfig) (speech.STTStream, error) {
	return nil, errors.New("bench: not opened")
}

type benchTTS struct{ id speech.ProviderID }

func (s *benchTTS) ID() speech.ProviderID { return s.id }
func (s *benchTTS) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming:   true,
		Languages:   []speech.Language{langEN},
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}
func (s *benchTTS) OpenTTS(context.Context, speech.TTSConfig) (speech.TTSStream, error) {
	return nil, errors.New("bench: not opened")
}

// ---------------------------------------------------------------------------
// 1. Provider routing
// ---------------------------------------------------------------------------

// BenchmarkRouting_PickSTT measures one selection through the registry.
//
// The registry adds a delegation; the work is speech.ProviderRouter's tier
// walk, capability match and breaker check. This is on the path of every turn.
func BenchmarkRouting_PickSTT(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.PickSTT(langEN); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRouting_PickTTS(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.PickTTS(langEN); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRouting_PickSTTParallel measures selection under contention, which
// is the shape a busy process actually presents: one router, many turns.
func BenchmarkRouting_PickSTTParallel(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := reg.PickSTT(langEN); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRouting_Describe measures reading a provider descriptor, which a
// readiness endpoint does rather than a turn.
func BenchmarkRouting_Describe(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := reg.Describe(ProviderID("stt-bench")); !ok {
			b.Fatal("missing descriptor")
		}
	}
}

// ---------------------------------------------------------------------------
// 2. VoiceSession creation
// ---------------------------------------------------------------------------

func BenchmarkSession_NewFSM(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewSessionFSM(FSMConfig{
			Session: SessionID("ses-bench"), Call: CallID("call-bench"),
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSession_NewPipeline measures assembling a whole session: config
// validation, queue allocation and the interruption controller.
//
// Once per call, so it is a startup cost rather than a per-turn one — but a
// process answering hundreds of calls a minute pays it that often.
func BenchmarkSession_NewPipeline(b *testing.B) {
	reg := benchRegistry(b)
	cfg := benchPipelineConfig(b, reg)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-bench")})
		if err != nil {
			b.Fatal(err)
		}
		cfg.FSM = fsm
		if _, err := NewPipeline(cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func benchPipelineConfig(b *testing.B, reg *ProviderRegistry) PipelineConfig {
	b.Helper()

	return PipelineConfig{
		Session: SessionID("ses-bench"), Call: CallID("call-bench"),
		Language: langEN, Format: benchFormat(),
		Registry: reg,
		Intel:    &scriptedIntel{onsetAt: 1, endAt: 3},
		Planner:  &scriptedPlanner{},
		Governor: &recordingGovernor{
			answer: scriptedDecision{outcome: governance.OutcomeAllow},
		},
		Generator:        &scriptedGenerator{tokens: []string{"Hello?"}},
		Output:           &countingSink{},
		MaxPendingFrames: 64, MaxPendingSegments: 16, MaxPendingAudio: 64,
		TurnTimeout: 15 * time.Second, Tier: rt.TierFast,
	}
}

// ---------------------------------------------------------------------------
// 3. Event dispatch
// ---------------------------------------------------------------------------

// BenchmarkEvents_StateTransition measures one FSM move end to end: table
// lookup, reason validation, history append, metric increment and publish.
//
// The publish target is the in-memory recorder, so this is the cost this layer
// adds, not a broker's.
func BenchmarkEvents_StateTransition(b *testing.B) {
	pub := NewBoundedRecordingEventPublisher(8)
	fsm, err := NewSessionFSM(FSMConfig{
		Session: SessionID("ses-bench"), Publisher: pub, MaxHistory: 8,
	})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	if err := fsm.To(ctx, StateListening, ReasonOK); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// A two-step cycle so every iteration performs real transitions.
		if err := fsm.To(ctx, StateSpeakingDetected, ReasonOK); err != nil {
			b.Fatal(err)
		}
		if err := fsm.To(ctx, StateListening, ReasonOK); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEvents_Publish measures the publisher alone, for subtraction.
func BenchmarkEvents_Publish(b *testing.B) {
	pub := NewBoundedRecordingEventPublisher(8)
	ctx := context.Background()
	e := VoiceEvent{
		Type: EventStateChanged, Session: SessionID("ses-bench"),
		From: StateListening, To: StateSpeakingDetected, Reason: ReasonOK,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := pub.Publish(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEvents_RejectedTransition measures the refusal path, which a
// misbehaving caller drives and which must not be expensive enough to be a
// denial-of-service vector.
func BenchmarkEvents_RejectedTransition(b *testing.B) {
	fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-bench")})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := fsm.To(ctx, StateListening, ReasonOK); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// listening -> speaking is not a declared edge.
		if err := fsm.To(ctx, StateSpeaking, ReasonOK); err == nil {
			b.Fatal("the undeclared transition was accepted")
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Cancellation
// ---------------------------------------------------------------------------

// BenchmarkCancellation_PipelineStop measures tearing a live session down.
//
// It includes the generation bump, the FSM move, context cancellation and
// joining every goroutine the session started. It does NOT include a provider
// process exiting: there is no process here.
func BenchmarkCancellation_PipelineStop(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cfg := benchPipelineConfig(b, reg)
		fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-bench")})
		if err != nil {
			b.Fatal(err)
		}
		cfg.FSM = fsm
		p, err := NewPipeline(cfg)
		if err != nil {
			b.Fatal(err)
		}
		if err := p.Start(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		if err := p.Cancel(ReasonRequested); err != nil {
			b.Fatal(err)
		}
		<-p.Done()
	}
}

// BenchmarkCancellation_GenerationGuard measures the check that stops stale
// audio, which runs on EVERY outbound frame.
//
// One atomic load per frame at 50 frames a second per call, so its cost is the
// thing that decides whether the guard is affordable at scale.
func BenchmarkCancellation_GenerationGuard(b *testing.B) {
	p := &Pipeline{}
	p.generation.Store(7)

	b.ReportAllocs()
	b.ResetTimer()
	var stale int
	for i := 0; i < b.N; i++ {
		if p.generation.Load() != 7 {
			stale++
		}
	}
	if stale != 0 {
		b.Fatal("unexpected staleness")
	}
}

// ---------------------------------------------------------------------------
// 5. Provider switching
// ---------------------------------------------------------------------------

// BenchmarkProviderSwitch_Failover measures selection after the primary's
// breaker has opened: the tier walk that skips a dead provider.
func BenchmarkProviderSwitch_Failover(b *testing.B) {
	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		b.Fatal(err)
	}

	primary := &benchSTT{id: speech.ProviderID("stt-primary")}
	secondary := &benchSTT{id: speech.ProviderID("stt-secondary")}
	if err := reg.RegisterSTT(primary, benchSpec("whisper.cpp")); err != nil {
		b.Fatal(err)
	}
	secondarySpec := benchSpec("whisper.cpp")
	secondarySpec.Tier = speech.TierSecondary
	if err := reg.RegisterSTT(secondary, secondarySpec); err != nil {
		b.Fatal(err)
	}

	for i := 0; i < speech.DefaultRouterConfig().FailureThreshold; i++ {
		reg.Report(ProviderID(primary.id), speech.OutcomeFailure)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := reg.PickSTT(langEN)
		if err != nil {
			b.Fatal(err)
		}
		if p.ID() != secondary.id {
			b.Fatalf("failover chose %s", p.ID())
		}
	}
}

// BenchmarkProviderSwitch_ReportOutcome measures feeding the breaker, which
// happens once per provider call.
func BenchmarkProviderSwitch_ReportOutcome(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.Report(ProviderID("stt-bench"), speech.OutcomeSuccess)
	}
}

// ---------------------------------------------------------------------------
// 6. Partial transcript forwarding
// ---------------------------------------------------------------------------

// benchObserver is the cheapest possible consumer, so the number is the
// pipeline's forwarding cost rather than a consumer's.
type benchObserver struct {
	segments int
	chunks   int
}

func (o *benchObserver) OnTranscript(speech.TranscriptSegment) { o.segments++ }
func (o *benchObserver) OnResponseChunk(speech.Chunk)          { o.chunks++ }
func (o *benchObserver) OnTurnComplete(TurnOutcome, string)    {}

// BenchmarkTranscript_ForwardPartial measures handing one segment onward:
// the observer call plus the content-free event that accompanies it.
func BenchmarkTranscript_ForwardPartial(b *testing.B) {
	obs := &benchObserver{}
	pub := NewBoundedRecordingEventPublisher(8)

	seg := speech.TranscriptSegment{
		Session: speech.SessionID("ses-bench"), Turn: speech.TurnID("turn-1"),
		Segment: speech.SegmentID("seg-1"),
		Text:    "I would like to check my balance please",
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs.OnTranscript(seg)
		_ = pub.Publish(ctx, VoiceEvent{
			Type: EventTranscriptPartial, Session: SessionID("ses-bench"),
			Turn: TurnID("turn-1"), CharCount: len([]rune(seg.Text)),
		})
	}
}

// ---------------------------------------------------------------------------
// 7. TTS chunk scheduling
// ---------------------------------------------------------------------------

// nopTTSStream accepts chunks and produces nothing, so the measurement is the
// scheduling path and not a synthesiser.
type nopTTSStream struct{ audio chan media.Frame }

func (s *nopTTSStream) Synthesize(speech.Chunk) error { return nil }
func (s *nopTTSStream) Audio() <-chan media.Frame     { return s.audio }
func (s *nopTTSStream) CloseSend() error              { return nil }
func (s *nopTTSStream) Close() error                  { return nil }

// BenchmarkTTS_ChunkScheduling measures the streaming seam: a generated token
// arriving, the chunker deciding whether a clause is complete, and a completed
// clause going to the synthesiser.
//
// This runs once per generated token, so it is the hottest orchestration path
// in a response.
func BenchmarkTTS_ChunkScheduling(b *testing.B) {
	chunker, err := speech.NewChunker(speech.DefaultChunkConfig())
	if err != nil {
		b.Fatal(err)
	}

	p := &Pipeline{clock: rt.SystemClock{}, m: NewVoiceMetrics()}
	sink := &synthesisSink{
		pipeline: p, chunker: chunker,
		stream:  &nopTTSStream{audio: make(chan media.Frame)},
		turn:    TurnID("turn-1"),
		started: time.Now(),
	}

	// Alternating tokens so a clause completes every other call: the realistic
	// mix of "buffer this" and "a sentence just ended".
	tokens := []rt.Chunk{
		{Kind: rt.ChunkText, Text: "Your balance is fifty pounds"},
		{Kind: rt.ChunkText, Text: " and the last payment cleared?"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Write(tokens[i%len(tokens)]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTTS_ChunkerOnly measures the frozen chunker alone, so the scheduling
// figure above can be attributed.
func BenchmarkTTS_ChunkerOnly(b *testing.B) {
	chunker, err := speech.NewChunker(speech.DefaultChunkConfig())
	if err != nil {
		b.Fatal(err)
	}
	texts := []string{"Your balance is fifty pounds", " and it cleared?"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunker.Push(texts[i%len(texts)])
	}
}

// ---------------------------------------------------------------------------
// 8. Process supervision
// ---------------------------------------------------------------------------
//
// Supervision overhead is measured in providers/process, against a real child
// program, because spawn and reap are operating-system costs and a stand-in
// cannot have them. See BenchmarkProcess_* there.
//
// Nothing in this package benchmarks a supervised process, precisely so a
// child's own work can never be reported as supervision overhead.

// ---------------------------------------------------------------------------
// 9. Concurrent session handling
// ---------------------------------------------------------------------------

// BenchmarkConcurrent_Sessions measures a whole turn per session at several
// concurrency levels, over ONE shared registry — the deployed shape.
//
// The unit is a completed turn, so the figure is throughput-shaped rather than
// latency-shaped: it answers "what does this process cost per turn when N calls
// are in flight", not "how fast is one turn".
func BenchmarkConcurrent_Sessions(b *testing.B) {
	for _, sessions := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("sessions=%d", sessions), func(b *testing.B) {
			// ONE registry, one recogniser, one voice, shared by every session
			// in the run — the deployed shape, and the only one where router
			// contention is visible.
			reg := benchTurnRegistry(b)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				for s := 0; s < sessions; s++ {
					wg.Add(1)
					go func(n int) {
						defer wg.Done()
						runBenchTurn(b, reg, n)
					}(s)
				}
				wg.Wait()
			}
		})
	}
}

// runBenchTurn drives one session through a complete turn.
func runBenchTurn(b *testing.B, reg *ProviderRegistry, n int) {
	b.Helper()

	id := SessionID(fmt.Sprintf("ses-b%02d", n))
	obs := newObserver()

	fsm, err := NewSessionFSM(FSMConfig{Session: id})
	if err != nil {
		b.Error(err)
		return
	}

	p, err := NewPipeline(PipelineConfig{
		Session: id, Language: langEN, Format: benchFormat(),
		Registry:  reg,
		Intel:     &scriptedIntel{onsetAt: 1, endAt: 3},
		Planner:   &scriptedPlanner{},
		Governor:  benchGovernor{outcome: governance.OutcomeAllow},
		Generator: &scriptedGenerator{tokens: []string{"Answering now?"}},
		Output:    &countingSink{},
		FSM:       fsm, Observer: obs,
		MaxPendingFrames: 64, MaxPendingSegments: 16, MaxPendingAudio: 64,
		TurnTimeout: 15 * time.Second, Tier: rt.TierFast,
	})
	if err != nil {
		b.Error(err)
		return
	}
	if err := p.Start(context.Background()); err != nil {
		b.Error(err)
		return
	}

	for i := 0; i < 4; i++ {
		_ = p.WriteFrame(benchFrame(uint64(i)))
	}

	select {
	case <-obs.turnDone:
	case <-time.After(30 * time.Second):
		b.Error("turn did not complete")
	}

	_ = p.Disconnect()
	<-p.Done()
}

// benchTurnRegistry wires providers that actually produce transcripts and
// audio, so a whole turn can be driven through them.
//
// Safe to share: recordingSTT and recordingTTS guard their state, and sharing
// them is the point — a per-session registry would hide exactly the router and
// provider contention these benchmarks exist to measure.
func benchTurnRegistry(b *testing.B) *ProviderRegistry {
	b.Helper()

	stt := &recordingSTT{id: speech.ProviderID("stt-turn"), segments: defaultSegments()}
	tts := &recordingTTS{id: speech.ProviderID("tts-turn"), framesPerChunk: 2}

	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		b.Fatal(err)
	}
	if err := reg.RegisterSTT(stt, benchSpec("whisper.cpp")); err != nil {
		b.Fatal(err)
	}
	if err := reg.RegisterTTS(tts, benchSpec("piper")); err != nil {
		b.Fatal(err)
	}
	return reg
}

// ---------------------------------------------------------------------------
// 10. Governance gateway overhead
// ---------------------------------------------------------------------------

// nopInvoker executes nothing, so the number is the gateway's own cost.
type nopInvoker struct{}

func (nopInvoker) InvokeTool(context.Context, ToolRequest) (ToolResult, error) {
	return ToolResult{Completed: true}, nil
}

// BenchmarkGovernance_AllowedAction measures the whole gate on the allow path:
// intent validation, request assembly, Decide, and minting the authorisation.
//
// The governor is a stand-in returning a fixed decision, so this is the
// GATEWAY's overhead and not the policy engine's evaluation cost.
func BenchmarkGovernance_AllowedAction(b *testing.B) {
	gw, err := NewToolGateway(GatewayConfig{
		Governor: benchGovernor{outcome: governance.OutcomeAllow},
		Invoker:  nopInvoker{}, Session: SessionID("ses-bench"),
		Actor: governance.ActorID("voice-agent"),
	})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	intent := testIntent()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Invoke(ctx, TurnID("turn-1"), intent); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGovernance_DeniedAction measures the refusal path, including
// building the denial error and copying the obligations.
func BenchmarkGovernance_DeniedAction(b *testing.B) {
	gw, err := NewToolGateway(GatewayConfig{
		Governor: benchGovernor{outcome: governance.OutcomeDeny},
		Invoker:  nopInvoker{}, Session: SessionID("ses-bench"),
		Actor: governance.ActorID("voice-agent"),
	})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	intent := testIntent()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Invoke(ctx, TurnID("turn-1"), intent); err == nil {
			b.Fatal("a denial reported success")
		}
	}
}

// BenchmarkGovernance_RequestValidation measures the authorisation check an
// invoker performs, which runs on every executed action.
func BenchmarkGovernance_RequestValidation(b *testing.B) {
	gw, err := NewToolGateway(GatewayConfig{
		Governor: benchGovernor{outcome: governance.OutcomeAllow},
		Invoker:  &capturingInvoker{}, Session: SessionID("ses-bench"),
		Actor: governance.ActorID("voice-agent"),
	})
	if err != nil {
		b.Fatal(err)
	}

	capture := gw.cfg.Invoker.(*capturingInvoker)
	if _, err := gw.Invoke(context.Background(), TurnID("t"), testIntent()); err != nil {
		b.Fatal(err)
	}
	req := capture.req

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := req.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

// benchGovernor answers without remembering anything.
//
// recordingGovernor keeps every request it was asked about, which is right for
// a test and wrong for a benchmark: the slice grows for the length of the run,
// and its reallocation is attributed to the gateway. That showed up as ~1.3 KB
// per operation against ZERO allocations — a contradiction that is the
// signature of a test double being measured instead of the code.
type benchGovernor struct{ outcome governance.Outcome }

func (g benchGovernor) Decide(governance.Request) governance.Decision {
	return governance.Decision{
		ID: governance.DecisionID("dec-bench"), Outcome: g.outcome,
		Reason: "bench", DecidedBy: governance.PolicyID("pol-bench"),
	}
}

type capturingInvoker struct{ req ToolRequest }

func (c *capturingInvoker) InvokeTool(_ context.Context, r ToolRequest) (ToolResult, error) {
	c.req = r
	return ToolResult{Completed: true}, nil
}

// ---------------------------------------------------------------------------
// 11. Barge-in orchestration
// ---------------------------------------------------------------------------

// BenchmarkBargeIn_Interrupt measures the orchestration hop: the floor check,
// the generation bump, the Phase 11C call through the adapter, abandoning the
// live turn and two state transitions.
//
// It is NOT ADR-0004 §12's 20 ms, which budgets the PROVIDER's cancellation.
// Nothing here cancels a provider, because no provider is running.
func BenchmarkBargeIn_Interrupt(b *testing.B) {
	reg := benchRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()

		fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-bench")})
		if err != nil {
			b.Fatal(err)
		}
		cfg := benchPipelineConfig(b, reg)
		cfg.FSM = fsm
		p, err := NewPipeline(cfg)
		if err != nil {
			b.Fatal(err)
		}
		p.ctx, p.cancel = context.WithCancelCause(context.Background())

		// Put the session where a barge-in is legal, by declared transitions.
		ctx := context.Background()
		for _, s := range []SessionState{
			StateListening, StateSpeakingDetected, StateTranscribing,
			StateThinking, StateSynthesizing,
		} {
			if err := fsm.To(ctx, s, ReasonOK); err != nil {
				b.Fatal(err)
			}
		}

		b.StartTimer()
		if err := p.interrupts.Interrupt(ctx, "caller_spoke"); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// 12. Streaming pipeline orchestration
// ---------------------------------------------------------------------------

// BenchmarkPipeline_FrameIngest measures the inbound hot path: the bounded
// queue handover a caller's audio makes 50 times a second.
func BenchmarkPipeline_FrameIngest(b *testing.B) {
	reg := benchRegistry(b)
	cfg := benchPipelineConfig(b, reg)
	cfg.MaxPendingFrames = 1024

	// No onset, so no turn opens. This benchmark is about the INBOUND QUEUE
	// HANDOVER — the thing a caller's audio does fifty times a second — and a
	// turn starting would drag recognition, planning and synthesis into a
	// figure that is supposed to isolate one channel send.
	cfg.Intel = &scriptedIntel{}

	fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-bench")})
	if err != nil {
		b.Fatal(err)
	}
	cfg.FSM = fsm

	p, err := NewPipeline(cfg)
	if err != nil {
		b.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = p.Disconnect() }()

	frame := benchFrame(0)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Backpressure is a legitimate outcome here and is not an error: the
		// consumer is deliberately slower than this loop.
		if err := p.WriteFrame(frame); err != nil && !errors.Is(err, ErrBackpressure) {
			b.Fatal(err)
		}
	}
}

// BenchmarkPipeline_WholeTurn measures one complete orchestrated turn.
//
// # This is the headline orchestration figure, and its limits matter
//
// It contains: analysis, recognition handover, partial forwarding, the
// conversation plan, the governance decision, generation fan-out, chunking,
// synthesis scheduling, the generation guard and media delivery.
//
// It contains NO inference of any kind. A real turn adds the recogniser, the
// model and the synthesiser, each of which is orders of magnitude larger than
// everything measured here.
func BenchmarkPipeline_WholeTurn(b *testing.B) {
	reg := benchTurnRegistry(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runBenchTurn(b, reg, 0)
	}
}

// ---------------------------------------------------------------------------
// Measurement quality
// ---------------------------------------------------------------------------

// BenchmarkZZZ_MeasurementResolution reports what this machine can resolve.
//
// Named to sort last so it appears at the end of a run. It is not a
// measurement of this system at all: it measures the CLOCK, so a reader can
// tell which of the figures above are real and which are at the floor of what
// can be observed here.
func BenchmarkZZZ_MeasurementResolution(b *testing.B) {
	b.Run("clock-granularity", func(b *testing.B) {
		var total time.Duration
		const probes = 1000
		for i := 0; i < probes; i++ {
			a := time.Now()
			for {
				c := time.Now()
				if c.After(a) {
					total += c.Sub(a)
					break
				}
			}
		}
		b.ReportMetric(float64(total.Nanoseconds())/probes, "ns/tick")
		b.ReportMetric(0, "ns/op")
	})

	b.Run("empty-loop", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = i
		}
	})
}
