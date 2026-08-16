package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// Isolation under genuinely shared state
// ---------------------------------------------------------------------------
//
// # Why these tests do not reuse the existing harness
//
// Every harness elsewhere in this package builds a session its OWN registry,
// its own provider instances and its own metrics. Concurrency tests written on
// top of that prove isolation between separate object graphs, which is the easy
// case and not the deployed one.
//
// A real process has ONE router, ONE registered whisper, ONE registered voice
// and ONE metric set, serving every call at once. That is where cross-session
// leakage would actually live: a provider that remembers the last session, a
// generation counter shared by accident, a queue reused between turns. So every
// session below shares all of that and keeps only what a session genuinely
// owns — its pipeline, its FSM, its sink, its gateway.
//
// # Identity is carried in the data, not asserted about pointers
//
// Each session's transcript embeds its own session id; the generator echoes the
// transcript, so the response carries it too; the synthesiser stamps each frame
// with the session that asked for it. A frame arriving at the wrong sink is
// therefore visible AS AUDIO, and a leaked transcript is visible as text —
// externally observable, rather than a private field being inspected.

// sharedSTT is one recogniser serving every session at once.
type sharedSTT struct {
	id speech.ProviderID

	opened  atomic.Int64
	written atomic.Int64

	// failFor makes recognition fail for particular sessions only, which is how
	// the adversarial scenario mixes outcomes through a SHARED provider.
	mu      sync.Mutex
	failFor map[SessionID]string
}

func newSharedSTT() *sharedSTT {
	return &sharedSTT{id: speech.ProviderID("stt-shared"), failFor: map[SessionID]string{}}
}

func (s *sharedSTT) ID() speech.ProviderID { return s.id }
func (s *sharedSTT) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: true, PartialResults: true,
		Languages:   []speech.Language{langEN},
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}

func (s *sharedSTT) fault(session SessionID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failFor[session]
}

func (s *sharedSTT) OpenSTT(_ context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	s.opened.Add(1)

	session := SessionID(cfg.Session)
	if s.fault(session) == "open" {
		return nil, fmt.Errorf("%w: recogniser refused %s",
			speech.ErrProviderUnavailable, session)
	}

	st := &sharedSTTStream{
		provider: s, cfg: cfg, fault: s.fault(session),
		results: make(chan speech.TranscriptSegment, 4),
		done:    make(chan struct{}),
	}
	go st.emit()
	return st, nil
}

type sharedSTTStream struct {
	provider *sharedSTT
	cfg      speech.STTConfig
	fault    string
	results  chan speech.TranscriptSegment
	done     chan struct{}
	once     sync.Once
	closed   atomic.Bool
}

// transcriptFor is the caller's words, carrying the session that said them.
//
// Every session hears something different, so a transcript arriving in the
// wrong pipeline is visible as the wrong words rather than needing a pointer
// comparison.
func transcriptFor(session speech.SessionID) string {
	return fmt.Sprintf("caller of %s asks a question", session)
}

func (s *sharedSTTStream) emit() {
	defer close(s.results)

	if s.fault == "crash" {
		select {
		case s.results <- speech.TranscriptSegment{
			Session: s.cfg.Session, Turn: s.cfg.Turn,
			Segment: speech.SegmentID("seg-partial"),
			Text:    transcriptFor(s.cfg.Session),
		}:
		case <-s.done:
		}
		return // no final: the utterance was lost
	}

	segments := []speech.TranscriptSegment{
		{Segment: speech.SegmentID("seg-1"), Text: transcriptFor(s.cfg.Session)},
		{Segment: speech.SegmentID("seg-2"), Text: transcriptFor(s.cfg.Session), IsFinal: true},
	}
	for _, seg := range segments {
		seg.Session, seg.Turn = s.cfg.Session, s.cfg.Turn
		select {
		case s.results <- seg:
		case <-s.done:
			return
		}
	}
}

func (s *sharedSTTStream) Write(media.Frame) error {
	if s.closed.Load() {
		return speech.ErrSpeechSessionClosed
	}
	s.provider.written.Add(1)
	return nil
}
func (s *sharedSTTStream) Results() <-chan speech.TranscriptSegment { return s.results }
func (s *sharedSTTStream) CloseSend() error                         { return nil }
func (s *sharedSTTStream) Close() error {
	s.closed.Store(true)
	s.once.Do(func() { close(s.done) })
	return nil
}

// sharedTTS is one voice serving every session at once.
//
// It stamps every frame with the session that asked for it, which is what makes
// "audio from A reached B" an observable fact at B's media sink.
type sharedTTS struct {
	id speech.ProviderID

	// Speech is PACED rather than instant. A voice that emits its whole
	// response in a microsecond is never holding the floor when a detection
	// arrives, so a barge-in can never be delivered and the interruption tests
	// silently test nothing. This is the shape of a real synthesiser, not a
	// sleep hiding a race: the frames are produced on a schedule because that
	// is what speaking is.
	frameDelay     time.Duration
	framesPerChunk int

	mu       sync.Mutex
	marks    map[SessionID]uint16
	nextMark uint16
	spoken   map[SessionID][]string
	failFor  map[SessionID]string
}

func newSharedTTS() *sharedTTS {
	return &sharedTTS{
		id:             speech.ProviderID("tts-shared"),
		frameDelay:     8 * time.Millisecond,
		framesPerChunk: 12,
		marks:          map[SessionID]uint16{},
		spoken:         map[SessionID][]string{},
		failFor:        map[SessionID]string{},
	}
}

