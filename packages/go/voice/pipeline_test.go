package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	conversation "github.com/callscreen/callscreen-platform/packages/go/conversation"
	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------
//
// These stand in for the frozen engines. They are NOT here to make assertions
// easy — pipeline.go carries a compile-time assertion that each real type
// satisfies the port it is standing in for, so a signature drift breaks the
// build rather than being papered over here.
//
// They exist because constructing a real kernel, policy store and dialogue
// engine for every pipeline test would make each one a test of that whole
// assembly: a failure anywhere in it would present as a pipeline failure, which
// is the exact problem audiobridge documents about depending on concrete types.
//
// What they do NOT do is simulate the behaviour under test. The streaming
// ordering, the queue bounds, the cancellation and the stale-output guard are
// all properties of pipeline.go, observed from outside it.

// scriptedIntel replays audio-intelligence verdicts.
type scriptedIntel struct {
	mu       sync.Mutex
	frames   int
	onsetAt  int
	endAt    int
	analyzed int
	err      error
}

func (s *scriptedIntel) Analyze(
	_ context.Context, _ media.Frame, _ audiointel.ConversationState,
	_ audiointel.SpeechController, _ audiointel.OutboundEnvelope,
) (audiointel.Analysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return audiointel.Analysis{}, s.err
	}
	s.frames++
	s.analyzed++

	var a audiointel.Analysis
	if s.frames == s.onsetAt {
		a.VAD.OnsetConfirmed = true
	}
	if s.frames == s.endAt {
		a.Endpoint.Confirmed = true
	}
	return a, nil
}

func (s *scriptedIntel) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.analyzed
}

// scriptedPlanner returns a fixed plan.
type scriptedPlanner struct {
	action conversation.Action
	err    error
	calls  atomic.Int64
	seen   atomic.Value // string: the last utterance it was given
}

func (p *scriptedPlanner) Handle(e conversation.Event) (conversation.Plan, error) {
	p.calls.Add(1)
	p.seen.Store(e.Utterance.Text)
	if p.err != nil {
		return conversation.Plan{}, p.err
	}
	return conversation.Plan{Action: p.action, Reason: "test"}, nil
}

// scriptedGovernor answers with a fixed outcome and counts the asking.
type scriptedGovernor struct {
	outcome governance.Outcome
	calls   atomic.Int64
	last    atomic.Value // governance.Request
}

func (g *scriptedGovernor) Decide(r governance.Request) governance.Decision {
	g.calls.Add(1)
	g.last.Store(r)
	return governance.Decision{Outcome: g.outcome, Reason: "test"}
}

// scriptedGenerator emits tokens on a schedule and records when it closed.
//
// It drives a real runtime.Dispatcher, so the streaming behaviour under test is
// the frozen dispatcher's, not a reimplementation of it.
type scriptedGenerator struct {
	tokens []string
	// gap is the delay between tokens, which is what makes "synthesis started
	// before generation finished" a meaningful ordering rather than a race.
	gap time.Duration
	err error

	mu       sync.Mutex
	closedAt time.Time
	started  atomic.Int64
}

func (g *scriptedGenerator) Generate(
	ctx context.Context, _ rt.GenerateSpec, sinks ...rt.Sink,
) (*rt.Dispatcher, error) {
	if g.err != nil {
		return nil, g.err
	}
	g.started.Add(1)

	stream := &scriptedTokenStream{gen: g, tokens: g.tokens, gap: g.gap}
	// The REAL dispatcher, with the frozen defaults — including ADR-0011's 20ms
	// abort budget. Using it means the streaming and abort behaviour under test
	// is Phase 10A's, not a reimplementation living in this file.
	//
	// SinkWriteTimeout is raised because the synthesis sink here drives a
	// stand-in engine whose scheduling is deliberately slowed in some tests; the
	// 50ms default would detach the sink and the test would be measuring the
	// detachment rather than the pipeline.
	dcfg := rt.DefaultDispatcherConfig()
	dcfg.SinkWriteTimeout = 5 * time.Second
	dcfg.MaxChunkGap = 0

	d, err := rt.NewDispatcher(dcfg, nil, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range sinks {
		if err := d.AddSink(s); err != nil {
			return nil, err
		}
	}

	go func() { d.Run(ctx, stream) }()
	return d, nil
}

func (g *scriptedGenerator) closed() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closedAt
}

// scriptedTokenStream feeds tokens one at a time.
type scriptedTokenStream struct {
	gen    *scriptedGenerator
	tokens []string
	gap    time.Duration
	pos    int
	closed atomic.Bool
}

func (s *scriptedTokenStream) Recv() (rt.Chunk, error) {
	if s.closed.Load() {
		return rt.Chunk{}, rt.ErrClosed
	}
	if s.pos >= len(s.tokens) {
		s.gen.mu.Lock()
		if s.gen.closedAt.IsZero() {
			s.gen.closedAt = time.Now()
		}
		s.gen.mu.Unlock()
		return rt.Chunk{Kind: rt.ChunkDone, Index: s.pos}, nil
	}
	if s.gap > 0 {
		time.Sleep(s.gap)
	}
	c := rt.Chunk{Kind: rt.ChunkText, Text: s.tokens[s.pos], Index: s.pos}
	s.pos++
	return c, nil
}

func (s *scriptedTokenStream) Close() error { s.closed.Store(true); return nil }

// recordingSTT is a recognition provider that emits a scripted transcript.
type recordingSTT struct {
	id       speech.ProviderID
	segments []speech.TranscriptSegment
	// gap paces the segments so partials genuinely precede the final in time.
	gap     time.Duration
	written atomic.Int64
	opened  atomic.Int64

	// Fault injection. Every one is deterministic: a scripted behaviour, not a
	// sleep or a random failure. A flaky failure test is worse than none —
	// it gets muted, and then the failure path is untested AND believed tested.
	openErr error  // OpenSTT refuses: the provider is missing or will not start
	fault   string // "crash" | "hang" | "invalid" | ""

	// closeAfterWrites makes Write report a closed stream once this many
	// frames have been accepted, which is what a recogniser does when turn
	// teardown closes it underneath the ingest goroutine. Zero disables it.
	closeAfterWrites int64
}

