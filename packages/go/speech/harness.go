package speech

import (
	"context"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// The fakes and the harness below are EXPORTED ON PURPOSE, following the
// convention every phase since 10A has established: a service embedding this
// engine needs to test its own code against it, and forcing every consumer to
// rebuild this scaffolding is how six subtly different fakes come to exist.

// FakeSTTProvider is a scripted recognition provider.
//
// # It performs no recognition, and that is correct
//
// This package is orchestration. A fake that guessed at words would be testing
// a guess. This one emits exactly the segments it was handed, in order, so a
// test asserting ordering, deduplication or failover observes a fixed sequence
// and no test depends on acoustic behaviour that does not exist here.
type FakeSTTProvider struct {
	id     ProviderID
	caps   Capabilities
	clock  rt.Clock
	script []TranscriptSegment

	mu       sync.Mutex
	failNext error
	stall    time.Duration
	opened   int
	streams  []*fakeSTTStream
}

// NewFakeSTTProvider builds a scripted STT provider.
//
// The script's Session and Turn are overwritten from the stream's config, so
// one script is reusable across sessions without leaking identifiers between
// them.
func NewFakeSTTProvider(id ProviderID, script []TranscriptSegment, clock rt.Clock) *FakeSTTProvider {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &FakeSTTProvider{
		id:     id,
		clock:  clock,
		script: append([]TranscriptSegment(nil), script...),
		caps: Capabilities{
			Languages:      []Language{LangEnglishIN, LangHindi, LangHinglish},
			Streaming:      true,
			PartialResults: true,
			SampleRates:    []media.SampleRate{media.Rate8kHz, media.Rate16kHz},
		},
	}
}

// ID implements STTProvider.
func (p *FakeSTTProvider) ID() ProviderID { return p.id }

// Capabilities implements STTProvider.
func (p *FakeSTTProvider) Capabilities() Capabilities { return p.caps }

// SetCapabilities overrides what the provider declares, for routing tests.
func (p *FakeSTTProvider) SetCapabilities(c Capabilities) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = c
}

// FailNext makes the next OpenSTT fail. Failure injection.
func (p *FakeSTTProvider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

// StallFor makes the next OpenSTT take d on the injected clock. Timeout
// injection.
func (p *FakeSTTProvider) StallFor(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stall = d
}

// Opened returns how many streams were opened.
func (p *FakeSTTProvider) Opened() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opened
}

// OpenSTT implements STTProvider.
func (p *FakeSTTProvider) OpenSTT(ctx context.Context, cfg STTConfig) (STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if err := p.failNext; err != nil {
		p.failNext = nil
		p.mu.Unlock()
		return nil, err
	}
	stall := p.stall
	p.stall = 0
	p.opened++
	script := append([]TranscriptSegment(nil), p.script...)
	p.mu.Unlock()

	if stall > 0 {
		if err := p.clock.Sleep(ctx, stall); err != nil {
			return nil, ErrProviderTimeout
		}
	}

	s := &fakeSTTStream{
		results: make(chan TranscriptSegment, len(script)+1),
		frames:  make([]media.Frame, 0, 16),
		cfg:     cfg,
		id:      p.id,
		script:  script,
	}
	p.mu.Lock()
	p.streams = append(p.streams, s)
	p.mu.Unlock()
	return s, nil
}

// Streams returns the streams opened so far, for assertions.
func (p *FakeSTTProvider) Streams() []*fakeSTTStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*fakeSTTStream(nil), p.streams...)
}

type fakeSTTStream struct {
	cfg    STTConfig
	id     ProviderID
	script []TranscriptSegment

	mu      sync.Mutex
	frames  []media.Frame
	results chan TranscriptSegment
	sent    int
	closed  bool
	sendEnd bool
}

// Write implements STTStream. It records the frame and releases the next
// scripted result, so results are driven by audio rather than by wall time.
func (s *fakeSTTStream) Write(f media.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSTTCancelled
	}
	// Retained deliberately, to prove the caller cloned: if the orchestrator
	// handed over a borrowed payload, a later mutation would show up here.
	s.frames = append(s.frames, f)

	if s.sent < len(s.script) && !s.script[s.sent].IsFinal {
		s.emitLocked()
	}
	return nil
}