func (r *sharedTTS) ID() speech.ProviderID { return r.id }
func (r *sharedTTS) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: true,
		Languages: []speech.Language{langEN},
		// 8kHz is declared too so a capability mismatch cannot be the reason a
		// session fails; the faults here are deliberate, not incidental.
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}

// mark returns the stable per-session stamp, assigning one on first use.
func (r *sharedTTS) mark(session SessionID) uint16 {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m, ok := r.marks[session]; ok {
		return m
	}
	r.nextMark++
	r.marks[session] = r.nextMark
	return r.nextMark
}

func (r *sharedTTS) markOf(session SessionID) (uint16, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.marks[session]
	return m, ok
}

func (r *sharedTTS) recordSpoken(session SessionID, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spoken[session] = append(r.spoken[session], text)
}

func (r *sharedTTS) spokenFor(session SessionID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.spoken[session]...)
}

func (r *sharedTTS) OpenTTS(_ context.Context, cfg speech.TTSConfig) (speech.TTSStream, error) {
	session := SessionID(cfg.Session)

	r.mu.Lock()
	fault := r.failFor[session]
	r.mu.Unlock()

	if fault == "open" {
		return nil, fmt.Errorf("%w: voice refused %s",
			speech.ErrProviderUnavailable, session)
	}

	return &sharedTTSStream{
		provider: r, session: session, mark: r.mark(session), fault: fault,
		format: cfg.Format,
		audio:  make(chan media.Frame, 64),
		done:   make(chan struct{}),
	}, nil
}

type sharedTTSStream struct {
	provider *sharedTTS
	session  SessionID
	mark     uint16
	fault    string
	format   media.AudioFormat

	audio chan media.Frame
	done  chan struct{}

	seq       atomic.Uint64
	once      sync.Once
	closed    atomic.Bool
	mu        sync.Mutex
	inflight  int
	sendDone  bool
	audioOnce sync.Once
}

func (s *sharedTTSStream) Synthesize(c speech.Chunk) error {
	if s.fault == "synth" {
		return fmt.Errorf("%w: voice refused the chunk", speech.ErrInvalidAudio)
	}

	s.mu.Lock()
	if s.closed.Load() || s.sendDone {
		s.mu.Unlock()
		return speech.ErrSpeechSessionClosed
	}
	s.inflight++
	s.mu.Unlock()

	s.provider.recordSpoken(s.session, c.Text)

	go func() {
		defer s.finished()

		if s.fault == "crash" {
			return
		}
		for i := 0; i < s.provider.framesPerChunk; i++ {
			if s.provider.frameDelay > 0 {
				select {
				case <-time.After(s.provider.frameDelay):
				case <-s.done:
					return
				}
			}
			payload := make([]byte, s.format.BytesFor(20*time.Millisecond))
			if len(payload) >= 2 {
				// THE SESSION'S OWN STAMP, in the audio itself.
				binary.LittleEndian.PutUint16(payload, s.mark)
			}
			select {
			case s.audio <- media.Frame{
				Sequence: s.seq.Add(1) - 1, Format: s.format, Payload: payload,
			}:
			case <-s.done:
				return
			}
		}
	}()
	return nil
}

func (s *sharedTTSStream) finished() {
	s.mu.Lock()
	s.inflight--
	last := s.sendDone && s.inflight == 0
	s.mu.Unlock()
	if last {
		s.audioOnce.Do(func() { close(s.audio) })
	}
}

func (s *sharedTTSStream) Audio() <-chan media.Frame { return s.audio }

func (s *sharedTTSStream) CloseSend() error {
	s.mu.Lock()
	s.sendDone = true
	last := s.inflight == 0
	s.mu.Unlock()
	if last {
		s.audioOnce.Do(func() { close(s.audio) })
	}
	return nil
}

func (s *sharedTTSStream) Close() error {
	s.closed.Store(true)
	s.once.Do(func() { close(s.done) })

	s.mu.Lock()
	s.sendDone = true
	last := s.inflight == 0
	s.mu.Unlock()
	if last {
		s.audioOnce.Do(func() { close(s.audio) })
	}
	return nil
}

// echoGenerator answers with the transcript it was given.
//
// The response therefore carries the asking session's identity, so a leak
// anywhere between recognition and synthesis shows up as the wrong words being
// spoken to the wrong caller.
type echoGenerator struct {
	mu      sync.Mutex
	failFor map[SessionID]bool
	started atomic.Int64
}

func newEchoGenerator() *echoGenerator {
	return &echoGenerator{failFor: map[SessionID]bool{}}
}