func (r *recordingSTT) ID() speech.ProviderID { return r.id }
func (r *recordingSTT) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: true, PartialResults: true,
		Languages:   []speech.Language{langEN},
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}

func (r *recordingSTT) OpenSTT(ctx context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	r.opened.Add(1)
	if r.openErr != nil {
		return nil, r.openErr
	}
	s := &recordingSTTStream{
		provider: r,
		results:  make(chan speech.TranscriptSegment, len(r.segments)+1),
		done:     make(chan struct{}),
		cfg:      cfg,
	}
	go s.emit()
	return s, nil
}

type recordingSTTStream struct {
	provider *recordingSTT
	results  chan speech.TranscriptSegment
	done     chan struct{}
	cfg      speech.STTConfig
	once     sync.Once
	closed   atomic.Bool
}

func (s *recordingSTTStream) emit() {
	defer close(s.results)

	switch s.provider.fault {
	case "hang":
		// Recognition that never answers and never ends. The session context
		// is the only thing that can end it, which is what the test checks.
		<-s.done
		return

	case "crash":
		// One partial, then the stream dies mid-utterance with no final. The
		// caller said something and the recogniser lost it.
		select {
		case s.results <- speech.TranscriptSegment{
			Session: s.cfg.Session, Turn: s.cfg.Turn,
			Segment: speech.SegmentID("seg-partial"), Text: "I would like",
		}:
		case <-s.done:
		}
		return

	case "invalid":
		// A final that carries nothing usable. Not a crash — the provider
		// believes it succeeded, which is the harder case to notice.
		select {
		case s.results <- speech.TranscriptSegment{
			Session: s.cfg.Session, Turn: s.cfg.Turn,
			Segment: speech.SegmentID("seg-empty"), Text: "", IsFinal: true,
		}:
		case <-s.done:
		}
		return
	}

	for _, seg := range s.provider.segments {
		if s.provider.gap > 0 {
			select {
			case <-time.After(s.provider.gap):
			case <-s.done:
				return
			}
		}
		seg.Session = s.cfg.Session
		seg.Turn = s.cfg.Turn
		select {
		case s.results <- seg:
		case <-s.done:
			return
		}
	}
}

func (s *recordingSTTStream) Write(media.Frame) error {
	if s.closed.Load() {
		return speech.ErrSpeechSessionClosed
	}
	n := s.provider.written.Add(1)
	if max := s.provider.closeAfterWrites; max > 0 && n > max {
		// The stream was closed underneath the writer. Deterministic here;
		// in a live call it is turn teardown racing frame ingest.
		return speech.ErrSpeechSessionClosed
	}
	return nil
}
func (s *recordingSTTStream) Results() <-chan speech.TranscriptSegment { return s.results }
func (s *recordingSTTStream) CloseSend() error                         { return nil }
func (s *recordingSTTStream) Close() error {
	s.closed.Store(true)
	s.once.Do(func() { close(s.done) })
	return nil
}

// recordingTTS is a synthesis provider that records what it was asked to say.
type recordingTTS struct {
	id speech.ProviderID
	// framesPerChunk is how much audio each submitted clause produces.
	framesPerChunk int
	// queue bounds the audio channel, which is the slow-consumer bound.
	queue int
	// synthDelay slows synthesis so backpressure is reachable.
	synthDelay time.Duration

	// Fault injection, deterministic as above.
	openErr    error  // OpenTTS refuses: the voice is missing or will not start
	synthErr   error  // Synthesize refuses a chunk
	audioFault string // "crash" | "hang" | "invalid" | ""

	mu        sync.Mutex
	submitted []submission
	streams   []*recordingTTSStream
	nextIndex int
}

type submission struct {
	chunk speech.Chunk
	at    time.Time
}

func (r *recordingTTS) ID() speech.ProviderID { return r.id }
func (r *recordingTTS) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: true,
		Languages: []speech.Language{langEN},
		// The pipeline's format must be one this voice declares.
		SampleRates: []media.SampleRate{media.Rate16kHz},
	}
}

func (r *recordingTTS) OpenTTS(_ context.Context, cfg speech.TTSConfig) (speech.TTSStream, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	queue := r.queue
	if queue == 0 {
		queue = 64
	}
	r.mu.Lock()
	index := r.nextIndex
	r.nextIndex++
	r.mu.Unlock()

	s := &recordingTTSStream{
		provider: r,
		audio:    make(chan media.Frame, queue),
		done:     make(chan struct{}),
		format:   cfg.Format,
		index:    index,
	}
	r.mu.Lock()
	r.streams = append(r.streams, s)
	r.mu.Unlock()
	return s, nil
}

// submissions returns what was sent for synthesis, in order.
func (r *recordingTTS) submissions() []submission {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]submission(nil), r.submitted...)
}

// producedFrames counts every frame the synthesiser actually generated across
// all of this provider's streams, delivered or not.
func (r *recordingTTS) producedFrames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, st := range r.streams {
		total += int(st.seq.Load())
	}
	return total
}

// streamCount returns how many synthesis streams have been opened.
func (r *recordingTTS) streamCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

func (r *recordingTTS) record(c speech.Chunk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.submitted = append(r.submitted, submission{chunk: c, at: time.Now()})
}

