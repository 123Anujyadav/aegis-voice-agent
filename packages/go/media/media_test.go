package media

import (
	"context"
	"errors"
	"reflect"

	"strings"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func harness(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h, err := NewHarness(opts...)
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	return h
}

func started(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h := harness(t, opts...)
	if _, err := h.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _, _ = h.Stop(context.Background()) })
	return h
}

// ---------------------------------------------------------------------------
// Frame model
// ---------------------------------------------------------------------------

func TestFormat_BytesAndDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		format   AudioFormat
		duration time.Duration
		bytes    int
	}{
		{"8k mono pcm16 / 20ms", PCM16Mono8k(), 20 * time.Millisecond, 320},
		{"16k mono pcm16 / 20ms", PCM16Mono16k(), 20 * time.Millisecond, 640},
		{"48k stereo pcm16 / 20ms",
			AudioFormat{FormatPCM16, LayoutStereo, Rate48kHz, CodecPCM},
			20 * time.Millisecond, 3840},
		{"8k mono pcm32 / 20ms",
			AudioFormat{FormatPCM32, LayoutMono, Rate8kHz, CodecPCM},
			20 * time.Millisecond, 640},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.format.BytesFor(tc.duration); got != tc.bytes {
				t.Errorf("BytesFor(%v) = %d, want %d", tc.duration, got, tc.bytes)
			}
			// Round trip: bytes back to duration must return what we started with.
			if got := tc.format.DurationFor(tc.bytes); got != tc.duration {
				t.Errorf("DurationFor(%d) = %v, want %v", tc.bytes, got, tc.duration)
			}
		})
	}
}

func TestFormat_ValidationReportsEveryProblem(t *testing.T) {
	t.Parallel()
	err := AudioFormat{}.Validate()
	if err == nil {
		t.Fatal("an empty format validated")
	}
	var cfg *ConfigError
	if !errors.As(err, &cfg) {
		t.Fatalf("err = %T, want *ConfigError", err)
	}
	// Format, layout and rate are all unset.
	if len(cfg.Problems) != 3 {
		t.Errorf("reported %d problems, want 3: %v", len(cfg.Problems), cfg.Problems)
	}
}

// TestFormat_OpaqueCodecHasNoDuration pins a deliberate refusal.
func TestFormat_OpaqueCodecHasNoDuration(t *testing.T) {
	t.Parallel()
	opaque := AudioFormat{FormatPCM16, LayoutMono, Rate8kHz, CodecOpaque}

	// Only a codec's own decoder knows how many samples a compressed payload
	// holds. Returning a plausible-looking wrong number would be worse than
	// returning nothing.
	if got := opaque.DurationFor(320); got != 0 {
		t.Errorf("DurationFor = %v for an opaque codec, want 0", got)
	}
	f := Frame{Sequence: 1, Format: opaque, Payload: make([]byte, 7)}
	// 7 bytes is not a multiple of the PCM16 stride, and must NOT be refused:
	// the stride does not apply to an opaque payload.
	if err := f.Validate(); err != nil {
		t.Errorf("an opaque frame was validated against a PCM stride: %v", err)
	}
}

func TestFrame_ValidateRefusesPartialSamples(t *testing.T) {
	t.Parallel()
	f := Frame{Sequence: 7, Format: PCM16Mono8k(), Payload: make([]byte, 321)}

	err := f.Validate()
	if err == nil {
		t.Fatal("a frame with a partial sample validated")
	}
	// A partial sample means the producer and this engine disagree about the
	// format, which is worth saying rather than silently truncating.
	if !strings.Contains(err.Error(), "stride") {
		t.Errorf("error does not explain the cause: %v", err)
	}
}

func TestFrame_ValidateRefusesEmpty(t *testing.T) {
	t.Parallel()
	if err := (Frame{Sequence: 1, Format: PCM16Mono8k()}).Validate(); err == nil {
		t.Error("an empty frame validated")
	}
}

func TestFrame_CloneOwnsItsPayload(t *testing.T) {
	t.Parallel()
	backing := []byte{1, 2, 3, 4}
	f := Frame{Sequence: 1, Format: PCM16Mono8k(), Payload: backing}

	clone := f.Clone()
	backing[0] = 99

	if clone.Payload[0] == 99 {
		t.Error("Clone shares the original payload; the escape hatch does not escape")
	}
}

func TestFrame_CloneIntoRefusesUndersizedDestination(t *testing.T) {
	t.Parallel()
	f := Frame{Sequence: 1, Format: PCM16Mono8k(), Payload: make([]byte, 320)}

	// A partial copy that silently truncated audio would be worse than a
	// refusal the caller can act on.
	if _, ok := f.CloneInto(make([]byte, 100)); ok {
		t.Error("CloneInto accepted a destination smaller than the payload")
	}
	if _, ok := f.CloneInto(make([]byte, 320)); !ok {
		t.Error("CloneInto refused an exactly-sized destination")
	}
}

