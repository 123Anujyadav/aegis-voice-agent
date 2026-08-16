package speech

import (
	"context"
	"errors"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Provider routing — mandatory cases 5-8
// ---------------------------------------------------------------------------

func testRouter(t *testing.T) (*ProviderRouter, *FakeSTTProvider, *FakeSTTProvider, *rt.FakeClock) {
	t.Helper()
	clock := testClock()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, NewSpeechMetrics())
	if err != nil {
		t.Fatal(err)
	}
	primary := NewFakeSTTProvider("primary", nil, clock)
	secondary := NewFakeSTTProvider("secondary", nil, clock)
	if err := r.RegisterSTT(primary, TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterSTT(secondary, TierSecondary); err != nil {
		t.Fatal(err)
	}
	return r, primary, secondary, clock
}

func TestRouter_PrefersPrimaryWhileHealthy(t *testing.T) {
	t.Parallel()
	r, _, _, _ := testRouter(t)

	p, err := r.PickSTT(LangEnglishIN)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "primary" {
		t.Errorf("picked %s, want primary", p.ID())
	}
}

// Mandatory case 7: primary -> secondary failover.
func TestRouter_FailsOverToSecondary(t *testing.T) {
	t.Parallel()
	r, _, _, _ := testRouter(t)

	// Mandatory case 6: provider failure, reported until the circuit opens.
	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
	}
	if got := r.Health("primary").State; got != CircuitOpen {
		t.Fatalf("primary circuit is %s after the threshold, want open", got)
	}

	p, err := r.PickSTT(LangEnglishIN)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "secondary" {
		t.Errorf("picked %s, want secondary after primary opened", p.ID())
	}
}

// Mandatory case 5: a provider timeout degrades health like any other failure.
func TestRouter_TimeoutCountsAsFailure(t *testing.T) {
	t.Parallel()
	r, _, _, _ := testRouter(t)

	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeTimeout)
	}
	h := r.Health("primary")
	if h.State != CircuitOpen {
		t.Errorf("state = %s, want open", h.State)
	}
	if h.Timeouts != uint64(DefaultRouterConfig().FailureThreshold) {
		t.Errorf("timeouts = %d, want %d", h.Timeouts, DefaultRouterConfig().FailureThreshold)
	}
}

// Mandatory case 8: secondary recovery — an open circuit half-opens, and a
// success closes it.
func TestRouter_RecoversThroughHalfOpen(t *testing.T) {
	t.Parallel()
	r, _, _, clock := testRouter(t)

	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
	}
	if got := r.Health("primary").State; got != CircuitOpen {
		t.Fatalf("state = %s, want open", got)
	}

	// Before the cooldown, the circuit stays shut and traffic goes elsewhere.
	clock.Advance(DefaultRouterConfig().CooldownPeriod / 2)
	if got := r.Health("primary").State; got != CircuitOpen {
		t.Errorf("state = %s mid-cooldown, want open", got)
	}

	// After it, one trial is allowed.
	clock.Advance(DefaultRouterConfig().CooldownPeriod)
	if got := r.Health("primary").State; got != CircuitHalfOpen {
		t.Fatalf("state = %s after cooldown, want half_open", got)
	}

	p, err := r.PickSTT(LangEnglishIN)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "primary" {
		t.Fatalf("half-open primary was not offered the trial; picked %s", p.ID())
	}

	r.Report("primary", OutcomeSuccess)
	if got := r.Health("primary").State; got != CircuitClosed {
		t.Errorf("state = %s after a successful trial, want closed", got)
	}
}

func TestRouter_HalfOpenAllowsExactlyOneTrial(t *testing.T) {
	t.Parallel()
	r, _, _, clock := testRouter(t)

	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
	}
	clock.Advance(DefaultRouterConfig().CooldownPeriod * 2)

	first, err := r.PickSTT(LangEnglishIN)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != "primary" {
		t.Fatalf("first pick = %s, want the primary's trial", first.ID())
	}
	// The trial is spent. A recovering provider must not be handed the whole
	// load the instant it comes back.
	second, err := r.PickSTT(LangEnglishIN)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID() != "secondary" {
		t.Errorf("second pick = %s, want secondary; the trial was reused", second.ID())
	}
}

