package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/governance"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T7 — THE INTERNAL CALL DRIVER, DRIVEN.
//
// Every call below is placed by the PRODUCTION driver, registered through the
// production buildService path, under a real platform.Service. Nothing here
// opens a conversation, mints an identifier or constructs a pipeline itself —
// that was the whole point of writing the driver.
//
// REAL INBOUND TRANSPORT IS NOT PRESENT. Inbound audio is a deterministic,
// non-acoustic byte pattern handed to Call.Audio. No claim is made about
// telephony, SIP, RTP, WebRTC, microphones or recognition accuracy.

// ---------------------------------------------------------------------------
// Session-aware recognition
// ---------------------------------------------------------------------------

// t7STT hands a different script to each SESSION, and a different one to each
// stream within a session.
//
// WHY IT EXISTS: speech.FakeSTTProvider replays one script on every stream, and
// t4STT dispatches by the ORDER streams are opened — which is deterministic for
// one call and not for two running at once. Recognition here is keyed on
// speech.STTConfig.Session, which the port already carries, so two concurrent
// calls hear their own caller no matter how their turns interleave.
//
// A TEST DOUBLE. It implements the frozen exported interfaces, lives in a
// _test.go file, and nothing production references it.
type t7STT struct {
	id   speech.ProviderID
	caps speech.Capabilities

	mu      sync.Mutex
	scripts map[speech.SessionID][][]speech.TranscriptSegment
	opened  map[speech.SessionID]int
	total   int

	// openErr fails OpenSTT for ONE session.
	//
	// Per session rather than per provider: the registry is process-wide, so a
	// provider that failed outright would fail every call and could not express
	// "one call's recogniser died while its neighbour kept working" — which is
	// the isolation question a real subprocess recogniser makes urgent.
	openErr map[speech.SessionID]error

	// silent ends a session's stream with NO final transcript, which is what a
	// recogniser that times out internally produces.
	silent map[speech.SessionID]bool
}

var _ speech.STTProvider = (*t7STT)(nil)

func newT7STT() *t7STT {
	return &t7STT{
		id: speech.ProviderID("t7-stt"),
		caps: speech.Capabilities{
			Languages: []speech.Language{
				speech.LangEnglishIN, speech.LangHindi, speech.LangHinglish},
			Streaming:      true,
			PartialResults: true,
			SampleRates:    []media.SampleRate{media.Rate8kHz, media.Rate16kHz},
		},
		scripts: make(map[speech.SessionID][][]speech.TranscriptSegment),
		opened:  make(map[speech.SessionID]int),
		openErr: make(map[speech.SessionID]error),
		silent:  make(map[speech.SessionID]bool),
	}
}

// script queues what this session's caller will say, in order.
func (p *t7STT) script(s voice.SessionID, utterances ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, u := range utterances {
		p.scripts[speech.SessionID(s)] = append(p.scripts[speech.SessionID(s)],
			[]speech.TranscriptSegment{
				{Sequence: uint64(2 * i), Text: u[:len(u)/2], IsFinal: false,
					Confidence: 0.55, Language: t2Lang},
				{Sequence: uint64(2*i + 1), Text: u, IsFinal: true,
					Confidence: 0.94, Language: t2Lang},
			})
	}
}

// failFor makes OpenSTT refuse for one session, as a missing or broken local
// recogniser does.
func (p *t7STT) failFor(s voice.SessionID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.openErr[speech.SessionID(s)] = err
}

// silentFor makes one session's recogniser produce nothing at all.
func (p *t7STT) silentFor(s voice.SessionID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.silent[speech.SessionID(s)] = true
}

func (p *t7STT) ID() speech.ProviderID             { return p.id }
func (p *t7STT) Capabilities() speech.Capabilities { return p.caps }

func (p *t7STT) opens() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

func (p *t7STT) OpenSTT(_ context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if err := p.openErr[cfg.Session]; err != nil {
		p.mu.Unlock()
		return nil, err
	}
	idx := p.opened[cfg.Session]
	p.opened[cfg.Session]++
	p.total++
	var script []speech.TranscriptSegment
	if all := p.scripts[cfg.Session]; idx < len(all) {
		script = append([]speech.TranscriptSegment(nil), all[idx]...)
	} else if p.silent[cfg.Session] {
		script = nil
	}
	if p.silent[cfg.Session] {
		script = nil
	}
	p.mu.Unlock()

	// A stream beyond the script is EMPTY rather than a repeat: silently
	// replaying the last utterance would make an unexpected extra turn look
	// like a correct one.
	return &t4Stream{
		results: make(chan speech.TranscriptSegment, len(script)+1),
		script:  script, cfg: cfg, id: p.id,
	}, nil
}

// t7FailingTTS refuses to open. A deterministic provider fault, not a sleep and
// not a random failure.
type t7FailingTTS struct{ caps speech.Capabilities }

var _ speech.TTSProvider = (*t7FailingTTS)(nil)

func (p *t7FailingTTS) ID() speech.ProviderID             { return speech.ProviderID("t7-tts-broken") }
func (p *t7FailingTTS) Capabilities() speech.Capabilities { return p.caps }
func (p *t7FailingTTS) OpenTTS(context.Context, speech.TTSConfig) (speech.TTSStream, error) {
	return nil, errors.New("t7: the voice will not open")
}

// t7FailingGenerator refuses to generate.
type t7FailingGenerator struct{}

func (g *t7FailingGenerator) Generate(
	context.Context, rt.GenerateSpec, ...rt.Sink,
) (*rt.Dispatcher, error) {
	return nil, errors.New("t7: the model is unavailable")
}

// ---------------------------------------------------------------------------
// Per-call ports
// ---------------------------------------------------------------------------

// t7CallPorts is one call's audio intelligence, media sink and observer.
type t7CallPorts struct {
	intel *scriptedIntel
	sink  *countingSink
	obs   *t2Observer
}

