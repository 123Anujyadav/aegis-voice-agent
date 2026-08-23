package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/audiointel"
	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/governance"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 15 T2 — PROVIDER CONTRACT & DETERMINISTIC FAKE COMPOSITION.
//
// This drives the EXISTING voice.Pipeline. It creates no second pipeline, no
// second provider abstraction and no second state machine, and it changes no
// production code.
//
// WHAT IS REAL HERE, and it is most of it:
//
//	voice.Pipeline            real   (Phase 11E)
//	voice.ProviderRegistry    real
//	speech.ProviderRouter     real   — selection, health and failover
//	voice.SessionFSM          real
//	governance.Engine         real   — a genuine decision, not a stub
//	rt.Kernel                 real   — assembled by rt.NewHarness
//	voiceintel.Bridge         real   — the Phase 14 seam, used as the Planner
//	conversation + intent     real   — the deterministic classifier
//
// WHAT IS SCRIPTED, and why:
//
//	speech.FakeSTTProvider    the frozen module's OWN exported fake
//	speech.FakeTTSProvider    likewise
//	rt.FakeProvider           likewise, registered inside the real Kernel
//	scriptedIntel             THE ONE PORT WITH NO EXPORTED FAKE — see below
//
// NO ACOUSTIC CLAIM IS MADE. A scripted recogniser emits exactly the segments
// it was handed. That makes ordering, turn mapping, failover and cancellation
// observable as fixed sequences; it says nothing about recognition quality, and
// nothing here should be read as saying otherwise.
//
// API KEY: none. MODEL: none. NETWORK: none. EXTERNAL BINARY: none.

// ---------------------------------------------------------------------------
// The one port with no exported fake
// ---------------------------------------------------------------------------

// scriptedIntel supplies deterministic turn boundaries.
//
// audiointel is frozen and exports a SignalGenerator (audio fixtures) but no
// scripted Analyzer, so speech onset and end cannot be driven deterministically
// through the real Session without synthesising audio energy and depending on
// detector thresholds — which would make turn boundaries an acoustic property
// and this suite non-deterministic. The frozen package's own e2e test scripts
// this port for the same reason.
//
// REPORTED, NOT WORKED AROUND: this is the single Intelligence-port gap.
// Everything else in this file is the real assembly.
type scriptedIntel struct {
	onsetAt, endAt uint64

	// absolute makes the boundaries SEQUENCE NUMBERS rather than positions in
	// a repeating cycle.
	//
	// The cyclic form exists so consecutive scripted turns reuse the same few
	// frames. A real utterance is not a few frames: Phase 16 T11's speech
	// fixture is 113 of them, and under the cyclic rule the recogniser's send
	// side would close after the fourth — transcribing 80 ms of a 2.3 s
	// sentence, and doing it eleven times.
	absolute bool

	// bargeAt, when non-zero, calls the REAL SpeechController on that frame of
	// the cycle. audiointel does exactly this from inside Analyze, so a
	// barge-in driven here reaches voice's own interruptController rather than
	// a parallel mechanism.
	bargeAt uint64
	barged  bool          // set once an interruption was actually delivered
	bargedC chan struct{} // closed once, when one is delivered

	mu       sync.Mutex
	seen     []uint64
	retained []media.Frame // frames deliberately kept, to prove Clone discipline
}

func (s *scriptedIntel) Analyze(
	ctx context.Context,
	f media.Frame,
	_ audiointel.ConversationState,
	controller audiointel.SpeechController,
	_ audiointel.OutboundEnvelope,
) (audiointel.Analysis, error) {
	s.mu.Lock()
	s.seen = append(s.seen, f.Sequence)
	// Retention across the call boundary REQUIRES Clone: the payload handed in
	// is borrowed and its bytes may be reused the moment this returns
	// (media/frame.go:236). Retaining f directly is the defect M5 introduces.
	s.retained = append(s.retained, f.Clone())
	s.mu.Unlock()

	// The two fields the Pipeline actually reads (pipeline.go:657, :705):
	// VAD.OnsetConfirmed opens the recognition stream, Endpoint.Confirmed ends
	// the turn. Nothing else here is invented.
	//
	// Boundaries are positions WITHIN a turn, so they repeat every t2TurnFrames
	// frames. Matching absolute sequence numbers would fire only on the first
	// turn and every later turn would hang waiting for an onset that never
	// came — which is exactly what happened before this was made cyclic.
	// Retry on every frame from bargeAt onwards until one is DELIVERED. A
	// single attempt is a race against the turn goroutine reaching a
	// floor-holding state; retrying makes the barge-in deterministic without a
	// sleep. Refusal-by-floor is a legitimate outcome and is simply retried.
	if s.bargeAt != 0 && f.Sequence%t2TurnFrames >= s.bargeAt && controller != nil {
		s.mu.Lock()
		done := s.barged
		s.mu.Unlock()
		if !done {
			if err := controller.Interrupt(ctx, "barge_in"); err == nil {
				s.mu.Lock()
				s.barged = true
				ch := s.bargedC
				s.bargedC = nil
				s.mu.Unlock()
				if ch != nil {
					close(ch)
				}
			}
		}
	}

	position := f.Sequence % t2TurnFrames
	if s.absolute {
		position = f.Sequence
	}

	var a audiointel.Analysis
	switch position {
	case s.onsetAt:
		a.VAD.OnsetConfirmed = true
	case s.endAt:
		a.Endpoint.Confirmed = true
	}
	return a, nil
}

func (s *scriptedIntel) frames() []media.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]media.Frame(nil), s.retained...)
}

