package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/audiointel"
	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T10 — THE LOCAL RECOGNISER, THROUGH THE REAL PATH.
//
// Everything here uses the REAL registry and the REAL router. The provider is
// the real whispercli adapter, and where the tool is installed it is really
// executed — no FakeSTTProvider appears in any real-provider test in this file.
//
// WHAT IS NOT CLAIMED: nothing about recognition accuracy, model quality or
// telephony. The one real inference performed here runs on a TONE, and the only
// correct transcript for a tone is none at all.

const (
	t10Lang  = speech.LangEnglishIN
	t10Model = "tiny"
)

func t10Format() media.AudioFormat { return media.PCM16Mono16k() }

// t10Config is a valid configuration pointing at whatever tool path is given.
func t10Config(executable string) LocalSTTConfig {
	return LocalSTTConfig{
		Enabled:           true,
		Mode:              voice.ModeDevelopment,
		ID:                speech.ProviderID("whisper-cli-t10"),
		Executable:        executable,
		Model:             t10Model,
		Language:          t10Lang,
		Format:            t10Format(),
		MaxAudio:          30 * time.Second,
		MaxPendingResults: 16,
		StartTimeout:      5 * time.Minute,
		StopTimeout:       10 * time.Second,
		MaxStderrBytes:    4096,
	}
}

// t10Registry builds the REAL registry over the REAL router.
func t10Registry(t *testing.T, mode voice.DeploymentMode) *voice.ProviderRegistry {
	t.Helper()

	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	reg, err := voice.NewProviderRegistry(mode, router)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

// findWhisper locates the installed tool, or skips.
//
// SKIPS, NEVER FAKES. A machine without the tool cannot produce evidence that
// the tool ran, and substituting a fake here would turn "the real provider was
// reached" into a claim about a stand-in.
func findWhisper(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("whisper")
	if err != nil {
		t.Skipf("LOCAL PROVIDER RUNTIME NOT AVAILABLE: openai-whisper is not on "+
			"PATH (%v).\n  to fix: pip install openai-whisper", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Skipf("cannot resolve %q: %v", path, err)
	}

	// A COLD CACHE WOULD TRIGGER A DOWNLOAD, which this phase must not do.
	// Reported as unavailable rather than fetched.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve the home directory: %v", err)
	}
	model := filepath.Join(home, ".cache", "whisper", t10Model+".pt")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("the %q model is not cached at %s (%v); running would trigger a "+
			"download, which this phase does not do", t10Model, model, err)
	}
	return abs
}

// ---------------------------------------------------------------------------
// 1. Registration through the existing registry and router
// ---------------------------------------------------------------------------

// TestT10_WhisperCLIRegistersThroughTheExistingRegistry.
//
// The registration path only — the tool is never executed here, so this runs
// everywhere including CI. A non-existent executable is refused before
// construction, so the path used must be a real file; the test binary itself
// serves, and nothing spawns it.
func TestT10_WhisperCLIRegistersThroughTheExistingRegistry(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeDevelopment)
	if err := registerLocalSTT(reg, t10Config(os.Args[0])); err != nil {
		t.Fatalf("registerLocalSTT: %v", err)
	}

	if got := reg.Count(); got != 1 {
		t.Fatalf("the registry holds %d providers, want 1", got)
	}

	d, ok := reg.Describe(voice.ProviderID("whisper-cli-t10"))
	if !ok {
		t.Fatal("the provider was registered but cannot be described")
	}
	if d.Kind != voice.KindSTT {
		t.Errorf("kind = %v, want STT", d.Kind)
	}
	if d.Class != voice.ClassDevelopment {
		t.Errorf("class = %v, want development: openai-whisper is a batch tool "+
			"and calling it production-class is a claim ADR-0006 does not support",
			d.Class)
	}
	if d.Locality != voice.LocalityProcess {
		t.Errorf("locality = %v, want local_process", d.Locality)
	}
	if d.Engine != "openai-whisper" {
		t.Errorf("engine = %q, want openai-whisper", d.Engine)
	}
	if string(d.Model.Model) != t10Model {
		t.Errorf("model = %q, want %q", d.Model.Model, t10Model)
	}
	// The adapter declares its own limitation, and the descriptor carries it
	// rather than flattering it.
	if d.Capabilities.Streaming {
		t.Error("the descriptor claims streaming; the batch CLI cannot stream, " +
			"and an orchestrator told otherwise waits for partials that never come")
	}

	// --- the ROUTER, not a second selection mechanism ----------------------
	picked, err := reg.PickSTT(t10Lang)
	if err != nil {
		t.Fatalf("PickSTT: %v", err)
	}
	if picked.ID() != speech.ProviderID("whisper-cli-t10") {
		t.Errorf("the router selected %q", picked.ID())
	}
	if _, err := reg.PickSTT(speech.Language("xx-XX")); err == nil {
		t.Error("an unregistered language selected a provider")
	}
}