// emitLocked releases one scripted segment, stamped for this stream.
func (s *fakeSTTStream) emitLocked() {
	seg := s.script[s.sent]
	seg.Session = s.cfg.Session
	seg.Turn = s.cfg.Turn
	seg.Segment = NewSegmentID()
	seg.Meta.Provider = s.id
	if seg.Language == LangUnknown {
		seg.Language = s.cfg.Language
	}
	s.sent++
	select {
	case s.results <- seg:
	default: // bounded; a test that overruns the buffer is asserting the wrong thing
	}
}

// Frames returns the frames the stream received, for clone assertions.
func (s *fakeSTTStream) Frames() []media.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]media.Frame(nil), s.frames...)
}

// CloseSend implements STTStream: it releases any remaining scripted results,
// which is how a final transcript follows end-of-speech.
func (s *fakeSTTStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sendEnd {
		return nil
	}
	s.sendEnd = true
	for s.sent < len(s.script) {
		s.emitLocked()
	}
	close(s.results)
	return nil
}

// Results implements STTStream.
func (s *fakeSTTStream) Results() <-chan TranscriptSegment { return s.results }

// Close implements STTStream. Idempotent.
func (s *fakeSTTStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.sendEnd {
		s.sendEnd = true
		close(s.results)
	}
	return nil
}

// FakeTTSProvider emits a fixed number of frames per chunk.
type FakeTTSProvider struct {
	id     ProviderID
	caps   Capabilities
	clock  rt.Clock
	frames int

	mu        sync.Mutex
	failNext  error
	stall     time.Duration
	opened    int
	cancelled int
	closed    int
}

// NewFakeTTSProvider builds a synthesis provider emitting framesPerChunk frames
// for every chunk it is given.
func NewFakeTTSProvider(id ProviderID, framesPerChunk int, clock rt.Clock) *FakeTTSProvider {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if framesPerChunk <= 0 {
		framesPerChunk = 3
	}
	return &FakeTTSProvider{
		id: id, clock: clock, frames: framesPerChunk,
		caps: Capabilities{
			Languages:   []Language{LangEnglishIN, LangHindi, LangHinglish},
			Streaming:   true,
			SampleRates: []media.SampleRate{media.Rate8kHz, media.Rate16kHz},
		},
	}
}

// ID implements TTSProvider.
func (p *FakeTTSProvider) ID() ProviderID { return p.id }

// Capabilities implements TTSProvider.
func (p *FakeTTSProvider) Capabilities() Capabilities { return p.caps }

// SetCapabilities overrides what the provider declares, for routing tests.
func (p *FakeTTSProvider) SetCapabilities(c Capabilities) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = c
}

// FailNext makes the next OpenTTS fail.
func (p *FakeTTSProvider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

// StallFor makes the next OpenTTS take d on the injected clock.
func (p *FakeTTSProvider) StallFor(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stall = d
}

// Opened returns how many streams were opened.
func (p *FakeTTSProvider) Opened() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opened
}

// Cancelled returns how many streams were closed while audio was still
// undelivered.
//
// INHERENTLY TIMING-DEPENDENT: whether audio is still outstanding at the moment
// of cancellation depends on how far the consumer had drained. Assert on
// [FakeTTSProvider.Closed] instead unless a test is specifically about the
// outstanding-audio case.
func (p *FakeTTSProvider) Cancelled() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancelled
}

// Closed returns how many streams were closed, for any reason.
//
// Deterministic: a cancellation always closes its stream, so this is the signal
// a test asserting "synthesis was stopped" should use.
func (p *FakeTTSProvider) Closed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *FakeTTSProvider) noteClosed(cancelled bool) {
	p.mu.Lock()
	p.closed++
	if cancelled {
		p.cancelled++
	}
	p.mu.Unlock()
}