// t7Ports produces a FRESH set for every call and remembers them so a test can
// assert on one call's audio without reaching into another's.
type t7Ports struct {
	mu             sync.Mutex
	byCall         map[voice.SessionID]*t7CallPorts
	bargeAt        uint64
	onsetAt, endAt uint64
	absolute       bool
}

func newT7Ports() *t7Ports {
	return &t7Ports{byCall: make(map[voice.SessionID]*t7CallPorts)}
}

func (f *t7Ports) new(s voice.SessionID) (CallPorts, error) {
	onset, end := uint64(t2OnsetAt), uint64(t2EndAt)
	if f.endAt != 0 {
		onset, end = f.onsetAt, f.endAt
	}
	p := &t7CallPorts{
		intel: &scriptedIntel{
			onsetAt: onset, endAt: end, absolute: f.absolute,
			bargeAt: f.bargeAt, bargedC: make(chan struct{}),
		},
		sink: &countingSink{},
		obs:  newT2Observer(),
	}
	f.mu.Lock()
	f.byCall[s] = p
	f.mu.Unlock()
	return CallPorts{Intel: p.intel, Output: p.sink, Observer: p.obs}, nil
}

func (f *t7Ports) of(t *testing.T, c *Call) *t7CallPorts {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byCall[c.Session()]
	if !ok {
		t.Fatalf("no ports were built for call %s", c.ID())
	}
	return p
}

// count returns how many calls were given ports. Every call gets its own; the
// number is what proves no sharing.
func (f *t7Ports) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byCall)
}

// ---------------------------------------------------------------------------
// The rig: a real service with the driver registered
// ---------------------------------------------------------------------------

type t7Rig struct {
	svc      *platform.Service
	vi       *voiceIntelligence
	driver   *CallDriver
	registry *voice.ProviderRegistry
	stt      *t7STT
	model    *rt.FakeProvider
	ports    *t7Ports
	logs     *logSink
	cancel   context.CancelFunc
	done     <-chan error

	// stopOnce makes teardown idempotent: a test that shuts the service down
	// explicitly must not have its cleanup wait on a channel already drained.
	stopOnce sync.Once
	stopped  chan struct{}
}

type t7Opts struct {
	// denyEverything leaves the governance policy registry empty, so the REAL
	// engine denies with "no_policy_matched". Genuine deny-by-default, not a
	// stubbed Governor.
	denyEverything bool
	brokenTTS      bool
	brokenModel    bool
	bargeAt        uint64

	// stt replaces the deterministic recogniser with whatever the caller
	// registers on the REAL registry. Phase 16 T10 uses it to put the real
	// whispercli adapter behind the same router every other test uses; nil
	// keeps the frozen fake.
	stt func(*voice.ProviderRegistry) error

	// onsetAt/endAt/absolute override the scripted turn boundaries, so one real
	// utterance can occupy one turn instead of being chopped into cycles.
	onsetAt, endAt uint64
	absolute       bool

	// maxPendingFrames widens the inbound queue. A real utterance is written
	// far faster than it plays, and a dropped frame would silently change the
	// audio the recogniser sees — and therefore the transcript.
	maxPendingFrames int
}

const t7GreetingInstruction = "open the call with the lawful-basis announcement"