// TestT10_ProductionModeRefusesTheDevelopmentRecogniser is the guard, asserted
// against THIS wiring rather than against the registry in isolation.
//
// The refusal must reach the caller as an error: a wiring that logged it and
// carried on would start a production service with no recogniser and no
// complaint, which is worse than failing.
func TestT10_ProductionModeRefusesTheDevelopmentRecogniser(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeProduction)
	err := registerLocalSTT(reg, func() LocalSTTConfig {
		c := t10Config(os.Args[0])
		c.Mode = voice.ModeProduction
		return c
	}())

	if err == nil {
		t.Fatal("a development-class recogniser was registered in production mode")
	}
	if !strings.Contains(err.Error(), "ADR-0006") {
		t.Errorf("the refusal must name the decision it enforces, got %v", err)
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("a refused provider was still described (%d in the registry)", got)
	}
	if _, perr := reg.PickSTT(t10Lang); perr == nil {
		t.Error("the refused provider reached the router")
	}
}

// TestT10_DisabledRecogniserIsANoOp.
func TestT10_DisabledRecogniserIsANoOp(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeDevelopment)
	cfg := t10Config("/definitely/not/a/real/path")
	cfg.Enabled = false

	if err := registerLocalSTT(reg, cfg); err != nil {
		t.Fatalf("a disabled provider reported an error: %v", err)
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("a disabled provider registered %d entries", got)
	}
}

// TestT10_MissingToolIsTypedAndFailsStartup.
//
// The remedy matters as much as the refusal: the first time anybody enables
// this, the overwhelmingly likely outcome is that the tool is not installed.
func TestT10_MissingToolIsTypedAndFailsStartup(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeDevelopment)
	err := registerLocalSTT(reg, t10Config(filepath.Join(t.TempDir(), "not-whisper")))

	if err == nil {
		t.Fatal("a missing tool registered successfully")
	}
	if !errors.Is(err, voice.ErrProviderUnavailable) {
		t.Errorf("error = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "pip install openai-whisper") {
		t.Errorf("the error names no remedy: %v", err)
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("a missing tool left %d entries in the registry", got)
	}
}

// TestT10_ConfigurationIsValidatedBeforeAnythingIsConstructed.
//
// Every problem at once, and the deployment mode among them: it has no default,
// because defaulting it is how a development-class recogniser reaches
// production traffic.
func TestT10_ConfigurationIsValidatedBeforeAnythingIsConstructed(t *testing.T) {
	t.Parallel()

	reg := t10Registry(t, voice.ModeDevelopment)
	err := registerLocalSTT(reg, LocalSTTConfig{Enabled: true})
	if err == nil {
		t.Fatal("an empty configuration was accepted")
	}

	var cfgErr *voice.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error = %v, want a *voice.ConfigError listing every problem", err)
	}
	for _, want := range []string{"STT.Mode", "STT.Executable", "STT.Model"} {
		found := false
		for _, p := range cfgErr.Problems {
			if strings.Contains(p, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no problem reported for %s; got %v", want, cfgErr.Problems)
		}
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("an invalid configuration registered %d entries", got)
	}
}