type recordingTTSStream struct {
	provider *recordingTTS
	audio    chan media.Frame
	done     chan struct{}
	format   media.AudioFormat

	// index identifies which synthesis stream — and therefore which TURN —
	// produced a frame. Stamped into the payload so a test can tell an
	// interrupted turn's audio from its successor's AT THE MEDIA SINK, rather
	// than inferring it from a counter that cannot see what was delivered.
	index int

	seq    atomic.Uint64
	once   sync.Once
	closed atomic.Bool

	// inflight counts synthesis still producing frames.
	//
	// The audio channel MUST be closed when synthesis ends — that is how a
	// consumer learns no more audio is coming, and the real adapters do it with
	// a deferred close. A double that never closed it would hang every caller
	// draining it, which is exactly what it did before this was written.
	mu        sync.Mutex
	inflight  int
	sendDone  bool
	audioOnce sync.Once
}

func (s *recordingTTSStream) Synthesize(c speech.Chunk) error {
	// The refusal is checked BEFORE an inflight slot is taken. Taking one and
	// then returning early leaks the slot — finished() never runs, the audio
	// channel can never close, and every caller draining it waits for the turn
	// timeout. That was a real bug in this double, and it presented as a
	// pipeline hang, which is exactly how a bad test double wastes a day.
	if s.provider.synthErr != nil {
		return s.provider.synthErr
	}

	s.mu.Lock()
	if s.closed.Load() || s.sendDone {
		s.mu.Unlock()
		return speech.ErrSpeechSessionClosed
	}
	s.inflight++
	s.mu.Unlock()

	s.provider.record(c)

	go func() {
		defer s.finished()

		switch s.provider.audioFault {
		case "crash":
			// The engine dies mid-utterance: no audio for this chunk, ever.
			return
		case "hang":
			// It accepts the text and produces nothing, forever.
			<-s.done
			return
		}

		if s.provider.synthDelay > 0 {
			select {
			case <-time.After(s.provider.synthDelay):
			case <-s.done:
				return
			}
		}
		n := s.provider.framesPerChunk
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			seq := s.seq.Add(1) - 1
			payload := make([]byte, s.format.BytesFor(20*time.Millisecond))
			if len(payload) >= 2 {
				binary.LittleEndian.PutUint16(payload, uint16(s.index))
			}
			f := media.Frame{Sequence: seq, Format: s.format, Payload: payload}
			select {
			case s.audio <- f:
			case <-s.done:
				return
			}
		}
	}()
	return nil
}

// finished retires one synthesis and ends the audio when the last one goes.
func (s *recordingTTSStream) finished() {
	s.mu.Lock()
	s.inflight--
	last := s.sendDone && s.inflight == 0
	s.mu.Unlock()

	if last {
		s.closeAudio()
	}
}

// closeAudio ends the audio channel exactly once. Only ever called with no
// producer in flight, so no send can race the close.
func (s *recordingTTSStream) closeAudio() {
	s.audioOnce.Do(func() { close(s.audio) })
}

func (s *recordingTTSStream) Audio() <-chan media.Frame { return s.audio }

func (s *recordingTTSStream) CloseSend() error {
	s.mu.Lock()
	s.sendDone = true
	last := s.inflight == 0
	s.mu.Unlock()

	if last {
		s.closeAudio()
	}
	return nil
}

func (s *recordingTTSStream) Close() error {
	s.closed.Store(true)
	s.once.Do(func() { close(s.done) })

	// Abandoning the stream also ends its audio: producers return on done, and
	// the last one out closes the channel.
	s.mu.Lock()
	s.sendDone = true
	last := s.inflight == 0
	s.mu.Unlock()

	if last {
		s.closeAudio()
	}
	return nil
}

// countingSink is the media output.
type countingSink struct {
	mu       sync.Mutex
	frames   []media.Frame
	delay    time.Duration
	bound    int
	rejected int
}

func (c *countingSink) Deliver(ctx context.Context, f media.Frame) error {
	// Checked FIRST. The port says Deliver takes a context, and the pipeline's
	// second stale-output guard depends on a sink honouring it: a frame handed
	// over for a turn that has just been interrupted must not be played.
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bound > 0 && len(c.frames) >= c.bound {
		c.rejected++
		return ErrBackpressure
	}
	c.frames = append(c.frames, f)
	return nil
}

func (c *countingSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

// countFrom returns how many delivered frames came from one synthesis stream.
//
// This is the media-level question a barge-in test has to ask: not "how many
// frames arrived" but "did the abandoned turn's audio reach the caller".
func (c *countingSink) countFrom(streamIndex int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for _, f := range c.frames {
		if len(f.Payload) >= 2 &&
			int(binary.LittleEndian.Uint16(f.Payload)) == streamIndex {
			n++
		}
	}
	return n
}

// recordingObserver captures the order of everything the pipeline reports.
type recordingObserver struct {
	mu         sync.Mutex
	events     []string
	transcript []speech.TranscriptSegment
	chunks     []speech.Chunk
	outcome    TurnOutcome
	reason     string
	turnDone   chan struct{}
	once       sync.Once

	// Timestamps taken WHERE THE EVENT HAPPENS. An earlier version polled these
	// from the test goroutine on a 1ms tick, which measured the tick: every
	// figure came back quantised to the polling interval and the whole-turn and
	// first-transcript numbers were identical to the microsecond.
	firstTranscriptAt time.Time
	turnCompleteAt    time.Time
}

func newObserver() *recordingObserver {
	return &recordingObserver{turnDone: make(chan struct{})}
}

func (o *recordingObserver) OnTranscript(seg speech.TranscriptSegment) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.firstTranscriptAt.IsZero() {
		o.firstTranscriptAt = time.Now()
	}
	kind := "partial"
	if seg.IsFinal {
		kind = "final"
	}
	o.events = append(o.events, "transcript:"+kind)
	o.transcript = append(o.transcript, seg)
}

func (o *recordingObserver) OnResponseChunk(c speech.Chunk) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, "tts_submit")
	o.chunks = append(o.chunks, c)
}