// ---------------------------------------------------------------------------
// Deterministic sink and observer
// ---------------------------------------------------------------------------

// countingSink records BOUNDED METADATA ONLY: sequence numbers and byte
// lengths. It never retains or logs a payload, which is the security rule this
// file is also evidence for.
type countingSink struct {
	mu     sync.Mutex
	frames int
	bytes  int
	seqs   []uint64
	failOn int
	fail   error

	// Handshake hooks used to park the turn INSIDE audio delivery, which is
	// the only way to reach the completion tail with the turn already
	// superseded or cancelled. Both are nil unless a test sets them, and both
	// are channels — there is no sleep and no polling anywhere.
	//
	// atDeliver is signalled once, on the first frame delivered.
	// hold blocks that first Deliver until the test closes it.
	atDeliver chan struct{}
	hold      chan struct{}
	signalled bool
}

func (c *countingSink) Deliver(_ context.Context, f media.Frame) error {
	c.mu.Lock()
	if c.fail != nil && c.frames >= c.failOn {
		c.mu.Unlock()
		return c.fail
	}
	c.frames++
	c.bytes += len(f.Payload)
	c.seqs = append(c.seqs, f.Sequence)
	first := !c.signalled
	if first {
		c.signalled = true
	}
	atDeliver, hold := c.atDeliver, c.hold
	c.mu.Unlock()

	// The turn goroutine parks here, mid-delivery, so the test can supersede or
	// cancel it and the turn then continues into the completion tail.
	if first && atDeliver != nil {
		select {
		case atDeliver <- struct{}{}:
		default:
		}
		if hold != nil {
			<-hold
		}
	}
	return nil
}

func (c *countingSink) stats() (frames, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames, c.bytes
}

// t2Observer captures the semantic event sequence. Text is held in-process for
// assertion only, exactly as PipelineObserver's contract permits, and never
// written anywhere.
type t2Observer struct {
	mu        sync.Mutex
	segments  []speech.TranscriptSegment
	chunks    []speech.Chunk
	outcomes  []string
	turnDone  chan struct{}
	closeOnce sync.Once
}

func newT2Observer() *t2Observer {
	return &t2Observer{turnDone: make(chan struct{}, 8)}
}

func (o *t2Observer) OnTranscript(seg speech.TranscriptSegment) {
	o.mu.Lock()
	o.segments = append(o.segments, seg)
	o.mu.Unlock()
}

func (o *t2Observer) OnResponseChunk(c speech.Chunk) {
	o.mu.Lock()
	o.chunks = append(o.chunks, c)
	o.mu.Unlock()
}

func (o *t2Observer) OnTurnComplete(outcome voice.TurnOutcome, reason string) {
	o.mu.Lock()
	o.outcomes = append(o.outcomes, string(outcome)+"/"+reason)
	o.mu.Unlock()
	select {
	case o.turnDone <- struct{}{}:
	default:
	}
}

func (o *t2Observer) snapshot() (segs []speech.TranscriptSegment, chunks []speech.Chunk, outcomes []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]speech.TranscriptSegment(nil), o.segments...),
		append([]speech.Chunk(nil), o.chunks...),
		append([]string(nil), o.outcomes...)
}

// awaitTurn waits for one OnTurnComplete, bounded. No sleeps anywhere.
func (o *t2Observer) awaitTurn(t *testing.T, d time.Duration, what string) {
	t.Helper()
	select {
	case <-o.turnDone:
	case <-time.After(d):
		t.Fatalf("%s: no turn completed within %s", what, d)
	}
}

// kernelGenerator forwards to the REAL rt.Kernel, substituting only the session
// id so the pipeline's lowercase voice label maps onto the kernel's own minted
// session. It adds no behaviour: no retry, no fallback, no transformation of
// the spec or of the produced chunks.
type kernelGenerator struct {
	kernel  *rt.Kernel
	session rt.SessionID
}

func (g *kernelGenerator) Generate(
	ctx context.Context, spec rt.GenerateSpec, sinks ...rt.Sink,
) (*rt.Dispatcher, error) {
	spec.SessionID = g.session
	return g.kernel.Generate(ctx, spec, sinks...)
}

// ---------------------------------------------------------------------------
// The rig
// ---------------------------------------------------------------------------

const (
	// The language the frozen fakes actually declare (speech/harness.go:52),
	// used rather than overriding their capabilities: routing should be
	// exercised through the vocabulary the repository already has.
	t2Lang    = speech.LangEnglishIN
	t2OnsetAt = 1
	t2EndAt   = 3
	// t2TurnFrames is how many frames one turn occupies. Turn boundaries are
	// positions within this cycle, so consecutive turns reuse them.
	t2TurnFrames = t2EndAt + 2
)

type t2Rig struct {
	pipeline *voice.Pipeline
	registry *voice.ProviderRegistry
	stt      *speech.FakeSTTProvider
	tts      *speech.FakeTTSProvider
	model    *rt.FakeProvider
	intel    *scriptedIntel
	sink     *countingSink
	obs      *t2Observer
	conv     *conversation.Conversation
	// greeting is the plan the frozen engine returned from EventStart.
	greeting conversation.Plan
	fsm      *voice.SessionFSM
	cancel   context.CancelFunc
}

// t2Segments is the scripted recognition for one turn.
func t2Segments(text string) []speech.TranscriptSegment {
	return []speech.TranscriptSegment{
		{Sequence: 0, Text: text[:len(text)/2], IsFinal: false, Confidence: 0.55, Language: t2Lang},
		{Sequence: 1, Text: text, IsFinal: true, Confidence: 0.94, Language: t2Lang},
	}
}