// ---------------------------------------------------------------------------
// 2. The real tool, through the real pipeline
// ---------------------------------------------------------------------------

// TestT10_RealWhisperCLIRunsThroughTheDriverAndPipeline is the evidence test.
//
// # What it proves
//
// A real openai-whisper subprocess is reached through the production wiring:
// registerLocalSTT -> the real ProviderRegistry -> the real speech.ProviderRouter
// -> the real voice.Pipeline -> the real call driver. Audio goes in as
// media.Frame values and a real process transcribes them.
//
// # What it deliberately does not prove
//
// The fixture is a 220 Hz TONE, produced by the frozen audiointel.SignalGenerator
// — the repository's existing deterministic audio fixture, reproducible from a
// fixed seed and containing nobody's voice. A tone is not speech, so the only
// correct transcript for it is NONE, and that is what is asserted: the provider
// ran and fabricated nothing.
//
// NO INTENT IS CLAIMED FROM REAL AUDIO. Proving "real transcript -> real intent"
// needs real speech, and no speech fixture exists in this repository that can be
// produced without an external speech engine. See docs/phase16/WHISPERCLI_WIRING.md.
func TestT10_RealWhisperCLIRunsThroughTheDriverAndPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}
	whisper := findWhisper(t)

	workDir := t.TempDir()
	cfg := t10Config(whisper)
	cfg.WorkDir = workDir

	// ONE turn holding the WHOLE fixture, and enough audio for the model to
	// conclude there is no speech in it.
	//
	// This used to feed five frames — 100 ms — and assert zero segments. It
	// passed, but for the wrong reason: the adapter was sending the platform's
	// BCP-47 tag as --language, argparse rejected it, and whisper never ran at
	// all. With that fixed (Phase 16 T11) the model really does run, and on
	// 100 ms of tone it emits a hallucinated "You" — a known behaviour of
	// whisper on very short non-speech input, and not something this adapter
	// can or should prevent.
	//
	// 1.2 s is the length at which "a tone produces nothing" is stable, which
	// is also what the adapter's own 2 s test and T11's non-speech test use.
	const toneFrames = 60

	r := newT7Rig(t, t7Opts{
		stt: func(reg *voice.ProviderRegistry) error {
			return registerLocalSTT(reg, cfg)
		},
		onsetAt: 0, endAt: toneFrames - 1, absolute: true,
		maxPendingFrames: toneFrames * 2,
	})

	call := r.place(t)
	p := r.ports.of(t, call)
	r.greet(t, call)

	// The fixture: a continuous tone, from the frozen generator, at the
	// session's own format and cadence.
	gen := audiointel.NewSignalGenerator(t10Format(), 20*time.Millisecond)
	gen.Reseed(audiointel.FixtureSeed)
	tone := gen.Tone(220, 0.4, toneFrames)

	started := time.Now()
	for i, f := range tone {
		// The scripted analyser decides the turn boundaries; the payload is what
		// reaches the recogniser. Sequence numbers drive the onset and endpoint,
		// so they come from the loop rather than from the generator.
		f.Sequence = uint64(i)
		if err := call.Audio(f); err != nil {
			// NOT tolerated: a dropped frame changes the audio the recogniser
			// analyses, and with it what it concludes about the tone.
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	// Bounded, and generous: batch inference is the whole cost of the turn.
	p.obs.awaitTurn(t, 5*time.Minute, "the real recognition turn")
	elapsed := time.Since(started)

	// --- the REAL provider was reached -------------------------------------
	picked, err := r.registry.PickSTT(t10Lang)
	if err != nil {
		t.Fatalf("PickSTT: %v", err)
	}
	if picked.ID() != cfg.ID {
		t.Fatalf("the pipeline would have used %q, not the real recogniser", picked.ID())
	}
	if d, ok := r.registry.Describe(voice.ProviderID(cfg.ID)); !ok ||
		d.Engine != "openai-whisper" {
		t.Fatalf("the registry does not describe a real openai-whisper provider")
	}

	// --- and it fabricated nothing -----------------------------------------
	segs, _, outcomes := p.obs.snapshot()
	for _, s := range segs {
		t.Logf("a 220 Hz tone produced a segment: %q (final=%v)", s.Text, s.IsFinal)
	}
	if len(segs) > 0 {
		t.Errorf("a tone produced %d transcript segments; either the model "+
			"hallucinated or the adapter fabricated them", len(segs))
	}

	// --- the session survived it -------------------------------------------
	if got := call.State(); got.Terminal() {
		t.Errorf("the voice session is %v after a non-speech turn; silence is not "+
			"a fault", got)
	}
	if err := call.Pipeline().Err(); err != nil {
		t.Errorf("the pipeline recorded a fault for a non-speech turn: %v", err)
	}
	if got := completions(call.Conversation()); got != 0 {
		t.Errorf("%d speech completions for a turn with no transcript, want 0", got)
	}

	// --- no temporary audio survived the turn ------------------------------
	leftovers, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatalf("reading the work directory: %v", err)
	}
	if len(leftovers) != 0 {
		names := make([]string, 0, len(leftovers))
		for _, e := range leftovers {
			names = append(names, e.Name())
		}
		t.Errorf("the recogniser left %v in its work directory; a caller's audio "+
			"must not outlive the turn", names)
	}

	t.Logf("REAL RUN: openai-whisper model=%s reached through the production "+
		"registry/router/pipeline; %d frames of non-speech audio, turn outcomes=%v, "+
		"%s wall (PROVIDER INFERENCE, not orchestration overhead). "+
		"NO ACCURACY CLAIM IS MADE.", t10Model, len(tone), outcomes, elapsed)
}