// newT7Rig starts a real platform.Service with a CallDriver registered through
// the production buildService path.
func newT7Rig(t *testing.T, o t7Opts) *t7Rig {
	t.Helper()

	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// --- process-wide ports: real router, real registry ---------------------
	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	reg, err := voice.NewProviderRegistry(voice.ModeDevelopment, router)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stt := newT7STT()
	if o.stt != nil {
		// The caller owns recognition entirely. Registering the fake as well
		// would leave the router choosing between them, and the test would be
		// measuring that choice rather than the real provider.
		if err := o.stt(reg); err != nil {
			t.Fatalf("registering the recogniser: %v", err)
		}
	} else if err := reg.RegisterSTT(stt, t2Spec("fake-stt")); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	if o.brokenTTS {
		broken := &t7FailingTTS{caps: speech.Capabilities{
			Languages:   []speech.Language{speech.LangEnglishIN, speech.LangHindi, speech.LangHinglish},
			Streaming:   true,
			SampleRates: []media.SampleRate{media.Rate8kHz, media.Rate16kHz},
		}}
		if err := reg.RegisterTTS(broken, t2Spec("fake-tts")); err != nil {
			t.Fatalf("RegisterTTS: %v", err)
		}
	} else {
		tts := speech.NewFakeTTSProvider(speech.ProviderID("t7-tts"), 2, clock)
		if err := reg.RegisterTTS(tts, t2Spec("fake-tts")); err != nil {
			t.Fatalf("RegisterTTS: %v", err)
		}
	}

	// --- real generation kernel ---------------------------------------------
	kernel, err := rt.NewHarness()
	if err != nil {
		t.Fatalf("rt.NewHarness: %v", err)
	}
	if err := kernel.Start(context.Background()); err != nil {
		t.Fatalf("kernel start: %v", err)
	}
	t.Cleanup(func() { _ = kernel.Stop(context.Background()) })

	kernelSession, err := kernel.NewSession()
	if err != nil {
		t.Fatalf("kernel session: %v", err)
	}
	var generator voice.Generator = &kernelGenerator{
		kernel: kernel.Kernel, session: kernelSession.ID()}
	if o.brokenModel {
		generator = &t7FailingGenerator{}
	}

	// --- real governance ----------------------------------------------------
	var govOpts []governance.HarnessOption
	if !o.denyEverything {
		govOpts = append(govOpts, governance.WithBaseline())
	}
	govHarness, err := governance.NewHarness(govOpts...)
	if err != nil {
		t.Fatalf("governance harness: %v", err)
	}

	ports := newT7Ports()
	ports.bargeAt = o.bargeAt
	ports.onsetAt, ports.endAt, ports.absolute = o.onsetAt, o.endAt, o.absolute

	maxPending := o.maxPendingFrames
	if maxPending == 0 {
		maxPending = 64
	}

	driverCfg := CallDriverConfig{
		NewCallPorts: ports.new,
		Pipeline: voice.PipelineConfig{
			Language: t2Lang,
			Format:   media.PCM16Mono16k(),
			Registry: reg,
			Governor: govHarness.Engine,
			// Generator is the real Kernel unless the test breaks it.
			Generator:          generator,
			Clock:              clock,
			MaxPendingFrames:   maxPending,
			MaxPendingSegments: 16,
			MaxPendingAudio:    64,
			TurnTimeout:        20 * time.Second,
			Tier:               rt.TierFast,
			Actor:              governance.ActorID("agent-t7"),
			Subject:            governance.SubjectID("caller-t7"),
			Org:                governance.OrgID("org-t7"),
			Business:           governance.BusinessID("biz-t7"),
			System:             "You are a receptionist. Answer briefly.",
			// CONFIGURATION, NOT CONTENT. An instruction to greet, not a
			// greeting: no test in this repository authors the words a caller
			// would hear, and the deterministic provider ignores them anyway.
			GreetingSystem:  t7GreetingInstruction,
			MaxOutputTokens: 64,
		},
	}

	// --- the production service, with the driver registered ------------------
	logs := &logSink{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))

	cfg := testConfig()
	cfg.HTTPPort = freePort(t)
	cfg.HealthPort = freePort(t)
	cfg.ShutdownTimeout = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	var driver *CallDriver
	svc, vi, err := buildService(cfg, logger, platform.NewHealth(cfg),
		withCallDriver(driverCfg, func(d *CallDriver) { driver = d }))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	if driver == nil {
		t.Fatal("buildService registered no call driver")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// The driver's own readiness, not a poll and not a sleep: Run closes this
	// channel as it publishes the context every call is bound to.
	select {
	case <-driver.ready:
	case err := <-done:
		t.Fatalf("the service exited before the driver started: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatalf("the call driver never started; log:\n%s", logs.dump())
	}

	r := &t7Rig{
		svc: svc, vi: vi, driver: driver, registry: reg, stt: stt,
		model: kernel.Provider, ports: ports, logs: logs,
		cancel: cancel, done: done, stopped: make(chan struct{}),
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

// stop cancels the service and waits for shutdown. Idempotent.
func (r *t7Rig) stop(t *testing.T) {
	t.Helper()
	r.stopOnce.Do(func() {
		defer close(r.stopped)
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("the service returned %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Errorf("the service did not shut down; log:\n%s", r.logs.dump())
		}
	})
	<-r.stopped
}

// place opens a call through the driver and scripts what its caller will say.
func (r *t7Rig) place(t *testing.T, utterances ...string) *Call {
	t.Helper()
	call, err := r.driver.Place(context.Background())
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	t.Cleanup(func() { _ = call.End(voice.ReasonCallEnded) })
	r.stt.script(call.Session(), utterances...)
	return call
}

// speak drives one caller turn: enough frames to cross the scripted onset and
// endpoint, then waits for the turn to complete.
func (r *t7Rig) speak(t *testing.T, call *Call, turn int) {
	t.Helper()
	p := r.ports.of(t, call)
	base := uint64(turn) * t2TurnFrames
	for i := uint64(0); i < t2TurnFrames; i++ {
		err := call.Audio(t2Frame(base + i))
		if err != nil && !errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("turn %d frame %d: %v", turn, base+i, err)
		}
	}
	p.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("caller turn %d", turn))
}

// greet takes the opening turn and consumes the turn token it produces, so a
// later wait waits for the CALLER's turn rather than returning on the
// greeting's.
func (r *t7Rig) greet(t *testing.T, call *Call) {
	t.Helper()
	if err := call.Greet(context.Background()); err != nil {
		t.Fatalf("Greet: %v", err)
	}
	r.ports.of(t, call).obs.awaitTurn(t, 30*time.Second, "the greeting")
}

// awaitCallEnd waits, BOUNDED, for a terminated call's pipeline to finish.
//
// Bounded rather than a bare receive: a driver that stopped tearing calls down
// would hang the suite instead of failing it, and a hang reports nothing.
func awaitCallEnd(t *testing.T, c *Call, what string) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not finish after End; the call was never torn down", what)
	}
}

// runnerCount reads the runner count platform logs as it becomes ready.
func runnerCount(t *testing.T, sink *logSink) int {
	t.Helper()
	ready := sink.find("service ready")
	if len(ready) != 1 {
		t.Fatalf("expected one 'service ready' record, got %d", len(ready))
	}
	n, ok := ready[0]["runners"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("'service ready' carries no runner count: %v", ready[0])
	}
	return int(n)
}

// ---------------------------------------------------------------------------
// 1. Ownership
// ---------------------------------------------------------------------------

// TestT7_TheDriverOwnsEveryLifecycleStepT2FoundOwnerless.
//
// Phase 16 T2 audited nine responsibilities and found the same answer for every
// one: nobody. This is that table, answered.
func TestT7_TheDriverOwnsEveryLifecycleStepT2FoundOwnerless(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	call := r.place(t, "transfer me to rajesh")

	// 1. an identifier exists, and the driver minted it
	if call.ID() == "" {
		t.Fatal("the call has no conversation identifier")
	}
	// 2. the conversation exists, and it came from the BRIDGE
	conv, ok := r.vi.Bridge().Conversation(call.ID())
	if !ok {
		t.Fatal("the bridge does not know the conversation the driver opened")
	}
	if conv != call.Conversation() {
		t.Error("the call holds a conversation the bridge did not produce; a " +
			"second engine is in play")
	}
	// 3. a pipeline exists and is running
	if call.Pipeline() == nil {
		t.Fatal("the call has no pipeline")
	}
	if got := call.State(); got != voice.StateListening {
		t.Errorf("session state after Place = %v, want listening", got)
	}
	// 4-6. the opening turn
	r.greet(t, call)
	// 7. caller turns
	r.speak(t, call, 0)
	// 8-9. termination and cleanup
	if err := call.End(voice.ReasonCallEnded); err != nil {
		t.Errorf("End: %v", err)
	}
	select {
	case <-call.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the call's pipeline did not finish after End")
	}
}

