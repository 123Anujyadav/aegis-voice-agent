package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/audiointel"
	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T11 — REAL SPEECH, REAL RECOGNISER, REAL INTENT.
//
// # The gap this closes
//
// Phase 16 T10 proved a real openai-whisper subprocess is reached through the
// production registry, router and pipeline — but it proved it with a TONE, and
// a tone can only ever demonstrate the ABSENCE of hallucination. Whether a real
// transcript reaches the real intent classifier was still unproven.
//
// This file closes that, and nothing else:
//
//	committed speech fixture (2.27 s of PCM)
//	  -> voice.ProviderRegistry -> speech.ProviderRouter
//	  -> the real whispercli adapter -> a real subprocess
//	  -> a real transcript
//	  -> voice.Pipeline -> conversation.Engine -> the real intent classifier
//	  -> an observable Plan
//
// NO ACCURACY CLAIM IS MADE. One fixture is not a measurement of recognition
// quality, and nothing here should be read as one. What is asserted is that a
// known fixture produces a known result under the EXISTING contract.

const (
	// t11Fixture is committed audio, not audio this suite generates. See
	// testdata/README.md for how it was produced and why it is a file.
	t11Fixture = "testdata/speech_transfer_16k_mono_pcm16.raw"

	// t11Spoken is what the fixture says.
	t11Spoken = "Transfer me to Rajesh"

	// t11Transcript is what the real recogniser actually returns for it,
	// normalised only for surrounding whitespace and case.
	//
	// MEASURED, NOT HOPED FOR: three consecutive runs of whisper tiny against
	// this exact audio produced " Transfer me to Rajesh." each time. The
	// trailing full stop is the model's, and it is asserted rather than
	// trimmed away, because trimming it would be widening the assertion until
	// it passed.
	t11Transcript = "transfer me to rajesh."

	// t11Intent is what the EXISTING lexicon resolves that transcript to. The
	// lexicon is not modified by this task.
	t11Intent = "request_transfer"

	// 20 ms at 16 kHz, 16-bit mono.
	t11FrameBytes = 640
)

// t11Frames slices the committed fixture into media frames.
//
// The frames carry ONLY the fixture's bytes: no resampling, no padding and no
// generated audio, so the recogniser sees exactly what was committed.
func t11Frames(t *testing.T) []media.Frame {
	t.Helper()

	pcm, err := os.ReadFile(t11Fixture)
	if err != nil {
		t.Fatalf("reading the speech fixture: %v", err)
	}
	if len(pcm) == 0 {
		t.Fatal("the speech fixture is empty")
	}

	format := media.PCM16Mono16k()
	var out []media.Frame
	for off := 0; off < len(pcm); off += t11FrameBytes {
		end := off + t11FrameBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		i := off / t11FrameBytes
		out = append(out, media.Frame{
			Sequence:  uint64(i),
			Timestamp: time.Duration(i) * 20 * time.Millisecond,
			Format:    format,
			Payload:   pcm[off:end],
		})
	}
	return out
}

// t11Rig assembles the T7 rig with the REAL recogniser and one utterance's
// worth of turn boundaries.
func t11Rig(t *testing.T, frames int) *t7Rig {
	t.Helper()

	whisper := findWhisper(t)
	cfg := t10Config(whisper)
	cfg.WorkDir = t.TempDir()

	return newT7Rig(t, t7Opts{
		stt: func(reg *voice.ProviderRegistry) error {
			return registerLocalSTT(reg, cfg)
		},
		// ONE utterance, ONE turn: speech starts at the first frame and ends at
		// the last. Under the cyclic default the send side would close after
		// four frames and transcribe 80 ms of a 2.3 s sentence.
		onsetAt: 0, endAt: uint64(frames - 1), absolute: true,
		// Wide enough that the whole fixture is queued without a drop. A
		// dropped frame changes the audio, and therefore the transcript.
		maxPendingFrames: frames * 2,
	})
}

