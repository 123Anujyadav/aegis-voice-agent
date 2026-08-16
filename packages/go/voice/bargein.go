package voice

import (
	"context"
	"fmt"
	"sync/atomic"

	audiobridge "github.com/callscreen/callscreen-platform/packages/go/audiobridge"
	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
)

// ---------------------------------------------------------------------------
// Barge-in orchestration
// ---------------------------------------------------------------------------
//
// # Detection is not here, and neither is cancellation
//
// Phase 11D decides that an interruption happened: the acoustics, the onset
// confirmation, the debounce, the staleness window and the agent-speaking gate
// are all audiointel.BargeInDetector's, and this file contains none of them.
// Phase 11C cancels its own synthesis: speech.SpeechSession.Interrupt drops the
// pending chunks, transitions the turn and opens the next one, reached through
// the existing audiobridge adapter.
//
// What is left — and the only thing this file does — is what the ORCHESTRATION
// layer owns and nobody below it can do:
//
//   - invalidate the generation, so audio already synthesised for the abandoned
//     turn can never reach media;
//   - abort the live token stream, which is Phase 10A's Dispatcher.Abort;
//   - close the synthesiser this pipeline opened;
//   - move the session through the Task 10 state table.
//
// None of those is a second interruption mechanism. There is exactly one
// detection, exactly one call to Phase 11C's Interrupt, and this reacts to the
// same single event.
//
// # Why the generation is bumped BEFORE anything else
//
// Every other step takes time — a lock, a channel, a process. Audio already
// sitting in the synthesiser's output queue is racing all of it, and a frame
// that wins that race is the agent talking over the caller who just
// interrupted it. Bumping the counter first closes the race at its narrowest
// point: [Pipeline.pumpAudio] compares the generation at the moment of
// DELIVERY, so a frame in flight is stopped even after it has been read from
// the stream.
//
// # A refusal from Phase 11C is recorded, not propagated
//
// speech.SpeechSession.Interrupt refuses when its own turn is no longer
// responding or speaking. That can legitimately disagree with this pipeline in
// the window where the turn moved on between detection and delivery, and
// audiobridge documents the same race.
//
// When it happens, this pipeline has still interrupted its own synthesis and
// its own generation — the FSM said the agent held the floor, and that is
// authoritative for what this pipeline is doing. Returning the refusal upward
// would make audiointel count a BargeInRefused for an interruption that did in
// fact happen here, undercounting the thing the metric exists to measure. So
// the disagreement is counted separately, where it can be seen, and the
// interruption reports the truth about this layer.

// interruptController is this pipeline's audiointel.SpeechController.
//
// Phase 11D calls exactly one of these, from inside Analyze, on the frame that
// completed the detection.
type interruptController struct {
	p *Pipeline

	// delivered counts interruptions this layer carried out.
	delivered atomic.Uint64

	// refusedByFloor counts detections that arrived when this pipeline's own
	// state said the agent did not hold the floor.
	refusedByFloor atomic.Uint64

	// speechRefusals counts times Phase 11C declined an interruption this layer
	// went on to perform anyway. A non-zero value is the two layers disagreeing
	// about who holds the floor, which is worth seeing and is not an error.
	speechRefusals atomic.Uint64
}

// Compile-time proof that this satisfies the Phase 11D port.
var _ audiointel.SpeechController = (*interruptController)(nil)

// Compile-time proof that the EXISTING adapter satisfies the same port, so it
// can be handed to [PipelineConfig.Controller] with nothing in between.
//
// This is the seam the phase brief requires: detection reaches
// speech.SpeechSession.Interrupt through packages/go/audiobridge, not through
// anything invented here. audiobridge in turn carries its own compile-time
// assertion that *speech.SpeechSession satisfies its interface, so the chain
// from this line to the frozen method is checked by the compiler end to end.
var _ audiointel.SpeechController = (*audiobridge.Adapter)(nil)