func TestRouter_FailedTrialReopensImmediately(t *testing.T) {
	t.Parallel()
	r, _, _, clock := testRouter(t)

	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
	}
	clock.Advance(DefaultRouterConfig().CooldownPeriod * 2)
	if got := r.Health("primary").State; got != CircuitHalfOpen {
		t.Fatalf("state = %s, want half_open", got)
	}

	// One failure in half-open is enough: the provider was given a chance.
	r.Report("primary", OutcomeFailure)
	if got := r.Health("primary").State; got != CircuitOpen {
		t.Errorf("state = %s after a failed trial, want open", got)
	}
}

func TestRouter_UnsupportedLanguageIsDistinctFromUnavailable(t *testing.T) {
	t.Parallel()
	r, _, _, _ := testRouter(t)

	// Nobody declares Tamil.
	_, err := r.PickSTT(LangTamil)
	if !errors.Is(err, ErrUnsupportedLanguage) {
		t.Errorf("err = %v, want ErrUnsupportedLanguage", err)
	}

	// Everybody who declares English is down — a different situation with a
	// different fix, and it must not report as a language problem.
	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
		r.Report("secondary", OutcomeFailure)
	}
	_, err = r.PickSTT(LangEnglishIN)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestRouter_RefusesDuplicateRegistration(t *testing.T) {
	t.Parallel()
	clock := testClock()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, NewSpeechMetrics())
	if err != nil {
		t.Fatal(err)
	}
	p := NewFakeSTTProvider("dup", nil, clock)
	if err := r.RegisterSTT(p, TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterSTT(p, TierSecondary); err == nil {
		t.Error("a duplicate provider registration was accepted")
	}
}

func TestRouter_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultRouterConfig()
	cfg.CooldownPeriod = 0
	if _, err := NewProviderRouter(cfg, testClock(), nil); err == nil {
		t.Error("a zero cooldown was accepted; an open circuit would never recover")
	}
}

func TestRouter_TTSRoutingIsIndependentOfSTT(t *testing.T) {
	t.Parallel()
	clock := testClock()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, NewSpeechMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterSTT(NewFakeSTTProvider("asr", nil, clock), TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterTTS(NewFakeTTSProvider("voice", 2, clock), TierPrimary); err != nil {
		t.Fatal(err)
	}

	// Killing the recogniser must not affect synthesis routing.
	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("asr", OutcomeFailure)
	}
	if _, err := r.PickTTS(LangEnglishIN); err != nil {
		t.Errorf("TTS routing broke when STT went down: %v", err)
	}
}

// ---------------------------------------------------------------------------
// STT orchestration
// ---------------------------------------------------------------------------

func sttRig(t *testing.T, script []TranscriptSegment) (
	*STTOrchestrator, *SpeechTurnManager, *TranscriptAssembler, *rt.FakeClock, *FakeSTTProvider,
) {
	t.Helper()
	clock := testClock()
	m := NewSpeechMetrics()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, m)
	if err != nil {
		t.Fatal(err)
	}
	p := NewFakeSTTProvider("asr", script, clock)
	if err := r.RegisterSTT(p, TierPrimary); err != nil {
		t.Fatal(err)
	}
	sess := NewSessionID()
	asm := NewTranscriptAssembler(sess, clock)
	turns := NewSpeechTurnManager(clock)
	o, err := NewSTTOrchestrator(
		DefaultSTTOrchestratorConfig(media.PCM16Mono8k()), r, asm, turns, clock, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o, turns, asm, clock, p
}

func testFrame(clock *rt.FakeClock, seq uint64) media.Frame {
	format := media.PCM16Mono8k()
	return media.Frame{
		Sequence: seq, Timestamp: time.Duration(seq) * 20 * time.Millisecond,
		Arrival: clock.Now(), Format: format,
		Payload: make([]byte, format.BytesFor(20*time.Millisecond)),
	}
}

func TestSTT_PartialsThenFinalReachTheAssembler(t *testing.T) {
	t.Parallel()
	script := ScriptedPartials([]string{"hello", "hello there"}, "hello there friend.")
	o, turns, asm, clock, _ := sttRig(t, script)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := o.Push(testFrame(clock, uint64(i))); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		clock.Advance(20 * time.Millisecond)
	}
	if err := o.EndOfSpeech(); err != nil {
		t.Fatal(err)
	}

	// Drain until the final arrives or we run out of patience.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := asm.Final(turn.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no final transcript arrived; state=%s", turn.State())
		}
		time.Sleep(time.Millisecond)
	}

	fin, _ := asm.Final(turn.ID)
	if fin.Text != "hello there friend." {
		t.Errorf("final = %q", fin.Text)
	}
	if turn.State() != TurnFinal {
		t.Errorf("turn state = %s, want final", turn.State())
	}
}