// normalise applies the smallest reduction the existing contract already
// implies: surrounding whitespace and case.
//
// NOT fuzzy matching. The recogniser emits a leading space and a capital; both
// are cosmetic, and neither is what this test is about. Everything else —
// including the model's full stop — is compared exactly.
func normalise(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ---------------------------------------------------------------------------
// The proof
// ---------------------------------------------------------------------------

// TestT11_RealSpeechProducesTheExpectedTranscript.
//
// Every component between the fixture and the transcript is the production one.
// No FakeSTTProvider, no scripted transcript and no shortened path: if the
// subprocess does not run, there is no transcript at all.
//
// The chain STOPS at the transcript, and deliberately. Carrying it into the
// intent is blocked by a contract mismatch this task will not paper over — see
// TestT11_RealTranscriptIsGatedAsNoiseByUnknownConfidence.
func TestT11_RealSpeechProducesTheExpectedTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}

	frames := t11Frames(t)
	r := t11Rig(t, len(frames))
	call := r.place(t)
	p := r.ports.of(t, call)
	r.greet(t, call)

	// --- THE PROVIDER IS THE REAL ONE, established before anything is fed ---
	//
	// This is what separates the test from one that passes on a fake: the
	// router's own selection is inspected, and the descriptor names the engine
	// behind the port.
	picked, err := r.registry.PickSTT(t10Lang)
	if err != nil {
		t.Fatalf("PickSTT: %v", err)
	}
	if picked.ID() != t10Config("").ID {
		t.Fatalf("the router selected %q, not the real recogniser", picked.ID())
	}
	d, ok := r.registry.Describe(voice.ProviderID(picked.ID()))
	if !ok || d.Engine != "openai-whisper" {
		t.Fatalf("the selected provider is not openai-whisper: %+v", d)
	}
	if d.Capabilities.Streaming {
		t.Fatal("the selected provider claims streaming; the real batch CLI does not")
	}

	started := time.Now()
	for _, f := range frames {
		if err := call.Audio(f); err != nil {
			// NOT tolerated. A dropped frame silently changes the audio the
			// recogniser sees, and with it the transcript this test asserts.
			t.Fatalf("frame %d: %v", f.Sequence, err)
		}
	}

	p.obs.awaitTurn(t, 5*time.Minute, "the real speech turn")
	elapsed := time.Since(started)

	// --- the real transcript ------------------------------------------------
	segs, chunks, outcomes := p.obs.snapshot()
	var finals []string
	for _, s := range segs {
		if s.IsFinal {
			finals = append(finals, s.Text)
		}
	}
	if len(finals) == 0 {
		t.Fatalf("the real recogniser produced no final transcript; segments=%d "+
			"outcomes=%v", len(segs), outcomes)
	}
	got := normalise(strings.Join(finals, " "))
	if got != t11Transcript {
		t.Errorf("transcript = %q, want %q (normalised for whitespace and case only)",
			got, t11Transcript)
	}

	// --- THE TRANSCRIPT WHERE IT IS USED, not merely where it is announced ---
	//
	// The observer is told about a segment BEFORE the pipeline consumes it, so
	// asserting only there would miss any corruption between the two. The
	// generation request is downstream of the consumption point and carries the
	// caller's words verbatim, so it is the honest place to check that the real
	// transcript actually travelled the response path.
	//
	// Found by mutation: substituting the transcript after the observer call
	// passed every other assertion in this test.
	req := r.model.LastRequest()
	if req == nil {
		t.Fatal("the generator recorded no request, so the transcript reached nothing")
	}
	if len(req.Messages) != 1 {
		t.Fatalf("the generation request carried %d messages, want 1", len(req.Messages))
	}
	if carried := normalise(req.Messages[0].Content); carried != t11Transcript {
		t.Errorf("the response was generated from %q, want the real transcript %q",
			carried, t11Transcript)
	}

	// --- the confidence the real provider reports ---------------------------
	//
	// ConfidenceUnknown, not a number: openai-whisper exposes no calibrated
	// per-utterance confidence and the adapter refuses to invent one. This is
	// measured here because it is what blocks the intent step.
	for _, seg := range segs {
		if seg.IsFinal && seg.Confidence != speech.ConfidenceUnknown {
			t.Errorf("final segment confidence = %v, want ConfidenceUnknown (%v); "+
				"if the provider has started reporting a real confidence, the "+
				"blocker pinned by TestT11_RealTranscriptIsGatedAsNoiseByUnknown"+
				"Confidence must be re-measured",
				seg.Confidence, speech.ConfidenceUnknown)
		}
	}

	// --- the complete lifecycle ---------------------------------------------
	conv := call.Conversation()
	if got := completions(conv); got != 1 {
		t.Errorf("%d speech completions, want exactly 1", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments, want 0", got)
	}
	if got := len(conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions, want 0", got)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v, want caller", got)
	}
	if got := conv.State(); got == conversation.StateEnded {
		t.Error("the conversation ended on a successful turn")
	}
	if got := call.State(); got.Terminal() {
		t.Errorf("the voice session is %v after a successful turn", got)
	}
	if len(chunks) == 0 {
		t.Error("no clause reached the synthesiser, so the turn did not answer")
	}
	if frames, _ := p.sink.stats(); frames == 0 {
		t.Error("no audio reached the caller")
	}

	audioSeconds := float64(len(frames)*t11FrameBytes) / 32000.0
	t.Logf("REAL RUN: fixture %q (%.2fs) -> real openai-whisper (model=%s) -> "+
		"transcript %q -> plan spoken. %s wall including inference. "+
		"NO ACCURACY CLAIM IS MADE: this is one fixture under an existing "+
		"contract, not a measurement of recognition quality.",
		t11Spoken, audioSeconds, t10Model, got, elapsed)
}