// TestT7_TheDriverHoldsNoSessionRegistry.
//
// Session ownership is structural: the returned value IS the call. Nothing maps
// an identifier to a live session, so a stale identifier cannot resolve and two
// calls cannot reach each other.
func TestT7_TheDriverHoldsNoSessionRegistry(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	a := r.place(t)
	b := r.place(t)

	if a.ID() == b.ID() {
		t.Fatal("two calls were given the same identifier")
	}
	if a.Conversation() == b.Conversation() {
		t.Error("two calls share one conversation")
	}
	if a.Pipeline() == b.Pipeline() {
		t.Error("two calls share one pipeline")
	}
	if got := r.ports.count(); got != 2 {
		t.Errorf("%d sets of per-call ports were built for 2 calls, want 2: "+
			"audio intelligence and the media sink must not be shared", got)
	}

	// The ONLY route to a conversation is the bridge, and it answers for the
	// identifier it was given — that is the frozen engine's map, not a second
	// one in this service.
	if _, ok := r.vi.Bridge().Conversation(a.ID()); !ok {
		t.Error("the bridge lost call A's conversation")
	}
	if _, ok := r.vi.Bridge().Conversation(conversation.ConversationID("no-such-call")); ok {
		t.Error("an unknown identifier resolved to a conversation")
	}
}

// ---------------------------------------------------------------------------
// 2. The greeting
// ---------------------------------------------------------------------------

// TestT7_TheGreetingRunsEndToEndThroughTheDriver.
func TestT7_TheGreetingRunsEndToEndThroughTheDriver(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	p := r.ports.of(t, call)

	if got := call.Conversation().State(); got != conversation.StateIdle {
		t.Fatalf("a placed call starts in %v, want idle", got)
	}

	r.greet(t, call)

	if got := r.model.Calls(); got != 1 {
		t.Errorf("the generator ran %d times for the greeting, want 1", got)
	}
	if frames, bytes := p.sink.stats(); frames == 0 || bytes == 0 {
		t.Errorf("the greeting delivered %d frames / %d bytes", frames, bytes)
	}
	if got := r.stt.opens(); got != 0 {
		t.Errorf("recognition opened %d times for the greeting; the caller has "+
			"not spoken", got)
	}
}

// TestT7_TheGreetingCompletionIsTheFrozenContract is the critical assertion.
//
// Not the final state. EventSpeechComplete would produce the same one, from the
// same place, with no visible difference — the trigger and the floor are the
// only discriminators.
//
// NOTE ON THE FLOOR: after the opening turn the holder is NONE, not the caller.
// handleGreetingComplete RELEASES the agent's turn rather than handing it over
// (engine.go:549), which is what an announcement is. Asserting PartyCaller here
// would pass with the wrong event and would misstate the frozen contract.
func TestT7_TheGreetingCompletionIsTheFrozenContract(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	r.greet(t, call)

	conv := call.Conversation()
	if got := greetings(conv); got != 1 {
		t.Errorf("the trace records %d greeting completions, want exactly 1", got)
	}
	if got := completions(conv); got != 0 {
		t.Errorf("the trace records %d SPEECH completions for the opening turn, want 0", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("the trace records %d abandonments for a delivered greeting, want 0", got)
	}
	if got := conv.State(); got != conversation.StateListening {
		t.Errorf("state after the greeting = %v, want listening", got)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("floor after the greeting = %v, want none", got)
	}
}

// TestT7_TheDriverNeverEmitsCompletionEventsItself.
//
// The driver sends EventStart and EventHangup and nothing else. Speech
// completion means "the audio finished", which only the component that
// delivered it can know — and a driver that sent it too would send it twice on
// every call.
func TestT7_TheDriverNeverEmitsCompletionEventsItself(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t, "transfer me to rajesh")

	r.greet(t, call)
	r.speak(t, call, 0)

	conv := call.Conversation()
	if got := greetings(conv); got != 1 {
		t.Errorf("%d greeting completions after one greeting, want 1", got)
	}
	if got := completions(conv); got != 1 {
		t.Errorf("%d speech completions after one caller turn, want 1", got)
	}
}

// TestT7_GreetingTextIsNeverAuthoredByTheDriver.
//
// The greeting is an INSTRUCTION from configuration, and the generation request
// carries no caller message. A driver that put words in the caller's mouth to
// make the greeting work would show up here as a message.
func TestT7_GreetingTextIsNeverAuthoredByTheDriver(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	r.greet(t, call)

	req := r.model.LastRequest()
	if req == nil {
		t.Fatal("the generator recorded no request for the greeting")
	}
	if len(req.Messages) != 0 {
		t.Errorf("the greeting request carried %d message(s); a greeting has no "+
			"caller transcript and none may be invented", len(req.Messages))
	}
	if req.System != t7GreetingInstruction {
		t.Errorf("the greeting request carried System=%q, want the configured "+
			"instruction", req.System)
	}
}