func (o *recordingObserver) OnTurnComplete(outcome TurnOutcome, reason string) {
	o.mu.Lock()
	o.turnCompleteAt = time.Now()
	o.events = append(o.events, "turn_complete")
	o.outcome, o.reason = outcome, reason
	o.mu.Unlock()
	o.once.Do(func() { close(o.turnDone) })
}

func (o *recordingObserver) order() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (o *recordingObserver) waitTurn(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-o.turnDone:
	case <-time.After(d):
		t.Fatalf("the turn did not complete within %s; observed: %v", d, o.order())
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	pipeline *Pipeline
	fsm      *SessionFSM
	intel    *scriptedIntel
	planner  *scriptedPlanner
	governor *scriptedGovernor
	gen      *scriptedGenerator
	stt      *recordingSTT
	tts      *recordingTTS
	out      *countingSink
	obs      *recordingObserver
	metrics  *VoiceMetrics
	pub      *RecordingEventPublisher
}

type harnessOpts struct {
	tokens     []string
	tokenGap   time.Duration
	segments   []speech.TranscriptSegment
	segmentGap time.Duration
	// deny makes governance refuse. A bool rather than an Outcome field because
	// governance.OutcomeDeny is the ZERO VALUE of that type — the safe default
	// there, and a trap here, since an unset field would silently deny every
	// turn and the streaming tests would pass by never running.
	deny           bool
	action         conversation.Action
	plannerErr     error
	generatorErr   error
	sinkDelay      time.Duration
	sinkBound      int
	ttsQueue       int
	ttsSynthDelay  time.Duration
	framesPerChunk int
	maxFrames      int
	maxAudio       int
	turnTimeout    time.Duration

	// Deterministic provider faults, forwarded to the stand-in providers.
	sttOpenErr          error
	sttFault            string
	sttCloseAfterWrites int64
	ttsOpenErr          error
	ttsSynthErr         error
	ttsFault            string
}