// TestT11_TheIntentIsTheClassifiersAndNotAFallback proves the result is
// non-vacuous.
//
// request_transfer is a RESOLVED intent. If the classifier were absent — the
// pre-Phase-13 world this wiring exists to end — the same words would resolve
// to the fallback instead, and the plan would say so. Asserting the resolved
// name distinguishes the two; asserting only "a plan came back" would not.
func TestT11_TheIntentIsTheClassifiersAndNotAFallback(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	p, conv, err := openPlannerRaw(t, vi, conversation.ConversationID("t11-nonvacuous"))
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			t.Fatalf("opening the floor: %v", err)
		}
	}

	// The transcript EXACTLY as the real recogniser returns it — leading space,
	// capital and full stop included. The classifier's own normalisation is
	// what makes this work, and pre-normalising it here would be testing this
	// test's normalisation instead.
	plan, err := p.Handle(utter(" Transfer me to Rajesh."))
	if err != nil {
		t.Fatalf("utterance: %v", err)
	}

	if string(plan.Intent) != t11Intent {
		t.Errorf("intent = %q, want %q", plan.Intent, t11Intent)
	}
	if plan.Reason != "intent_accepted" {
		t.Errorf("reason = %q, want intent_accepted: a fallback would not be "+
			"accepted, and that is the difference this asserts", plan.Reason)
	}
	if plan.Action != conversation.ActionRespond {
		t.Errorf("action = %v, want respond", plan.Action)
	}
	if got := conv.State(); got != conversation.StateSpeaking {
		t.Errorf("state = %v, want speaking", got)
	}
}

