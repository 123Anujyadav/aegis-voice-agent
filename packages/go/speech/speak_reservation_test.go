package speech

import (
	"context"
	"errors"
	"testing"
	"time"
)

// speakAsync starts Speak on its own goroutine and returns its result channel.
func speakAsync(o *TTSOrchestrator, turn TurnID, text string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- o.Speak(context.Background(), turn, text, LangEnglishIN) }()
	return done
}

func waitErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Speak did not return")
		return nil
	}
}

// TestTTS_ConcurrentSpeakIsRefused is the recorded Probe B as a regression
// test. Speak used to check `speaking`, release the lock, open the provider
// stream and only then mark synthesis live, so a second call made while the
// first was opening passed the check too and two streams were opened.
func TestTTS_ConcurrentSpeakIsRefused(t *testing.T) {
	t.Parallel()
	o, turns, clock, provider := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	provider.StallFor(time.Second)
	first := speakAsync(o, turn.ID, "First.")
	clock.BlockUntil(1) // the first Speak is inside OpenTTS

	err := o.Speak(context.Background(), turn.ID, "Second.", LangEnglishIN)
	if !errors.Is(err, ErrInternalFailure) {
		t.Fatalf("a Speak made while another was opening returned %v, want refusal", err)
	}

	clock.Advance(time.Second)
	if err := waitErr(t, first); err != nil {
		t.Fatalf("the first Speak failed: %v", err)
	}
	if got := provider.Opened(); got != 1 {
		t.Errorf("%d provider streams were opened, want 1", got)
	}
}

// TestTTS_SpeakForAnEndedTurnProducesNoAudio: a turn interrupted or cancelled
// before synthesis starts must not be spoken. The transition to Speaking used
// to be attempted and its refusal ignored, so the audio played anyway.
func TestTTS_SpeakForAnEndedTurnProducesNoAudio(t *testing.T) {
	t.Parallel()
	o, turns, _, _ := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)
	if err := turns.Transition(turn.ID, TurnInterrupted, "barge_in"); err != nil {
		t.Fatal(err)
	}

	err := o.Speak(context.Background(), turn.ID, "Too late.", LangEnglishIN)
	if !errors.Is(err, ErrTTSCancelled) {
		t.Fatalf("Speak for an interrupted turn returned %v, want ErrTTSCancelled", err)
	}
	if o.Speaking() {
		t.Error("the orchestrator is speaking for an interrupted turn")
	}
	select {
	case f := <-o.Frames():
		t.Fatalf("an interrupted turn produced audio: frame %d", f.Sequence)
	case <-time.After(50 * time.Millisecond):
	}

	// The refusal releases the reservation: the next turn can be spoken.
	next := advanceTurnToResponding(t, turns)
	if err := o.Speak(context.Background(), next.ID, "Next.", LangEnglishIN); err != nil {
		t.Fatalf("Speak after a refused turn failed: %v", err)
	}
}

// TestTTS_CancelWhileOpeningAbandonsTheStream: a barge-in that lands while the
// provider stream is still opening must stop that synthesis, not let it start
// playing afterwards under the new generation.
func TestTTS_CancelWhileOpeningAbandonsTheStream(t *testing.T) {
	t.Parallel()
	o, turns, clock, provider := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	provider.StallFor(time.Second)
	done := speakAsync(o, turn.ID, "Interrupted.")
	clock.BlockUntil(1)

	if _, err := o.Cancel("barge_in"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)

	if err := waitErr(t, done); !errors.Is(err, ErrTTSCancelled) {
		t.Fatalf("Speak cancelled while opening returned %v, want ErrTTSCancelled", err)
	}
	if o.Speaking() {
		t.Error("synthesis is live after a cancel during open")
	}
	select {
	case f := <-o.Frames():
		t.Fatalf("a cancelled synthesis produced audio: frame %d", f.Sequence)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestTTS_CloseWhileOpeningIsSafe: Close while a stream is opening must not
// leave a pump writing to the closed output.
func TestTTS_CloseWhileOpeningIsSafe(t *testing.T) {
	t.Parallel()
	o, turns, clock, provider := ttsRig(t, 1)
	turn := advanceTurnToResponding(t, turns)

	provider.StallFor(time.Second)
	done := speakAsync(o, turn.ID, "Closing.")
	clock.BlockUntil(1)

	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)

	if err := waitErr(t, done); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Fatalf("Speak during Close returned %v, want ErrSpeechSessionClosed", err)
	}
	if o.Speaking() {
		t.Error("synthesis is live after Close")
	}
}

// TestSTT_FinalForAnEndedTurnIsNotDelivered: a final transcript that arrives
// after its turn ended must not be delivered as if the turn were still
// current; it would start a response to a caller who had already moved on.
func TestSTT_FinalForAnEndedTurnIsNotDelivered(t *testing.T) {
	t.Parallel()
	script := ScriptedPartials([]string{"hello"}, "hello there.")
	o, turns, asm, clock, _ := sttRig(t, script)

	turn, err := turns.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), turn.ID, LangEnglishIN); err != nil {
		t.Fatal(err)
	}
	if err := o.Push(testFrame(clock, 0)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(20 * time.Millisecond)

	if err := turns.Transition(turn.ID, TurnCancelled, "caller_hung_up"); err != nil {
		t.Fatal(err)
	}
	if err := o.EndOfSpeech(); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case seg := <-o.Segments():
			if seg.IsFinal {
				t.Fatal("a final for a cancelled turn was delivered")
			}
		case <-deadline:
			t.Fatalf("the late final was neither delivered nor counted (cancellations=%d)",
				o.metrics.Cancellations.Value("stt"))
		default:
			if o.metrics.Cancellations.Value("stt") > 0 {
				if _, ok := asm.Final(turn.ID); !ok {
					t.Error("the late final was not kept in the transcript")
				}
				if turn.State() != TurnCancelled {
					t.Errorf("turn state = %s, want cancelled", turn.State())
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
}