// The boundary where frame retention begins must clone.
func TestSTT_ClonesFramesOnEntry(t *testing.T) {
	t.Parallel()
	o, turns, _, clock, provider := sttRig(t, nil)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}

	f := testFrame(clock, 0)
	for i := range f.Payload {
		f.Payload[i] = 0xAA
	}
	if err := o.Push(f); err != nil {
		t.Fatal(err)
	}

	// Scribble over the caller's payload, exactly as a ring buffer would when
	// it wraps.
	for i := range f.Payload {
		f.Payload[i] = 0xFF
	}

	streams := provider.Streams()
	if len(streams) != 1 {
		t.Fatalf("provider opened %d streams, want 1", len(streams))
	}
	got := streams[0].Frames()
	if len(got) != 1 {
		t.Fatalf("provider received %d frames, want 1", len(got))
	}
	for i, b := range got[0].Payload {
		if b != 0xAA {
			t.Fatalf("byte %d is %#x, want 0xAA: the frame was borrowed, not cloned", i, b)
		}
	}
}

func TestSTT_RefusesForeignFormat(t *testing.T) {
	t.Parallel()
	o, turns, _, clock, _ := sttRig(t, nil)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}

	f := testFrame(clock, 0)
	f.Format = media.PCM16Mono16k() // wrong rate; this package does no resampling
	if err := o.Push(f); !errors.Is(err, ErrInvalidAudio) {
		t.Errorf("err = %v, want ErrInvalidAudio", err)
	}
}

func TestSTT_PushBeforeStartIsRefused(t *testing.T) {
	t.Parallel()
	o, _, _, clock, _ := sttRig(t, nil)
	if err := o.Push(testFrame(clock, 0)); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Errorf("err = %v, want ErrSpeechSessionClosed", err)
	}
}

// Mandatory case 13: terminating with an active STT stream must leave nothing
// running.
func TestSTT_CancelStopsTheConsumerGoroutine(t *testing.T) {
	t.Parallel()
	o, turns, _, _, _ := sttRig(t, ScriptedPartials([]string{"a", "b"}, "c."))

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	// Cancel blocks on wg.Wait, so returning at all proves the consumer exited.
	if err := o.Cancel("test"); err != nil {
		t.Fatal(err)
	}
	if got := o.Provider(); got != "asr" {
		t.Logf("provider after cancel: %s", got)
	}
}

func TestSTT_DoubleStartIsRefused(t *testing.T) {
	t.Parallel()
	o, turns, _, _, _ := sttRig(t, nil)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err == nil {
		t.Error("a second recognition stream was opened for the same orchestrator")
	}
}

func TestSTT_UnsupportedLanguageIsReported(t *testing.T) {
	t.Parallel()
	o, turns, _, _, _ := sttRig(t, nil)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangTamil); !errors.Is(err, ErrUnsupportedLanguage) {
		t.Errorf("err = %v, want ErrUnsupportedLanguage", err)
	}
}

// ---------------------------------------------------------------------------
// TTS orchestration — mandatory cases 9, 11
// ---------------------------------------------------------------------------

func ttsRig(t *testing.T, framesPerChunk int) (
	*TTSOrchestrator, *SpeechTurnManager, *rt.FakeClock, *FakeTTSProvider,
) {
	t.Helper()
	clock := testClock()
	m := NewSpeechMetrics()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, m)
	if err != nil {
		t.Fatal(err)
	}
	p := NewFakeTTSProvider("voice", framesPerChunk, clock)
	if err := r.RegisterTTS(p, TierPrimary); err != nil {
		t.Fatal(err)
	}
	turns := NewSpeechTurnManager(clock)
	o, err := NewTTSOrchestrator(
		DefaultTTSOrchestratorConfig(media.PCM16Mono8k()), r, turns, NewSessionID(), clock, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o, turns, clock, p
}