// TestT11_TheRealTranscriptItselfResolvesToTheRealIntent joins the two halves
// by DATA FLOW rather than by a hand-copied string.
//
// # The weakness this closes
//
// TestT11_RealSpeechProducesTheExpectedTranscript proves fixture -> subprocess
// -> transcript. TestT11_TheIntentIsTheClassifiersAndNotAFallback proves
// transcript -> intent. But the transcript in the second test is a LITERAL a
// human copied out of a log. Nothing connects the two: if the real recogniser's
// output drifted, the first test would fail while the second went on passing
// against a stale constant, and the pair would still be described as a chain.
//
// Here the string handed to the classifier is the one the real subprocess
// actually produced on this run. No literal stands between them.
//
// # Nothing is substituted any more
//
// Until the Phase 16 T14 amendment this test carried the real WORDS across the
// seam but supplied an in-domain ASRConfidence, because forwarding what the
// provider actually reports classified every real utterance as noise. That was
// the last synthetic element in the chain and it is now gone: the utterance
// below carries the real transcript AND speech.ConfidenceUnknown, exactly as
// whispercli.go:691 emits it.
//
// So what is proven is the whole chain — real audio, real subprocess, real
// transcript, real confidence representation, real classifier, real plan.
//
// NO ACCURACY CLAIM. One fixture under an existing contract is not a
// measurement of recognition quality.
func TestT11_TheRealTranscriptItselfResolvesToTheRealIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}

	// --- half one: the real subprocess, and what it actually said -----------
	frames := t11Frames(t)
	r := t11Rig(t, len(frames))
	call := r.place(t)
	p := r.ports.of(t, call)
	r.greet(t, call)

	// THE PROVIDER IS THE REAL ONE, established before any audio is fed.
	//
	// FOUND BY MUTATION, NOT BY FORESIGHT: without this block, registering the
	// frozen FakeSTTProvider and scripting it with "Transfer me to Rajesh."
	// satisfied every remaining assertion in this test — in 0.21s rather than
	// the ~5s a real inference takes. The transcript assertions below cannot
	// distinguish a real recogniser from a fake told the answer, so the
	// provider's own identity is checked here instead.
	picked, err := r.registry.PickSTT(t10Lang)
	if err != nil {
		t.Fatalf("PickSTT: %v", err)
	}
	if picked.ID() != t10Config("").ID {
		t.Fatalf("the router selected %q, not the real recogniser", picked.ID())
	}
	d, ok := r.registry.Describe(voice.ProviderID(picked.ID()))
	if !ok || d.Engine != "openai-whisper" {
		t.Fatalf("the selected provider is not openai-whisper: %+v", d)
	}

	for _, f := range frames {
		if err := call.Audio(f); err != nil {
			t.Fatalf("frame %d: %v", f.Sequence, err)
		}
	}
	p.obs.awaitTurn(t, 5*time.Minute, "the real speech turn")

	// Captured DOWNSTREAM of the pipeline's consumption point. The observer is
	// told about a segment before the pipeline uses it, so capturing there
	// would let a corruption between the two travel invisibly into the intent
	// assertion below and still be called "the real transcript".
	req := r.model.LastRequest()
	if req == nil || len(req.Messages) != 1 {
		t.Fatalf("the real turn produced no single-message generation request: %+v", req)
	}
	captured := req.Messages[0].Content
	if strings.TrimSpace(captured) == "" {
		t.Fatal("the real recogniser produced an empty transcript; there is " +
			"nothing to carry into the classifier")
	}

	// --- half two: THAT string, through the real classifier ------------------
	_, vi := build(t)
	planner, conv, err := openPlannerRaw(t, vi,
		conversation.ConversationID("t11-real-transcript-to-intent"))
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := planner.Handle(e); err != nil {
			t.Fatalf("opening the floor: %v", err)
		}
	}

	// `captured`, not a literal. Un-normalised: leading space, capital and full
	// stop are the recogniser's, and the classifier's own normalisation is what
	// has to cope with them. Pre-cleaning it here would test this test.
	//
	// THE CONFIDENCE IS THE REAL ONE NOW. Until the Phase 16 T14 amendment this
	// line substituted an in-domain value, because forwarding what whispercli
	// actually reports classified every utterance as noise. That substitution
	// was the last synthetic element in this chain, and it is gone: the
	// utterance below carries exactly what the adapter emits
	// (whispercli.go:691), so nothing about this transcript OR its confidence
	// is invented.
	plan, err := planner.Handle(conversation.Event{
		Kind:  conversation.EventUtterance,
		Party: conversation.PartyCaller,
		Utterance: conversation.Utterance{
			Text:          captured,
			ASRConfidence: speech.ConfidenceUnknown,
		},
	})
	if err != nil {
		t.Fatalf("utterance: %v", err)
	}

	if string(plan.Intent) != t11Intent {
		t.Errorf("the real transcript %q resolved to intent %q, want %q",
			captured, plan.Intent, t11Intent)
	}
	if plan.Reason != "intent_accepted" {
		t.Errorf("reason = %q, want intent_accepted: a fallback would not be "+
			"accepted, and that is the difference this asserts", plan.Reason)
	}
	if plan.Action != conversation.ActionRespond {
		t.Errorf("action = %v, want respond", plan.Action)
	}
	if got := conv.State(); got != conversation.StateSpeaking {
		t.Errorf("state = %v, want speaking", got)
	}

	// --- the words must not have reached the logs ---------------------------
	//
	// Checked here rather than only in the deterministic security suite because
	// this is the one test where the text is a REAL recognition of REAL audio
	// travelling the whole path: adapter, pipeline, conversation and generator.
	// A leak introduced anywhere along it shows up here and nowhere else.
	if dump := strings.ToLower(r.logs.dump()); strings.Contains(dump, "rajesh") {
		t.Error("the caller's words reached the logs; transcript text is " +
			"SENSITIVE and must never be logged")
	}

	t.Logf("REAL TRANSCRIPT -> REAL INTENT: the subprocess produced %q on this "+
		"run, and that exact string — carried with the provider's own "+
		"ConfidenceUnknown (%v), nothing substituted — resolved to intent=%q "+
		"action=%v reason=%q. NO ACCURACY CLAIM IS MADE.",
		captured, speech.ConfidenceUnknown, plan.Intent, plan.Action, plan.Reason)
}

