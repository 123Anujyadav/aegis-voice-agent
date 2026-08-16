package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Sink receives chunks from a dispatcher.
//
// CONTRACT. Write must not block indefinitely. The dispatcher checks for abort
// between sink writes, so a sink that blocks forever holds the barge-in budget
// open and breaks the 20 ms guarantee for every other sink on the same stream.
// A sink that cannot keep up must drop, buffer with a bound, or fail — see
// [BufferedSink], which does the first two.
//
// Implementations must be safe for concurrent Close, which may be called from
// the dispatcher's pump or from an aborting goroutine.
type Sink interface {
	// Write delivers one chunk. A returned error detaches the sink from the
	// stream; it does not fail the stream, because one slow consumer must not
	// be able to kill a live call for everyone else.
	Write(Chunk) error

	// Close ends the sink. err is nil on normal completion, ErrAborted on
	// barge-in, or the terminating error. Close is called exactly once per
	// sink per stream and must be idempotent regardless.
	Close(err error)

	// AcceptsThinking reports whether this sink may receive ChunkThinking.
	//
	// INVARIANT INV-AI-10: chain-of-thought is never persisted, never
	// published in an event, and never rendered to any user. The dispatcher
	// refuses to deliver thinking chunks to a sink that does not explicitly
	// opt in, so the default — a sink author who did not think about it — is
	// the safe one.
	AcceptsThinking() bool
}

// sinkBufferDepth is the per-sink handover buffer.
//
// Small on purpose. It absorbs a brief stall without letting a slow consumer
// accumulate an unbounded backlog of a live conversation — a sink 500 chunks
// behind is not going to catch up, and holding those chunks costs memory per
// concurrent session, which is the platform's actual capacity unit.
const sinkBufferDepth = 32

// DispatcherConfig tunes streaming behaviour.
type DispatcherConfig struct {
	// AbortBudget is the maximum time the dispatcher may take to stop
	// delivering after an abort. ADR-0011 fixes barge-in at one frame
	// interval, 20 ms.
	//
	// It is enforced as a measured assertion in the test suite rather than as a
	// runtime timeout: a timeout here would mean detecting the breach after it
	// had already happened, which does the caller no good.
	AbortBudget time.Duration

	// SinkWriteTimeout bounds one sink write. A sink exceeding it is detached
	// rather than waited on.
	SinkWriteTimeout time.Duration

	// MaxChunkGap is the longest silence permitted between chunks before the
	// stream is considered stalled. Zero disables the check.
	//
	// A stalled stream is distinct from a slow one: the provider has stopped
	// sending without closing, and without this the call hangs until the
	// overall deadline, wasting the entire remaining budget.
	MaxChunkGap time.Duration
}

// DefaultDispatcherConfig returns the configuration used unless overridden.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		AbortBudget:      20 * time.Millisecond,
		SinkWriteTimeout: 50 * time.Millisecond,
		MaxChunkGap:      2 * time.Second,
	}
}

func (c DispatcherConfig) validate() []string {
	var p []string
	if c.AbortBudget <= 0 {
		p = append(p, "dispatcher: AbortBudget must be positive")
	}
	if c.SinkWriteTimeout <= 0 {
		p = append(p, "dispatcher: SinkWriteTimeout must be positive")
	}
	if c.MaxChunkGap < 0 {
		p = append(p, "dispatcher: MaxChunkGap cannot be negative")
	}
	return p
}

// StreamResult reports how a dispatched stream ended.
type StreamResult struct {
	// StreamID identifies the stream.
	StreamID StreamID

	// Chunks counts delivered chunks of every kind.
	Chunks int

	// Usage is the accumulated token accounting.
	Usage Usage

	// TimeToFirstToken measures request start to first ChunkText.
	//
	// The metric that matters on a conversational path. Total duration is
	// nearly irrelevant to how responsive a call feels; the gap before the
	// first syllable is everything.
	TimeToFirstToken time.Duration

	// Duration is the total stream lifetime.
	Duration time.Duration

	// Aborted reports whether the stream ended by barge-in.
	Aborted bool

	// AbortLatency measures abort request to delivery stopping. Recorded for
	// every abort so the 20 ms budget is observed in production, not only
	// asserted in test.
	AbortLatency time.Duration

	// Err is the terminating error, nil on normal completion.
	Err error
}