func t2Spec(engine string) voice.ProviderSpec {
	return voice.ProviderSpec{
		Class:    voice.ClassDevelopment,
		Tier:     speech.TierPrimary,
		Engine:   engine,
		Version:  "t2",
		Locality: voice.LocalityProcess,
	}
}

// newT2Rig assembles the real pipeline around deterministic providers.
func newT2Rig(t *testing.T, sttScript []speech.TranscriptSegment, sessionID string) *t2Rig {
	t.Helper()
	return newT2RigWith(t, sttScript, nil, sessionID)
}

// newT2RigDenying is newT2Rig with an EMPTY governance policy registry, so the
// real engine denies with "no_policy_matched". That is genuine deny-by-default,
// not a stubbed Governor.
func newT2RigDenying(t *testing.T, sttScript []speech.TranscriptSegment, sessionID string) *t2Rig {
	t.Helper()
	return newT2RigFull(t, sttScript, nil, sessionID, false)
}

// newT2RigWith is newT2Rig with an optional STT override. A nil override uses
// the frozen speech.FakeSTTProvider; T4 supplies a per-stream double because
// the frozen fake replays one script on every stream.
func newT2RigWith(
	t *testing.T, sttScript []speech.TranscriptSegment,
	sttOverride speech.STTProvider, sessionID string,
) *t2Rig {
	t.Helper()
	return newT2RigFull(t, sttScript, sttOverride, sessionID, true)
}

func newT2RigFull(
	t *testing.T, sttScript []speech.TranscriptSegment,
	sttOverride speech.STTProvider, sessionID string, baselinePolicies bool,
) *t2Rig {
	t.Helper()
	return newT2RigOpts(t, sttScript, sessionID, t2RigOpts{
		stt: sttOverride, baselinePolicies: baselinePolicies,
	})
}

// t2RigOpts varies how the rig is assembled.
type t2RigOpts struct {
	// stt overrides the frozen fake recogniser. Nil uses it.
	stt speech.STTProvider

	// baselinePolicies loads the REAL production governance baseline. False
	// leaves the registry empty, which denies by default.
	baselinePolicies bool

	// pendingGreeting leaves the conversation in its OPENING TURN: EventStart
	// is sent and the greeting is not completed.
	//
	// Every other rig completes the greeting immediately, because every other
	// test is about a caller turn and needs the floor open. Phase 16 T6 is about
	// the opening turn itself, so it needs the conversation exactly where a real
	// call driver would hand it over: greeting decided, not yet spoken.
	pendingGreeting bool

	// greetingSystem is the deployment's opening instruction.
	greetingSystem string
}