// TestT7_GreetReturnsOnlyAfterTheGreetingHasBeenDelivered pins the ORDERING the
// driver is responsible for.
//
// Greet blocking until the announcement has actually been delivered is what
// makes "then accept caller utterances" safe. A driver that started the
// greeting and returned would let a caller turn open while the opening
// announcement was still playing — and INV-CV-2 makes that turn non-yielding,
// so the caller would be queued behind audio the driver had already declared
// finished.
//
// Observed with the sink handshake: the greeting is parked mid-delivery, and
// Greet must still be running at that moment. No sleep decides this.
func TestT7_GreetReturnsOnlyAfterTheGreetingHasBeenDelivered(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	p := r.ports.of(t, call)

	atDeliver := make(chan struct{}, 1)
	hold := make(chan struct{})
	p.sink.mu.Lock()
	p.sink.atDeliver, p.sink.hold = atDeliver, hold
	p.sink.mu.Unlock()

	returned := make(chan error, 1)
	go func() { returned <- call.Greet(context.Background()) }()

	select {
	case <-atDeliver:
	case err := <-returned:
		t.Fatalf("Greet returned before any greeting audio was delivered: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no greeting audio reached the sink")
	}

	// Parked inside delivery. Greet MUST still be running, and the conversation
	// must still be in its opening turn.
	select {
	case err := <-returned:
		t.Fatalf("Greet returned while the greeting was still being delivered "+
			"(err=%v); a caller turn could now open over the announcement", err)
	default:
	}
	if got := call.Conversation().State(); got != conversation.StateGreeting {
		t.Errorf("mid-greeting the conversation is %v, want greeting", got)
	}
	if got := greetings(call.Conversation()); got != 0 {
		t.Errorf("%d greeting completions while the greeting is still playing", got)
	}

	close(hold)

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Greet: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Greet did not return after delivery was released")
	}
	if got := greetings(call.Conversation()); got != 1 {
		t.Errorf("%d greeting completions after Greet returned, want 1", got)
	}
	if got := call.Conversation().State(); got != conversation.StateListening {
		t.Errorf("state after Greet returned = %v, want listening", got)
	}
}

// ---------------------------------------------------------------------------
// 3. Caller turns
// ---------------------------------------------------------------------------

// TestT7_TheFirstCallerTurnTraversesEveryStage.
func TestT7_TheFirstCallerTurnTraversesEveryStage(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t, "transfer me to rajesh")
	p := r.ports.of(t, call)

	r.greet(t, call)
	greetingFrames, _ := p.sink.stats()

	r.speak(t, call, 0)

	segs, chunks, outcomes := p.obs.snapshot()
	if len(segs) < 2 {
		t.Errorf("observed %d transcript segments for one turn, want a partial "+
			"and a final", len(segs))
	}
	if len(chunks) == 0 {
		t.Error("no clause reached the synthesiser for the caller's turn")
	}
	if frames, _ := p.sink.stats(); frames <= greetingFrames {
		t.Errorf("no audio was delivered for the caller's turn (%d frames "+
			"total, %d were the greeting's)", frames, greetingFrames)
	}
	if len(outcomes) != 2 {
		t.Errorf("observed %d turn outcomes (%v), want the greeting and one "+
			"caller turn", len(outcomes), outcomes)
	}

	conv := call.Conversation()
	if got := completions(conv); got != 1 {
		t.Errorf("%d speech completions, want 1", got)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after the caller's turn = %v, want caller", got)
	}
	last, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if !ok || last.Value != "request_transfer" {
		t.Errorf("last_intent = %v (present=%v), want request_transfer", last.Value, ok)
	}
}

// TestT7_ThreeCallerTurnsOnOneCall proves the lifecycle holds across turns.
func TestT7_ThreeCallerTurnsOnOneCall(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t,
		"transfer me to rajesh",
		"call me back on 9876543210",
		"transfer me to rajesh")

	id := call.ID()
	r.greet(t, call)

	conv := call.Conversation()
	wantIntents := []string{"request_transfer", "request_callback", "request_transfer"}

	for turn := 0; turn < 3; turn++ {
		r.speak(t, call, turn)

		if got := call.ID(); got != id {
			t.Fatalf("turn %d: the conversation identifier changed to %s", turn, got)
		}
		if got := completions(conv); got != turn+1 {
			t.Errorf("turn %d: %d speech completions, want %d", turn, got, turn+1)
		}
		if got := conv.Turns().Holder(); got != conversation.PartyCaller {
			t.Errorf("turn %d: floor = %v, want caller", turn, got)
		}
		last, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok || last.Value != wantIntents[turn] {
			t.Errorf("turn %d: last_intent = %v, want %q", turn, last.Value, wantIntents[turn])
		}
	}

	// The greeting happened once and stayed happened.
	if got := greetings(conv); got != 1 {
		t.Errorf("%d greeting completions after three turns, want 1", got)
	}
	if got := len(conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions on a call nobody interrupted", got)
	}
	if got := r.stt.opens(); got != 3 {
		t.Errorf("recognition opened %d times for three caller turns", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Interruption
// ---------------------------------------------------------------------------

// TestT7_BargeInReachesTheConversationThroughTheProductionPath.
//
// Driven through audio: the analyser calls the pipeline's own interrupt
// controller, exactly as audiointel does. The driver injects no EventInterrupt
// — it does not know the agent is speaking, and it must not pretend to.
func TestT7_BargeInReachesTheConversationThroughTheProductionPath(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t, "transfer me to rajesh")
	p := r.ports.of(t, call)

	// The greeting first, UNINTERRUPTED. Arming the barge-in before it would
	// interrupt the lawful-basis announcement, which is a compliance decision
	// this task does not make.
	r.greet(t, call)

	// Determinism comes from two existing seams, not from timing: the sink
	// handshake parks the caller's turn inside audio delivery, which is
	// provably a floor-holding state, and the analyser retries the
	// interruption until one is DELIVERED.
	atDeliver := make(chan struct{}, 1)
	hold := make(chan struct{})
	barged := make(chan struct{})
	p.sink.mu.Lock()
	p.sink.atDeliver, p.sink.hold = atDeliver, hold
	// RE-ARMED. The sink signals on its FIRST delivery, and the greeting has
	// already used it; without this the handshake would never fire and the
	// test would time out having proved nothing.
	p.sink.signalled = false
	p.sink.mu.Unlock()
	p.intel.mu.Lock()
	p.intel.bargeAt, p.intel.bargedC, p.intel.barged = t2EndAt+1, barged, false
	p.intel.mu.Unlock()

	for i := uint64(0); i <= t2EndAt; i++ {
		if err := call.Audio(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	select {
	case <-atDeliver:
	case <-time.After(30 * time.Second):
		t.Fatal("the turn never reached audio delivery; the agent never held " +
			"the floor and there was no barge-in window")
	}

	// Parked mid-delivery. Retried on a FIXED sequence number, which is neither
	// the onset nor the endpoint position in the turn cycle — walking the
	// sequence forward would start a second turn and mask the barge-in.
	go func() {
		for n := 0; n < 4*t2TurnFrames; n++ {
			select {
			case <-barged:
				return
			default:
			}
			_ = call.Audio(t2Frame(t2EndAt + 1))
		}
	}()
	select {
	case <-barged:
	case <-time.After(30 * time.Second):
		close(hold)
		t.Fatal("no barge-in was delivered while the turn was parked in delivery")
	}
	if call.Pipeline().BargeIns() == 0 {
		close(hold)
		t.Fatal("the pipeline did not record the interruption as delivered")
	}
	close(hold)
	p.obs.awaitTurn(t, 30*time.Second, "interrupted turn")

	conv := call.Conversation()
	if got := len(conv.Interruptions().History()); got != 1 {
		t.Errorf("%d interruptions recorded, want exactly 1", got)
	}
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions for an interrupted response, want 0", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments for an interrupted response, want 0: it was "+
			"interrupted, not abandoned", got)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after the barge-in = %v, want caller", got)
	}
	if conv.State() == conversation.StateEnded {
		t.Error("the conversation ended on a barge-in")
	}

	// The caller can still speak, and the session is still usable.
	if got := call.State(); got.Terminal() {
		t.Errorf("the voice session is %v after a barge-in", got)
	}
	plan, err := conv.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("the caller's utterance after a barge-in failed: %v", err)
	}
	if plan.Reason == "floor_queued" {
		t.Errorf("the caller is queued after their own barge-in (action=%v reason=%q)",
			plan.Action, plan.Reason)
	}
}