func defaultSegments() []speech.TranscriptSegment {
	return []speech.TranscriptSegment{
		{Segment: speech.SegmentID("seg-1"), Sequence: 0, Text: "I would like", IsFinal: false},
		{Segment: speech.SegmentID("seg-2"), Sequence: 1, Text: "I would like to check", IsFinal: false},
		{Segment: speech.SegmentID("seg-3"), Sequence: 2, Text: "I would like to check my balance", IsFinal: true},
	}
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()

	if o.tokens == nil {
		o.tokens = []string{"Your balance", " is fifty pounds.", " Anything else?"}
	}
	if o.segments == nil {
		o.segments = defaultSegments()
	}
	if o.action == 0 {
		o.action = conversation.ActionRespond
	}
	if o.maxFrames == 0 {
		o.maxFrames = 32
	}
	if o.maxAudio == 0 {
		o.maxAudio = 32
	}
	if o.turnTimeout == 0 {
		o.turnTimeout = 15 * time.Second
	}
	if o.framesPerChunk == 0 {
		o.framesPerChunk = 1
	}

	format := media.PCM16Mono16k()

	stt := &recordingSTT{
		id: speech.ProviderID("stt-test"), segments: o.segments, gap: o.segmentGap,
		openErr: o.sttOpenErr, fault: o.sttFault,
		closeAfterWrites: o.sttCloseAfterWrites,
	}
	tts := &recordingTTS{
		id: speech.ProviderID("tts-test"), framesPerChunk: o.framesPerChunk,
		queue: o.ttsQueue, synthDelay: o.ttsSynthDelay,
		openErr: o.ttsOpenErr, synthErr: o.ttsSynthErr, audioFault: o.ttsFault,
	}

	reg := newRegistry(t, ModeDevelopment)
	sttSpecLocal := sttSpec()
	if err := reg.RegisterSTT(stt, sttSpecLocal); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	ttsSpecLocal := sttSpec()
	ttsSpecLocal.Engine = "piper"
	ttsSpecLocal.Model = ModelIdentity{Model: ModelID("test-voice")}
	if err := reg.RegisterTTS(tts, ttsSpecLocal); err != nil {
		t.Fatalf("RegisterTTS: %v", err)
	}

	metrics := NewVoiceMetrics()
	pub := NewRecordingEventPublisher()

	fsm, err := NewSessionFSM(FSMConfig{
		Session: SessionID("ses-pipeline"), Call: CallID("call-1"),
		Metrics: metrics, Publisher: pub,
	})
	if err != nil {
		t.Fatalf("NewSessionFSM: %v", err)
	}

	h := &harness{
		fsm:      fsm,
		intel:    &scriptedIntel{onsetAt: 1, endAt: 3},
		planner:  &scriptedPlanner{action: o.action, err: o.plannerErr},
		governor: &scriptedGovernor{outcome: governanceOutcome(o.deny)},
		gen:      &scriptedGenerator{tokens: o.tokens, gap: o.tokenGap, err: o.generatorErr},
		stt:      stt,
		tts:      tts,
		out:      &countingSink{delay: o.sinkDelay, bound: o.sinkBound},
		obs:      newObserver(),
		metrics:  metrics,
		pub:      pub,
	}

	p, err := NewPipeline(PipelineConfig{
		Session: SessionID("ses-pipeline"), Call: CallID("call-1"),
		Language: langEN, Format: format,
		Registry: reg, Intel: h.intel, Planner: h.planner,
		Governor: h.governor, Generator: h.gen, Output: h.out,
		FSM: fsm, Metrics: metrics, Publisher: pub, Observer: h.obs,
		MaxPendingFrames: o.maxFrames, MaxPendingSegments: 16,
		MaxPendingAudio: o.maxAudio, TurnTimeout: o.turnTimeout,
		Tier: rt.TierFast, Actor: governance.ActorID("voice-agent"),
		MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	h.pipeline = p
	return h
}

// governanceOutcome maps the harness flag onto the frozen enum.
func governanceOutcome(deny bool) governance.Outcome {
	if deny {
		return governance.OutcomeDeny
	}
	return governance.OutcomeAllow
}

func testFrame(seq uint64) media.Frame {
	format := media.PCM16Mono16k()
	return media.Frame{
		Sequence: seq, Format: format,
		Payload: make([]byte, format.BytesFor(20*time.Millisecond)),
	}
}

// feed writes n frames, which drives the scripted intelligence forward.
func (h *harness) feed(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. Partials arrive before the final
// ---------------------------------------------------------------------------

func TestPipeline_ForwardsPartialsBeforeTheFinalTranscript(t *testing.T) {
	t.Parallel()

	// A partial's whole value is that it is early. One delivered alongside the
	// final did nothing, and a pipeline that accumulated segments and released
	// them together would pass any test that only counted them.
	h := newHarness(t, harnessOpts{segmentGap: 20 * time.Millisecond})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)

	h.obs.mu.Lock()
	segments := append([]speech.TranscriptSegment(nil), h.obs.transcript...)
	h.obs.mu.Unlock()

	if len(segments) != 3 {
		t.Fatalf("observed %d segments, want 3", len(segments))
	}
	for i, seg := range segments[:2] {
		if seg.IsFinal {
			t.Errorf("segment %d arrived final; the first two are partials", i)
		}
	}
	if !segments[2].IsFinal {
		t.Error("the last segment is not the final one")
	}

	// And the ORDER, not just the contents: a partial must be observed before
	// the final exists.
	order := h.obs.order()
	firstPartial, finalAt := -1, -1
	for i, e := range order {
		if e == "transcript:partial" && firstPartial < 0 {
			firstPartial = i
		}
		if e == "transcript:final" {
			finalAt = i
		}
	}
	if firstPartial < 0 || finalAt < 0 || firstPartial >= finalAt {
		t.Errorf("partials did not precede the final; observed %v", order)
	}
}

// ---------------------------------------------------------------------------
// 2. Synthesis begins before generation ends
// ---------------------------------------------------------------------------

func TestPipeline_SynthesisBeginsBeforeGenerationEnds(t *testing.T) {
	t.Parallel()

	// The requirement that separates real streaming from buffer-then-split.
	// The generator emits tokens with a gap, so the first completed clause
	// exists long before the response does; the first synthesis submission must
	// happen in that window.
	//
	// # Why the first clause ends in a question mark
	//
	// speech.Chunker treats a PERIOD at the end of its buffer as undecidable
	// while text is still streaming — "1234." looks like a finished sentence
	// until "56" arrives — so only Flush resolves one. '?' and '!' are
	// unambiguous and cut immediately, precisely so a question does not cost a
	// generator round trip.
	//
	// That is frozen, deliberate behaviour. A script whose only period was the
	// last thing generated would therefore see both clauses emitted at Flush and
	// would read as buffer-then-split when the pipeline is doing nothing wrong.
	h := newHarness(t, harnessOpts{
		tokens: []string{
			"Can I help with anything else?",
			" Your balance is fifty pounds.",
			" Thanks for calling.",
		},
		tokenGap: 120 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 20*time.Second)

	subs := h.tts.submissions()
	if len(subs) == 0 {
		t.Fatal("nothing was submitted for synthesis")
	}

	closedAt := h.gen.closed()
	if closedAt.IsZero() {
		t.Fatal("the generator never closed its stream")
	}

	first := subs[0].at
	if !first.Before(closedAt) {
		t.Errorf("the first clause was submitted at %s but generation had already "+
			"ended at %s: the response was buffered and then split, which costs the "+
			"caller the whole generation latency as dead air",
			first.Format(time.StampMilli), closedAt.Format(time.StampMilli))
	}

	t.Logf("ORCHESTRATION MEASUREMENT (stand-in providers, not model inference):\n"+
		"  first clause submitted %s before the token stream closed\n"+
		"  clauses submitted: %d", closedAt.Sub(first), len(subs))
}

func TestPipeline_DoesNotWaitForTheWholeResponseBeforeSpeaking(t *testing.T) {
	t.Parallel()

	// The same property observed from the other end: audio for the first clause
	// reaches media while later tokens are still being generated.
	h := newHarness(t, harnessOpts{
		tokens:   []string{"First clause here.", " Second clause here.", " Third clause here."},
		tokenGap: 150 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)

	// Watch for audio BEFORE the turn completes.
	deadline := time.After(20 * time.Second)
	sawAudioEarly := false
	for !sawAudioEarly {
		select {
		case <-h.obs.turnDone:
			// The turn finished; check whether audio had arrived before it did.
			sawAudioEarly = h.out.count() > 0
			goto done
		case <-deadline:
			t.Fatal("the turn never completed")
		case <-time.After(10 * time.Millisecond):
			if h.out.count() > 0 {
				sawAudioEarly = true
			}
		}
	}
done:
	if !sawAudioEarly {
		t.Error("no audio reached media before the turn ended")
	}

	h.obs.waitTurn(t, 20*time.Second)
	if h.out.count() == 0 {
		t.Error("no audio was delivered at all")
	}
}

// ---------------------------------------------------------------------------
// 3. Bounds and backpressure
// ---------------------------------------------------------------------------

func TestPipeline_InboundQueueIsBoundedAndReportsBackpressure(t *testing.T) {
	t.Parallel()

	// Blocking here would stall the media reader, and a stalled media reader
	// backs up into the carrier.
	h := newHarness(t, harnessOpts{maxFrames: 4})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	// The ingest goroutine drains, so the queue only fills if we outrun it.
	var backpressure error
	for i := 0; i < 5000 && backpressure == nil; i++ {
		if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil {
			backpressure = err
		}
	}

	if backpressure == nil {
		t.Fatal("5000 frames were accepted against a bound of 4: the inbound queue " +
			"is unbounded")
	}
	if !errors.Is(backpressure, ErrBackpressure) {
		t.Errorf("want ErrBackpressure, got %v", backpressure)
	}
	if got := h.metrics.Backpressure.Value(string(StageAudioIn)); got == 0 {
		t.Error("backpressure was returned but not counted")
	}
}

func TestPipeline_SlowMediaSinkDoesNotGrowAnUnboundedQueue(t *testing.T) {
	t.Parallel()

	// A media sink that stalls must not accumulate a call's worth of audio.
	// The sink here refuses past its bound, and the pipeline must drop and
	// count rather than buffer.
	h := newHarness(t, harnessOpts{
		tokens:         []string{"One. Two. Three. Four. Five. Six. Seven. Eight."},
		framesPerChunk: 12,
		ttsQueue:       4, // the synthesiser's own bound
		sinkBound:      3, // the media sink accepts only three frames
		sinkDelay:      5 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 25*time.Second)

	delivered := h.out.count()
	if delivered > 3 {
		t.Errorf("the media sink accepted %d frames against its bound of 3", delivered)
	}

	dropped := h.metrics.DroppedChunks.Value(string(StageAudioOut), ReasonQueueFull)
	if dropped == 0 {
		t.Error("a refusing media sink produced no dropped-frame count: the audio " +
			"was queued somewhere instead of being dropped")
	}

	t.Logf("QUEUE DEPTH UNDER A SLOW CONSUMER: delivered=%d dropped=%d "+
		"(synthesiser queue bound %d, media sink bound %d)",
		delivered, dropped, 4, 3)
}

// ---------------------------------------------------------------------------
// 4. Cancellation, disconnect and stale output
// ---------------------------------------------------------------------------

func TestPipeline_CancellationStopsThePipeline(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOpts{
		tokens:   []string{"A long answer.", " That keeps going.", " And going."},
		tokenGap: 200 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)

	// Let the turn get under way.
	time.Sleep(150 * time.Millisecond)

	start := time.Now()
	if err := h.pipeline.Cancel(ReasonRequested); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-h.pipeline.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the pipeline did not stop after Cancel")
	}
	elapsed := time.Since(start)

	if got := h.fsm.State(); got != StateCancelled {
		t.Errorf("the session is in %s, want cancelled", got)
	}

	t.Logf("CANCELLATION LATENCY (orchestration only, stand-in providers): %s "+
		"— this is teardown of the whole pipeline, not the ADR-0004 §12 20ms "+
		"signal budget, and the two must not be conflated", elapsed)
}

func TestPipeline_DisconnectStopsThePipelineAsACompletedCall(t *testing.T) {
	t.Parallel()

	// A caller hanging up completed their call. Recording it as cancelled would
	// make an ordinary ending look like an abort in every report counting them.
	h := newHarness(t, harnessOpts{tokenGap: 100 * time.Millisecond})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)
	time.Sleep(100 * time.Millisecond)

	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	select {
	case <-h.pipeline.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the pipeline did not stop after Disconnect")
	}

	if got := h.fsm.State(); got != StateCompleted {
		t.Errorf("the session is in %s, want completed", got)
	}
	// And nothing accepts audio afterwards.
	if err := h.pipeline.WriteFrame(testFrame(999)); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("a disconnected pipeline accepted a frame: %v", err)
	}
}

func TestPipeline_StaleAudioCannotReachMediaAfterCancellation(t *testing.T) {
	t.Parallel()

	// The correctness property behind barge-in: audio synthesised for a turn
	// that has been abandoned must never be delivered. Checking "is the stream
	// closed" is not enough, because a frame already read has passed that
	// check.
	h := newHarness(t, harnessOpts{
		// A long answer whose clauses resolve early (question marks cut
		// immediately), each producing many frames, synthesised slowly. That
		// keeps audio genuinely in flight at the moment of cancellation.
		tokens: []string{
			"Shall I read your recent transactions?",
			" There are twelve of them?",
			" Here is the first?",
			" Here is the second?",
		},
		tokenGap:       40 * time.Millisecond,
		framesPerChunk: 60,
		ttsSynthDelay:  30 * time.Millisecond,
		// A deliberately slow media sink. Without it the synthesiser's whole
		// clause drains before the test can cancel, and there is nothing left in
		// flight for the guard to stop — the test then passes while proving
		// nothing, which is how the first version of it went green.
		sinkDelay: 4 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)

	// Cancel only once audio is genuinely flowing. Cancelling before the first
	// frame would leave nothing to be stale, and the test would pass without
	// exercising the guard at all — which is exactly what an earlier version of
	// it did, reporting "frames withheld=0" and still going green.
	deadline := time.After(20 * time.Second)
	for h.out.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("no audio ever reached media; the guard was never exercised")
		case <-time.After(2 * time.Millisecond):
		}
	}

	before := h.out.count()
	if err := h.pipeline.Cancel(ReasonBargeIn); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-h.pipeline.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the pipeline did not stop")
	}

	// Give any in-flight frame every chance to leak through.
	time.Sleep(300 * time.Millisecond)

	if after := h.out.count(); after != before {
		t.Errorf("%d frames reached media after cancellation (%d before, %d after): "+
			"audio from an abandoned turn was spoken to the caller",
			after-before, before, after)
	}

	// The synthesiser must have had more to say than was delivered, or the
	// cancellation happened after everything had already been spoken and this
	// test proved nothing.
	produced := 0
	h.tts.mu.Lock()
	for _, st := range h.tts.streams {
		produced += int(st.seq.Load())
	}
	h.tts.mu.Unlock()

	if produced <= before {
		t.Errorf("the synthesiser produced %d frames and %d were delivered before "+
			"cancellation: nothing was left in flight, so the stale-output guard "+
			"was not exercised", produced, before)
	}

	t.Logf("stale-output guard: generation=%d, frames withheld=%d",
		h.pipeline.Generation(), h.pipeline.StaleFramesBlocked())
}