func (g *echoGenerator) Generate(
	ctx context.Context, spec rt.GenerateSpec, sinks ...rt.Sink,
) (*rt.Dispatcher, error) {
	g.mu.Lock()
	fail := g.failFor[SessionID(spec.SessionID)]
	g.mu.Unlock()

	if fail {
		return nil, fmt.Errorf("%w: no model for %s",
			rt.ErrProviderUnavailable, spec.SessionID)
	}
	g.started.Add(1)

	said := ""
	if len(spec.Messages) > 0 {
		said = spec.Messages[len(spec.Messages)-1].Content
	}
	// Question marks terminate a clause immediately, so the chunker emits
	// without waiting for a following token — see speech.Chunker's period rule.
	tokens := []string{
		fmt.Sprintf("You said %s?", said),
		fmt.Sprintf(" Confirming for %s?", spec.SessionID),
	}

	cfg := rt.DefaultDispatcherConfig()
	cfg.SinkWriteTimeout = 5 * time.Second
	cfg.MaxChunkGap = 0

	d, err := rt.NewDispatcher(cfg, nil, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range sinks {
		if err := d.AddSink(s); err != nil {
			return nil, err
		}
	}

	stream := &scriptedTokenStream{gen: &scriptedGenerator{}, tokens: tokens}
	go func() { d.Run(ctx, stream) }()
	return d, nil
}

// ---------------------------------------------------------------------------
// A fleet of sessions over one shared provider set
// ---------------------------------------------------------------------------

// fleet is the shared state every session in a test draws on.
type fleet struct {
	registry *ProviderRegistry
	stt      *sharedSTT
	tts      *sharedTTS
	gen      *echoGenerator
	metrics  *VoiceMetrics
}

func newFleet(t *testing.T) *fleet {
	t.Helper()

	f := &fleet{
		stt: newSharedSTT(), tts: newSharedTTS(),
		gen: newEchoGenerator(), metrics: NewVoiceMetrics(),
	}

	// ONE registry, and therefore one speech.ProviderRouter, for every session.
	f.registry = newRegistry(t, ModeDevelopment)

	if err := f.registry.RegisterSTT(f.stt, sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	ttsSpecLocal := sttSpec()
	ttsSpecLocal.Engine = "piper"
	ttsSpecLocal.Model = ModelIdentity{Model: ModelID("test-voice")}
	if err := f.registry.RegisterTTS(f.tts, ttsSpecLocal); err != nil {
		t.Fatalf("RegisterTTS: %v", err)
	}
	return f
}

// session is one call, owning only what a call genuinely owns.
type session struct {
	id       SessionID
	pipeline *Pipeline
	fsm      *SessionFSM
	sink     *countingSink
	obs      *recordingObserver
	pub      *RecordingEventPublisher
	gateway  *ToolGateway
	invoker  *recordingInvoker
	intel    *bargeIntel
}

type sessionOpts struct {
	sttFault   string // "open" | "crash"
	ttsFault   string // "open" | "synth" | "crash"
	llmFails   bool
	denyTools  bool
	bargeAt    int
	turnTimout time.Duration
}

func (f *fleet) open(t *testing.T, n int, o sessionOpts) *session {
	t.Helper()

	id := SessionID(fmt.Sprintf("ses-%02d", n))

	if o.sttFault != "" {
		f.stt.mu.Lock()
		f.stt.failFor[id] = o.sttFault
		f.stt.mu.Unlock()
	}
	if o.ttsFault != "" {
		f.tts.mu.Lock()
		f.tts.failFor[id] = o.ttsFault
		f.tts.mu.Unlock()
	}
	if o.llmFails {
		f.gen.mu.Lock()
		f.gen.failFor[id] = true
		f.gen.mu.Unlock()
	}

	pub := NewRecordingEventPublisher()
	fsm, err := NewSessionFSM(FSMConfig{
		Session: id, Call: CallID(fmt.Sprintf("call-%02d", n)),
		Metrics: f.metrics, Publisher: pub,
	})
	if err != nil {
		t.Fatalf("NewSessionFSM(%s): %v", id, err)
	}

	outcome := governance.OutcomeAllow
	if o.denyTools {
		outcome = governance.OutcomeDeny
	}
	invoker := &recordingInvoker{}
	gateway, err := NewToolGateway(GatewayConfig{
		Governor: &recordingGovernor{answer: scriptedDecision{outcome: outcome}},
		Invoker:  invoker, Session: id,
		Actor:   governance.ActorID("voice-agent"),
		Metrics: f.metrics,
	})
	if err != nil {
		t.Fatalf("NewToolGateway(%s): %v", id, err)
	}

	turnTimeout := o.turnTimout
	if turnTimeout == 0 {
		turnTimeout = 15 * time.Second
	}

	s := &session{
		id: id, fsm: fsm, sink: &countingSink{}, obs: newObserver(),
		pub: pub, gateway: gateway, invoker: invoker,
		intel: &bargeIntel{onsetAt: 1, endAt: 3, bargeAt: o.bargeAt},
	}

	p, err := NewPipeline(PipelineConfig{
		Session: id, Call: CallID(fmt.Sprintf("call-%02d", n)),
		Language: langEN, Format: media.PCM16Mono16k(),
		Registry:         f.registry, // SHARED
		Intel:            s.intel,
		Planner:          &scriptedPlanner{action: 0},
		Governor:         &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}},
		Generator:        f.gen, // SHARED
		Output:           s.sink,
		Tools:            s.gateway,
		FSM:              fsm,
		Metrics:          f.metrics, // SHARED
		Publisher:        pub,
		Observer:         s.obs,
		MaxPendingFrames: 128, MaxPendingSegments: 16, MaxPendingAudio: 64,
		TurnTimeout: turnTimeout,
		Tier:        rt.TierFast,
		Actor:       governance.ActorID("voice-agent"),
	})
	if err != nil {
		t.Fatalf("NewPipeline(%s): %v", id, err)
	}
	s.pipeline = p
	return s
}