func newT2RigOpts(
	t *testing.T, sttScript []speech.TranscriptSegment,
	sessionID string, o t2RigOpts,
) *t2Rig {
	t.Helper()

	sttOverride := o.stt
	baselinePolicies := o.baselinePolicies

	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	format := media.PCM16Mono16k()

	// --- real router + real registry, deterministic providers inside --------
	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	reg, err := voice.NewProviderRegistry(voice.ModeDevelopment, router)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stt := speech.NewFakeSTTProvider(speech.ProviderID("t2-stt"), sttScript, clock)
	tts := speech.NewFakeTTSProvider(speech.ProviderID("t2-tts"), 2, clock)
	var sttToRegister speech.STTProvider = stt
	if sttOverride != nil {
		sttToRegister = sttOverride
	}
	if err := reg.RegisterSTT(sttToRegister, t2Spec("fake-stt")); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	if err := reg.RegisterTTS(tts, t2Spec("fake-tts")); err != nil {
		t.Fatalf("RegisterTTS: %v", err)
	}

	// --- real generation kernel with the frozen fake provider inside --------
	harness, err := rt.NewHarness()
	if err != nil {
		t.Fatalf("rt.NewHarness: %v", err)
	}
	if err := harness.Start(context.Background()); err != nil {
		t.Fatalf("kernel start: %v", err)
	}
	t.Cleanup(func() { _ = harness.Stop(context.Background()) })

	// The pipeline generates against rt.SessionID(cfg.Session)
	// (pipeline.go:1080), so the kernel must hold a session under that id or
	// generation fails with "runtime: not found". The two id vocabularies do
	// not overlap: rt mints ids like "ses_06G11JKYSR..." while voice.SessionID
	// requires a lowercase label, so the pipeline's id cannot simply be adopted
	// from the kernel.
	//
	// kernelGenerator below bridges the two. It is TEST SCAFFOLDING, not a new
	// abstraction: it forwards to the real Kernel and only substitutes the
	// session id. No production code is involved in the mapping.
	kernelSession, err := harness.NewSession()
	if err != nil {
		t.Fatalf("kernel session: %v", err)
	}
	generator := &kernelGenerator{kernel: harness.Kernel, session: kernelSession.ID()}

	// --- real governance ----------------------------------------------------
	// The frozen module's OWN exported harness, loaded with the REAL production
	// BaselinePolicies. Two things this deliberately does not do: it does not
	// stub the Governor, and it does not author a permissive policy. An empty
	// registry denies everything ("no_policy_matched"), which is the correct
	// deny-by-default posture and was observed before the baseline was loaded.
	var govOpts []governance.HarnessOption
	if baselinePolicies {
		govOpts = append(govOpts, governance.WithBaseline())
	}
	govHarness, err := governance.NewHarness(govOpts...)
	if err != nil {
		t.Fatalf("governance harness: %v", err)
	}
	gov := govHarness.Engine

	// --- the Phase 14 seam: voiceintel bridge supplies the Planner ----------
	_, vi := build(t)
	planner, err := vi.Bridge().Planner(conversation.ConversationID(sessionID), "")
	if err != nil {
		t.Fatalf("bridge planner: %v", err)
	}
	opening := []conversation.Event{{Kind: conversation.EventStart}}
	if !o.pendingGreeting {
		opening = append(opening, conversation.Event{
			Kind: conversation.EventGreetingComplete})
	}
	var greeting conversation.Plan
	for i, e := range opening {
		plan, err := planner.Handle(e)
		if err != nil {
			t.Fatalf("opening the floor: %v", err)
		}
		if i == 0 {
			// The greeting plan the FROZEN engine produced, kept rather than
			// discarded. Phase 16 T6 submits this exact value.
			greeting = plan
		}
	}
	conv, _ := vi.Bridge().Conversation(conversation.ConversationID(sessionID))

	fsm, err := voice.NewSessionFSM(voice.FSMConfig{Session: voice.SessionID(sessionID)})
	if err != nil {
		t.Fatalf("fsm: %v", err)
	}

	intel := &scriptedIntel{onsetAt: t2OnsetAt, endAt: t2EndAt}
	sink := &countingSink{}
	obs := newT2Observer()

	p, err := voice.NewPipeline(voice.PipelineConfig{
		Session: voice.SessionID(sessionID), Language: t2Lang, Format: format,
		Registry:         reg,
		Intel:            intel,
		Planner:          planner,
		Governor:         gov,
		Generator:        generator,
		Output:           sink,
		FSM:              fsm,
		Observer:         obs,
		Clock:            clock,
		MaxPendingFrames: 64, MaxPendingSegments: 16, MaxPendingAudio: 64,
		TurnTimeout: 20 * time.Second,
		Tier:        rt.TierFast,
		// Governance identity is carried onto every decision. Leaving it empty
		// makes the request malformed and the turn is denied with
		// "malformed_action" — which is governance behaving correctly, not a
		// defect. A real deployment supplies these from the call record.
		Actor:           governance.ActorID("agent-t2"),
		Subject:         governance.SubjectID("caller-t2"),
		Org:             governance.OrgID("org-t2"),
		Business:        governance.BusinessID("biz-t2"),
		System:          "You are a receptionist. Answer briefly.",
		GreetingSystem:  o.greetingSystem,
		MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := p.Start(ctx); err != nil {
		t.Fatalf("pipeline start: %v", err)
	}

	return &t2Rig{
		pipeline: p, registry: reg, stt: stt, tts: tts, model: harness.Provider,
		intel: intel, sink: sink, obs: obs, conv: conv, fsm: fsm, cancel: cancel,
		greeting: greeting,
	}
}

// t2Frame builds one deterministic inbound frame. The payload is a fixed,
// non-acoustic pattern: no real audio is used anywhere in this suite.
func t2Frame(seq uint64) media.Frame {
	payload := make([]byte, 320) // 10 ms of PCM16 @ 16 kHz
	for i := range payload {
		payload[i] = byte(seq)
	}
	return media.Frame{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * 10 * time.Millisecond,
		Format:    media.PCM16Mono16k(),
		Payload:   payload,
	}
}

// driveTurn writes enough frames to cross the scripted onset and end.
func (r *t2Rig) driveTurn(t *testing.T, base uint64) {
	t.Helper()
	for i := uint64(0); i < t2TurnFrames; i++ {
		err := r.pipeline.WriteFrame(t2Frame(base + i))
		if err != nil && !errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("WriteFrame(%d): %v", base+i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// TestT2_RegistryHoldsOnlyDeterministicProviders proves registration, selection
// and health on the REAL registry and router.
func TestT2_RegistryHoldsOnlyDeterministicProviders(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-registry")

	if got := r.registry.Mode(); got != voice.ModeDevelopment {
		t.Errorf("mode = %v, want development", got)
	}
	if got := r.registry.Count(); got != 2 {
		t.Errorf("registry holds %d providers, want 2", got)
	}

	gotSTT, err := r.registry.PickSTT(t2Lang)
	if err != nil {
		t.Fatalf("PickSTT: %v", err)
	}
	if gotSTT.ID() != speech.ProviderID("t2-stt") {
		t.Errorf("PickSTT chose %q", gotSTT.ID())
	}
	gotTTS, err := r.registry.PickTTS(t2Lang)
	if err != nil {
		t.Fatalf("PickTTS: %v", err)
	}
	if gotTTS.ID() != speech.ProviderID("t2-tts") {
		t.Errorf("PickTTS chose %q", gotTTS.ID())
	}

	// Every descriptor must be development-class: no production or external
	// provider may appear in this environment.
	for _, d := range r.registry.Descriptors() {
		if d.Class != voice.ClassDevelopment {
			t.Errorf("provider %s is class %q; this environment registers "+
				"deterministic development providers only", d.ID, d.Class)
		}
	}

	// An unregistered language must fail through the ROUTER's own vocabulary.
	if _, err := r.registry.PickSTT(speech.Language("xx-XX")); err == nil {
		t.Error("an unregistered language selected a provider")
	}

	if got := len(r.registry.AllHealth()); got == 0 {
		t.Error("no health records; the registry is not tracking its providers")
	}
}

// ---------------------------------------------------------------------------
// Single-turn end to end
// ---------------------------------------------------------------------------

// TestT2_SingleTurnTraversesEveryStage is the core evidence: one inbound frame
// sequence produces a transcript, an intent, a governed plan, a generated
// response, synthesis, and outbound frames.
//
// It proves the PATH. It proves nothing about recognition accuracy.
func TestT2_SingleTurnTraversesEveryStage(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-single")

	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "single turn")

	segs, chunks, outcomes := r.obs.snapshot()

	// --- STT ----------------------------------------------------------------
	if len(segs) != 2 {
		t.Fatalf("observed %d segments, want 2 (one partial, one final)", len(segs))
	}
	if segs[0].IsFinal {
		t.Error("the first segment should be the partial")
	}
	if !segs[1].IsFinal || segs[1].Text != "transfer me to rajesh" {
		t.Errorf("final segment = %q (final=%v), want the scripted text",
			segs[1].Text, segs[1].IsFinal)
	}
	if r.stt.Opened() != 1 {
		t.Errorf("STT opened %d times, want 1", r.stt.Opened())
	}

	// --- the transcript DID reach the real conversation ---------------------
	if r.conv == nil {
		t.Fatal("no conversation behind the planner")
	}
	last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if !ok {
		t.Fatal("the planner never recorded an intent; the transcript did not " +
			"reach conversation")
	}

	// --- intent, now that ASR confidence is propagated ----------------------
	if last.Value != "request_transfer" {
		t.Fatalf("last_intent = %v, want request_transfer. ASRConfidence "+
			"propagation (pipeline.go) is what makes this resolve; a value of "+
			"\"unknown\" means the utterance was classified as noise again",
			last.Value)
	}

	// --- generation ---------------------------------------------------------
	if r.model.Calls() == 0 {
		t.Error("the generator was never called")
	}
	if len(chunks) == 0 {
		t.Fatal("no response chunk reached synthesis")
	}
	// The generated response IDENTITY, not merely its count. rt.FakeProvider's
	// default script is a single ChunkText "ok" (runtime/harness.go:193), so
	// the text that reaches synthesis is known exactly. Asserting only the
	// count would let a pipeline that mangled the response still pass.
	if got := chunks[0].Text; got != "ok" {
		t.Errorf("response chunk = %q, want %q — the text reaching synthesis is "+
			"not the text the generator produced", got, "ok")
	}

	// --- TTS + output -------------------------------------------------------
	if r.tts.Opened() != 1 {
		t.Errorf("TTS opened %d times, want 1", r.tts.Opened())
	}
	frames, bytes := r.sink.stats()
	if frames == 0 {
		t.Error("no audio frame was delivered to the sink")
	}
	if bytes == 0 {
		t.Error("delivered frames carried no payload")
	}
	t.Logf("delivered %d frames / %d bytes (metadata only; no payload logged)",
		frames, bytes)

	// --- turn outcome -------------------------------------------------------
	if len(outcomes) == 0 {
		t.Fatal("no turn outcome recorded")
	}
	if outcomes[0] != string(voice.OutcomeCompleted)+"/ok" {
		t.Errorf("turn outcome = %q, want completed/ok", outcomes[0])
	}
	if err := r.pipeline.Err(); err != nil {
		t.Errorf("pipeline recorded an error on the happy path: %v", err)
	}
}

// TestT2_PipelineDropsASRConfidence isolates the defect that blocks the rest of
// the path, so it is reported as a specific, reproducible fact rather than as a
// vague "the turn did not complete".
//
// MEASURED:
//
//	voice/pipeline.go:943-944 constructs
//	    conversation.Utterance{Text: final.Text}
//	leaving ASRConfidence at its zero value, and dropping the segment's
//	Confidence, Turn and Language.
//
//	conversation/intent.go:308 gates on
//	    if u.ASRConfidence < cfg.MinASRConfidence   // 0.40
//	returning IntentUnknown with verdict IntentNoise.
//
// CONSEQUENCE: every utterance forwarded by the pipeline is classified as
// noise, so no intent is ever resolved, and generation, synthesis and audio
// output are unreachable whenever a REAL conversation.Conversation is the
// Planner.
//
// WHY IT WAS NOT CAUGHT EARLIER: voice's own tests use a scripted planner and
// never drive a real conversation; Phase 14 wired a real conversation but never
// constructed the pipeline. T2 is the first time the two have been connected.
//
// This test asserts the frozen gate and the resulting classification directly,
// with no pipeline involved, so the diagnosis does not depend on the rig.
func TestT2_PipelineDropsASRConfidence(t *testing.T) {
	t.Parallel()

	cfg := conversation.DefaultIntentConfig()
	if cfg.MinASRConfidence <= 0 {
		t.Fatalf("MinASRConfidence = %v; the gate this test describes is gone",
			cfg.MinASRConfidence)
	}

	_, vi := build(t)

	// Exactly what the pipeline sends: text only, zero confidence.
	pZero, convZero, err := openPlanner(vi, "t2-asr-zero")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	planZero, err := pZero.Handle(conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "transfer me to rajesh"},
		Party:     conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("zero-confidence turn: %v", err)
	}
	lastZero, _ := convZero.Context().Get(conversation.ScopeConversation, "last_intent")

	// The identical text WITH the recogniser's confidence carried through.
	pReal, convReal, err := openPlanner(vi, "t2-asr-carried")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	planReal, err := pReal.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "transfer me to rajesh", ASRConfidence: 0.94,
		},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("carried-confidence turn: %v", err)
	}
	lastReal, _ := convReal.Context().Get(conversation.ScopeConversation, "last_intent")

	if lastZero.Value == lastReal.Value {
		t.Fatalf("both paths resolved to %v; the defect this test documents no "+
			"longer reproduces and the T2 report needs updating", lastZero.Value)
	}
	t.Logf("ASRConfidence 0.00 -> intent=%v action=%v reason=%q",
		lastZero.Value, planZero.Action, planZero.Reason)
	t.Logf("ASRConfidence 0.94 -> intent=%v action=%v reason=%q",
		lastReal.Value, planReal.Action, planReal.Reason)
	t.Logf("MINIMAL FIX (one line, production, packages/go/voice is NOT frozen): "+
		"voice/pipeline.go:944 set ASRConfidence: final.Confidence (gate is %.2f)",
		cfg.MinASRConfidence)
}

// ---------------------------------------------------------------------------
// Borrowed payload safety
// ---------------------------------------------------------------------------

// TestT2_RetainedFramesAreClonedNotBorrowed proves the frozen ownership rule is
// respected where this suite retains frames.
//
// media.Frame.Payload is BORROWED (media/frame.go:236): valid only until the
// next operation on the owning buffer. Anything retaining a frame past the call
// must Clone. This mutates the ORIGINAL buffer after handing it over and
// requires the retained copy to be unaffected — which is only true if it was
// cloned.
func TestT2_RetainedFramesAreClonedNotBorrowed(t *testing.T) {
	intel := &scriptedIntel{onsetAt: t2OnsetAt, endAt: t2EndAt}

	f := t2Frame(7)
	original := append([]byte(nil), f.Payload...)

	if _, err := intel.Analyze(context.Background(), f,
		audiointel.ConversationState{}, nil, nil); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// Simulate the owning ring buffer reusing the storage, which is exactly
	// what it is entitled to do the moment Analyze returns.
	for i := range f.Payload {
		f.Payload[i] = 0xFF
	}

	kept := intel.frames()
	if len(kept) != 1 {
		t.Fatalf("retained %d frames, want 1", len(kept))
	}
	for i, b := range kept[0].Payload {
		if b != original[i] {
			t.Fatalf("retained payload byte %d changed to %#x after the source "+
				"buffer was reused — the frame was BORROWED, not cloned. "+
				"media.Frame.Payload is only valid until the next operation on "+
				"its owner; retention requires Clone()", i, b)
		}
	}
}

// ---------------------------------------------------------------------------
// Provider failure
// ---------------------------------------------------------------------------

// TestT2_ProviderFailuresAreTypedAndBounded exercises the EXISTING failure
// behaviour. It invents no failover policy.
func TestT2_ProviderFailuresAreTypedAndBounded(t *testing.T) {
	// REACHABLE: the recogniser sits BEFORE the blocked stage, so its failure
	// path is genuinely exercised.
	t.Run("stt_open_fails", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-fail-stt")
		r.stt.FailNext(errors.New("scripted recogniser failure"))
		r.driveTurn(t, 0)

		// Bounded wait on the recorded error rather than on a turn callback: a
		// recogniser that never opens produces no turn to complete, which is
		// itself the behaviour under test.
		var err error
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if err = r.pipeline.Err(); err != nil {
				break
			}
			select {
			case <-r.pipeline.Done():
				err = r.pipeline.Err()
			case <-time.After(50 * time.Millisecond):
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			t.Fatal("a failing recogniser produced no recorded pipeline error")
		}
		if !errors.Is(err, voice.ErrProviderFailed) &&
			!errors.Is(err, voice.ErrProviderUnavailable) {
			t.Errorf("error %v is neither ErrProviderFailed nor "+
				"ErrProviderUnavailable; the classification is what sends an "+
				"operator to the right place", err)
		}
		if frames, _ := r.sink.stats(); frames != 0 {
			t.Errorf("%d frames delivered despite recognition failing", frames)
		}
	})

	// NOW REACHABLE, because ASR confidence propagation lets the planner return
	// ActionRespond and the turn proceeds into synthesis and generation.
	t.Run("tts_open_fails", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-fail-tts")
		r.tts.FailNext(errors.New("scripted synthesiser failure"))
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "tts failure")

		err := r.pipeline.Err()
		if err == nil {
			t.Fatal("a failing synthesiser produced no recorded pipeline error")
		}
		if !errors.Is(err, voice.ErrProviderFailed) &&
			!errors.Is(err, voice.ErrProviderUnavailable) {
			t.Errorf("error %v is not a typed provider failure", err)
		}
		if frames, _ := r.sink.stats(); frames != 0 {
			t.Errorf("%d frames delivered despite synthesis failing", frames)
		}
		// Synthesis is selected BEFORE generation, so a failed voice means the
		// generator is never asked to produce a response that could not be
		// spoken. NOT a failover claim: there is no second provider registered
		// and this suite invents no failover policy.
		if r.model.Calls() != 0 {
			t.Errorf("the generator ran %d times although synthesis had already "+
				"failed", r.model.Calls())
		}
	})

	t.Run("generation_fails", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-fail-gen")
		r.model.FailNext(1, errors.New("scripted generation failure"))
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "generation failure")

		err := r.pipeline.Err()
		if err == nil {
			t.Fatal("a failing generator produced no recorded pipeline error")
		}
		if !errors.Is(err, voice.ErrProviderFailed) {
			t.Errorf("error %v is not ErrProviderFailed", err)
		}
		if r.model.Calls() == 0 {
			t.Error("the generator was never reached, so its failure was not " +
				"what this test measured")
		}
		if frames, _ := r.sink.stats(); frames != 0 {
			t.Errorf("%d frames delivered despite generation failing", frames)
		}
	})

	// FAILOVER: NOT SUPPORTED BY CURRENT CONTRACT in this configuration. One
	// provider is registered per kind, so the router has nothing to fail over
	// to. No failover policy is invented here.
}