// ---------------------------------------------------------------------------
// 5. Failure paths
// ---------------------------------------------------------------------------

// TestT7_AGreetingThatCannotBeGeneratedIsAbandoned.
func TestT7_AGreetingThatCannotBeGeneratedIsAbandoned(t *testing.T) {
	r := newT7Rig(t, t7Opts{brokenModel: true})
	call := r.place(t, "transfer me to rajesh")

	if err := call.Greet(context.Background()); err == nil {
		t.Fatal("Greet reported success with a broken generator")
	}
	assertAbandoned(t, call)
}

// TestT7_AGreetingThatCannotBeSynthesisedIsAbandoned.
func TestT7_AGreetingThatCannotBeSynthesisedIsAbandoned(t *testing.T) {
	r := newT7Rig(t, t7Opts{brokenTTS: true})
	call := r.place(t, "transfer me to rajesh")

	if err := call.Greet(context.Background()); err == nil {
		t.Fatal("Greet reported success with a broken voice")
	}
	assertAbandoned(t, call)
}

// TestT7_AGreetingGovernanceRefusesIsAbandoned.
func TestT7_AGreetingGovernanceRefusesIsAbandoned(t *testing.T) {
	r := newT7Rig(t, t7Opts{denyEverything: true})
	call := r.place(t, "transfer me to rajesh")

	if err := call.Greet(context.Background()); err == nil {
		t.Fatal("Greet reported success for a refused greeting")
	}
	if got := r.model.Calls(); got != 0 {
		t.Errorf("generation ran %d times after a refusal", got)
	}
	assertAbandoned(t, call)
}

// assertAbandoned is the contract every accepted-but-unspoken response shares.
//
// The floor is what matters. INV-CV-2 makes the opening turn non-yielding, so a
// greeting that strands the floor on the agent cannot be recovered by the
// caller talking — they are queued behind an announcement that will never play.
func assertAbandoned(t *testing.T, call *Call) {
	t.Helper()

	conv := call.Conversation()
	if got := abandonments(conv); got != 1 {
		t.Errorf("%d abandonments, want exactly 1", got)
	}
	if got := greetings(conv) + completions(conv); got != 0 {
		t.Errorf("%d completions for a response that was never spoken, want 0", got)
	}
	if got := conv.State(); got != conversation.StateListening {
		t.Errorf("state after abandonment = %v, want listening", got)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after abandonment = %v, want caller", got)
	}

	// THE NEXT CALLER TURN IS NOT SWALLOWED. Asked of the conversation
	// directly: whether the floor genuinely came back is a dialogue question.
	plan, err := conv.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("the caller's utterance after an abandoned response failed: %v", err)
	}
	if plan.Reason == "floor_queued" {
		t.Errorf("the caller is queued behind the abandoned response "+
			"(action=%v reason=%q)", plan.Action, plan.Reason)
	}
	if call.Pipeline().Err() == nil && conv.State() == conversation.StateEnded {
		t.Error("the call ended on a recoverable failure")
	}
}

// ---------------------------------------------------------------------------
// 6. Cancellation
// ---------------------------------------------------------------------------