// feed writes n frames into the session.
func (s *session) feed(n int) {
	for i := 0; i < n; i++ {
		_ = s.pipeline.WriteFrame(testFrame(uint64(i)))
	}
}

// framesFrom returns how many delivered frames carry a given session's stamp.
func (s *session) framesFrom(mark uint16) int {
	s.sink.mu.Lock()
	defer s.sink.mu.Unlock()

	n := 0
	for _, f := range s.sink.frames {
		if len(f.Payload) >= 2 && binary.LittleEndian.Uint16(f.Payload) == mark {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Isolation
// ---------------------------------------------------------------------------

func TestConcurrency_SessionsSharingProvidersStayIsolated(t *testing.T) {
	t.Parallel()

	// Twelve calls, one router, one recogniser, one voice, one metric set.
	const sessions = 12

	f := newFleet(t)
	all := make([]*session, sessions)

	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		s := f.open(t, i, sessionOpts{})
		all[i] = s

		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}

		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			s.feed(4)
			select {
			case <-s.obs.turnDone:
			case <-time.After(40 * time.Second):
				t.Errorf("%s never completed a turn", s.id)
			}
		}(s)
	}
	wg.Wait()

	for _, s := range all {
		_ = s.pipeline.Disconnect()
		<-s.pipeline.Done()
	}

	// --- audio: every frame a session heard was synthesised FOR it ----------
	for _, s := range all {
		mine, ok := f.tts.markOf(s.id)
		if !ok {
			t.Errorf("%s never opened a voice", s.id)
			continue
		}
		if got := s.framesFrom(mine); got == 0 {
			t.Errorf("%s received none of its own audio", s.id)
		}
		if total, own := s.sink.count(), s.framesFrom(mine); total != own {
			t.Errorf("%s received %d frames of which only %d were its own: "+
				"audio from another call reached this caller", s.id, total, own)
		}
	}

	// --- transcripts: nobody heard anybody else's words ---------------------
	for _, s := range all {
		s.obs.mu.Lock()
		segs := append([]speech.TranscriptSegment(nil), s.obs.transcript...)
		s.obs.mu.Unlock()

		if len(segs) == 0 {
			t.Errorf("%s received no transcript", s.id)
		}
		want := transcriptFor(speech.SessionID(s.id))
		for _, seg := range segs {
			if seg.Text != want {
				t.Errorf("%s received the transcript %q, which belongs to another "+
					"call", s.id, seg.Text)
			}
			if seg.Session != speech.SessionID(s.id) {
				t.Errorf("%s received a segment stamped %q", s.id, seg.Session)
			}
		}
	}

	// --- responses: the voice was asked to say the right thing per call -----
	for _, s := range all {
		said := f.tts.spokenFor(s.id)
		if len(said) == 0 {
			t.Errorf("%s synthesised nothing", s.id)
		}
		for _, text := range said {
			if !strings.Contains(text, string(s.id)) {
				t.Errorf("%s was given %q to say, which names another call",
					s.id, text)
			}
		}
	}

	// --- generation counters are per session --------------------------------
	for _, s := range all {
		if got := s.pipeline.Generation(); got != 1 {
			// One increment, from its own Disconnect. Anything else means a
			// neighbour's interruption moved this session's counter.
			t.Errorf("%s has generation %d, want 1", s.id, got)
		}
	}

	// --- FSM histories are per session --------------------------------------
	for _, s := range all {
		history := s.fsm.History()
		if len(history) == 0 {
			t.Errorf("%s has no state history", s.id)
		}
		for _, c := range history {
			if !CanTransition(c.From, c.To) {
				t.Errorf("%s: undeclared transition %s -> %s", s.id, c.From, c.To)
			}
		}
		for _, e := range s.pub.Events() {
			if e.Session != s.id {
				t.Errorf("%s's publisher received an event for %s", s.id, e.Session)
			}
		}
	}

	// --- the shared provider served everybody -------------------------------
	if got := f.stt.opened.Load(); int(got) < sessions {
		t.Errorf("the shared recogniser opened %d streams for %d sessions",
			got, sessions)
	}
}

func TestConcurrency_OneSessionCannotCancelAnother(t *testing.T) {
	t.Parallel()

	f := newFleet(t)
	victim := f.open(t, 1, sessionOpts{})
	bystander := f.open(t, 2, sessionOpts{})

	for _, s := range []*session{victim, bystander} {
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}
	}
	defer func() { _ = bystander.pipeline.Disconnect() }()

	victim.feed(4)
	bystander.feed(4)

	// End one call outright.
	if err := victim.pipeline.Cancel(ReasonRequested); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	<-victim.pipeline.Done()

	if got := victim.fsm.State(); got != StateCancelled {
		t.Errorf("the cancelled session is in %s", got)
	}

	// The other call must be completely unaffected: still live, still able to
	// finish a turn, still able to accept audio.
	if bystander.fsm.Terminal() {
		t.Fatalf("cancelling %s ended %s", victim.id, bystander.id)
	}
	select {
	case <-bystander.obs.turnDone:
	case <-time.After(40 * time.Second):
		t.Fatalf("%s could not finish its turn after %s was cancelled",
			bystander.id, victim.id)
	}

	if err := bystander.pipeline.WriteFrame(testFrame(99)); err != nil &&
		!errors.Is(err, ErrBackpressure) {
		t.Errorf("%s stopped accepting audio after a neighbour was cancelled: %v",
			bystander.id, err)
	}
	if bystander.sink.count() == 0 {
		t.Errorf("%s produced no audio", bystander.id)
	}
	if err := bystander.pipeline.Err(); err != nil {
		t.Errorf("%s recorded a fault from a neighbour's cancellation: %v",
			bystander.id, err)
	}
}