// OpenTTS implements TTSProvider.
func (p *FakeTTSProvider) OpenTTS(ctx context.Context, cfg TTSConfig) (TTSStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if err := p.failNext; err != nil {
		p.failNext = nil
		p.mu.Unlock()
		return nil, err
	}
	stall := p.stall
	p.stall = 0
	p.opened++
	p.mu.Unlock()

	if stall > 0 {
		if err := p.clock.Sleep(ctx, stall); err != nil {
			return nil, ErrProviderTimeout
		}
	}

	return &fakeTTSStream{
		owner: p, cfg: cfg,
		// Generously buffered so a test that never drains does not deadlock the
		// producer; the orchestrator's own bound is what the tests assert on.
		audio:  make(chan media.Frame, 256),
		perCh:  p.frames,
		format: cfg.Format,
	}, nil
}

type fakeTTSStream struct {
	owner  *FakeTTSProvider
	cfg    TTSConfig
	perCh  int
	format media.AudioFormat

	mu      sync.Mutex
	audio   chan media.Frame
	seq     uint64
	ts      time.Duration
	closed  bool
	sendEnd bool
}

// Synthesize implements TTSStream, emitting perCh frames for the chunk.
func (s *fakeTTSStream) Synthesize(c Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrTTSCancelled
	}

	const frameDur = 20 * time.Millisecond
	n := s.format.BytesFor(frameDur)
	if n <= 0 {
		n = 320
	}
	for i := 0; i < s.perCh; i++ {
		f := media.Frame{
			Sequence:  s.seq,
			Timestamp: s.ts,
			Format:    s.format,
			// Owned by the receiver, per the TTSStream contract.
			Payload: make([]byte, n),
		}
		s.seq++
		s.ts += frameDur
		select {
		case s.audio <- f:
		default:
			return ErrBackpressure
		}
	}
	return nil
}

// Audio implements TTSStream.
func (s *fakeTTSStream) Audio() <-chan media.Frame { return s.audio }

// CloseSend implements TTSStream.
func (s *fakeTTSStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sendEnd {
		return nil
	}
	s.sendEnd = true
	close(s.audio)
	return nil
}

// Close implements TTSStream. Idempotent.
//
// # What counts as a cancellation
//
// Closing while audio remains UNDELIVERED — either because the text was never
// finished (no CloseSend) or because frames are still queued for the consumer.
// Defining it as "Close before CloseSend" would be wrong: an orchestrator
// correctly calls CloseSend as soon as the last chunk of TEXT is submitted,
// while audio for it is still being produced and drained. Barge-in almost
// always arrives in exactly that window.
func (s *fakeTTSStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	wasCancelled := !s.sendEnd || len(s.audio) > 0
	s.closed = true
	if !s.sendEnd {
		s.sendEnd = true
		close(s.audio)
	}
	s.mu.Unlock()

	// Reported outside our own lock: noteClosed takes the provider's, and
	// taking two locks in one order here while some other path takes them in
	// the other is how a deadlock gets built.
	s.owner.noteClosed(wasCancelled)
	return nil
}

// ScriptedPartials builds a script of n partials followed by one final.
//
// The commonest shape in these tests, and writing it out by hand in every test
// is how a subtle difference creeps into one of them.
func ScriptedPartials(texts []string, final string) []TranscriptSegment {
	out := make([]TranscriptSegment, 0, len(texts)+1)
	for i, text := range texts {
		out = append(out, TranscriptSegment{
			Sequence: uint64(i), Text: text, IsFinal: false,
			Confidence: 0.7, Role: RoleCaller,
			StartTime: 0, EndTime: time.Duration(i+1) * 200 * time.Millisecond,
		})
	}
	out = append(out, TranscriptSegment{
		Sequence: uint64(len(texts)), Text: final, IsFinal: true,
		Confidence: 0.95, Role: RoleCaller,
		StartTime: 0, EndTime: time.Duration(len(texts)+1) * 200 * time.Millisecond,
	})
	return out
}