// TestFrame_StringNeverCarriesAudio is a privacy check.
func TestFrame_StringNeverCarriesAudio(t *testing.T) {
	t.Parallel()
	// A recognisable byte pattern. A frame's bytes are somebody's voice; a log
	// line carrying them would be a recording nobody consented to.
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	f := Frame{Sequence: 1, Format: PCM16Mono8k(), Payload: payload}

	rendered := f.String()
	for _, forbidden := range []string{"222", "173", "\xde", "dead"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Errorf("String() = %q, which appears to carry payload bytes", rendered)
		}
	}
	if !strings.Contains(rendered, "4B") {
		t.Errorf("String() = %q, want the payload SIZE reported", rendered)
	}
}

func TestSilenceFrame_IsCorrectlySized(t *testing.T) {
	t.Parallel()
	format := PCM16Mono8k()
	f := SilenceFrame(5, 100*time.Millisecond, format, 20*time.Millisecond)

	if got := f.Duration(); got != 20*time.Millisecond {
		t.Errorf("Duration() = %v, want 20ms", got)
	}
	if !f.Flags.Has(FlagSilence) || !f.Flags.Has(FlagDiscontinuity) {
		t.Errorf("flags = %s, want silence and discontinuity", f.Flags)
	}
	for i, b := range f.Payload {
		if b != 0 {
			t.Fatalf("silence frame byte %d = %d, want 0", i, b)
		}
	}
}

// ---------------------------------------------------------------------------
// Ring buffer
// ---------------------------------------------------------------------------

func newBuffer(t *testing.T, capacity int, policy OverflowPolicy) *RingBuffer {
	t.Helper()
	cfg := DefaultBufferConfig(PCM16Mono8k())
	cfg.Capacity = capacity
	cfg.Policy = policy
	b, err := NewRingBuffer(cfg)
	if err != nil {
		t.Fatalf("new buffer: %v", err)
	}
	return b
}

func TestBuffer_WriteReadRoundTrip(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 4, DropNewest)
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	for i := 0; i < 3; i++ {
		if err := b.Write(gen.Next(time.Time{})); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := b.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}

	for i := 0; i < 3; i++ {
		f, err := b.Read()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if f.Sequence != uint64(i) {
			t.Errorf("frame %d has sequence %d", i, f.Sequence)
		}
	}
	if _, err := b.Read(); !errors.Is(err, ErrBufferEmpty) {
		t.Errorf("err = %v, want ErrBufferEmpty", err)
	}
}

// TestBuffer_ReadPayloadIsBorrowed demonstrates the sharpest edge in the
// package, so nobody has to discover it in production.
func TestBuffer_ReadPayloadIsBorrowed(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 2, DropNewest)
	format := PCM16Mono8k()

	first := Frame{Sequence: 1, Format: format, Payload: []byte{1, 1, 1, 1}}
	if err := b.Write(first); err != nil {
		t.Fatal(err)
	}

	got, err := b.Read()
	if err != nil {
		t.Fatal(err)
	}
	// Retaining the frame without cloning...
	retained := got

	// ...and then writing enough to wrap the backing array.
	for i := 0; i < 4; i++ {
		if err := b.Write(Frame{Sequence: uint64(i + 2), Format: format,
			Payload: []byte{9, 9, 9, 9}}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Read(); err != nil {
			t.Fatal(err)
		}
	}

	// The retained frame's bytes now belong to a different frame. This is the
	// documented contract, not a bug — and the reason Read's doc comment says
	// so in capitals.
	if retained.Payload[0] == 1 {
		t.Skip("the backing array happened not to be reused; the hazard is real regardless")
	}
	t.Logf("borrowed payload was overwritten as documented: %v", retained.Payload)

	// The safe path.
	b2 := newBuffer(t, 2, DropNewest)
	if err := b2.Write(first); err != nil {
		t.Fatal(err)
	}
	f, _ := b2.Read()
	safe := f.Clone()
	for i := 0; i < 4; i++ {
		_ = b2.Write(Frame{Sequence: uint64(i + 2), Format: format,
			Payload: []byte{9, 9, 9, 9}})
		_, _ = b2.Read()
	}
	if safe.Payload[0] != 1 {
		t.Error("a CLONED frame was corrupted; the escape hatch does not work")
	}
}

func TestBuffer_DropNewestKeepsBufferedAudio(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 2, DropNewest)
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	first := gen.Next(time.Time{}).Clone()
	if err := b.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(gen.Next(time.Time{})); err != nil {
		t.Fatal(err)
	}

	// Full. The buffered frames are older and closer to being played, so the
	// newest is the one to refuse.
	if err := b.Write(gen.Next(time.Time{})); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("err = %v, want ErrBufferFull", err)
	}
	got, err := b.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != first.Sequence {
		t.Errorf("head is sequence %d, want the oldest (%d) preserved",
			got.Sequence, first.Sequence)
	}
}