func TestConcurrency_BargeInDoesNotCrossSessions(t *testing.T) {
	t.Parallel()

	f := newFleet(t)
	interrupted := f.open(t, 1, sessionOpts{})
	quiet := f.open(t, 2, sessionOpts{})

	for _, s := range []*session{interrupted, quiet} {
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}
		defer func(s *session) { _ = s.pipeline.Disconnect() }(s)
	}

	// Get both speaking.
	deadline := time.After(40 * time.Second)
	for interrupted.sink.count() == 0 || quiet.sink.count() == 0 {
		interrupted.feed(1)
		quiet.feed(1)
		select {
		case <-deadline:
			t.Fatal("the sessions never spoke")
		case <-time.After(3 * time.Millisecond):
		}
	}

	quietBefore := quiet.sink.count()
	quietGeneration := quiet.pipeline.Generation()

	// Interrupt exactly one of them.
	interrupted.intel.mu.Lock()
	interrupted.intel.bargeAt = interrupted.intel.frames + 1
	interrupted.intel.mu.Unlock()

	for i := 0; i < 200 && interrupted.pipeline.BargeIns() == 0; i++ {
		interrupted.feed(1)
		time.Sleep(2 * time.Millisecond)
	}
	if interrupted.pipeline.BargeIns() == 0 {
		t.Fatal("the barge-in never landed")
	}

	// The interrupted session's generation moved; the other's did not.
	if got := interrupted.pipeline.Generation(); got == 0 {
		t.Error("the interrupted session's generation did not move")
	}
	if got := quiet.pipeline.Generation(); got != quietGeneration {
		t.Errorf("%s's generation moved from %d to %d because a NEIGHBOUR was "+
			"interrupted", quiet.id, quietGeneration, got)
	}
	if quiet.pipeline.BargeIns() != 0 {
		t.Errorf("%s recorded a barge-in it never had", quiet.id)
	}

	// And the quiet session keeps speaking, with only its own audio.
	quiet.feed(4)
	time.Sleep(300 * time.Millisecond)

	if quiet.sink.count() <= quietBefore {
		t.Errorf("%s stopped speaking after a neighbour was interrupted", quiet.id)
	}
	if mark, ok := f.tts.markOf(quiet.id); ok {
		if total, own := quiet.sink.count(), quiet.framesFrom(mark); total != own {
			t.Errorf("%s heard %d frames of which %d were its own", quiet.id, total, own)
		}
	}
	if quiet.fsm.Terminal() {
		t.Errorf("%s ended because a neighbour was interrupted", quiet.id)
	}
}