// ---------------------------------------------------------------------------
// Negative cases
// ---------------------------------------------------------------------------

// TestT11_NonSpeechProducesNoFabricatedIntent.
//
// The same real recogniser, the same real path, audio that is not speech. The
// correct transcript for a tone is none, and none must not become an intent.
func TestT11_NonSpeechProducesNoFabricatedIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}

	const frames = 60
	r := t11Rig(t, frames)
	call := r.place(t)
	p := r.ports.of(t, call)
	r.greet(t, call)

	gen := audiointel.NewSignalGenerator(media.PCM16Mono16k(), 20*time.Millisecond)
	gen.Reseed(audiointel.FixtureSeed)
	for i, f := range gen.Tone(220, 0.4, frames) {
		f.Sequence = uint64(i)
		if err := call.Audio(f); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	p.obs.awaitTurn(t, 5*time.Minute, "the non-speech turn")

	segs, _, _ := p.obs.snapshot()
	for _, s := range segs {
		t.Logf("a tone produced a segment: %q", s.Text)
	}
	if len(segs) != 0 {
		t.Errorf("%d transcript segments from non-speech audio, want 0", len(segs))
	}

	conv := call.Conversation()
	if _, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent"); ok {
		t.Error("a tone produced an intent; silence was turned into meaning")
	}
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions for a turn with nothing to answer, want 0", got)
	}
	if got := call.State(); got.Terminal() {
		t.Errorf("the session is %v after non-speech audio; silence is not a fault", got)
	}
}

// TestT11_AMissingFixtureIsATestFailureNotASkip also enforces ADR-0023 §2.
//
// The fixture is committed, so its absence is a broken checkout rather than a
// missing local tool — and a skip would hide the one thing this task delivered.
//
// # Why the checksum is here and not only in the README
//
// ADR-0023 requires a committed binary to pin its SHA-256 and byte size,
// because a binary cannot be reviewed in a diff: a reviewer approving a change
// to this file is approving bytes nobody can read. The size alone is a weak
// guard — audio of identical length is trivially substituted, and the
// transcript assertion elsewhere in this file was measured against these exact
// bytes and nothing else.
//
// Recording the digest in prose would leave the requirement aspirational. This
// is the same fixture-integrity argument the repository already applies to its
// .golden files, applied to the one artifact where a human cannot check it by
// eye.
func TestT11_AMissingFixtureIsATestFailureNotASkip(t *testing.T) {
	t.Parallel()

	// ADR-0023 §2. Update BOTH of these and testdata/README.md together, and
	// only alongside a re-measurement of the transcript assertion.
	const (
		wantSize   = 72640
		wantSHA256 = "8eed99cb93b575df01c128b66312d25d1fca3aba1d149fd2eada6059411ff870"
	)

	info, err := os.Stat(t11Fixture)
	if err != nil {
		t.Fatalf("the committed speech fixture is missing: %v", err)
	}
	if info.Size()%t11FrameBytes != 0 && info.Size() < t11FrameBytes {
		t.Fatalf("the fixture is %d bytes, too small to be 16 kHz speech", info.Size())
	}
	if got := info.Size(); got != wantSize {
		t.Errorf("the fixture is %d bytes, want %d: the transcript assertion "+
			"was measured against this exact audio and must be re-measured if it "+
			"changes", got, wantSize)
	}

	pcm, err := os.ReadFile(t11Fixture)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(pcm)); got != wantSHA256 {
		t.Errorf("the fixture's SHA-256 is %s, want %s: these bytes are not the "+
			"audio the transcript assertion was measured against (ADR-0023 §2)",
			got, wantSHA256)
	}
}