func TestBuffer_DropOldestEvictsHead(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 2, DropOldest)
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	for i := 0; i < 3; i++ {
		if err := b.Write(gen.Next(time.Time{})); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, err := b.Peek()
	if err != nil {
		t.Fatal(err)
	}
	// Sequence 0 was evicted to make room for 2.
	if got.Sequence != 1 {
		t.Errorf("head is sequence %d, want 1 after the oldest was evicted", got.Sequence)
	}
	if b.Stats().Dropped == 0 {
		t.Error("the eviction was not counted; a consumer seeing a sequence gap " +
			"could not tell a loss from a deliberate discard")
	}
}

func TestBuffer_RefusesFormatMismatchAndOversize(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 4, DropNewest)

	wrong := Frame{Sequence: 1, Format: PCM16Mono16k(), Payload: make([]byte, 320)}
	if err := b.Write(wrong); !errors.Is(err, ErrFormatMismatch) {
		t.Errorf("err = %v, want ErrFormatMismatch", err)
	}

	huge := Frame{Sequence: 1, Format: PCM16Mono8k(), Payload: make([]byte, 99999)}
	if err := b.Write(huge); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("err = %v, want ErrFrameTooLarge", err)
	}
}

// TestBuffer_UnderflowIsNotAFailure pins a semantic that is easy to get wrong.
func TestBuffer_UnderflowIsNotAFailure(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 4, DropNewest)

	// A consumer reading faster than a producer writes is the NORMAL state of a
	// healthy real-time stream. The buffer is supposed to be nearly empty.
	for i := 0; i < 5; i++ {
		if _, err := b.Read(); !errors.Is(err, ErrBufferEmpty) {
			t.Fatalf("err = %v, want ErrBufferEmpty", err)
		}
	}
	if got := b.Stats().Underflows; got != 5 {
		t.Errorf("underflows = %d, want 5 counted separately from drops", got)
	}
	if got := b.Stats().Dropped; got != 0 {
		t.Errorf("dropped = %d; an underflow is not a drop", got)
	}
}

func TestBuffer_DepthTracksAudioNotFrames(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 10, DropNewest)
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	for i := 0; i < 5; i++ {
		if err := b.Write(gen.Next(time.Time{})); err != nil {
			t.Fatal(err)
		}
	}
	// Frame count is misleading across formats; depth in time is what maps to
	// the latency a listener experiences.
	if got := b.Depth(); got != 100*time.Millisecond {
		t.Errorf("Depth() = %v, want 100ms", got)
	}
}

func TestBuffer_FlushAndDrainDifferPrecisely(t *testing.T) {
	t.Parallel()
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	b1 := newBuffer(t, 10, DropNewest)
	for i := 0; i < 4; i++ {
		_ = b1.Write(gen.Next(time.Time{}))
	}
	// Flush THROWS AWAY.
	if n := b1.Flush(); n != 4 {
		t.Errorf("Flush() = %d, want 4", n)
	}
	if b1.Len() != 0 {
		t.Error("Flush left frames behind")
	}

	gen.Reset()
	b2 := newBuffer(t, 10, DropNewest)
	for i := 0; i < 4; i++ {
		_ = b2.Write(gen.Next(time.Time{}))
	}
	// Drain HANDS OVER — and clones, because the caller keeps what it takes.
	frames := b2.Drain(0)
	if len(frames) != 4 {
		t.Fatalf("Drain returned %d frames, want 4", len(frames))
	}
	if b2.Len() != 0 {
		t.Error("Drain left frames behind")
	}
	// Overwrite the buffer thoroughly; drained frames must survive.
	for i := 0; i < 10; i++ {
		_ = b2.Write(gen.Next(time.Time{}))
	}
	if frames[0].Sequence != 0 {
		t.Error("a drained frame was corrupted; Drain must clone")
	}
}

func TestBuffer_SnapshotAndRestore(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 10, DropNewest)
	gen := NewFrameGenerator(PCM16Mono8k(), 20*time.Millisecond)

	for i := 0; i < 4; i++ {
		_ = b.Write(gen.Next(time.Time{}))
	}
	snap := b.Snapshot()

	if len(snap.Frames) != 4 {
		t.Fatalf("snapshot holds %d frames, want 4", len(snap.Frames))
	}
	// The snapshot must survive the buffer.
	b.Flush()
	if snap.Frames[0].Sequence != 0 {
		t.Error("the snapshot aliased the buffer's storage")
	}

	restored := newBuffer(t, 10, DropNewest)
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Len() != 4 {
		t.Errorf("restored buffer holds %d frames, want 4", restored.Len())
	}
	// Counters are NOT restored: they count what this instance did, and
	// carrying a previous instance's totals would make a drop rate describe two
	// buffers averaged together.
	if got := restored.Stats().Written; got != 0 {
		t.Errorf("restored Written = %d, want 0", got)
	}
}