func TestConcurrency_AuthorizationDoesNotLeakBetweenSessions(t *testing.T) {
	t.Parallel()

	f := newFleet(t)
	allowed := f.open(t, 1, sessionOpts{})
	refused := f.open(t, 2, sessionOpts{denyTools: true})

	for _, s := range []*session{allowed, refused} {
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}
		defer func(s *session) { _ = s.pipeline.Disconnect() }(s)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Both ask for the same action at the same time; only one may execute.
	go func() {
		defer wg.Done()
		if _, err := allowed.pipeline.InvokeTool(context.Background(), testIntent()); err != nil {
			t.Errorf("%s was allowed but failed: %v", allowed.id, err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := refused.pipeline.InvokeTool(context.Background(), testIntent()); err == nil {
			t.Errorf("%s was denied but executed", refused.id)
		}
	}()
	wg.Wait()

	if got := allowed.invoker.calls.Load(); got != 1 {
		t.Errorf("%s executed %d times, want 1", allowed.id, got)
	}
	if got := refused.invoker.calls.Load(); got != 0 {
		t.Errorf("%s executed %d times despite a refusal: an authorisation "+
			"crossed sessions", refused.id, got)
	}

	// The granted authorisation names its own session, and nobody else's.
	req := allowed.invoker.lastRequest(t)
	if req.Session != allowed.id {
		t.Errorf("the authorisation was issued for %q, want %q", req.Session, allowed.id)
	}
	if !req.Auth.Valid() {
		t.Error("the executed request carried no valid authorisation")
	}
}

func TestConcurrency_InboundAudioIsNotShared(t *testing.T) {
	t.Parallel()

	// Frames written to one session must never be analysed by another. The
	// analyser counts what it saw, and the counts must add up per session.
	f := newFleet(t)
	busy := f.open(t, 1, sessionOpts{})
	idle := f.open(t, 2, sessionOpts{})

	for _, s := range []*session{busy, idle} {
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}
		defer func(s *session) { _ = s.pipeline.Disconnect() }(s)
	}

	const written = 30
	busy.feed(written)

	// Let the ingest goroutines drain.
	deadline := time.After(20 * time.Second)
	for busy.intel.count() < written {
		select {
		case <-deadline:
			t.Fatalf("only %d of %d frames were analysed", busy.intel.count(), written)
		case <-time.After(5 * time.Millisecond):
		}
	}

	if got := idle.intel.count(); got != 0 {
		t.Errorf("%s analysed %d frames that were written to %s", idle.id, got, busy.id)
	}
	if idle.sink.count() != 0 {
		t.Errorf("%s produced audio without ever being spoken to", idle.id)
	}
	if idle.fsm.State() != StateListening {
		t.Errorf("%s moved to %s without receiving audio", idle.id, idle.fsm.State())
	}
}

// ---------------------------------------------------------------------------
// The adversarial fleet
// ---------------------------------------------------------------------------

// outcomeKind is what a session was set up to end as.
type outcomeKind string

const (
	outcomeHealthy   outcomeKind = "healthy"
	outcomeSTTFails  outcomeKind = "stt_fails"
	outcomeTTSFails  outcomeKind = "tts_fails"
	outcomeLLMFails  outcomeKind = "llm_fails"
	outcomeCancelled outcomeKind = "cancelled"
	outcomeBargedIn  outcomeKind = "barged_in"
	outcomeDenied    outcomeKind = "tool_denied"
)

func TestConcurrency_AdversarialFleetReachesIndependentOutcomes(t *testing.T) {
	t.Parallel()

	// Every failure mode this phase knows about, running at the same time, over
	// one shared provider set. The point is not that each fails correctly —
	// Task 14 established that — but that each reaches ITS OWN outcome while
	// the others are misbehaving.
	plan := []outcomeKind{
		outcomeHealthy, outcomeSTTFails, outcomeTTSFails, outcomeLLMFails,
		outcomeCancelled, outcomeBargedIn, outcomeDenied,
		outcomeHealthy, outcomeTTSFails, outcomeHealthy,
		outcomeSTTFails, outcomeBargedIn,
	}

	f := newFleet(t)
	sessions := make([]*session, len(plan))
	kinds := make([]outcomeKind, len(plan))

	for i, kind := range plan {
		var o sessionOpts
		switch kind {
		case outcomeSTTFails:
			o.sttFault = "crash"
		case outcomeTTSFails:
			o.ttsFault = "open"
		case outcomeLLMFails:
			o.llmFails = true
		case outcomeDenied:
			o.denyTools = true
		}
		sessions[i] = f.open(t, i, o)
		kinds[i] = kind
	}

	for _, s := range sessions {
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}
	}

	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Add(1)
		go func(s *session, kind outcomeKind) {
			defer wg.Done()

			switch kind {
			case outcomeCancelled:
				s.feed(4)
				time.Sleep(20 * time.Millisecond)
				_ = s.pipeline.Cancel(ReasonRequested)
				return

			case outcomeBargedIn:
				for i := 0; i < 400 && s.sink.count() == 0; i++ {
					s.feed(1)
					time.Sleep(2 * time.Millisecond)
				}
				s.intel.mu.Lock()
				s.intel.bargeAt = s.intel.frames + 1
				s.intel.mu.Unlock()
				for i := 0; i < 400 && s.pipeline.BargeIns() == 0; i++ {
					s.feed(1)
					time.Sleep(2 * time.Millisecond)
				}
				return

			case outcomeDenied:
				s.feed(4)
				if _, err := s.pipeline.InvokeTool(context.Background(), testIntent()); err == nil {
					t.Errorf("%s executed a denied action", s.id)
				}
				<-s.obs.turnDone
				return

			default:
				s.feed(4)
				select {
				case <-s.obs.turnDone:
				case <-time.After(40 * time.Second):
					t.Errorf("%s (%s) never resolved", s.id, kind)
				}
			}
		}(s, kinds[i])
	}
	wg.Wait()

	// --- each session reached its own correct outcome -----------------------
	for i, s := range sessions {
		kind := kinds[i]

		switch kind {
		case outcomeCancelled:
			<-s.pipeline.Done()
			if got := s.fsm.State(); got != StateCancelled {
				t.Errorf("%s (%s) is in %s", s.id, kind, got)
			}

		case outcomeBargedIn:
			if s.pipeline.BargeIns() == 0 {
				t.Errorf("%s (%s) was never interrupted", s.id, kind)
			}
			if s.fsm.Terminal() {
				t.Errorf("%s (%s) ended on a barge-in", s.id, kind)
			}

		case outcomeHealthy:
			if s.sink.count() == 0 {
				t.Errorf("%s (%s) produced no audio", s.id, kind)
			}
			if err := s.pipeline.Err(); err != nil {
				t.Errorf("%s (%s) failed: %v", s.id, kind, err)
			}

		case outcomeSTTFails:
			if s.sink.count() != 0 {
				t.Errorf("%s (%s) spoke despite losing the utterance", s.id, kind)
			}
			if s.fsm.Terminal() {
				t.Errorf("%s (%s) ended the call over one lost utterance", s.id, kind)
			}

		case outcomeTTSFails, outcomeLLMFails:
			if s.sink.count() != 0 {
				t.Errorf("%s (%s) produced audio despite the failure", s.id, kind)
			}
			if err := s.pipeline.Err(); err == nil {
				t.Errorf("%s (%s) recorded no fault", s.id, kind)
			}
			if s.fsm.Terminal() {
				t.Errorf("%s (%s) ended the call over one failed turn", s.id, kind)
			}

		case outcomeDenied:
			if got := s.invoker.calls.Load(); got != 0 {
				t.Errorf("%s (%s) executed a tool %d times", s.id, kind, got)
			}
		}

		// Whatever happened, this session's own state is coherent and its own.
		if !s.fsm.State().Valid() {
			t.Errorf("%s is in an undeclared state %q", s.id, s.fsm.State())
		}
		for _, c := range s.fsm.History() {
			if !CanTransition(c.From, c.To) {
				t.Errorf("%s: undeclared transition %s -> %s", s.id, c.From, c.To)
			}
		}
		for _, e := range s.pub.Events() {
			if e.Session != s.id {
				t.Errorf("%s received an event belonging to %s", s.id, e.Session)
			}
		}
	}

	// --- no healthy session was damaged by its neighbours -------------------
	healthy := 0
	for i, s := range sessions {
		if kinds[i] != outcomeHealthy {
			continue
		}
		healthy++
		mark, ok := f.tts.markOf(s.id)
		if !ok {
			t.Errorf("%s never opened a voice", s.id)
			continue
		}
		if total, own := s.sink.count(), s.framesFrom(mark); total != own {
			t.Errorf("%s heard %d frames of which only %d were its own, while its "+
				"neighbours were failing", s.id, total, own)
		}
	}
	if healthy == 0 {
		t.Fatal("no healthy session was in the fleet; isolation was not demonstrated")
	}

	for _, s := range sessions {
		_ = s.pipeline.Disconnect()
	}

	t.Logf("adversarial fleet: %d concurrent sessions over one shared router, "+
		"recogniser, voice and metric set; %d healthy, %d deliberately broken",
		len(sessions), healthy, len(sessions)-healthy)
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// runSignature is everything about a run that must not vary.
//
// Deliberately excludes timings and frame counts: those depend on the
// scheduler, and demanding them would be asserting that Go's runtime is
// deterministic, which it is not and does not claim to be.
type runSignature struct {
	states     string
	outcome    TurnOutcome
	reason     string
	provider   speech.ProviderID
	governance string
	failure    string
	eventTypes string
}