// TestT7_CancellingTheGreetingProducesNoLifecycleEvent.
//
// The cancellation lands while the first frame is mid-delivery, held there by
// the sink handshake the suite already has. No sleep, and no guess about how
// wide the window is.
func TestT7_CancellingTheGreetingProducesNoLifecycleEvent(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	p := r.ports.of(t, call)

	p.sink.atDeliver = make(chan struct{}, 1)
	p.sink.hold = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- call.Greet(ctx) }()

	select {
	case <-p.sink.atDeliver:
	case err := <-errc:
		t.Fatalf("the greeting finished before any audio was delivered: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no greeting audio reached the sink")
	}

	cancel()
	close(p.sink.hold)

	select {
	case err := <-errc:
		if err == nil {
			t.Error("Greet reported success for a cancelled greeting")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Greet did not return after cancellation")
	}

	conv := call.Conversation()
	if got := greetings(conv) + completions(conv); got != 0 {
		t.Errorf("%d completions after cancellation, want 0", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments after cancellation, want 0: the call was "+
			"cancelled, not abandoned mid-response", got)
	}
}

// TestT7_EndingACallStopsItAndIsIdempotent.
func TestT7_EndingACallStopsItAndIsIdempotent(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t, "transfer me to rajesh")
	r.greet(t, call)

	if err := call.End(voice.ReasonCallEnded); err != nil {
		t.Fatalf("End: %v", err)
	}
	select {
	case <-call.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the pipeline did not finish after End")
	}

	// Repeated termination is safe, and the call cannot continue.
	for i := 0; i < 3; i++ {
		if err := call.End(voice.ReasonCallEnded); err != nil {
			t.Errorf("End #%d: %v", i+2, err)
		}
	}
	if err := call.Audio(t2Frame(99)); err == nil {
		t.Error("an ended call accepted another frame")
	}
	if err := call.Greet(context.Background()); err == nil {
		t.Error("an ended call was greeted")
	}
	if got := call.Conversation().State(); got != conversation.StateEnded {
		t.Errorf("conversation state after End = %v, want ended", got)
	}
}

// TestT7_CancellingOneCallLeavesAnotherHealthy.
func TestT7_CancellingOneCallLeavesAnotherHealthy(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	a := r.place(t, "transfer me to rajesh")
	b := r.place(t, "transfer me to rajesh")
	r.greet(t, a)
	r.greet(t, b)

	if err := a.End(voice.ReasonCallEnded); err != nil {
		t.Fatalf("End(A): %v", err)
	}
	awaitCallEnd(t, a, "call A")

	// B is untouched.
	r.speak(t, b, 0)
	if got := completions(b.Conversation()); got != 1 {
		t.Errorf("call B recorded %d completions after A ended, want 1", got)
	}
	if got := a.Conversation().State(); got != conversation.StateEnded {
		t.Errorf("call A is %v after End, want ended", got)
	}
	if got := b.Conversation().State(); got == conversation.StateEnded {
		t.Error("call B ended when call A did")
	}
}

// ---------------------------------------------------------------------------
// 7. Isolation
// ---------------------------------------------------------------------------

// TestT7_TwoConcurrentCallsDoNotLeakIntoEachOther.
func TestT7_TwoConcurrentCallsDoNotLeakIntoEachOther(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	a := r.place(t, "call me back on 9876543210")
	b := r.place(t, "transfer me to rajesh")

	var wg sync.WaitGroup
	for _, c := range []*Call{a, b} {
		wg.Add(1)
		go func(c *Call) {
			defer wg.Done()
			if err := c.Greet(context.Background()); err != nil {
				t.Errorf("Greet(%s): %v", c.ID(), err)
			}
		}(c)
	}
	wg.Wait()

	for _, c := range []*Call{a, b} {
		r.ports.of(t, c).obs.awaitTurn(t, 30*time.Second, "greeting")
	}
	r.speak(t, a, 0)
	r.speak(t, b, 0)

	if a.ID() == b.ID() {
		t.Fatal("both calls have the same identifier")
	}

	wantA, wantB := "request_callback", "request_transfer"
	gotA, _ := a.Conversation().Context().Get(conversation.ScopeConversation, "last_intent")
	gotB, _ := b.Conversation().Context().Get(conversation.ScopeConversation, "last_intent")
	if gotA.Value != wantA {
		t.Errorf("call A last_intent = %v, want %q", gotA.Value, wantA)
	}
	if gotB.Value != wantB {
		t.Errorf("call B last_intent = %v, want %q", gotB.Value, wantB)
	}

	// Audio did not cross either.
	pa, pb := r.ports.of(t, a), r.ports.of(t, b)
	if fa, _ := pa.sink.stats(); fa == 0 {
		t.Error("call A received no audio")
	}
	if fb, _ := pb.sink.stats(); fb == 0 {
		t.Error("call B received no audio")
	}
	if pa.sink == pb.sink {
		t.Error("the two calls share one media sink")
	}
}

// ---------------------------------------------------------------------------
// 8. Sequential calls
// ---------------------------------------------------------------------------

// TestT7_ASecondCallInheritsNothingFromTheFirst.
func TestT7_ASecondCallInheritsNothingFromTheFirst(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	first := r.place(t, "call me back on 9876543210")
	r.greet(t, first)
	r.speak(t, first, 0)
	if err := first.End(voice.ReasonCallEnded); err != nil {
		t.Fatalf("End(first): %v", err)
	}
	awaitCallEnd(t, first, "the first call")

	second := r.place(t, "transfer me to rajesh")
	if second.ID() == first.ID() {
		t.Fatal("the second call reused the first call's identifier")
	}

	conv := second.Conversation()
	if got := conv.State(); got != conversation.StateIdle {
		t.Errorf("a fresh call starts in %v, want idle", got)
	}
	if _, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent"); ok {
		t.Error("the second call inherited last_intent from the first")
	}
	if got := len(conv.Trace()); got != 0 {
		t.Errorf("a fresh call already has %d transitions in its trace", got)
	}
	if got := len(conv.Interruptions().History()); got != 0 {
		t.Errorf("a fresh call already has %d interruptions", got)
	}
	if got := completions(conv) + greetings(conv); got != 0 {
		t.Errorf("a fresh call already has %d completions", got)
	}

	r.greet(t, second)
	r.speak(t, second, 0)
	last, _ := conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if last.Value != "request_transfer" {
		t.Errorf("second call last_intent = %v, want request_transfer", last.Value)
	}
}

