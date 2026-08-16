package speech

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// STTConfigDefaults are the orchestrator's queue bounds and deadlines.
type STTOrchestratorConfig struct {
	// Format is the audio the orchestrator accepts. A frame of any other format
	// is refused rather than resampled: this package does no DSP.
	Format media.AudioFormat

	// AudioQueue bounds frames awaiting submission to the provider.
	//
	// 50 frames is one second at 20 ms. Full behaviour: ErrBackpressure to the
	// caller. The caller is packages/go/media, which already expresses
	// backpressure to its producer, so propagating is both honest and bounded.
	AudioQueue int

	// TranscriptQueue bounds assembled segments awaiting the consumer.
	//
	// Full behaviour: ErrBackpressure. NOTHING ALREADY ACCEPTED IS DROPPED —
	// silently discarding an accepted transcript loses caller speech, which the
	// brief forbids outright and which no downstream consumer can detect.
	TranscriptQueue int

	// ProviderTimeout bounds opening a provider stream.
	ProviderTimeout time.Duration
}

// DefaultSTTOrchestratorConfig returns the telephony baseline.
func DefaultSTTOrchestratorConfig(format media.AudioFormat) STTOrchestratorConfig {
	return STTOrchestratorConfig{
		Format:          format,
		AudioQueue:      50,
		TranscriptQueue: 256,
		// ADR-0005 budgets 120 ms p50 / 250 ms p95 for the final transcript.
		// Two seconds to merely OPEN a stream is far outside that, so a provider
		// slower than this has already lost the turn.
		ProviderTimeout: 2 * time.Second,
	}
}

func (c STTOrchestratorConfig) validate() []string {
	var problems []string
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.AudioQueue <= 0 {
		problems = append(problems, "stt: AudioQueue must be positive")
	}
	if c.TranscriptQueue <= 0 {
		problems = append(problems, "stt: TranscriptQueue must be positive")
	}
	if c.ProviderTimeout <= 0 {
		problems = append(problems, "stt: ProviderTimeout must be positive")
	}
	return problems
}

// STTOrchestrator drives one recognition stream at a time.
//
// # The AudioInputAdapter is Push, and it clones
//
// media.Frame payloads are borrowed from a ring buffer that is overwritten as
// it wraps. This is the boundary where retention begins — the provider stream
// outlives the call that delivered the frame — so every frame is cloned on
// entry. Without that, a provider receives audio from a later point in the
// stream than the one it was handed, which is the Phase 11B borrowed-payload
// hazard reaching into this package.
//
// # Backpressure is expressed, never absorbed
//
// Both queues are bounded and both refuse with ErrBackpressure when full.
// Neither silently drops. A dropped frame is audio the caller spoke and nobody
// will ever hear; a dropped transcript is a sentence that vanishes between
// recognition and the conversation engine. Refusing is recoverable; discarding
// is not.
type STTOrchestrator struct {
	cfg       STTOrchestratorConfig
	router    *ProviderRouter
	assembler *TranscriptAssembler
	turns     *SpeechTurnManager
	clock     rt.Clock
	metrics   *SpeechMetrics

	segments chan TranscriptSegment

	mu       sync.Mutex
	stream   STTStream
	provider ProviderID
	turn     TurnID
	lang     Language
	started  bool
	closed   bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// startedAt stamps stream open, for the first-partial measurement.
	startedAt time.Time
	// sawPartial guards the first-partial histogram against being observed once
	// per partial rather than once per turn.
	sawPartial bool
	// endOfSpeechAt stamps the endpoint, which is where the ADR-0005 budget
	// starts counting.
	endOfSpeechAt time.Time
	haveEOS       bool
}