func (r runSignature) String() string {
	return fmt.Sprintf("states=%s outcome=%s/%s provider=%s gov=%s fail=%s events=%s",
		r.states, r.outcome, r.reason, r.provider, r.governance, r.failure, r.eventTypes)
}

// captureRun executes one fixed scenario and returns its behavioural signature.
func captureRun(t *testing.T, deny bool, sttFault string) runSignature {
	t.Helper()

	f := newFleet(t)
	s := f.open(t, 1, sessionOpts{denyTools: deny, sttFault: sttFault})

	if err := s.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Provider selection, asked before anything else can disturb it.
	picked, pickErr := f.registry.PickSTT(langEN)
	provider := speech.ProviderID("none")
	if pickErr == nil {
		provider = picked.ID()
	}

	s.feed(4)
	select {
	case <-s.obs.turnDone:
	case <-time.After(40 * time.Second):
		// WHAT the session was doing matters more than that it was slow.
		//
		// A session still in `created` or `listening` never started a turn —
		// starvation of the ingest goroutine. One parked in `thinking` or
		// `speaking` started and stopped — a wedge the turn timeout should have
		// caught. Reporting only "never resolved" cannot tell those apart, and
		// they have very different production meanings.
		s.obs.mu.Lock()
		segments, chunks := len(s.obs.transcript), len(s.obs.chunks)
		s.obs.mu.Unlock()

		t.Fatalf("the run never resolved: state=%s terminal=%v transitions=%d "+
			"transcripts=%d tts_chunks=%d frames_analysed=%d audio_frames=%d "+
			"pipeline_err=%v",
			s.fsm.State(), s.fsm.Terminal(), len(s.fsm.History()),
			segments, chunks, s.intel.count(), s.sink.count(), s.pipeline.Err())
	}

	govResult := "allow"
	if _, err := s.pipeline.InvokeTool(context.Background(), testIntent()); err != nil {
		switch {
		case errors.Is(err, ErrGovernanceDenied):
			govResult = "denied"
		case errors.Is(err, ErrObligationsUnmet):
			govResult = "obligations"
		default:
			govResult = "error"
		}
	}

	_ = s.pipeline.Disconnect()
	<-s.pipeline.Done()

	// The state path, in order.
	var states []string
	for _, c := range s.fsm.History() {
		states = append(states, string(c.To))
	}

	// The event TYPES in order. Ordering within a session is contractual —
	// Sequence exists so a consumer can detect a gap — so it must not vary.
	var types []string
	for _, e := range s.pub.Events() {
		types = append(types, string(e.Type))
	}

	s.obs.mu.Lock()
	outcome, reason := s.obs.outcome, s.obs.reason
	s.obs.mu.Unlock()

	failure := "none"
	if err := s.pipeline.Err(); err != nil {
		switch {
		case errors.Is(err, ErrProviderUnavailable):
			failure = "provider_unavailable"
		case errors.Is(err, ErrProviderFailed):
			failure = "provider_failed"
		default:
			failure = "other"
		}
	}

	return runSignature{
		states:     strings.Join(states, ">"),
		outcome:    outcome,
		reason:     reason,
		provider:   provider,
		governance: govResult,
		failure:    failure,
		eventTypes: strings.Join(types, ">"),
	}
}

