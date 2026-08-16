package speech

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// TTSOrchestratorConfig configures synthesis.
type TTSOrchestratorConfig struct {
	// Format is the audio the orchestrator emits.
	Format media.AudioFormat

	// Voice and Prosody are the neutral delivery settings.
	Voice   VoiceID
	Prosody Prosody

	// Chunk configures the sentence boundary detector.
	Chunk ChunkConfig

	// ChunkQueue bounds text chunks awaiting synthesis.
	//
	// Full behaviour: ErrBackpressure. A reply longer than this many clauses is
	// a product bug, not a load condition.
	ChunkQueue int

	// FrameQueue bounds synthesised frames awaiting the consumer.
	//
	// Full behaviour: BLOCK on the context, NEVER DROP. See the type comment —
	// the asymmetry with ChunkQueue is deliberate.
	FrameQueue int

	// ProviderTimeout bounds opening a provider stream.
	ProviderTimeout time.Duration
}

// DefaultTTSOrchestratorConfig returns the telephony baseline.
func DefaultTTSOrchestratorConfig(format media.AudioFormat) TTSOrchestratorConfig {
	return TTSOrchestratorConfig{
		Format:     format,
		Voice:      "default",
		Prosody:    DefaultProsody(),
		Chunk:      DefaultChunkConfig(),
		ChunkQueue: 32,
		// 100 frames is two seconds at 20 ms — enough to absorb a scheduler
		// hiccup, short enough that a stalled consumer is detected quickly.
		FrameQueue: 100,
		// ADR-0007 budgets 90 ms p50 / 180 ms p95 to time-to-first-byte, so a
		// provider taking two seconds merely to open has lost the turn.
		ProviderTimeout: 2 * time.Second,
	}
}

func (c TTSOrchestratorConfig) validate() []string {
	var problems []string
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.Prosody.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	problems = append(problems, c.Chunk.validate()...)
	if c.ChunkQueue <= 0 {
		problems = append(problems, "tts: ChunkQueue must be positive")
	}
	if c.FrameQueue <= 0 {
		problems = append(problems, "tts: FrameQueue must be positive")
	}
	if c.ProviderTimeout <= 0 {
		problems = append(problems, "tts: ProviderTimeout must be positive")
	}
	return problems
}

// TTSOrchestrator turns response text into outbound audio frames.
//
// # Sentence-level streaming, because the budget requires it
//
// Synthesis begins on the first complete clause rather than on the whole reply.
// ADR-0007 is explicit that with this structure perceived latency is
// time-to-first-CLAUSE — roughly LLM first token plus TTS time-to-first-byte —
// and without it perceived latency is full generation plus full synthesis. The
// same providers with the wrong pipeline structure miss the budget outright.
//
// # The generation counter is what makes barge-in safe
//
// Cancellation increments a generation. Every frame is tagged with the
// generation it was produced under, and the output adapter discards any frame
// whose generation is stale. Without it, frames already in flight inside the
// provider stream leak out AFTER the caller interrupted — the agent talking
// over somebody who just interrupted it, which is the single most damaging
// audible failure this layer can produce.
//
// # The two queues behave differently, deliberately
//
// The chunk queue sheds load with ErrBackpressure. The frame queue does not
// drop at all; it blocks on the context. A dropped input chunk costs a clause
// the caller never hears, which is bad. A dropped output frame is a glitch
// inside a word they are already hearing, which is worse and is also
// undetectable downstream.
type TTSOrchestrator struct {
	cfg     TTSOrchestratorConfig
	router  *ProviderRouter
	turns   *SpeechTurnManager
	session SessionID
	clock   rt.Clock
	metrics *SpeechMetrics

	frames chan media.Frame

	// generation is read on every emitted frame, so it is atomic rather than
	// mutex-guarded: the emit path must not contend with a cancellation.
	generation atomic.Uint64

	mu       sync.Mutex
	stream   TTSStream
	provider ProviderID
	turn     TurnID
	speaking bool
	closed   bool
	cancel   context.CancelFunc
	pending  int
	wg       sync.WaitGroup
}

// NewTTSOrchestrator builds an orchestrator.
func NewTTSOrchestrator(
	cfg TTSOrchestratorConfig,
	router *ProviderRouter,
	turns *SpeechTurnManager,
	session SessionID,
	clock rt.Clock,
	m *SpeechMetrics,
) (*TTSOrchestrator, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if router == nil || turns == nil {
		return nil, fmt.Errorf("%w: tts orchestrator needs a router and a turn manager",
			ErrInternalFailure)
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if m == nil {
		m = NewSpeechMetrics()
	}
	return &TTSOrchestrator{
		cfg: cfg, router: router, turns: turns, session: session,
		clock: clock, metrics: m,
		frames: make(chan media.Frame, cfg.FrameQueue),
	}, nil
}

// Frames yields synthesised audio. This is the AudioOutputAdapter.
func (o *TTSOrchestrator) Frames() <-chan media.Frame { return o.frames }

// Generation returns the current generation. Frames from an earlier one are
// stale and are discarded.
func (o *TTSOrchestrator) Generation() uint64 { return o.generation.Load() }

// Provider returns the provider currently serving, if any.
func (o *TTSOrchestrator) Provider() ProviderID {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.provider
}

// Speaking reports whether synthesis is live.
func (o *TTSOrchestrator) Speaking() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.speaking
}