func TestBuffer_RestoreRefusesMismatchAndOverflow(t *testing.T) {
	t.Parallel()
	b := newBuffer(t, 2, DropNewest)

	wrong := BufferSnapshot{Format: PCM16Mono16k()}
	if err := b.Restore(wrong); !errors.Is(err, ErrFormatMismatch) {
		t.Errorf("err = %v, want ErrFormatMismatch", err)
	}

	tooMany := BufferSnapshot{Format: PCM16Mono8k(), Frames: make([]Frame, 5)}
	if err := b.Restore(tooMany); !errors.Is(err, ErrBufferFull) {
		t.Errorf("err = %v, want ErrBufferFull", err)
	}
}

func TestBufferConfig_RefusesUnbounded(t *testing.T) {
	t.Parallel()
	cfg := DefaultBufferConfig(PCM16Mono8k())
	cfg.Capacity = 0

	_, err := NewRingBuffer(cfg)
	if err == nil {
		t.Fatal("an unbounded buffer was accepted")
	}
	if !strings.Contains(err.Error(), "stale audio") {
		t.Errorf("error does not explain the consequence: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Frame pool
// ---------------------------------------------------------------------------

func TestFramePool_RecyclesBuffers(t *testing.T) {
	t.Parallel()
	p := NewFramePool(320, 4)

	buf := p.Get()
	if len(buf) != 320 {
		t.Fatalf("Get() returned %d bytes, want 320", len(buf))
	}
	p.Put(buf)

	// The second Get must reuse rather than allocate.
	_ = p.Get()
	if got := p.Stats().Misses; got != 1 {
		t.Errorf("misses = %d, want 1 (only the first Get should miss)", got)
	}
}

func TestFramePool_DiscardsUndersizedBuffers(t *testing.T) {
	t.Parallel()
	p := NewFramePool(320, 4)

	// A pool holding mixed sizes hands out buffers that are sometimes too
	// small, and the symptom is a truncated frame rather than an error.
	p.Put(make([]byte, 10))
	if got := p.Stats().Available; got != 0 {
		t.Errorf("available = %d; an undersized buffer was retained", got)
	}
}

func TestFramePool_BoundsOccupancy(t *testing.T) {
	t.Parallel()
	p := NewFramePool(320, 2)
	for i := 0; i < 10; i++ {
		p.Put(make([]byte, 320))
	}
	if got := p.Stats().Available; got != 2 {
		t.Errorf("available = %d, want the bound of 2", got)
	}
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func TestState_NineStatesExist(t *testing.T) {
	t.Parallel()
	if got := len(AllStates()); got != 9 {
		t.Errorf("AllStates() has %d entries, the brief requires 9", got)
	}
	seen := map[StreamState]bool{}
	for _, s := range AllStates() {
		if seen[s] {
			t.Errorf("state %s is listed twice", s)
		}
		seen[s] = true
		if !s.Valid() {
			t.Errorf("state %s does not validate", s)
		}
	}
}

// TestState_TransitionTableIsComplete is the check that "no implicit
// transitions" is enforceable.
func TestState_TransitionTableIsComplete(t *testing.T) {
	t.Parallel()
	spec := transitionSpec()

	for _, s := range AllStates() {
		targets, present := spec[s]
		if !present {
			t.Errorf("state %s is absent from the transition table", s)
			continue
		}
		if s.Terminal() && len(targets) > 0 {
			t.Errorf("terminal state %s declares %d outgoing transitions", s, len(targets))
		}
		if !s.Terminal() && len(targets) == 0 {
			t.Errorf("non-terminal state %s has no outgoing transitions; a stream "+
				"reaching it would stop forever", s)
		}
		for _, to := range targets {
			if !to.Valid() {
				t.Errorf("%s -> %s names an undeclared state", s, to)
			}
			if to == s {
				t.Errorf("%s declares a self-transition", s)
			}
		}
	}
}

func TestState_EveryStateIsReachable(t *testing.T) {
	t.Parallel()
	// Recovering is exempt: it is an initial state entered by RestoreStream,
	// not by a transition — and it is also reachable from Active, so it is
	// listed here for completeness rather than exemption.
	reachable := map[StreamState]bool{StateIdle: true, StateRecovering: true}
	for _, targets := range transitionSpec() {
		for _, to := range targets {
			reachable[to] = true
		}
	}
	for _, s := range AllStates() {
		if !reachable[s] {
			t.Errorf("state %s is unreachable", s)
		}
	}
}

func TestState_TerminalAndTimeout(t *testing.T) {
	t.Parallel()
	if !StateClosed.Terminal() || !StateFailed.Terminal() {
		t.Error("Closed and Failed must be terminal")
	}
	// A timed-out stream may recover, and still holds a buffer that must be
	// released. Making it terminal would leave the release unmodelled.
	if StateTimeout.Terminal() {
		t.Error("Timeout is terminal; recovery and buffer release would be unmodelled")
	}
	if !StateTimeout.HoldsResources() {
		t.Error("Timeout does not hold resources; capacity accounting would under-count")
	}
}

// TestState_OnlyActiveAcceptsFrames pins the write gate.
func TestState_OnlyActiveAcceptsFrames(t *testing.T) {
	t.Parallel()
	for _, s := range AllStates() {
		want := s == StateActive
		if got := s.AcceptsFrames(); got != want {
			t.Errorf("%s.AcceptsFrames() = %v, want %v", s, got, want)
		}
	}
}

// TestState_PausedAndClosingStillDeliver pins the read gate.
func TestState_PausedAndClosingStillDeliver(t *testing.T) {
	t.Parallel()
	// A paused stream's consumer may drain what was already buffered, and a
	// closing stream delivering nothing would discard exactly the audio the
	// drain exists to save.
	for _, s := range []StreamState{StateActive, StatePaused, StateClosing} {
		if !s.DeliversFrames() {
			t.Errorf("%s.DeliversFrames() = false", s)
		}
	}
	for _, s := range []StreamState{StateIdle, StateOpening, StateClosed, StateFailed} {
		if s.DeliversFrames() {
			t.Errorf("%s.DeliversFrames() = true", s)
		}
	}
}

func TestState_ClosingCannotReturnToActive(t *testing.T) {
	t.Parallel()
	// A drain is a commitment. A stream that resumed mid-drain would deliver
	// frames a consumer had already been told were the last.
	if CanTransition(StateClosing, StateActive) {
		t.Error("a draining stream can return to Active")
	}
}

func TestState_PausedCannotGoDirectlyToRecovering(t *testing.T) {
	t.Parallel()
	// A paused stream's source is still attached; recovery reattaches one.
	// Going through Active makes the reattachment explicit.
	if CanTransition(StatePaused, StateRecovering) {
		t.Error("a paused stream can go straight to Recovering")
	}
}

func TestState_FSMRefusesUndeclaredTransition(t *testing.T) {
	t.Parallel()
	fsm, err := newStreamFSM(StateIdle, rt.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsm.To(StateActive); !errors.Is(err, rt.ErrInvalidTransition) {
		t.Errorf("err = %v, want ErrInvalidTransition — Idle to Active is not declared", err)
	}
	if fsm.State() != StateIdle {
		t.Errorf("state moved to %s after a refused transition", fsm.State())
	}
}

func TestState_StreamMayOnlyBeginIdleOrRecovering(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Now())
	for _, s := range []StreamState{StateIdle, StateRecovering} {
		if _, err := newStreamFSM(s, clock); err != nil {
			t.Errorf("a stream could not begin at %s: %v", s, err)
		}
	}
	// Fabricating an active stream that never opened must be impossible.
	for _, s := range []StreamState{StateActive, StatePaused, StateClosed} {
		if _, err := newStreamFSM(s, clock); err == nil {
			t.Errorf("a stream was allowed to begin at %s", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func TestIDs_AlphabetIsAsciiSortable(t *testing.T) {
	t.Parallel()
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	if len(alphabet) != 32 {
		t.Fatalf("alphabet has %d characters, base32 needs 32", len(alphabet))
	}
	// Phase 11A shipped an alphabet whose byte order did not match its base32
	// value order, silently breaking the sortability its documentation claimed.
	// Checking the alphabet cannot pass by luck; sampling identifiers can.
	for i := 1; i < len(alphabet); i++ {
		if alphabet[i] <= alphabet[i-1] {
			t.Fatalf("alphabet is not ascending at %d (%q then %q)",
				i, alphabet[i-1], alphabet[i])
		}
	}
	for _, r := range "ilou" {
		if strings.ContainsRune(alphabet, r) {
			t.Errorf("alphabet contains %q, which Crockford omits as ambiguous", r)
		}
	}
	if got := idAlphabet.EncodeToString([]byte{0, 0, 0, 0, 0}); got != "00000000" {
		t.Errorf("encoder produced %q for zero bytes; it is not the checked alphabet", got)
	}
}

func TestIDs_AreUniqueAndSortable(t *testing.T) {
	t.Parallel()
	seen := make(map[StreamID]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewStreamID()
		if seen[id] {
			t.Fatalf("duplicate identifier after %d mints", i)
		}
		seen[id] = true
		if !id.Valid() {
			t.Fatalf("identifier %q does not validate", id)
		}
	}

	ids := make([]StreamID, 0, 20)
	for i := 0; i < 20; i++ {
		ids = append(ids, NewStreamID())
		time.Sleep(2 * time.Millisecond)
	}
	for i := 1; i < len(ids); i++ {
		if !(ids[i-1] < ids[i]) {
			t.Fatalf("identifiers are not time-ordered at %d: %s !< %s",
				i, ids[i-1], ids[i])
		}
	}
}

func TestSourceID_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   SourceID
		want bool
	}{
		{"carrier-a", true},
		{"source_1", true},
		{"", false},
		{"Source", false},
		{"source.a", false},
		{SourceID(strings.Repeat("a", 65)), false},
	}
	for _, tc := range cases {
		if got := tc.id.Valid(); got != tc.want {
			t.Errorf("SourceID(%q).Valid() = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestReasonCode_IsBounded(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, reason string }{
		{"empty", ""},
		{"too long", strings.Repeat("a", reasonCodeMax+1)},
		{"uppercase", "Stalled"},
		{"free text with a path", "source /var/run/media.sock closed"},
		{"hyphen", "source-closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkReasonCode(tc.reason); err == nil {
				t.Errorf("reason %q was accepted", tc.reason)
			}
		})
	}
	if err := checkReasonCode("source_closed.1"); err != nil {
		t.Errorf("a valid reason code was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

func TestConfig_DefaultIsValid(t *testing.T) {
	t.Parallel()
	if problems := DefaultConfig().validate(); len(problems) > 0 {
		t.Errorf("DefaultConfig is invalid: %v", problems)
	}
	if problems := TestConfig().validate(); len(problems) > 0 {
		t.Errorf("TestConfig is invalid: %v", problems)
	}
}

func TestConfig_RefusesUnboundedStreams(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxStreams = 0

	var found bool
	for _, p := range cfg.validate() {
		if strings.Contains(p, "traffic spike") {
			found = true
		}
	}
	if !found {
		t.Error("an unbounded runtime was accepted without explaining the consequence")
	}
}

// TestConfig_RefusesAPumpSlowerThanTheFrameCadence catches a misconfiguration
// whose only symptom is audio that is always late.
func TestConfig_RefusesAPumpSlowerThanTheFrameCadence(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.PumpInterval = 50 * time.Millisecond
	cfg.Pipeline.FrameInterval = 20 * time.Millisecond

	var found bool
	for _, p := range cfg.validate() {
		if strings.Contains(p, "every frame late") {
			found = true
		}
	}
	if !found {
		t.Errorf("a 50ms pump with a 20ms cadence was accepted: %v", cfg.validate())
	}
}

func TestConfig_RefusesASweepSlowerThanTheStallTimeout(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.StallTimeout = 100 * time.Millisecond
	cfg.SweepInterval = time.Second

	var found bool
	for _, p := range cfg.validate() {
		if strings.Contains(p, "SweepInterval") {
			found = true
		}
	}
	if !found {
		t.Error("a sweep slower than the stall timeout was accepted")
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_RefusesDuplicates(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewMediaRegistry()

	s, err := reg.Create(h.Context(), h.Runtime.Config().Pipeline, h.Clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(s); !errors.Is(err, ErrStreamExists) {
		t.Errorf("err = %v, want ErrStreamExists", err)
	}
}

func TestRegistry_LenIsO1AndAccurate(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewMediaRegistry()
	cfg := h.Runtime.Config().Pipeline

	var ids []StreamID
	for i := 0; i < 200; i++ {
		s, err := reg.Create(h.Context(), cfg, h.Clock)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID())
	}
	if got := reg.Len(); got != 200 {
		t.Errorf("Len() = %d, want 200", got)
	}
	for _, id := range ids[:50] {
		reg.Remove(id)
	}
	if got := reg.Len(); got != 150 {
		t.Errorf("Len() = %d after removals, want 150", got)
	}
	if got := reg.Total(); got != 200 {
		t.Errorf("Total() = %d, want 200", got)
	}
}

func TestRegistry_ShardsSpreadLoad(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewMediaRegistry()
	cfg := h.Runtime.Config().Pipeline

	const n = 1024
	for i := 0; i < n; i++ {
		if _, err := reg.Create(h.Context(), cfg, h.Clock); err != nil {
			t.Fatal(err)
		}
	}

	depths := reg.ShardDepths()
	expected := n / registryShards
	var empty, hot int
	for _, d := range depths {
		if d == 0 {
			empty++
		}
		if d > expected*3 {
			hot++
		}
	}
	if empty > 0 {
		t.Errorf("%d of %d shards are empty after %d streams", empty, registryShards, n)
	}
	if hot > 0 {
		t.Errorf("%d shards hold more than 3x the mean depth (%d)", hot, expected)
	}
}

func TestRegistry_EachDoesNotHoldAShardLock(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewMediaRegistry()
	cfg := h.Runtime.Config().Pipeline

	for i := 0; i < 10; i++ {
		if _, err := reg.Create(h.Context(), cfg, h.Clock); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		reg.Each(func(s *Stream) bool {
			reg.Has(s.ID())
			reg.Remove(s.ID())
			return true
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Each deadlocked while its callback touched the registry")
	}
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

func TestScheduler_RefusesAtCapacity(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxStreams = 3
	cfg.MaxStreamsPerSource = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := h.Open(ctx); err != nil {
			t.Fatalf("stream %d refused: %v", i, err)
		}
	}
	if _, err := h.Open(ctx); err == nil {
		t.Error("a stream was admitted past capacity")
	}
	if got := h.Runtime.Shed(); got != 1 {
		t.Errorf("shed = %d, want 1", got)
	}
}

func TestScheduler_PerSourceCeiling(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxStreams = 100
	cfg.MaxStreamsPerSource = 2

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := h.Open(ctx); err != nil {
			t.Fatalf("stream %d refused: %v", i, err)
		}
	}
	_, err := h.Open(ctx)
	if err == nil {
		t.Fatal("the per-source ceiling did not bind")
	}
	if !strings.Contains(err.Error(), "source_capacity_exceeded") {
		t.Errorf("err = %v, want the source ceiling named", err)
	}
}

func TestScheduler_ReleasePairsWithAdmit(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := s.Context().Source
	if got := h.Runtime.Scheduler().Live(source); got != 1 {
		t.Fatalf("live = %d after one stream, want 1", got)
	}

	if err := h.Coordinator.Close(ctx, s.ID(), "done"); err != nil {
		t.Fatal(err)
	}
	if got := h.Runtime.Scheduler().Live(source); got != 0 {
		t.Errorf("live = %d after close; the slot leaked", got)
	}
}

func TestScheduler_FailedOpenReleasesTheSlot(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	// A context whose format does not match the runtime's pipeline: admitted,
	// then the stream constructor refuses. The slot must come back.
	sc := h.Context()
	sc.Source = ""

	if _, err := h.Coordinator.Open(ctx, sc); err == nil {
		t.Fatal("a stream opened with an invalid source")
	}
	if got := h.Runtime.Scheduler().Live(sc.Source); got != 0 {
		t.Errorf("live = %d after a failed open; the slot leaked", got)
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_ExportHistogramsInFull(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	s, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := h.Dispatcher.Dispatch(s.ID(), h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
	}
	h.Dispatcher.ObserveStream(s)

	var histograms int
	for _, sample := range h.Metrics.Snapshot() {
		if len(sample.Bounds) == 0 {
			continue
		}
		histograms++
		if sample.Count > 0 && len(sample.Buckets) == 0 {
			t.Errorf("histogram %s exports no buckets", sample.Name)
		}
	}
	if histograms == 0 {
		t.Error("no histogram series reached the snapshot")
	}
}

func TestMetrics_StateCensusReportsZeroes(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Open(ctx); err != nil {
		t.Fatal(err)
	}
	h.Runtime.Sweep(ctx)

	names := map[string]bool{}
	for _, s := range h.Metrics.Snapshot() {
		names[s.Name] = true
	}
	for _, state := range AllStates() {
		want := "media_streams_state_" + string(state)
		if !names[want] {
			t.Errorf("no gauge for state %s", state)
		}
	}
}

// TestMetrics_DeliveryRateMayExceedOne pins a deliberate semantic.
func TestMetrics_DeliveryRateMayExceedOne(t *testing.T) {
	t.Parallel()
	m := NewMediaMetrics()
	m.FramesAccepted.Add(10, "inbound")
	m.FramesDelivered.Add(12, "inbound")

	// Gap-filled silence is delivered without ever being accepted. A rate above
	// 1 is exactly what should be shown: the engine is inventing audio.
	if got := m.DeliveryRate(); got <= 1 {
		t.Errorf("DeliveryRate() = %v, want > 1 when gaps were filled", got)
	}
}

// ---------------------------------------------------------------------------
// Read-through delivery
// ---------------------------------------------------------------------------

func TestRead_PumpsThroughWhenRingIsEmpty(t *testing.T) {
	t.Parallel()
	h := started(t)

	s, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// One frame in. It lands in the jitter buffer, not the output ring.
	res, err := s.Write(h.Gen.Next(h.Clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("frame refused: %s", res.Reason)
	}

	// Read WITHOUT calling Pump. Read-through must deliver it.
	f, err := s.Read()
	if err != nil {
		t.Fatalf("read-through did not deliver a due frame: %v", err)
	}
	if f.Sequence != 0 {
		t.Errorf("sequence = %d, want 0", f.Sequence)
	}
}

// ---------------------------------------------------------------------------
// Two-stage snapshot capture
// ---------------------------------------------------------------------------

func TestSnapshot_CapturesJitterHeldAudioNotJustTheRing(t *testing.T) {
	t.Parallel()
	h := started(t)

	s, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Five frames in, nothing read and nothing pumped. All of this audio is in
	// the jitter buffer and none of it has reached the output ring.
	for i := 0; i < 5; i++ {
		if _, err := s.Write(h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	if got := s.Pipeline().Buffer().Len(); got != 0 {
		t.Fatalf("precondition failed: ring already holds %d frames", got)
	}

	snap := s.Snapshot(SnapshotConfig{IncludeAudio: true, MaxAudioFrames: 25})
	if len(snap.Buffered) == 0 {
		t.Fatal("snapshot captured no audio though five frames are in flight")
	}

	// Playout order, oldest first.
	for i := 1; i < len(snap.Buffered); i++ {
		if snap.Buffered[i].Timestamp < snap.Buffered[i-1].Timestamp {
			t.Errorf("frame %d is out of playout order", i)
		}
	}
}

func TestSnapshot_BoundAppliesToBothStagesCombined(t *testing.T) {
	t.Parallel()
	h := started(t)

	s, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		if _, err := s.Write(h.Gen.Next(h.Clock.Now())); err != nil {
			t.Fatal(err)
		}
		h.Clock.Advance(20 * time.Millisecond)
	}
	// Some frames move to the ring; the rest stay held in the jitter buffer.
	s.Pump()

	snap := s.Snapshot(SnapshotConfig{IncludeAudio: true, MaxAudioFrames: 3})
	if len(snap.Buffered) != 3 {
		t.Fatalf("captured %d frames, want the bound of 3", len(snap.Buffered))
	}
	if snap.BufferedDropped == 0 {
		t.Error("BufferedDropped did not record the omitted frames")
	}
}

func TestSnapshot_AudioIsOffByDefault(t *testing.T) {
	t.Parallel()
	// MEDIA-PCM-1: PCM is ephemeral by default. This test is the policy.
	if DefaultSnapshotConfig().IncludeAudio {
		t.Fatal("IncludeAudio must default to false")
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func TestMediaEvent_TopicShape(t *testing.T) {
	t.Parallel()
	for _, ty := range AllMediaEventTypes() {
		topic := ty.Topic()
		if !strings.HasPrefix(topic, "media.stream.") {
			t.Errorf("%s: topic %q lacks the media.stream prefix", ty, topic)
		}
		if !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s: topic %q lacks the mandatory version suffix", ty, topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("%s: topic %q contains a hyphen", ty, topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("%s: topic %q is not lowercase", ty, topic)
		}
	}
}

func TestMediaEvent_CarriesNoAudio(t *testing.T) {
	t.Parallel()
	// MEDIA-PCM-1: events never carry PCM. This test is the policy, enforced
	// structurally so a later field addition cannot quietly violate it.
	forbidden := []string{"payload", "audio", "sample", "pcm", "frame", "buffer", "recording"}
	ty := reflect.TypeOf(MediaEvent{})
	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		name := strings.ToLower(field.Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("MediaEvent.%s may carry audio content", field.Name)
			}
		}
		if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
			t.Errorf("MediaEvent.%s is a byte slice and could carry audio", field.Name)
		}
	}
}

func TestRecordingEventPublisher_IsBounded(t *testing.T) {
	t.Parallel()
	p := NewBoundedRecordingEventPublisher(3)
	for i := 0; i < 10; i++ {
		if err := p.Publish(context.Background(), MediaEvent{Type: EventStreamCreated}); err != nil {
			t.Fatal(err)
		}
	}
	if p.Len() != 3 {
		t.Errorf("held %d events, want the bound of 3", p.Len())
	}
	if p.Dropped() != 7 {
		t.Errorf("dropped = %d, want 7", p.Dropped())
	}
}

func TestStreamStateEvent_CoversTheStatesThatMatter(t *testing.T) {
	t.Parallel()
	// Active, Closed and Failed must always publish — they are the states an
	// operator and every downstream consumer act on.
	for _, s := range []StreamState{StateActive, StateClosed, StateFailed} {
		if _, ok := streamStateEvent(s); !ok {
			t.Errorf("%s must publish an event", s)
		}
	}
	for _, s := range AllStates() {
		if _, ok := streamStateEvent(s); !ok {
			t.Logf("%s publishes no event (deliberate)", s)
		}
	}
}