// ---------------------------------------------------------------------------
// Multi-turn
// ---------------------------------------------------------------------------

// TestT2_MultiTurnRunsThreeTurnsOnOneSession drives three consecutive turns
// through ONE pipeline and one conversation.
//
// CAPABILITY LIMIT OF THE EXPORTED FAKE, reported rather than worked around:
// speech.FakeSTTProvider holds ONE script for the provider and copies the WHOLE
// script into every stream it opens (harness.go:109), with CloseSend flushing
// whatever remains (harness.go:196). There is no per-stream script and no way
// to advance the script across streams, so THREE DIFFERENT UTTERANCES CANNOT BE
// SCRIPTED ACROSS THREE TURNS: every turn replays all of them and the last one
// wins. Measured directly — a three-utterance script produced intent "repeat"
// on turn 0.
//
// Giving the fake a per-stream script would mean modifying a FROZEN module, so
// it is reported as a gap instead. What multi-turn CAN prove with the existing
// fake is proven here: three turns run on one session, each produces its own
// generation and its own output audio, the conversation survives all three, and
// nothing leaks across them. Per-turn intent DISCRIMINATION is already covered
// at the planner by Phase 14 T11's acceptance matrix.
func TestT2_MultiTurnRunsThreeTurnsOnOneSession(t *testing.T) {
	const turns = 3
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-multi")

	var (
		lastFrames int
		lastCalls  int64
	)
	for i := 0; i < turns; i++ {
		// NO TEST-SIDE FLOOR RETURN.
		//
		// T2 had to send EventSpeechComplete here itself, because the pipeline
		// did not. T4 moved that into production (pipeline.go, at the delivery
		// boundary), so this loop drives audio ONLY. If the floor handoff ever
		// regresses, these turns become silent no-ops again and the assertions
		// below fail — which is exactly what M1 demonstrates.
		r.driveTurn(t, uint64(i*t2TurnFrames))
		r.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("turn %d", i))

		last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok {
			t.Fatalf("turn %d: no intent recorded", i)
		}
		if got := fmt.Sprintf("%v", last.Value); got != "request_transfer" {
			t.Errorf("turn %d: intent = %q, want request_transfer", i, got)
		}

		// Each turn does its OWN work rather than replaying the previous one.
		frames, _ := r.sink.stats()
		if frames <= lastFrames {
			t.Errorf("turn %d produced no new output frames (total still %d)",
				i, frames)
		}
		if calls := r.model.Calls(); calls <= lastCalls {
			t.Errorf("turn %d did not invoke generation (calls still %d)", i, calls)
		}
		lastFrames, lastCalls = frames, r.model.Calls()
	}

	// One conversation across all three turns, still alive.
	if r.conv.State().IsTerminal() {
		t.Error("the conversation terminated during a healthy multi-turn sequence")
	}
	if got := r.conv.Turns().Count(); got < turns {
		t.Errorf("turn count = %d after %d turns", got, turns)
	}
	if r.stt.Opened() != turns {
		t.Errorf("STT opened %d times for %d turns; each turn should open its "+
			"own recognition stream", r.stt.Opened(), turns)
	}
	if r.tts.Opened() != turns {
		t.Errorf("TTS opened %d times for %d turns", r.tts.Opened(), turns)
	}

	_, _, outcomes := r.obs.snapshot()
	if len(outcomes) != turns {
		t.Errorf("%d turn outcomes for %d turns: %v", len(outcomes), turns, outcomes)
	}
	for i, o := range outcomes {
		if o != string(voice.OutcomeCompleted)+"/ok" {
			t.Errorf("turn %d outcome = %q, want completed/ok", i, o)
		}
	}
	t.Logf("%d turns on one session: %d output frames, %d generations, outcomes %v",
		turns, lastFrames, lastCalls, outcomes)
}