// NewSTTOrchestrator builds an orchestrator.
func NewSTTOrchestrator(
	cfg STTOrchestratorConfig,
	router *ProviderRouter,
	assembler *TranscriptAssembler,
	turns *SpeechTurnManager,
	clock rt.Clock,
	m *SpeechMetrics,
) (*STTOrchestrator, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if router == nil || assembler == nil || turns == nil {
		return nil, fmt.Errorf("%w: stt orchestrator needs a router, an assembler and a turn manager",
			ErrInternalFailure)
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if m == nil {
		m = NewSpeechMetrics()
	}
	return &STTOrchestrator{
		cfg: cfg, router: router, assembler: assembler, turns: turns,
		clock: clock, metrics: m,
		segments: make(chan TranscriptSegment, cfg.TranscriptQueue),
	}, nil
}

// Segments yields assembled segments. Closed when the orchestrator closes.
func (o *STTOrchestrator) Segments() <-chan TranscriptSegment { return o.segments }

// Provider returns the provider currently serving, if any.
func (o *STTOrchestrator) Provider() ProviderID {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.provider
}

// Start opens a recognition stream for a turn.
func (o *STTOrchestrator) Start(ctx context.Context, turn TurnID, lang Language) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return ErrSpeechSessionClosed
	}
	if o.started {
		o.mu.Unlock()
		return fmt.Errorf("%w: a recognition stream is already open for turn %s",
			ErrInternalFailure, o.turn)
	}
	o.mu.Unlock()

	provider, err := o.router.PickSTT(lang)
	if err != nil {
		return err
	}

	openCtx, cancel := context.WithCancel(ctx)
	cfg := STTConfig{
		Session:  o.assembler.Session(),
		Turn:     turn,
		Language: lang,
		Format:   o.cfg.Format,
		Timeout:  o.cfg.ProviderTimeout,
	}

	stream, err := provider.OpenSTT(openCtx, cfg)
	if err != nil {
		cancel()
		outcome := OutcomeFailure
		if errors.Is(err, ErrProviderTimeout) {
			outcome = OutcomeTimeout
		} else if errors.Is(err, ErrProviderRateLimited) {
			outcome = OutcomeRateLimited
		}
		o.router.Report(provider.ID(), outcome)
		return fmt.Errorf("open stt on %s: %w", provider.ID(), err)
	}

	o.mu.Lock()
	o.stream = stream
	o.provider = provider.ID()
	o.turn = turn
	o.lang = lang
	o.started = true
	o.cancel = cancel
	o.startedAt = o.clock.Now()
	o.sawPartial = false
	o.haveEOS = false
	o.mu.Unlock()

	o.metrics.STTStreams.Add(1)

	o.wg.Add(1)
	go o.consume(openCtx, stream, provider.ID(), turn, lang)
	return nil
}

// consume drains provider results into the assembler.
//
// Exits on ctx cancellation or on the provider closing its channel. Those are
// the only two ways out, which is what guarantees no goroutine outlives the
// session.
func (o *STTOrchestrator) consume(
	ctx context.Context, stream STTStream, provider ProviderID, turn TurnID, lang Language,
) {
	defer o.wg.Done()
	defer o.metrics.STTStreams.Add(-1)

	results := stream.Results()
	for {
		select {
		case <-ctx.Done():
			return
		case seg, ok := <-results:
			if !ok {
				o.router.Report(provider, OutcomeSuccess)
				return
			}
			o.handle(seg, turn, lang)
		}
	}
}

// handle applies one provider result.
func (o *STTOrchestrator) handle(seg TranscriptSegment, turn TurnID, lang Language) {
	// A result for a turn other than the live one is stale by construction —
	// the provider is answering a question we stopped asking.
	if seg.Turn != turn {
		o.metrics.AssemblyRejections.Add(1, string(ReasonOutOfOrder))
		return
	}
	if seg.Language == LangUnknown {
		seg.Language = lang
	}

	res, err := o.assembler.Apply(seg)
	if err != nil {
		o.metrics.AssemblyRejections.Add(1, string(ReasonOutOfOrder))
		return
	}
	if !res.Applied {
		o.metrics.AssemblyRejections.Add(1, string(res.Reason))
		return
	}

	label := seg.Language.Label()
	if seg.IsFinal {
		o.metrics.FinalsReceived.Add(1, label)
		o.observeFinalLatency(label)
		_ = o.turns.Transition(turn, TurnFinal, "final_transcript")
	} else {
		o.metrics.PartialsReceived.Add(1, label)
		o.observeFirstPartial(label)
		_ = o.turns.NotePartial(turn)
	}

	select {
	case o.segments <- seg:
	default:
		// The consumer is behind. Refusing here would mean dropping a segment
		// we already committed to the transcript, so this is counted and the
		// segment remains retrievable from the assembler — the transcript is
		// the record, the channel is only a notification.
		o.metrics.BackpressureEvents.Add(1, "transcript")
	}
}