// Interrupt orchestrates one interruption.
//
// Called by Phase 11D from inside Analyze, on the ingest goroutine.
func (c *interruptController) Interrupt(ctx context.Context, reason string) error {
	p := c.p

	// The same precondition Phase 11C applies, asked of this pipeline's own
	// state. A caller talking while we are listening is not interrupting —
	// their audio already belongs to the live turn, and cancelling would throw
	// away a transcript in progress.
	//
	// audiointel applies its own agent-speaking gate before calling here, so
	// the two normally agree; this catches the window where the turn moved on
	// in between.
	if !p.cfg.FSM.State().AgentHoldsFloor() {
		c.refusedByFloor.Add(1)
		return fmt.Errorf("%w: the agent does not hold the floor (%s); there is "+
			"no speech to interrupt", ErrInvalidTransition, p.cfg.FSM.State())
	}

	// FIRST, before anything that can block. See the file comment.
	generation := p.generation.Add(1)

	// Phase 11C's cancellation, through the existing adapter. THE existing
	// mechanism, not a parallel one.
	if p.cfg.Controller != nil {
		if err := p.cfg.Controller.Interrupt(ctx, reason); err != nil {
			c.speechRefusals.Add(1)
			p.m.ProviderFailures.Inc("speech_session", string(KindTTS), ReasonCancelled)
		}
	}

	// What this layer owns: the live token stream and the synthesiser it
	// opened. Both are abandoned; neither is waited on, because this runs on
	// the ingest goroutine and the abort budget is measured from here.
	p.abortActiveTurn()

	// The session moves through the declared table, never around it.
	if err := p.cfg.FSM.To(ctx, StateInterrupted, ReasonBargeIn); err != nil {
		return err
	}
	if err := p.cfg.FSM.To(ctx, StateListening, ReasonBargeIn); err != nil {
		return err
	}

	c.delivered.Add(1)
	p.m.StaleChunksBlocked.Add(0) // registers the series even at zero
	p.publish(VoiceEvent{
		Type:    EventBargeIn,
		Session: p.cfg.Session,
		Turn:    p.cfg.FSM.Turn(),
		Call:    p.cfg.Call,
		Reason:  ReasonBargeIn,
		At:      p.clock.Now(),
	})

	_ = generation
	return nil
}

// EndOfSpeech forwards the endpoint to Phase 11C.
//
// Phase 11D's analyzer does not call this — the endpoint reaches this pipeline
// as Analysis.Endpoint.Confirmed and is acted on there — but the port includes
// it, and a controller that silently dropped it would be lying about what it
// implements.
func (c *interruptController) EndOfSpeech(ctx context.Context) error {
	if c.p.cfg.Controller == nil {
		return nil
	}
	return c.p.cfg.Controller.EndOfSpeech(ctx)
}

// Delivered returns how many interruptions this layer carried out.
func (c *interruptController) Delivered() uint64 { return c.delivered.Load() }

// SpeechRefusals returns how many times Phase 11C declined an interruption this
// layer performed anyway.
func (c *interruptController) SpeechRefusals() uint64 { return c.speechRefusals.Load() }

// RefusedByFloor returns detections refused because the agent was not speaking.
func (c *interruptController) RefusedByFloor() uint64 { return c.refusedByFloor.Load() }

// ---------------------------------------------------------------------------

// abortActiveTurn abandons the live generation and synthesis.
//
// # Abort, not wait
//
// This runs on the ingest goroutine, inside Phase 11D's measurement of the
// interruption. Waiting for the dispatcher to drain or the synthesiser process
// to exit would put a provider's teardown inside a budget that exists to
// measure a signal. The turn's own goroutine observes the abort and unwinds.
func (p *Pipeline) abortActiveTurn() {
	p.mu.Lock()
	dispatcher := p.activeDispatcher
	stream := p.activeTTS
	cancel := p.activeTurnCancel
	p.activeDispatcher = nil
	p.activeTTS = nil
	p.activeTurnCancel = nil
	p.mu.Unlock()

	// Order matters. The dispatcher stops delivering text to the synthesis sink
	// first, so nothing new is queued while the synthesiser is being closed.
	if dispatcher != nil {
		dispatcher.Abort()
	}
	if stream != nil {
		_ = stream.Close()
	}
	if cancel != nil {
		// Unblocks the turn goroutine's wait without ending the SESSION: a
		// barge-in ends a turn, and the call continues.
		cancel()
	}
}

// BargeIns returns how many interruptions this pipeline has orchestrated.
func (p *Pipeline) BargeIns() uint64 {
	if p.interrupts == nil {
		return 0
	}
	return p.interrupts.Delivered()
}

// SpeechInterruptRefusals returns how many times Phase 11C disagreed.
func (p *Pipeline) SpeechInterruptRefusals() uint64 {
	if p.interrupts == nil {
		return 0
	}
	return p.interrupts.SpeechRefusals()
}