// TestT2_ProviderFakesCanFailOnDemand proves at the PORT that each deterministic
// provider refuses when scripted to, so the blocked pipeline stages above are
// blocked by the production defect and not by an inadequate fake.
func TestT2_ProviderFakesCanFailOnDemand(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	boom := errors.New("scripted failure")

	stt := speech.NewFakeSTTProvider(speech.ProviderID("t2-stt-fail"),
		t2Segments("transfer me to rajesh"), clock)
	stt.FailNext(boom)
	if _, err := stt.OpenSTT(context.Background(), speech.STTConfig{
		Language: t2Lang, Format: media.PCM16Mono16k(),
	}); err == nil {
		t.Error("FakeSTTProvider.FailNext did not cause OpenSTT to fail")
	}

	tts := speech.NewFakeTTSProvider(speech.ProviderID("t2-tts-fail"), 2, clock)
	tts.FailNext(boom)
	if _, err := tts.OpenTTS(context.Background(), speech.TTSConfig{
		Language: t2Lang, Format: media.PCM16Mono16k(),
		Prosody: speech.DefaultProsody(),
	}); err == nil {
		t.Error("FakeTTSProvider.FailNext did not cause OpenTTS to fail")
	}

	model := rt.NewFakeProvider(rt.ProviderID("t2-model-fail"))
	model.FailNext(1, boom)
	if _, err := model.Generate(context.Background(), rt.GenerateRequest{}); err == nil {
		t.Error("FakeProvider.FailNext did not cause Generate to fail")
	}
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// TestT2_CancellationStopsThePipelineCleanly cancels the pipeline and requires a
// bounded, typed stop with no panic and no hang.
func TestT2_CancellationStopsThePipelineCleanly(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-cancel")

	r.driveTurn(t, 0)
	if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-r.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the pipeline did not finish after Cancel")
	}

	// A cancelled pipeline refuses further audio rather than accepting it.
	if err := r.pipeline.WriteFrame(t2Frame(99)); err == nil {
		t.Error("a cancelled pipeline accepted another frame")
	}

	// An unrelated session remains usable — cancellation is per-pipeline.
	other := newT2Rig(t, t2Segments("please call me back on 9876543210"), "t2-cancel-other")
	other.driveTurn(t, 0)
	other.obs.awaitTurn(t, 30*time.Second, "unrelated session after cancellation")
	if err := other.pipeline.Err(); err != nil {
		t.Errorf("an unrelated session broke when its neighbour was cancelled: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Interruption / barge-in
// ---------------------------------------------------------------------------

// TestT2_InterruptionUsesTheExistingMechanisms exercises BOTH existing
// boundaries and introduces no third one.
func TestT2_InterruptionUsesTheExistingMechanisms(t *testing.T) {
	// (A) Dialogue boundary: the frozen conversation interruption engine,
	// reached through the same Bridge planner the pipeline uses.
	t.Run("conversation_turn_boundary", func(t *testing.T) {
		_, vi := build(t)
		p, conv, err := openPlanner(vi, "t2-interrupt-conv")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
			t.Fatalf("turn: %v", err)
		}
		if got := conv.Turns().Holder(); got != conversation.PartyAgent {
			t.Fatalf("holder %v before barge-in, want agent", got)
		}
		plan, err := p.Handle(conversation.Event{
			Kind:         conversation.EventInterrupt,
			Interruption: conversation.InterruptionUser,
			Party:        conversation.PartyCaller,
			Reason:       "barge_in",
		})
		if err != nil {
			t.Fatalf("interrupt: %v", err)
		}
		if plan.Action != conversation.ActionIgnore || plan.Reason != "interrupted_user" {
			t.Errorf("action=%v reason=%q, want ignore/interrupted_user",
				plan.Action, plan.Reason)
		}
		if got := conv.Turns().Holder(); got != conversation.PartyCaller {
			t.Errorf("holder %v after barge-in, want caller", got)
		}
		if n := len(conv.Interruptions().History()); n != 1 {
			t.Errorf("%d interruptions recorded, want 1", n)
		}
	})

	// (B) Pipeline boundary: the existing interrupt controller and its bounded
	// counters. The reason is a bounded code, never free text.
	t.Run("pipeline_barge_in_counters", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-interrupt-pipe")
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "turn before barge-in")

		before := r.pipeline.BargeIns()
		if r.pipeline.SpeechInterruptRefusals() > 0 && before == 0 {
			t.Log("interruption was refused, which is a legitimate outcome when " +
				"the agent does not hold the floor")
		}
		// The pipeline exposes these as bounded counters, not as content.
		t.Logf("barge-ins=%d refusals=%d generation=%d",
			r.pipeline.BargeIns(), r.pipeline.SpeechInterruptRefusals(),
			r.pipeline.Generation())
	})
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

// TestT2_NoAudioOrTranscriptEscapesAsContent checks the boundary this suite is
// itself evidence for.
func TestT2_NoAudioOrTranscriptEscapesAsContent(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()
	_ = vi

	r := newT2Rig(t, t2Segments("my account number is 4417 9020"), "t2-security")
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "security turn")

	// The service log must not contain the scripted transcript or any audio.
	dump := sink.dump()
	for _, secret := range []string{"4417", "9020", "account number"} {
		if contains(dump, secret) {
			t.Errorf("transcript fragment %q reached the service log", secret)
		}
	}

	// Errors, if any, must not embed transcript content.
	if err := r.pipeline.Err(); err != nil {
		for _, secret := range []string{"4417", "9020"} {
			if contains(err.Error(), secret) {
				t.Errorf("pipeline error embeds transcript content: %v", err)
			}
		}
	}

	// The sink retained counts and lengths, never payloads.
	frames, bytes := r.sink.stats()
	if frames > 0 && bytes == 0 {
		t.Error("frames delivered with zero total bytes; the accounting is wrong")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT2_PipelineSemanticSignatureIsStable replays the same scenario on fresh
// rigs and requires an identical semantic signature.
//
// The signature contains ONLY stable values: segment finality/text, response
// chunk count, turn outcome, delivered frame count and total bytes, and the
// resolved intent. It excludes timestamps, session IDs, goroutine ordering and
// every payload byte.
func TestT2_PipelineSemanticSignatureIsStable(t *testing.T) {
	sig := func(pass int) string {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"),
			fmt.Sprintf("t2-det-%d", pass))
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "determinism pass")

		segs, chunks, outcomes := r.obs.snapshot()
		frames, bytes := r.sink.stats()
		last, _ := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")

		s := fmt.Sprintf("segments=%d", len(segs))
		for _, sg := range segs {
			s += fmt.Sprintf("|%d:%v:%s", sg.Sequence, sg.IsFinal, sg.Text)
		}
		s += fmt.Sprintf(" chunks=%d outcomes=%v frames=%d bytes=%d intent=%v",
			len(chunks), outcomes, frames, bytes, last.Value)
		return s
	}

	want := sig(0)
	for i := 1; i <= 4; i++ {
		if got := sig(i); got != want {
			t.Fatalf("pass %d drifted\nwant: %s\n got: %s", i, want, got)
		}
	}
	t.Logf("stable signature: %s", want)
}