// TestT11_UnavailableRecogniserStillUsesTheExistingPolicy.
//
// Configured, enabled, and pointing at a tool that is not there. The existing
// availability check refuses it at registration with a typed error and a
// remedy; no fixture, no subprocess and no transcript are involved.
func TestT11_UnavailableRecogniserStillUsesTheExistingPolicy(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeDevelopment)
	cfg := t10Config(t.TempDir() + "/no-whisper-here")

	err := registerLocalSTT(reg, cfg)
	if !errors.Is(err, voice.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "pip install openai-whisper") {
		t.Errorf("the error names no remedy: %v", err)
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("an unavailable recogniser left %d entries in the registry", got)
	}
}

// TestT11_RealTranscriptWithUnknownConfidenceReachesTheIntent is the CONVERTED
// blocker test.
//
// # What it used to be
//
// TestT11_RealTranscriptIsGatedAsNoiseByUnknownConfidence, which pinned the
// defect: the pipeline forwarded speech.ConfidenceUnknown (-1.0) into a numeric
// gate, -1.0 < 0.40, and so every utterance from a recogniser that honestly
// reports no confidence was classified as noise. Its own doc comment instructed
// whoever resolved the blocker to update it rather than delete it. This is that
// update.
//
// # What it guards now
//
// EXACTLY what the pipeline forwards for a real whisper turn — the real
// transcript shape, and the real provider's sentinel — resolves through the
// real classifier.
//
// Nothing is substituted. The confidence below is the value whispercli.go:691
// actually emits, not an in-domain stand-in, which is what makes this the
// regression guard for the T14 amendment rather than a restatement of the
// control test.
func TestT11_RealTranscriptWithUnknownConfidenceReachesTheIntent(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	p, conv, err := openPlannerRaw(t, vi, conversation.ConversationID("t11-gate"))
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			t.Fatalf("opening the floor: %v", err)
		}
	}

	// The transcript exactly as the recogniser returns it — leading space,
	// capital and full stop included — and the confidence exactly as the
	// adapter reports it.
	const realTranscript = " Transfer me to Rajesh."
	plan, err := p.Handle(conversation.Event{
		Kind:  conversation.EventUtterance,
		Party: conversation.PartyCaller,
		Utterance: conversation.Utterance{
			Text:          realTranscript,
			ASRConfidence: speech.ConfidenceUnknown,
		},
	})
	if err != nil {
		t.Fatalf("utterance: %v", err)
	}

	if string(plan.Intent) != t11Intent {
		t.Errorf("intent = %q, want %q — a real provider's unreported confidence "+
			"must not stop its transcript reaching the classifier",
			plan.Intent, t11Intent)
	}
	if plan.Reason != "intent_accepted" {
		t.Errorf("reason = %q, want intent_accepted", plan.Reason)
	}
	if plan.Action != conversation.ActionRespond {
		t.Errorf("action = %v, want respond", plan.Action)
	}
	if plan.Reason == "noise_repeat" || plan.Reason == "noise_unrecoverable" {
		t.Errorf("the noise ladder is reachable again: %q", plan.Reason)
	}
	if got := conv.State(); got.IsTerminal() {
		t.Errorf("state = %v, want non-terminal", got)
	}

	t.Logf("RESOLVED: transcript %q with ASRConfidence=%v (ConfidenceUnknown, "+
		"exactly what whispercli reports) yields intent=%q action=%v reason=%q "+
		"state=%v. No numeric confidence was fabricated.",
		realTranscript, speech.ConfidenceUnknown,
		plan.Intent, plan.Action, plan.Reason, conv.State())
}