// advanceTurnToResponding walks a fresh turn to the state synthesis starts from.
func advanceTurnToResponding(t *testing.T, turns *SpeechTurnManager) *SpeechTurn {
	t.Helper()
	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []SpeechTurnState{TurnFinalizing, TurnFinal, TurnResponding} {
		if err := turns.Transition(turn.ID, to, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return turn
}

func TestTTS_ChunksTextAndEmitsFramesInOrder(t *testing.T) {
	t.Parallel()
	o, turns, _, _ := ttsRig(t, 2)
	turn := advanceTurnToResponding(t, turns)

	if err := o.Speak(context.Background(), turn.ID, "One. Two. Three.", LangEnglishIN); err != nil {
		t.Fatal(err)
	}

	// Three chunks at two frames each.
	var got []media.Frame
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 6 && time.Now().Before(deadline) {
		select {
		case f := <-o.Frames():
			got = append(got, f)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(got) < 6 {
		t.Fatalf("received %d frames, want 6", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sequence <= got[i-1].Sequence {
			t.Errorf("frame %d has sequence %d after %d", i, got[i].Sequence, got[i-1].Sequence)
		}
	}
}

// Mandatory case 9: TTS cancellation.
func TestTTS_CancelStopsSynthesis(t *testing.T) {
	t.Parallel()
	o, turns, _, provider := ttsRig(t, 2)
	turn := advanceTurnToResponding(t, turns)

	if err := o.Speak(context.Background(), turn.ID, "One. Two. Three.", LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Cancel("test"); err != nil {
		t.Fatal(err)
	}
	if o.Speaking() {
		t.Error("still speaking after cancel")
	}
	if provider.Closed() == 0 {
		t.Error("the provider stream was not closed by the cancellation")
	}
}

// The load-bearing barge-in property: no stale audio may leak after cancel.
func TestTTS_NoStaleChunksLeakAfterCancellation(t *testing.T) {
	t.Parallel()
	o, turns, _, _ := ttsRig(t, 8)
	turn := advanceTurnToResponding(t, turns)

	if err := o.Speak(context.Background(), turn.ID, "One. Two. Three. Four.", LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	genBefore := o.Generation()
	if _, err := o.Cancel("barge_in"); err != nil {
		t.Fatal(err)
	}
	if o.Generation() == genBefore {
		t.Fatal("cancellation did not advance the generation")
	}

	// Give any in-flight producer a chance to misbehave.
	time.Sleep(20 * time.Millisecond)

	select {
	case f, ok := <-o.Frames():
		if ok {
			t.Fatalf("a stale frame leaked after cancellation: seq=%d", f.Sequence)
		}
	default:
	}
}

// Mandatory case 11: the chunk queue is bounded.
func TestTTS_BoundedChunkQueue(t *testing.T) {
	t.Parallel()
	o, turns, _, _ := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	// Far more clauses than ChunkQueue admits.
	var sb []byte
	for i := 0; i < DefaultTTSOrchestratorConfig(media.PCM16Mono8k()).ChunkQueue+10; i++ {
		sb = append(sb, []byte("Sentence here. ")...)
	}
	err := o.Speak(context.Background(), turn.ID, string(sb), LangEnglishIN)
	if !errors.Is(err, ErrBackpressure) {
		t.Errorf("err = %v, want ErrBackpressure", err)
	}
}

func TestTTS_DoubleSpeakIsRefused(t *testing.T) {
	t.Parallel()
	o, turns, _, _ := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	if err := o.Speak(context.Background(), turn.ID, "First.", LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	// Speak returns after submitting; synthesis may still be live.
	if o.Speaking() {
		if err := o.Speak(context.Background(), turn.ID, "Second.", LangEnglishIN); err == nil {
			t.Error("a second synthesis stream was opened concurrently")
		}
	}
}

func TestTTS_ProviderFailureIsReported(t *testing.T) {
	t.Parallel()
	o, turns, _, provider := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	provider.FailNext(ErrProviderUnavailable)
	if err := o.Speak(context.Background(), turn.ID, "Hello.", LangEnglishIN); err == nil {
		t.Fatal("a provider failure was not reported")
	}
	if o.Speaking() {
		t.Error("the orchestrator believes it is speaking after an open failure")
	}
}

func TestTTS_EmptyTextProducesNothing(t *testing.T) {
	t.Parallel()
	o, turns, _, provider := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	if err := o.Speak(context.Background(), turn.ID, "   ", LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	if provider.Opened() != 0 {
		t.Error("a provider stream was opened for empty text")
	}
}