// Dispatcher pumps one provider stream to one or more sinks.
//
// One dispatcher serves one stream. It is not reusable: a new stream gets a new
// dispatcher, because the alternative is a reset path that must clear a dozen
// fields correctly every time and will eventually not.
type Dispatcher struct {
	cfg     DispatcherConfig
	clock   Clock
	metrics *Metrics

	id StreamID

	mu    sync.Mutex
	sinks []Sink

	abortOnce sync.Once
	abortCh   chan struct{}
	abortAt   atomic.Int64 // unix nanos, 0 when not aborted

	started    atomic.Bool
	doneOnce   sync.Once
	done       chan struct{}
	result     StreamResult
	finalizers []func(StreamResult)
}

// OnComplete registers a function run when the stream ends, BEFORE [Done] is
// closed and before [Result] returns.
//
// The ordering is the point. Without it a caller waiting on Done observes
// completion while the runtime is still releasing the scheduler slot and
// closing out the session request — so a caller that admits work in a loop,
// gated on Done, over-admits by however many goroutines are mid-cleanup. That
// is a real capacity bug and it only appears under load, which is the worst
// combination.
//
// Finalizers must not block and may only be registered before [Run].
func (d *Dispatcher) OnComplete(fn func(StreamResult)) error {
	if d.started.Load() {
		return errors.New("runtime: cannot add a finalizer to a running dispatcher")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finalizers = append(d.finalizers, fn)
	return nil
}

// finish publishes the result, runs finalizers, and closes Done exactly once.
func (d *Dispatcher) finish(res StreamResult) {
	d.doneOnce.Do(func() {
		d.mu.Lock()
		d.result = res
		finalizers := make([]func(StreamResult), len(d.finalizers))
		copy(finalizers, d.finalizers)
		d.mu.Unlock()

		for _, fn := range finalizers {
			fn(res)
		}
		close(d.done)
	})
}

// NewDispatcher constructs a dispatcher for one stream.
func NewDispatcher(cfg DispatcherConfig, clock Clock, metrics *Metrics) (*Dispatcher, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &Dispatcher{
		cfg:     cfg,
		clock:   clock,
		metrics: metrics,
		id:      NewStreamID(),
		abortCh: make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// ID returns the dispatcher's stream identifier.
func (d *Dispatcher) ID() StreamID { return d.id }

// AddSink attaches a sink. Sinks may be added only before Run.
//
// Adding mid-stream is deliberately unsupported: a sink attached halfway
// through receives a partial stream, and every consumer would then have to
// handle the case where it missed the beginning. A late consumer should read
// the completed record instead.
func (d *Dispatcher) AddSink(s Sink) error {
	if d.started.Load() {
		return errors.New("runtime: cannot add a sink to a running dispatcher")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sinks = append(d.sinks, s)
	return nil
}

// Abort stops delivery immediately.
//
// It is safe to call from any goroutine, at any time, any number of times. It
// does not block: it signals and returns, so the caller — typically a barge-in
// detector on the audio path — is never delayed by the dispatcher's teardown.
//
// This is the entry point for the ADR-0011 barge-in guarantee. Everything
// downstream of it is built to observe the signal within one select iteration.
func (d *Dispatcher) Abort() {
	d.abortOnce.Do(func() {
		d.abortAt.Store(d.clock.Now().UnixNano())
		close(d.abortCh)
	})
}

// Aborted reports whether Abort has been called.
func (d *Dispatcher) Aborted() bool { return d.abortAt.Load() != 0 }

// Done returns a channel closed when Run completes.
func (d *Dispatcher) Done() <-chan struct{} { return d.done }

// Result returns the stream outcome. It is valid only after Done is closed.
func (d *Dispatcher) Result() StreamResult {
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.result
}

// recvResult carries one provider read across the preemption boundary.
type recvResult struct {
	chunk Chunk
	err   error
}

// Run pumps the stream to completion, abort, or error.
//
// It blocks until the stream ends and closes every sink exactly once. The
// supplied stream is closed before Run returns, whatever the outcome.
//
// # How the 20 ms abort budget is met
//
// TokenStream.Recv blocks. A pump that called it directly could not observe an
// abort until the provider happened to send something, which on a stalled
// stream is never. So Recv runs on a separate goroutine and delivers through a
// channel, and the pump selects over {abort, context, delivery}. An abort is
// therefore observed at the next select — microseconds — rather than at the
// next chunk.
//
// The reader goroutine is then unblocked by closing the underlying stream. It
// is not leaked: Close causes the in-flight Recv to return, the goroutine sees
// its done channel closed, and it exits. This is asserted by a leak check in
// the test suite rather than assumed.
func (d *Dispatcher) Run(ctx context.Context, stream TokenStream) StreamResult {
	if d.started.Swap(true) {
		return StreamResult{StreamID: d.id, Err: errors.New("runtime: dispatcher already run")}
	}

	// res is declared before the defer so the deferred finish observes the
	// final value, including on a panic path.
	res := StreamResult{StreamID: d.id}
	defer func() { d.finish(res) }()

	start := d.clock.Now()

	d.mu.Lock()
	sinks := make([]Sink, len(d.sinks))
	copy(sinks, d.sinks)
	d.mu.Unlock()

	// live tracks which sinks are still attached. A sink that errors or times
	// out is detached and the stream continues for everyone else.
	writers := make([]*sinkWriter, len(sinks))
	live := make([]bool, len(sinks))
	for i, s := range sinks {
		writers[i] = newSinkWriter(s, sinkBufferDepth)
		live[i] = true
	}

	readerDone := make(chan struct{})
	recvCh := make(chan recvResult, 1)
	go func() {
		defer close(recvCh)
		for {
			chunk, err := stream.Recv()
			select {
			case recvCh <- recvResult{chunk, err}:
			case <-readerDone:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Ensure the reader is unblocked and the stream released on every exit
	// path, including panic.
	defer func() {
		close(readerDone)
		_ = stream.Close()
	}()

	var (
		gapTimer Timer
		gapC     <-chan time.Time
	)
	if d.cfg.MaxChunkGap > 0 {
		gapTimer = d.clock.NewTimer(d.cfg.MaxChunkGap)
		gapC = gapTimer.C()
		defer gapTimer.Stop()
	}

	firstTokenSeen := false

pump:
	for {
		select {
		case <-d.abortCh:
			res.Aborted = true
			res.Err = ErrAborted
			res.AbortLatency = d.clock.Now().Sub(time.Unix(0, d.abortAt.Load()))
			d.metrics.StreamAbortLatency.Observe(res.AbortLatency.Seconds())
			if res.AbortLatency > d.cfg.AbortBudget {
				// Recorded, not returned as a failure. The abort succeeded;
				// it was merely slower than budget, and turning that into an
				// error would fail a call that actually worked.
				d.metrics.StreamAbortBudgetExceeded.Inc()
			}
			break pump

		case <-ctx.Done():
			// context.Cause, not ctx.Err. The kernel delivers a budget
			// exhaustion as a cancellation cause so it stays distinguishable
			// from the caller simply going away; ctx.Err would flatten both to
			// "canceled" and lose the distinction that matters operationally.
			res.Err = context.Cause(ctx)
			break pump

		case <-gapC:
			res.Err = ErrBudgetExceeded
			d.metrics.StreamStalled.Inc()
			break pump

		case r, ok := <-recvCh:
			if !ok {
				break pump
			}
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					res.Err = nil
				} else {
					res.Err = r.err
				}
				break pump
			}

			if gapTimer != nil {
				gapTimer.Stop()
				gapTimer.Reset(d.cfg.MaxChunkGap)
				gapC = gapTimer.C()
			}

			chunk := r.chunk
			if chunk.ReceivedAt.IsZero() {
				chunk.ReceivedAt = d.clock.Now()
			}
			res.Chunks++

			switch chunk.Kind {
			case ChunkUsage:
				res.Usage.Add(chunk.Usage)
			case ChunkText:
				if !firstTokenSeen {
					firstTokenSeen = true
					res.TimeToFirstToken = chunk.ReceivedAt.Sub(start)
					d.metrics.StreamTimeToFirstToken.Observe(res.TimeToFirstToken.Seconds())
				}
			}

			d.deliver(writers, live, chunk)

			if chunk.Kind == ChunkDone {
				break pump
			}
		}
	}

	res.Duration = d.clock.Since(start)

	// Stop each writer before closing its sink, so every chunk already handed
	// over is delivered before the sink is told the stream ended. Closing first
	// would race the writer and silently drop the tail of the response.
	closeErr := res.Err
	for i, w := range writers {
		if !live[i] {
			continue
		}
		w.stop(d.clock, d.cfg.SinkWriteTimeout)
		w.sink.Close(closeErr)
	}

	d.metrics.StreamChunks.Add(float64(res.Chunks))
	d.metrics.StreamDuration.Observe(res.Duration.Seconds())
	if res.Aborted {
		d.metrics.StreamAborted.Inc()
	} else if res.Err != nil {
		d.metrics.StreamFailed.Inc()
	} else {
		d.metrics.StreamCompleted.Inc()
	}

	// The deferred finish publishes res, runs finalizers and closes Done.
	return res
}

// sinkWriter owns one sink's delivery goroutine for the life of a stream.
//
// WHY A GOROUTINE PER SINK PER STREAM, NOT PER WRITE.
//
// The first version of this dispatcher spawned a goroutine and allocated a
// timer for every write, so that a blocking sink could be abandoned without
// holding the barge-in budget open. It worked, and it cost 482 allocations to
// deliver a 64-token response — roughly 2.6 microseconds and 7 allocations per
// chunk, all of it on the hot path of every call the platform makes.
//
// This shape gets the same guarantee for one goroutine per sink per stream. The
// dispatcher hands a chunk over a buffered channel; the writer calls Write. If
// the sink is slow the buffer fills, the dispatcher's send fails, and the sink
// is detached — the same outcome, discovered by a channel send rather than by a
// timer. The common case is a non-blocking send: no goroutine, no timer, no
// allocation.
type sinkWriter struct {
	sink      Sink
	ch        chan Chunk
	done      chan struct{}
	closeOnce sync.Once
}

// newSinkWriter starts the writer goroutine.
func newSinkWriter(s Sink, depth int) *sinkWriter {
	w := &sinkWriter{
		sink: s,
		ch:   make(chan Chunk, depth),
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		for c := range w.ch {
			if err := s.Write(c); err != nil {
				// Drain without writing so the dispatcher's sends keep
				// succeeding until it notices the detach. Blocking here would
				// stall the stream for every other sink.
				for range w.ch {
				}
				return
			}
		}
	}()
	return w
}

// stop closes the handover channel and waits, BOUNDED, for the writer to drain.
//
// The bound is not optional. A sink blocked inside Write — the pathological
// consumer this whole design defends against — will never return, and an
// unbounded wait here converts one misbehaving consumer into a hung stream for
// everyone. That is the exact failure the per-write timeout used to prevent,
// and it had to be preserved when the timeout moved off the hot path.
//
// It reports whether the writer drained. On false the goroutine is abandoned:
// it exits on its own once Write finally returns and it observes the closed
// channel. The sink is then closed while a Write may still be in flight, which
// is why the Sink contract requires Close to be safe concurrently with Write.
func (w *sinkWriter) stop(clock Clock, timeout time.Duration) bool {
	w.closeOnce.Do(func() { close(w.ch) })

	timer := clock.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-w.done:
		return true
	case <-timer.C():
		return false
	}
}

// deliver writes one chunk to every live sink, honouring the thinking rule and
// bounding the time any one sink may take.
//
// It checks for abort between sinks so a fan-out to many sinks cannot extend
// the abort budget by the number of consumers.
func (d *Dispatcher) deliver(writers []*sinkWriter, live []bool, chunk Chunk) {
	for i, w := range writers {
		if !live[i] {
			continue
		}

		// Abort observed mid-fanout: stop delivering immediately. The
		// already-written sinks keep what they got; the rest get nothing more,
		// which is exactly what barge-in means.
		select {
		case <-d.abortCh:
			return
		default:
		}

		// INVARIANT INV-AI-10. A sink that has not opted in never sees
		// thinking output. This is a filter, not an error: a text sink
		// receiving a stream that happens to contain thinking is normal, and
		// failing it would break every ordinary consumer.
		if chunk.Kind == ChunkThinking && !w.sink.AcceptsThinking() {
			continue
		}

		// Fast path: buffer has room. One channel send, zero allocations.
		select {
		case w.ch <- chunk:
			continue
		default:
		}

		// Slow path: the sink has fallen behind far enough to fill its buffer.
		// Give it a bounded grace period before detaching, so a brief stall is
		// survivable and a sustained one is not.
		if !d.timedSend(w, chunk) {
			live[i] = false
			w.stop(d.clock, d.cfg.SinkWriteTimeout)
			w.sink.Close(errors.New("runtime: sink fell behind and was detached"))
			d.metrics.SinkDetached.Inc()
		}
	}
}

// timedSend attempts a bounded send, reporting whether it succeeded.
func (d *Dispatcher) timedSend(w *sinkWriter, chunk Chunk) bool {
	timer := d.clock.NewTimer(d.cfg.SinkWriteTimeout)
	defer timer.Stop()

	select {
	case w.ch <- chunk:
		return true
	case <-timer.C():
		return false
	case <-d.abortCh:
		// Not a detach: the stream is ending anyway, and marking the sink as
		// failed here would report a consumer problem that does not exist.
		return true
	}
}

// ---------------------------------------------------------------------------
// Sinks
// ---------------------------------------------------------------------------

// BufferedSink is a Sink with a bounded queue and an explicit overflow policy.
//
// It is the sink most consumers should use. A consumer that reads directly on
// the dispatcher's goroutine couples its own speed to the call's latency;
// buffering decouples them, and bounding the buffer means a slow consumer
// degrades itself rather than the call.
type BufferedSink struct {
	ch       chan Chunk
	thinking bool
	dropped  atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}
	err       atomic.Pointer[error]
}

// NewBufferedSink returns a sink with the given queue depth.
//
// acceptThinking must be true for the sink to receive ChunkThinking, and should
// be false for anything that persists, transmits or renders (INV-AI-10).
func NewBufferedSink(depth int, acceptThinking bool) *BufferedSink {
	if depth <= 0 {
		depth = 64
	}
	return &BufferedSink{
		ch:       make(chan Chunk, depth),
		thinking: acceptThinking,
		closed:   make(chan struct{}),
	}
}

// Write enqueues a chunk, dropping it if the queue is full.
//
// Dropping rather than blocking is the correct trade for a live call: a
// consumer that has fallen behind will not catch up by having the call wait for
// it, and the drop is counted so the degradation is visible rather than silent.
func (b *BufferedSink) Write(c Chunk) error {
	select {
	case <-b.closed:
		return ErrClosed
	default:
	}
	select {
	case b.ch <- c:
		return nil
	default:
		b.dropped.Add(1)
		return nil
	}
}

// Close ends the sink. Idempotent.
func (b *BufferedSink) Close(err error) {
	b.closeOnce.Do(func() {
		if err != nil {
			b.err.Store(&err)
		}
		close(b.closed)
		close(b.ch)
	})
}

// AcceptsThinking reports whether this sink opted into thinking chunks.
func (b *BufferedSink) AcceptsThinking() bool { return b.thinking }

// Chunks returns the receive channel. It is closed when the sink closes.
func (b *BufferedSink) Chunks() <-chan Chunk { return b.ch }

// Dropped reports how many chunks were discarded through overflow.
func (b *BufferedSink) Dropped() int64 { return b.dropped.Load() }

// Err returns the terminating error, or nil.
func (b *BufferedSink) Err() error {
	if p := b.err.Load(); p != nil {
		return *p
	}
	return nil
}

// FuncSink adapts a function to the Sink interface. The function must not
// block; see the Sink contract.
type FuncSink struct {
	// OnChunk receives each chunk.
	OnChunk func(Chunk) error

	// OnClose receives the terminating error. Optional.
	OnClose func(error)

	// Thinking opts the sink into ChunkThinking. Defaults to false, which is
	// the safe default under INV-AI-10.
	Thinking bool

	once sync.Once
}

// Write delivers a chunk.
func (f *FuncSink) Write(c Chunk) error {
	if f.OnChunk == nil {
		return nil
	}
	return f.OnChunk(c)
}

// Close ends the sink. Idempotent.
func (f *FuncSink) Close(err error) {
	f.once.Do(func() {
		if f.OnClose != nil {
			f.OnClose(err)
		}
	})
}

// AcceptsThinking reports the sink's opt-in.
func (f *FuncSink) AcceptsThinking() bool { return f.Thinking }
