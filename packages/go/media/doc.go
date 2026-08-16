// Package media implements the Aegis AI media streaming engine.
//
// # What this is
//
// Real-time audio transport between a producer and a consumer. A [MediaRuntime]
// owns [Stream] instances; each stream carries [Frame] values through a
// [RingBuffer] and a [Pipeline] that validates, orders and delivers them.
//
//	Carrier Adapter → Media Source → THIS → Audio Buffer → Pipeline → STT/TTS
//
// # What this is not
//
// There is no RTP, no WebRTC, no SIP, no socket, no codec, no resampler, no
// voice activity detection, no speech recognition and no synthesis. Not "not
// yet" — those are other layers and their absence here is the design.
//
// The distinction that matters: this package moves a frame of PCM from one side
// to the other, on time, in order, under backpressure. It does not know what a
// packet looks like on a wire, and it does not know what the samples mean. If
// this package ever needs to parse an RTP header, something has gone wrong
// upstream of it.
//
// # The performance regime is different from every other module
//
// Twenty-millisecond frames means fifty frames per second per stream. A
// thousand concurrent streams is fifty thousand frames per second, each one
// carrying 320 to 1,920 bytes of PCM.
//
// At that rate ALLOCATION IS THE DESIGN CONSTRAINT, not throughput. A single
// allocation per frame is fifty thousand allocations per second of garbage that
// the collector must chase, and GC pauses in a media path are audible.
//
// # What is actually zero-allocation, and what is not
//
// The buffer path is zero-allocation and TestZeroAllocation_SteadyState fails if
// that changes: ring write (37 ns), ring read, ring peek (22 ns), frame validate
// (7.9 ns) and frame duration (1.7 ns) all measure 0 allocs/op.
//
// THE FULL FRAME PATH IS NOT. It costs about two allocations and 415 bytes per
// frame, because [JitterBuffer] clones on Put — it retains frames across calls,
// and borrowing the caller's payload there would be exactly the hazard [Frame]
// warns about. At a thousand streams that is roughly 100,000 allocations and
// 20 MB/s of garbage.
//
// That cost is deliberate and it is the largest single optimisation left in this
// package. Removing it means giving the jitter buffer its own backing array, the
// way [RingBuffer] already has one. It is not done here because the jitter
// buffer reorders — frames move position after insertion — and a contiguous
// arena that supports reordering is a materially harder structure than a ring.
// PERFORMANCE.md carries the measurements and the analysis.
//
// The mechanism for the part that is zero-allocation is [FramePool] plus a ring
// buffer that owns its sample storage and never reallocates. See [Frame] on why
// frame payloads are borrowed rather than owned, which is the sharpest edge in
// this package.
//
// # No implicit transitions
//
// A stream is in exactly one of nine states, and every legal move is declared
// in [transitionSpec]. A transition not declared there is refused; a malformed
// table is refused at construction by runtime.NewFSM. Nothing assigns a state
// directly. This mirrors Phase 11A, for the same reason: a switch statement
// encodes transitions in the places that perform them, so "can a paused stream
// be drained" is answered by reading every call site.
//
// # Determinism
//
// Every clock is injected. Jitter windows, drift detection, frame deadlines and
// recovery timeouts all measure against [runtime.Clock], so a test advances a
// FakeClock and observes a late frame in microseconds without sleeping.
//
// Frame ordering is deterministic under a fixed input sequence, including the
// reordering the jitter buffer performs. Two runs of the same input produce the
// same output sequence and the same drop decisions.
package media