// observeFirstPartial records time-to-first-partial once per turn.
func (o *STTOrchestrator) observeFirstPartial(label string) {
	o.mu.Lock()
	if o.sawPartial {
		o.mu.Unlock()
		return
	}
	o.sawPartial = true
	started := o.startedAt
	o.mu.Unlock()

	o.metrics.FirstPartialLatency.Observe(o.clock.Since(started).Seconds(), label)
}

// observeFinalLatency records time from end-of-speech to final transcript.
//
// Measured from the ENDPOINT, not from stream open, because that is what
// ADR-0005 budgets at 120 ms p50 / 250 ms p95. Measuring from stream open would
// include however long the caller spoke and make the number meaningless.
func (o *STTOrchestrator) observeFinalLatency(label string) {
	o.mu.Lock()
	have, at := o.haveEOS, o.endOfSpeechAt
	o.mu.Unlock()

	if !have {
		return
	}
	o.metrics.FinalTranscriptLatency.Observe(o.clock.Since(at).Seconds(), label)
}

// Push submits one frame. This is the AudioInputAdapter.
func (o *STTOrchestrator) Push(f media.Frame) error {
	o.mu.Lock()
	stream, started, closed := o.stream, o.started, o.closed
	o.mu.Unlock()

	if closed {
		return ErrSpeechSessionClosed
	}
	if !started || stream == nil {
		return fmt.Errorf("%w: no recognition stream is open", ErrSpeechSessionClosed)
	}

	if f.Format != o.cfg.Format {
		return fmt.Errorf("%w: frame is %s, orchestrator accepts %s",
			ErrInvalidAudio, f.Format, o.cfg.Format)
	}
	if err := f.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAudio, err)
	}

	// CLONED. The payload is borrowed from a ring that will overwrite it; the
	// provider stream outlives this call.
	if err := stream.Write(f.Clone()); err != nil {
		if errors.Is(err, ErrBackpressure) {
			o.metrics.BackpressureEvents.Add(1, "audio")
		}
		return err
	}
	return nil
}

// EndOfSpeech signals that the caller stopped speaking.
//
// # This is the VAD boundary, not a VAD
//
// Endpoint detection is ADR-0005 C6's — "we own endpointing, vendor endpointing
// is disabled or ignored" — and it lives outside this package. This method is
// the seam it calls. No voice activity detection is implemented here.
func (o *STTOrchestrator) EndOfSpeech() error {
	o.mu.Lock()
	stream, started, closed := o.stream, o.started, o.closed
	turn := o.turn
	if started && !closed {
		o.endOfSpeechAt = o.clock.Now()
		o.haveEOS = true
	}
	o.mu.Unlock()

	if closed {
		return ErrSpeechSessionClosed
	}
	if !started || stream == nil {
		return fmt.Errorf("%w: no recognition stream is open", ErrSpeechSessionClosed)
	}

	// The turn may legitimately already be finalizing if the endpoint fired
	// twice; a refused transition is not an error here.
	_ = o.turns.Transition(turn, TurnFinalizing, "end_of_speech")
	return stream.CloseSend()
}

// Cancel abandons recognition.
func (o *STTOrchestrator) Cancel(reason string) error {
	o.mu.Lock()
	if !o.started {
		o.mu.Unlock()
		return nil
	}
	stream, cancel := o.stream, o.cancel
	o.stream, o.started, o.cancel = nil, false, nil
	o.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stream != nil {
		_ = stream.Close()
	}
	o.wg.Wait()

	o.metrics.Cancellations.Add(1, "stt")
	return nil
}

// Close shuts the orchestrator down permanently.
func (o *STTOrchestrator) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()

	_ = o.Cancel("close")

	close(o.segments)
	return nil
}