// ---------------------------------------------------------------------------
// 5. Governance and conversation are on the path
// ---------------------------------------------------------------------------

func TestPipeline_EverySpokenTurnPassesGovernance(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOpts{})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)

	if got := h.governor.calls.Load(); got != 1 {
		t.Fatalf("governance was consulted %d times for one spoken turn", got)
	}
	if got := h.planner.calls.Load(); got != 1 {
		t.Errorf("the conversation planner was consulted %d times", got)
	}

	req, _ := h.governor.last.Load().(governance.Request)
	if req.Action.Operation != "speak" {
		t.Errorf("the governed operation is %q, want speak", req.Action.Operation)
	}
	if req.Action.Reversibility != governance.ReversibleNever {
		t.Error("speech to a caller cannot be unsaid; it must be ReversibleNever")
	}
	// The decision must carry no caller content.
	for k, v := range req.Action.Attributes {
		if strings.Contains(fmt.Sprint(v), "balance") {
			t.Errorf("attribute %q carries caller content", k)
		}
	}
}

func TestPipeline_ARefusedTurnSpeaksNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOpts{deny: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)

	if got := h.gen.started.Load(); got != 0 {
		t.Errorf("generation ran %d times for a refused turn: governance was "+
			"consulted but not obeyed", got)
	}
	if subs := h.tts.submissions(); len(subs) != 0 {
		t.Errorf("%d clauses were synthesised for a refused turn", len(subs))
	}
	if h.out.count() != 0 {
		t.Error("audio reached the caller for a refused turn")
	}

	h.obs.mu.Lock()
	outcome := h.obs.outcome
	h.obs.mu.Unlock()
	if outcome != OutcomeDenied {
		t.Errorf("the turn outcome is %q, want denied", outcome)
	}

	// A refusal is not a failure: the session keeps listening.
	if got := h.fsm.State(); got != StateListening {
		t.Errorf("after a refusal the session is in %s, want listening", got)
	}
}