func TestConcurrency_BehaviourIsDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()

	// # What is asserted, and what deliberately is not
	//
	// BEHAVIOURAL determinism: the same scenario must produce the same state
	// path, the same turn outcome, the same provider choice, the same
	// governance answer, the same failure classification and the same event
	// ordering.
	//
	// NOT wall-clock determinism. Frame counts, latencies and goroutine
	// interleavings vary with the scheduler, and a test demanding those would
	// be asserting something Go does not promise — it would fail on a loaded
	// machine and get muted, taking the real assertions with it.
	scenarios := []struct {
		name     string
		deny     bool
		sttFault string
	}{
		{"healthy turn", false, ""},
		{"tool denied", true, ""},
		{"utterance lost", false, "crash"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			const replays = 8
			first := captureRun(t, sc.deny, sc.sttFault)

			for i := 1; i < replays; i++ {
				got := captureRun(t, sc.deny, sc.sttFault)
				if got != first {
					t.Fatalf("replay %d diverged:\n  first: %s\n  got:   %s",
						i, first, got)
				}
			}

			t.Logf("%s: %d replays identical\n  %s", sc.name, replays, first)
		})
	}
}

func TestConcurrency_ProviderSelectionIsDeterministicUnderLoad(t *testing.T) {
	t.Parallel()

	// Selection consults capabilities and health, in registration order within
	// a tier. Concurrent callers must therefore all get the same answer while
	// nothing has failed — a router that varied under load would make failover
	// untestable and reproduction impossible.
	f := newFleet(t)

	const askers = 32
	results := make(chan speech.ProviderID, askers)

	var wg sync.WaitGroup
	for i := 0; i < askers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.registry.PickSTT(langEN)
			if err != nil {
				results <- speech.ProviderID("error")
				return
			}
			results <- p.ID()
		}()
	}
	wg.Wait()
	close(results)

	seen := map[speech.ProviderID]int{}
	for id := range results {
		seen[id]++
	}
	if len(seen) != 1 {
		t.Errorf("%d concurrent selections produced %d different answers: %v",
			askers, len(seen), seen)
	}
	if seen[f.stt.id] != askers {
		t.Errorf("the healthy provider was chosen %d of %d times",
			seen[f.stt.id], askers)
	}
}

func TestConcurrency_SharedMetricsRemainCoherent(t *testing.T) {
	t.Parallel()

	// One metric set, many sessions. Counters are shared by design — that is
	// what makes them aggregate — so what must hold is that they ADD UP rather
	// than being isolated: a lost increment under concurrency is a metric that
	// silently under-reports, which is worse than no metric.
	f := newFleet(t)

	const sessions = 10
	all := make([]*session, sessions)

	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		s := f.open(t, i, sessionOpts{})
		all[i] = s
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start(%s): %v", s.id, err)
		}

		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			s.feed(4)
			select {
			case <-s.obs.turnDone:
			case <-time.After(40 * time.Second):
				t.Errorf("%s never resolved", s.id)
			}
		}(s)
	}
	wg.Wait()

	for _, s := range all {
		_ = s.pipeline.Disconnect()
		<-s.pipeline.Done()
	}

	// Every session started exactly one turn.
	if got := f.metrics.TurnsStarted.Total(); got != uint64(sessions) {
		t.Errorf("TurnsStarted is %d for %d sessions: increments were lost under "+
			"concurrency", got, sessions)
	}

	// Every session made the same declared transitions, so the shared counter
	// must be an exact multiple.
	if got := f.metrics.StateTransitions.Value(
		string(StateCreated), string(StateListening)); got != uint64(sessions) {
		t.Errorf("created->listening counted %d times for %d sessions", got, sessions)
	}

	if got := f.metrics.InvalidTransitions.Total(); got != 0 {
		t.Errorf("%d invalid transitions were recorded across the fleet", got)
	}

	// The label set stays bounded no matter how many sessions ran: a session
	// identifier reaching a label would make cardinality grow with traffic.
	for _, series := range f.metrics.StateTransitions.Series() {
		if strings.Contains(series, "ses-") {
			t.Errorf("a session identifier reached a metric label: %q", series)
		}
	}
}

func TestConcurrency_SessionIdentifiersNeverReachALabel(t *testing.T) {
	t.Parallel()

	// The cardinality guarantee, checked across every instrument after a fleet
	// has run. Unbounded label sets are how a metrics backend falls over, and
	// the values that would do it here are per-call identifiers.
	f := newFleet(t)

	const sessions = 6
	for i := 0; i < sessions; i++ {
		s := f.open(t, i, sessionOpts{})
		if err := s.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		s.feed(4)
		select {
		case <-s.obs.turnDone:
		case <-time.After(40 * time.Second):
			t.Fatalf("%s never resolved", s.id)
		}
		_ = s.pipeline.Disconnect()
		<-s.pipeline.Done()
	}

	var offending []string
	for _, sample := range f.metrics.Snapshot() {
		for _, v := range sample.Labels {
			if strings.HasPrefix(v, "ses-") || strings.HasPrefix(v, "call-") ||
				strings.Contains(v, "caller of") {
				offending = append(offending, fmt.Sprintf("%s{%v}", sample.Name, sample.Labels))
			}
		}
	}
	sort.Strings(offending)
	if len(offending) > 0 {
		t.Errorf("per-call values reached metric labels:\n  %s",
			strings.Join(offending, "\n  "))
	}
}