// ---------------------------------------------------------------------------
// 9. Invalid lifecycle
// ---------------------------------------------------------------------------

// TestT7_InvalidLifecycleSequencesAreRefused.
func TestT7_InvalidLifecycleSequencesAreRefused(t *testing.T) {
	t.Run("duplicate greeting", func(t *testing.T) {
		r := newT7Rig(t, t7Opts{})
		call := r.place(t)
		r.greet(t, call)

		if err := call.Greet(context.Background()); err == nil {
			t.Error("the opening turn was taken twice")
		}
		if got := greetings(call.Conversation()); got != 1 {
			t.Errorf("%d greeting completions after a duplicate Greet, want 1", got)
		}
	})

	t.Run("utterance before the greeting completes", func(t *testing.T) {
		r := newT7Rig(t, t7Opts{})
		call := r.place(t, "transfer me to rajesh")

		// EventStart, but the greeting is never spoken — the Phase 16 T4
		// "naive driver". The frozen semantics are what they are and are NOT
		// weakened here: the caller is queued behind the opening announcement.
		if _, err := call.Conversation().Handle(
			conversation.Event{Kind: conversation.EventStart}); err != nil {
			t.Fatalf("EventStart: %v", err)
		}
		plan, err := call.Conversation().Handle(utter("transfer me to rajesh"))
		if err == nil && plan.Reason == "intent_accepted" {
			t.Fatal("an utterance was classified normally while the opening " +
				"announcement was still owed; the frozen turn semantics have " +
				"changed and this test must be updated")
		}
		t.Logf("CONFIRMED: with the greeting owed, an utterance yields "+
			"action=%v reason=%q", plan.Action, plan.Reason)
	})

	t.Run("after termination", func(t *testing.T) {
		r := newT7Rig(t, t7Opts{})
		call := r.place(t)
		if err := call.End(voice.ReasonCallEnded); err != nil {
			t.Fatalf("End: %v", err)
		}
		if err := call.Audio(t2Frame(1)); !errors.Is(err, voice.ErrSessionClosed) {
			t.Errorf("audio after termination returned %v, want ErrSessionClosed", err)
		}
	})

	t.Run("place after shutdown", func(t *testing.T) {
		r := newT7Rig(t, t7Opts{})
		r.stop(t)

		if _, err := r.driver.Place(context.Background()); !errors.Is(err, ErrDriverNotRunning) {
			t.Errorf("Place after shutdown returned %v, want ErrDriverNotRunning", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 10. Service lifecycle
// ---------------------------------------------------------------------------

// TestT7_TheDriverIsOwnedByTheServiceLifecycle.
//
// Start → runner ready → a call runs → cancel → shutdown, through the real
// platform.Service. No second supervisor: the driver's Run is called by the
// service and returns when the service's context is cancelled.
func TestT7_TheDriverIsOwnedByTheServiceLifecycle(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	if !r.logs.has("call driver ready") {
		t.Error("the driver never reported ready through the service logger")
	}
	// TWO runners registered, which is what "owned by the service lifecycle"
	// means as distinct from a goroutine this file started.
	if n := runnerCount(t, r.logs); n != 2 {
		t.Errorf("platform reports %d runners, want 2 (intelligence + driver)", n)
	}

	call := r.place(t, "transfer me to rajesh")
	r.greet(t, call)
	r.speak(t, call, 0)

	// Shutdown reaches the call without the driver holding a list of calls: the
	// pipeline is bound to the driver's run context.
	r.stop(t)

	select {
	case <-call.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the live call's pipeline survived service shutdown")
	}
	if !r.logs.has("call driver stopped") {
		t.Error("the driver did not report shutdown")
	}

	// Read AFTER shutdown, when every runner has certainly logged: platform
	// starts them concurrently, so the record set is only complete at the end.
	var started []any
	for _, rec := range r.logs.find("runner starting") {
		started = append(started, rec["runner"])
	}
	if len(started) != 2 {
		t.Errorf("platform started %v, want the intelligence runner and the driver",
			started)
	}
	if !r.logs.has("shutdown complete") {
		t.Error("the platform did not report shutdown complete")
	}

	// Idempotent.
	if err := r.driver.Shutdown(context.Background()); err != nil {
		t.Errorf("repeated Shutdown: %v", err)
	}
}

// TestT7_ProductionRunRegistersNoDriverWithoutProviders states the boundary as
// an assertion rather than a comment.
//
// A call needs a recogniser, a voice, an analyser and a generator. Every shipped
// provider shells out to an external binary or a model, and this phase remains
// no-model/no-network, so the production build supplies none and registers only
// the intelligence runner. The driver exists and is exercised; it is not
// reachable in the shipped binary, and saying otherwise would be a claim of
// telephony support.
func TestT7_ProductionRunRegistersNoDriverWithoutProviders(t *testing.T) {
	t.Parallel()

	sink, _, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// platform logs the count of registered runners as it becomes ready; it
	// keeps the slice private, so this is the observable.
	if n := runnerCount(t, sink); n != 1 {
		t.Errorf("the production build registered %d runners, want 1 "+
			"(voice-intelligence only)", n)
	}
	for _, rec := range sink.find("runner starting") {
		if rec["runner"] == "voice-call-driver" {
			t.Error("the production build started a call driver; it has no " +
				"providers to place a call with")
		}
	}
	t.Log("REAL INBOUND TRANSPORT IS NOT PRESENT. The call driver orchestrates " +
		"existing components; it is registered only where audio-bearing " +
		"providers are supplied, and the production build supplies none.")
}