// Speak chunks text and streams synthesis for it.
//
// Returns once every chunk has been submitted; audio continues to arrive on
// [TTSOrchestrator.Frames] until synthesis completes or is cancelled.
func (o *TTSOrchestrator) Speak(ctx context.Context, turn TurnID, text string, lang Language) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return ErrSpeechSessionClosed
	}
	if o.speaking {
		o.mu.Unlock()
		return fmt.Errorf("%w: synthesis is already live for turn %s", ErrInternalFailure, o.turn)
	}
	o.mu.Unlock()

	provider, err := o.router.PickTTS(lang)
	if err != nil {
		return err
	}

	chunker, err := NewChunker(o.cfg.Chunk)
	if err != nil {
		return err
	}
	chunks := append(chunker.Push(text), chunker.Flush()...)
	if len(chunks) == 0 {
		return nil
	}
	if len(chunks) > o.cfg.ChunkQueue {
		o.metrics.BackpressureEvents.Add(1, "chunk")
		return fmt.Errorf("%w: %d chunks exceeds the queue bound of %d",
			ErrBackpressure, len(chunks), o.cfg.ChunkQueue)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	cfg := TTSConfig{
		Session: o.session, Turn: turn, Language: lang,
		Format: o.cfg.Format, Voice: o.cfg.Voice, Prosody: o.cfg.Prosody,
		Timeout: o.cfg.ProviderTimeout,
	}

	stream, err := provider.OpenTTS(streamCtx, cfg)
	if err != nil {
		cancel()
		outcome := OutcomeFailure
		if errors.Is(err, ErrProviderTimeout) {
			outcome = OutcomeTimeout
		} else if errors.Is(err, ErrProviderRateLimited) {
			outcome = OutcomeRateLimited
		}
		o.router.Report(provider.ID(), outcome)
		return fmt.Errorf("open tts on %s: %w", provider.ID(), err)
	}

	gen := o.generation.Load()

	o.mu.Lock()
	o.stream = stream
	o.provider = provider.ID()
	o.turn = turn
	o.speaking = true
	o.cancel = cancel
	o.pending = len(chunks)
	o.mu.Unlock()

	o.metrics.TTSStreams.Add(1)
	_ = o.turns.Transition(turn, TurnSpeaking, "synthesis_started")

	startedAt := o.clock.Now()
	o.wg.Add(1)
	go o.pump(streamCtx, stream, provider.ID(), gen, startedAt)

	// Submit every chunk. Synthesis of chunk one is already producing audio
	// while chunk three is still being submitted — that overlap is the point.
	for _, c := range chunks {
		if err := stream.Synthesize(c); err != nil {
			if errors.Is(err, ErrBackpressure) {
				o.metrics.BackpressureEvents.Add(1, "chunk")
			}
			_ = stream.CloseSend()
			return err
		}
		o.mu.Lock()
		o.pending--
		o.mu.Unlock()
	}
	return stream.CloseSend()
}

// pump moves provider audio to the output, discarding stale generations.
func (o *TTSOrchestrator) pump(
	ctx context.Context, stream TTSStream, provider ProviderID, gen uint64, startedAt time.Time,
) {
	defer o.wg.Done()
	defer o.metrics.TTSStreams.Add(-1)

	audio := stream.Audio()
	var first bool

	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-audio:
			if !ok {
				if o.generation.Load() == gen {
					o.router.Report(provider, OutcomeSuccess)
					o.mu.Lock()
					o.speaking = false
					o.mu.Unlock()
				}
				return
			}

			// THE STALE-FRAME GUARD. A cancellation bumped the generation, so
			// everything still in flight from this stream is audio the caller
			// has already interrupted. Dropping it here is the whole contract.
			if o.generation.Load() != gen {
				continue
			}

			if !first {
				first = true
				o.metrics.FirstAudioLatency.Observe(
					o.clock.Since(startedAt).Seconds(), string(provider))
			}

			// Blocks rather than drops. A dropped frame is a glitch inside a
			// word; the context bounds how long this can block.
			select {
			case o.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Cancel abandons synthesis and reports how many chunks never reached the
// provider.
//
// # Ordering matters
//
// The generation is bumped FIRST, so that frames already inside the provider
// stream are stale the instant this returns. Closing first would leave a window
// in which in-flight audio is still considered current.
func (o *TTSOrchestrator) Cancel(reason string) (int, error) {
	o.generation.Add(1)

	o.mu.Lock()
	if !o.speaking {
		o.mu.Unlock()
		return 0, nil
	}
	stream, cancel, dropped := o.stream, o.cancel, o.pending
	o.stream, o.speaking, o.cancel, o.pending = nil, false, nil, 0
	o.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stream != nil {
		_ = stream.Close()
	}
	o.wg.Wait()

	// Drain whatever reached the output before the generation moved. Leaving it
	// there would have the next turn's consumer read the previous turn's audio.
	for {
		select {
		case <-o.frames:
		default:
			o.metrics.Cancellations.Add(1, "tts")
			return dropped, nil
		}
	}
}

// Close shuts the orchestrator down permanently.
func (o *TTSOrchestrator) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()

	_, _ = o.Cancel("close")
	close(o.frames)
	return nil
}