func TestPipeline_APlanThatIsNotRespondSpeaksNothing(t *testing.T) {
	t.Parallel()

	// Transfer, escalation and hangup are the layer above's to execute. A
	// pipeline inventing words for them would be deciding policy.
	h := newHarness(t, harnessOpts{action: conversation.ActionTransfer})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)

	if got := h.governor.calls.Load(); got != 0 {
		t.Errorf("governance was consulted %d times for a non-speaking plan", got)
	}
	if got := h.gen.started.Load(); got != 0 {
		t.Errorf("generation ran for a non-speaking plan")
	}
}

// ---------------------------------------------------------------------------
// 6. Errors and FSM consistency
// ---------------------------------------------------------------------------

func TestPipeline_ProviderFailureIsTypedAndDoesNotKillTheCall(t *testing.T) {
	t.Parallel()

	// One provider hiccup should not end a conversation the caller is still in.
	h := newHarness(t, harnessOpts{generatorErr: errors.New("model daemon is down")})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)

	err := h.pipeline.Err()
	if err == nil {
		t.Fatal("a failed generation was not recorded")
	}
	if !errors.Is(err, ErrProviderFailed) {
		t.Errorf("want ErrProviderFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "model daemon is down") {
		t.Errorf("the provider's own message was lost: %v", err)
	}

	if got := h.fsm.State(); got != StateListening {
		t.Errorf("a failed turn left the session in %s; it must return the floor", got)
	}
	if h.fsm.Terminal() {
		t.Error("one provider failure ended the call")
	}
}

func TestPipeline_StateFollowsTheDeclaredTransitions(t *testing.T) {
	t.Parallel()

	// Pipeline state and session state must not be able to disagree: every move
	// goes through the Task 10 table, so an undeclared one would have failed.
	h := newHarness(t, harnessOpts{})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)
	h.obs.waitTurn(t, 15*time.Second)
	_ = h.pipeline.Disconnect()
	<-h.pipeline.Done()

	var seen []SessionState
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("the pipeline performed an undeclared transition %s -> %s",
				c.From, c.To)
		}
		seen = append(seen, c.To)
	}

	// The turn actually happened, rather than the session sitting still.
	for _, want := range []SessionState{
		StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking,
	} {
		found := false
		for _, s := range seen {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the session never entered %s; observed %v", want, seen)
		}
	}
}