// TestT10_RealWhisperCLIFailureReachesTheT9Policy joins the two halves of this
// phase: a real provider that cannot start, closed by the failure policy T9
// implemented.
//
// The tool is real and the configuration is real; only the executable path is
// wrong, which is the single most likely production misconfiguration.
func TestT10_RealWhisperCLIFailureReachesTheT9Policy(t *testing.T) {
	t.Parallel()

	// Registration itself refuses a missing tool, so the failure is injected
	// where a real one occurs: the executable exists at startup and cannot be
	// run when the turn opens.
	dir := t.TempDir()
	fake := filepath.Join(dir, "whisper-that-cannot-run")
	if err := os.WriteFile(fake, []byte("not an executable"), 0o600); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}

	cfg := t10Config(fake)
	cfg.WorkDir = dir
	cfg.StartTimeout = 20 * time.Second

	r := newT7Rig(t, t7Opts{stt: func(reg *voice.ProviderRegistry) error {
		return registerLocalSTT(reg, cfg)
	}})

	call := r.place(t)
	r.greet(t, call)

	for i := uint64(0); i < t2TurnFrames; i++ {
		if err := call.Audio(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	// The T9 policy: the session dies and the conversation is CLOSED, not left
	// open beside it.
	select {
	case <-call.Closed():
	case <-time.After(2 * time.Minute):
		t.Fatalf("the call did not close after the recogniser failed; session=%v",
			call.State())
	}

	conv := call.Conversation()
	if got := conv.State(); got != conversation.StateEnded {
		t.Errorf("conversation state = %v, want ended", got)
	}
	if !hasTrigger(conv, conversation.TriggerFault) {
		t.Error("the trace records no fault")
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments for a recogniser failure, want 0", got)
	}
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions, want 0", got)
	}

	// No orphaned child, and no caller audio left behind.
	leftovers, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the work directory: %v", err)
	}
	for _, e := range leftovers {
		if e.Name() != filepath.Base(fake) {
			t.Errorf("the failed recogniser left %q behind", e.Name())
		}
	}
}