func TestPipeline_RefusesAnIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	// The dependencies that may never be omitted, each named with why.
	base := func() PipelineConfig {
		reg := newRegistry(t, ModeDevelopment)
		fsm, _ := NewSessionFSM(FSMConfig{Session: SessionID("ses-x")})
		return PipelineConfig{
			Session: SessionID("ses-x"), Language: langEN,
			Format: media.PCM16Mono16k(), Registry: reg,
			Intel: &scriptedIntel{}, Planner: &scriptedPlanner{},
			Governor: &scriptedGovernor{}, Generator: &scriptedGenerator{},
			Output: &countingSink{}, FSM: fsm,
			MaxPendingFrames: 8, MaxPendingSegments: 8, MaxPendingAudio: 8,
			TurnTimeout: time.Second, Tier: rt.TierFast,
		}
	}

	cases := []struct {
		name   string
		mutate func(*PipelineConfig)
		want   string
	}{
		{"no governor", func(c *PipelineConfig) { c.Governor = nil }, "governance decision"},
		{"no planner", func(c *PipelineConfig) { c.Planner = nil }, "must not be bypassed"},
		{"no intelligence", func(c *PipelineConfig) { c.Intel = nil }, "Phase 11D"},
		{"no registry", func(c *PipelineConfig) { c.Registry = nil }, "second routing engine"},
		{"no fsm", func(c *PipelineConfig) { c.FSM = nil }, "must not be able to disagree"},
		{"unbounded audio", func(c *PipelineConfig) { c.MaxPendingAudio = 0 }, "unbounded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := base()
			tc.mutate(&cfg)

			_, err := NewPipeline(cfg)
			if err == nil {
				t.Fatalf("an incomplete configuration was accepted (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want mention of %q, got %v", tc.want, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 7. Isolation
// ---------------------------------------------------------------------------

func TestPipeline_ConcurrentSessionsAreIsolated(t *testing.T) {
	t.Parallel()

	// Independent sessions share a process and nothing else. One session's
	// transcript, response or audio reaching another is the worst failure this
	// system can have.
	const sessions = 4

	var wg sync.WaitGroup
	errs := make(chan error, sessions*4)

	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			marker := fmt.Sprintf("session %d speaking", n)
			h := newHarness(t, harnessOpts{
				tokens: []string{marker + "."},
				segments: []speech.TranscriptSegment{
					{Segment: speech.SegmentID("s1"), Text: marker, IsFinal: false},
					{Segment: speech.SegmentID("s2"), Text: marker, IsFinal: true},
				},
			})

			if err := h.pipeline.Start(context.Background()); err != nil {
				errs <- fmt.Errorf("session %d: Start: %w", n, err)
				return
			}
			defer func() { _ = h.pipeline.Disconnect() }()

			for i := 0; i < 4; i++ {
				if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil {
					errs <- fmt.Errorf("session %d: WriteFrame: %w", n, err)
					return
				}
			}

			select {
			case <-h.obs.turnDone:
			case <-time.After(25 * time.Second):
				errs <- fmt.Errorf("session %d: the turn never completed", n)
				return
			}

			// Its own transcript, and nobody else's.
			h.obs.mu.Lock()
			segs := append([]speech.TranscriptSegment(nil), h.obs.transcript...)
			h.obs.mu.Unlock()
			for _, s := range segs {
				if s.Text != marker {
					errs <- fmt.Errorf("session %d received transcript %q", n, s.Text)
					return
				}
			}

			// Its own response, and nobody else's.
			for _, sub := range h.tts.submissions() {
				if !strings.Contains(sub.chunk.Text, fmt.Sprintf("session %d", n)) {
					errs <- fmt.Errorf("session %d synthesised %q", n, sub.chunk.Text)
					return
				}
			}
			if got := h.planner.calls.Load(); got != 1 {
				errs <- fmt.Errorf("session %d: planner called %d times", n, got)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// 8. Measurement
// ---------------------------------------------------------------------------

// TestPipeline_MeasuresOrchestrationOverhead reports what THIS layer costs.
//
// # These numbers are not provider latency and must never be read as such
//
// Every provider here is a stand-in that returns immediately. What is left when
// inference is removed is the orchestration: channel handovers, the state
// machine, the chunker, governance, and the dispatcher's fan-out. That is the
// only quantity this phase owns and the only one it can honestly report.
//
// ADR-0006 and ADR-0011's 250 ms p50 / 550 ms p95 are NOT applied. They were set
// for a hosted frontier model over a network, and comparing them to a figure
// that contains no model at all would be meaningless in both directions.
func TestPipeline_MeasuresOrchestrationOverhead(t *testing.T) {
	t.Parallel()

	const turns = 20
	var (
		firstPartial []time.Duration
		firstClause  []time.Duration
		whole        []time.Duration
	)

	for i := 0; i < turns; i++ {
		h := newHarness(t, harnessOpts{
			tokens:   []string{"A short answer?"},
			segments: defaultSegments(),
		})

		if err := h.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}

		start := time.Now()
		h.feed(t, 4)
		h.obs.waitTurn(t, 20*time.Second)

		subs := h.tts.submissions()
		if len(subs) == 0 {
			t.Fatal("no synthesis")
		}

		h.obs.mu.Lock()
		partialAt, doneAt := h.obs.firstTranscriptAt, h.obs.turnCompleteAt
		h.obs.mu.Unlock()

		firstPartial = append(firstPartial, partialAt.Sub(start))
		firstClause = append(firstClause, subs[0].at.Sub(start))
		whole = append(whole, doneAt.Sub(start))

		_ = h.pipeline.Disconnect()
		<-h.pipeline.Done()
	}

	report := func(name string, samples []time.Duration) {
		var total time.Duration
		min, max := samples[0], samples[0]
		for _, s := range samples {
			total += s
			if s > max {
				max = s
			}
			if s < min {
				min = s
			}
		}
		t.Logf("  %-34s mean=%-12s min=%-12s max=%s",
			name, total/time.Duration(len(samples)), min, max)
	}

	// The platform's clock granularity bounds what any of this can mean. On
	// Windows time.Now() is coarse enough that a sub-millisecond stage can read
	// as zero, so it is measured and stated rather than assumed fine.
	var deltas []time.Duration
	for i := 0; i < 200; i++ {
		a := time.Now()
		var b time.Time
		for {
			b = time.Now()
			if b.After(a) {
				break
			}
		}
		deltas = append(deltas, b.Sub(a))
	}
	var resTotal time.Duration
	for _, d := range deltas {
		resTotal += d
	}
	resolution := resTotal / time.Duration(len(deltas))

	t.Logf("AEGIS ORCHESTRATION OVERHEAD over %d turns\n"+
		"  Stand-in providers throughout: NO model, recogniser or synthesiser\n"+
		"  inference is included in any figure below. These are not comparable to\n"+
		"  ADR-0006/ADR-0011's 250ms p50 / 550ms p95, which describe a hosted model\n"+
		"  over a network and are not applied here as a pass/fail gate.\n"+
		"  Measured clock granularity on this machine: %s — a figure at or below\n"+
		"  it is at the limit of what can be measured here, not a real zero.",
		turns, resolution)

	report("partial transcript forwarding", firstPartial)
	report("first clause scheduled for TTS", firstClause)
	report("whole turn (frame in -> turn done)", whole)
}
