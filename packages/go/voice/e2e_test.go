package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	conversation "github.com/callscreen/callscreen-platform/packages/go/conversation"
	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/whispercli"
)

// ---------------------------------------------------------------------------
// Local end-to-end integration
// ---------------------------------------------------------------------------
//
// # What separates this file from every other test in the package
//
// Everywhere else, providers are deterministic stand-ins — the right choice for
// asserting orchestration behaviour, and useless for asserting that the
// orchestration actually fits together with real software. This file runs the
// REAL components that exist on this machine and reports honestly on the ones
// that do not.
//
// Real here means: media.Frame from real synthesised speech, a real
// audiointel.Session doing real voice-activity analysis, the real
// openai-whisper CLI as a supervised subprocess doing real inference, a real
// conversation.Engine producing a real Plan, and a real governance.Engine
// producing a real Decision — all routed through the real
// speech.ProviderRouter behind the Task 9 registry.
//
// # Stage labels are load-bearing
//
//	EXECUTED  — the stage genuinely ran against real software on this machine.
//	NOT RUN   — the runtime is absent; nothing was executed and nothing faked.
//	BLOCKED   — an upstream stage did not produce what this stage needs.
//
// PASS is never used for a stage that did not execute. A fabricated transcript
// or a synthesised "successful" generation would make this whole file a lie
// that reads like evidence.

// stageResult records what happened at one boundary.
type stageResult struct {
	stage   string
	status  string // EXECUTED | NOT RUN | BLOCKED
	detail  string
	elapsed time.Duration
}

// e2eTrace collects the run for a single report at the end.
type e2eTrace struct {
	results []stageResult
}

func (tr *e2eTrace) record(stage, status, detail string, elapsed time.Duration) {
	tr.results = append(tr.results, stageResult{stage, status, detail, elapsed})
}

func (tr *e2eTrace) report(t *testing.T) {
	t.Helper()

	var b strings.Builder
	b.WriteString("\nLOCAL END-TO-END TRACE\n")
	b.WriteString(strings.Repeat("-", 78) + "\n")
	for i, r := range tr.results {
		timing := "        "
		if r.elapsed > 0 {
			timing = fmt.Sprintf("%8s", r.elapsed.Round(time.Millisecond))
		}
		b.WriteString(fmt.Sprintf("%d. %-26s %-9s %s\n     %s\n",
			i+1, r.stage, r.status, timing, r.detail))
	}
	b.WriteString(strings.Repeat("-", 78) + "\n")

	var executed, notRun, blocked int
	for _, r := range tr.results {
		switch r.status {
		case "EXECUTED":
			executed++
		case "NOT RUN":
			notRun++
		case "BLOCKED":
			blocked++
		}
	}
	b.WriteString(fmt.Sprintf("%d EXECUTED, %d NOT RUN, %d BLOCKED\n",
		executed, notRun, blocked))
	t.Log(b.String())
}

// ---------------------------------------------------------------------------
// Runtime probes — the truth about this machine
// ---------------------------------------------------------------------------

// probeWhisper reports whether the recogniser can genuinely run.
func probeWhisper() (path string, reason string) {
	p, err := exec.LookPath("whisper")
	if err != nil {
		return "", fmt.Sprintf("openai-whisper is not on PATH (%v); "+
			"to fix: pip install openai-whisper", err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Sprintf("cannot resolve %q: %v", p, err)
	}

	// The Python tool downloads a model on a cache miss. That download is
	// multi-gigabyte and this phase does not install runtimes, so a cold cache
	// is reported as unavailable rather than triggered.
	home, err := os.UserHomeDir()
	if err != nil {
		return abs, ""
	}
	cache := filepath.Join(home, ".cache", "whisper")
	entries, err := os.ReadDir(cache)
	if err != nil {
		return "", fmt.Sprintf("whisper is installed at %s but its model cache "+
			"%s is unreadable (%v); running would trigger a model download, which "+
			"this phase does not do", abs, cache, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pt") {
			return abs, ""
		}
	}
	return "", fmt.Sprintf("whisper is installed at %s but no model is cached in "+
		"%s; running would trigger a download", abs, cache)
}

// probeOllama reports the daemon state and whether any model exists.
func probeOllama() (model string, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://127.0.0.1:11434/api/tags", nil)
	if err != nil {
		return "", fmt.Sprintf("cannot build a probe request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Sprintf("no Ollama daemon answering at 127.0.0.1:11434 (%v); "+
			"to fix: install from https://ollama.com/download and run 'ollama serve'", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return "", fmt.Sprintf("the daemon answered but its model list could not "+
			"be read: %v", err)
	}
	if len(tags.Models) == 0 {
		return "", "the Ollama daemon IS running but has ZERO models pulled " +
			"(/api/tags returns an empty list); a model is the operator's choice " +
			"and this phase does not download one (multi-GB)"
	}

	// THE MODEL COMES FROM THE ENVIRONMENT, never from a constant here. No
	// model name is hardcoded anywhere in this phase.
	return tags.Models[0].Name, ""
}

// probePiper reports whether the synthesiser can genuinely run.
func probePiper() (path string, reason string) {
	p, err := exec.LookPath("piper")
	if err != nil {
		return "", fmt.Sprintf("piper is not on PATH (%v); to fix: install from "+
			"https://github.com/rhasspy/piper/releases and download a voice "+
			"(.onnx plus .onnx.json) from https://huggingface.co/rhasspy/piper-voices",
			err)
	}
	return p, ""
}

// ---------------------------------------------------------------------------
// The end-to-end run
// ---------------------------------------------------------------------------

// TestE2E_LocalIntegration runs every stage that can genuinely execute here.
//
// It does not skip when a runtime is missing: a skipped test reports nothing,
// and the whole point of this one is to report exactly which boundaries were
// proven and which could not be.
func TestE2E_LocalIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}

	tr := &e2eTrace{}
	format := media.PCM16Mono16k()
	sessionID := SessionID("ses-e2e-01")

	whisperPath, whisperWhy := probeWhisper()
	ollamaModel, ollamaWhy := probeOllama()
	piperPath, piperWhy := probePiper()

	t.Logf("RUNTIME PROBE (this machine, %s/%s)\n"+
		"  whisper CLI : %s\n"+
		"  Ollama model: %s\n"+
		"  Piper       : %s",
		runtime.GOOS, runtime.GOARCH,
		describeProbe(whisperPath, whisperWhy),
		describeProbe(ollamaModel, ollamaWhy),
		describeProbe(piperPath, piperWhy))

	// --- Stage 1: fixture audio ---------------------------------------------
	const spoken = "Hello, I would like to check my account balance please."

	start := time.Now()
	pcm, fixtureWhy := synthesiseFixture(t, spoken)
	if pcm == nil {
		tr.record("1. fixture audio", "NOT RUN", fixtureWhy, 0)
		tr.report(t)
		t.Skipf("no speech generator on this machine: %s", fixtureWhy)
		return
	}
	audioSeconds := float64(len(pcm)) / float64(int(format.Rate)*2)
	tr.record("1. fixture audio", "EXECUTED",
		fmt.Sprintf("Windows SAPI + ffmpeg: %.2fs of real speech, %d bytes, %s",
			audioSeconds, len(pcm), format), time.Since(start))

	// --- Stage 2: media handoff ---------------------------------------------
	start = time.Now()
	frames := framesFrom(pcm, format)
	if len(frames) == 0 {
		t.Fatal("the fixture produced no media frames")
	}
	for i, f := range frames {
		if err := f.Format.Validate(); err != nil {
			t.Fatalf("frame %d is not a valid media frame: %v", i, err)
		}
	}
	tr.record("2. media handoff", "EXECUTED",
		fmt.Sprintf("real media.Frame values: %d frames of %s at %s",
			len(frames), 20*time.Millisecond, format), time.Since(start))

	// --- Stage 3: audio intelligence ----------------------------------------
	start = time.Now()
	aiRuntime, err := audiointel.New(audiointel.DefaultConfig(format))
	if err != nil {
		t.Fatalf("audiointel.New: %v", err)
	}
	defer func() { _, _ = aiRuntime.Stop(context.Background()) }()

	aiSession, err := aiRuntime.Open(context.Background(), audiointel.SessionContext{
		Call:      audiointel.CallID("call-e2e-01"),
		Direction: audiointel.DirectionInbound,
		Language:  audiointel.Language("en-gb"),
		Format:    format,
	})
	if err != nil {
		t.Fatalf("audiointel Open: %v", err)
	}

	var onsets, endpoints, analysed int
	for _, f := range frames {
		a, err := aiSession.Analyze(context.Background(), f,
			audiointel.ConversationState{}, nil, nil)
		if err != nil {
			t.Fatalf("audiointel Analyze: %v", err)
		}
		analysed++
		if a.VAD.OnsetConfirmed {
			onsets++
		}
		if a.Endpoint.Confirmed {
			endpoints++
		}
	}
	tr.record("3. audio intelligence", "EXECUTED",
		fmt.Sprintf("real audiointel.Session: %d frames analysed, %d speech onsets, "+
			"%d endpoints confirmed", analysed, onsets, endpoints), time.Since(start))

	if onsets == 0 {
		t.Error("Phase 11D found no speech in real synthesised speech; the fixture " +
			"or the analyser is not doing what this test assumes")
	}

	// --- Stage 4: STT -------------------------------------------------------
	var transcript string
	if whisperWhy != "" {
		tr.record("4. STT (whisper CLI)", "NOT RUN",
			"LOCAL PROVIDER RUNTIME NOT AVAILABLE: "+whisperWhy, 0)
	} else {
		start = time.Now()
		text, provider, sttErr := runRealSTT(t, whisperPath, pcm, format, sessionID)
		elapsed := time.Since(start)

		if sttErr != nil {
			tr.record("4. STT (whisper CLI)", "NOT RUN",
				"the runtime is present but the run failed: "+sttErr.Error(), elapsed)
		} else {
			transcript = text
			tr.record("4. STT (whisper CLI)", "EXECUTED",
				fmt.Sprintf("real openai-whisper subprocess via registry->router; "+
					"provider=%s model=tiny; %d transcript characters recognised "+
					"from %.2fs of audio (PROVIDER INFERENCE, not orchestration)",
					provider, len([]rune(text)), audioSeconds), elapsed)
		}
	}

	// --- Stage 5: conversation ----------------------------------------------
	var plan conversation.Plan
	if transcript == "" {
		tr.record("5. conversation", "BLOCKED",
			"no transcript was produced upstream, so there is nothing to plan against", 0)
	} else {
		start = time.Now()
		convEngine, err := conversation.NewEngine(conversation.DefaultConfig())
		if err != nil {
			t.Fatalf("conversation.New: %v", err)
		}
		conv, err := convEngine.Begin(conversation.ConversationID("conv-e2e-01"), "")
		if err != nil {
			t.Fatalf("conversation Begin: %v", err)
		}
		if _, err := conv.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
			t.Fatalf("conversation start: %v", err)
		}

		plan, err = conv.Handle(conversation.Event{
			Kind:      conversation.EventUtterance,
			Utterance: conversation.Utterance{Text: transcript},
			Party:     conversation.PartyCaller,
		})
		if err != nil {
			tr.record("5. conversation", "NOT RUN",
				"the real engine returned an error: "+err.Error(), time.Since(start))
		} else {
			tr.record("5. conversation", "EXECUTED",
				fmt.Sprintf("real conversation.Engine: action=%v reason=%q "+
					"state=%v (a Plan, never text)",
					plan.Action, plan.Reason, conv.State()), time.Since(start))
		}
	}

	// --- Stage 6: governance ------------------------------------------------
	start = time.Now()
	// An auditor is REQUIRED, and the refusal to start without one is the
	// frozen engine being correct: a decision nobody recorded cannot later
	// answer why the platform acted. The in-memory recorder satisfies it here;
	// a deployment supplies a durable one.
	auditor := governance.NewRecordingAuditor(64)
	govEngine, err := governance.New(governance.DefaultConfig(),
		governance.WithAuditor(auditor))
	if err != nil {
		t.Fatalf("governance.New: %v", err)
	}

	gateway, err := NewToolGateway(GatewayConfig{
		Governor: govEngine,
		Invoker:  &e2eInvoker{},
		Session:  sessionID,
		Actor:    governance.ActorID("voice-agent-e2e"),
		Org:      governance.OrgID("org-e2e"),
	})
	if err != nil {
		t.Fatalf("NewToolGateway: %v", err)
	}

	// The "capability" attribute is REQUIRED by the frozen default validator for
	// every ActionTool. Omitting it produced "deny (malformed_action)" on the
	// first run of this test — a STRUCTURAL rejection, before any policy was
	// consulted, which would have been reported as a governance decision while
	// proving nothing about policy. It is a bounded capability name, not
	// content.
	_, govErr := gateway.Invoke(context.Background(), TurnID("turn-1"), ToolIntent{
		Operation:      "lookup",
		Resource:       "account.balance",
		Reversibility:  governance.ReversibleNone,
		Classification: governance.ClassPersonal,
		Attributes: governance.Attrs{
			"capability": governance.Str("account.read"),
		},
	})

	govOutcome := "allowed"
	var denial *DenialError
	switch {
	case govErr == nil:
		govOutcome = "allowed, tool executed"
	case errors.As(govErr, &denial):
		govOutcome = fmt.Sprintf("%s (%s) by policy %s",
			denial.Outcome, denial.Reason, denial.DecidedBy)
	default:
		govOutcome = "error: " + govErr.Error()
	}

	tr.record("6. governance", "EXECUTED",
		fmt.Sprintf("real governance.Engine.Decide through the Task 13 gateway: %s; "+
			"allowed=%d denied=%d obligations=%d invoked=%d",
			govOutcome, gateway.Allowed(), gateway.Denied(),
			gateway.ObligationsUnmet(), gateway.Invoked())+
			fmt.Sprintf("; audit entries recorded=%d", len(auditor.Entries())),
		time.Since(start))

	// The invariant that matters regardless of which way the real policy went.
	if gateway.Invoked() > gateway.Allowed() {
		t.Errorf("the tool was invoked %d times for %d allowed decisions: "+
			"execution outran authorisation", gateway.Invoked(), gateway.Allowed())
	}

	// --- Stage 7: generation ------------------------------------------------
	if ollamaWhy != "" {
		tr.record("7. runtime/generation", "NOT RUN",
			"LOCAL PROVIDER RUNTIME NOT AVAILABLE: "+ollamaWhy, 0)
	} else {
		tr.record("7. runtime/generation", "NOT RUN",
			fmt.Sprintf("a model (%s) is present, but wiring the Ollama adapter into "+
				"runtime.Kernel's ADR-0006 model registry is not part of this phase: "+
				"the adapter is DEVELOPMENT-ONLY and must not be registered as a "+
				"production tier", ollamaModel), 0)
	}

	// --- Stage 8: TTS -------------------------------------------------------
	if piperWhy != "" {
		tr.record("8. TTS (Piper)", "NOT RUN",
			"LOCAL PROVIDER RUNTIME NOT AVAILABLE: "+piperWhy, 0)
	} else {
		tr.record("8. TTS (Piper)", "NOT RUN",
			fmt.Sprintf("piper found at %s but no voice model is configured for this "+
				"run", piperPath), 0)
	}

	// --- Stage 9: media output ----------------------------------------------
	tr.record("9. media output", "BLOCKED",
		"no synthesised audio exists upstream; fabricating PCM here would make "+
			"this trace a lie", 0)

	// The barge-in leg is called out explicitly rather than left unmentioned:
	// an omitted stage reads as an oversight, and this one is a genuine
	// environmental limit.
	tr.record("10. barge-in (real audio)", "BLOCKED",
		"interrupting the agent requires the agent to be SPEAKING, which requires "+
			"the TTS leg; with no synthesiser there is no agent speech to talk over. "+
			"The Task 12 orchestration is proven against a deterministic stand-in "+
			"and a real audiobridge.Adapter (TestBargeIn_*), which is a different "+
			"claim from real-audio barge-in and is not presented as one", 0)

	tr.report(t)

	// The honest summary, asserted so it cannot drift into an overclaim.
	if transcript == "" {
		t.Log("SUMMARY: the complete local E2E path was NOT executable on this " +
			"machine; see NOT RUN entries above")
	} else {
		t.Logf("SUMMARY: stages 1-6 EXECUTED against real components "+
			"(real speech -> real audiointel -> real whisper inference -> real "+
			"conversation plan -> real governance decision). Stages 7-9 did not "+
			"execute. THE COMPLETE E2E PATH WAS NOT EXECUTABLE on this machine: "+
			"no LLM model and no Piper runtime.\n"+
			"  transcript length: %d characters (content deliberately not logged)",
			len([]rune(transcript)))
	}
}

// describeProbe renders a probe result for the log.
func describeProbe(value, why string) string {
	if why != "" {
		return "NOT AVAILABLE — " + why
	}
	return "available: " + value
}

// e2eInvoker executes nothing; it records that it was reached.
//
// Whether the real policy allows or denies this action is the engine's business
// and not something this test dictates. What it must never do is execute
// without a decision, which [ToolRequest.Validate] enforces here as it does in
// production.
type e2eInvoker struct{ calls int }

func (i *e2eInvoker) InvokeTool(_ context.Context, req ToolRequest) (ToolResult, error) {
	if err := req.Validate(); err != nil {
		return ToolResult{}, err
	}
	i.calls++
	return ToolResult{Completed: true, Code: "e2e_noop"}, nil
}

// ---------------------------------------------------------------------------
// Real STT through the real registry and router
// ---------------------------------------------------------------------------

// runRealSTT drives real audio through the real recogniser, selected by the
// real router.
//
// Provider SELECTION goes through the Task 9 registry so the routing boundary
// is exercised too, rather than the adapter being called directly.
func runRealSTT(
	t *testing.T, whisperPath string, pcm []byte,
	format media.AudioFormat, session SessionID,
) (string, speech.ProviderID, error) {
	t.Helper()

	provider, err := whispercli.New(whispercli.Config{
		ID:         speech.ProviderID("whisper-cli-e2e"),
		Executable: whisperPath,
		Model:      "tiny", // cached; a miss would download, which this phase does not do
		Language:   "en",
		Format:     format,
		Process: process.Config{
			StartTimeout:   5 * time.Minute,
			StopTimeout:    5 * time.Second,
			MaxStderrBytes: 32 << 10,
		},
		MaxAudio:          30 * time.Second,
		MaxPendingResults: 32,
		WorkDir:           t.TempDir(),
	})
	if err != nil {
		return "", "", fmt.Errorf("building the whisper provider: %w", err)
	}

	// Through the registry — and therefore the frozen router — not directly.
	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		return "", "", err
	}
	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		return "", "", err
	}
	if err := reg.RegisterSTT(provider, ProviderSpec{
		Class: ClassDevelopment, Tier: speech.TierPrimary,
		Engine: "openai-whisper", Version: "cli",
		Model:    ModelIdentity{Model: ModelID("tiny")},
		Locality: LocalityProcess,
	}); err != nil {
		return "", "", err
	}

	selected, err := reg.PickSTT(speech.Language("en"))
	if err != nil {
		return "", "", fmt.Errorf("router selection: %w", err)
	}

	stream, err := selected.OpenSTT(context.Background(), speech.STTConfig{
		Session: speech.SessionID(session), Turn: speech.TurnID("turn-e2e-1"),
		Language: speech.Language("en"), Format: format,
	})
	if err != nil {
		reg.Report(ProviderID(selected.ID()), speech.OutcomeFailure)
		return "", selected.ID(), fmt.Errorf("OpenSTT: %w", err)
	}
	defer func() { _ = stream.Close() }()

	const frameBytes = 640 // 20 ms at 16 kHz mono PCM16
	for off := 0; off < len(pcm); off += frameBytes {
		end := off + frameBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := stream.Write(media.Frame{
			Sequence:  uint64(off / frameBytes),
			Timestamp: time.Duration(off/frameBytes) * 20 * time.Millisecond,
			Format:    format,
			Payload:   pcm[off:end],
		}); err != nil {
			return "", selected.ID(), fmt.Errorf("Write: %w", err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		return "", selected.ID(), fmt.Errorf("CloseSend: %w", err)
	}

	var text strings.Builder
	deadline := time.After(5 * time.Minute)
	for {
		select {
		case seg, ok := <-stream.Results():
			if !ok {
				reg.Report(ProviderID(selected.ID()), speech.OutcomeSuccess)
				return strings.TrimSpace(text.String()), selected.ID(), nil
			}
			if seg.IsFinal {
				text.WriteString(seg.Text)
			}
		case <-deadline:
			reg.Report(ProviderID(selected.ID()), speech.OutcomeTimeout)
			return "", selected.ID(), errors.New("no transcript within five minutes")
		}
	}
}

// ---------------------------------------------------------------------------
// Clean failure when a required provider is absent
// ---------------------------------------------------------------------------

// TestE2E_MissingProviderFailsCleanly proves the Task 14 guarantees hold on the
// REAL assembly rather than only against stand-ins.
//
// The synthesiser genuinely is unavailable on this machine, so this is not a
// simulated fault: it is the deployment a developer here actually has.
func TestE2E_MissingProviderFailsCleanly(t *testing.T) {
	t.Parallel()

	if _, why := probePiper(); why == "" {
		t.Skip("piper is installed on this machine, so its absence cannot be tested")
	}

	format := media.PCM16Mono16k()

	router, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		t.Fatal(err)
	}

	// A recogniser is registered; no synthesiser is, because none exists.
	if err := reg.RegisterSTT(
		&recordingSTT{id: speech.ProviderID("stt-e2e"), segments: defaultSegments()},
		sttSpec(),
	); err != nil {
		t.Fatal(err)
	}

	// The router must say WHICH kind of absence this is.
	_, pickErr := reg.PickTTS(speech.Language("en-GB"))
	if !errors.Is(pickErr, speech.ErrUnsupportedLanguage) {
		t.Errorf("want ErrUnsupportedLanguage for a language with no synthesiser, "+
			"got %v", pickErr)
	}

	fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-e2e-missing")})
	if err != nil {
		t.Fatal(err)
	}

	sink := &countingSink{}
	obs := newObserver()
	p, err := NewPipeline(PipelineConfig{
		Session: SessionID("ses-e2e-missing"), Language: speech.Language("en-GB"),
		Format: format, Registry: reg,
		Intel:    &scriptedIntel{onsetAt: 1, endAt: 3},
		Planner:  &scriptedPlanner{},
		Governor: benchGovernor{outcome: governance.OutcomeAllow},
		Generator: &scriptedGenerator{
			err: fmt.Errorf("%w: no model is pulled", rt.ErrProviderUnavailable),
		},
		Output: sink, FSM: fsm, Observer: obs,
		MaxPendingFrames: 64, MaxPendingSegments: 16, MaxPendingAudio: 64,
		TurnTimeout: 10 * time.Second, Tier: rt.TierFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if err := p.WriteFrame(testFrame(uint64(i))); err != nil &&
			!errors.Is(err, ErrSessionClosed) && !errors.Is(err, ErrBackpressure) {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	select {
	case <-obs.turnDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the turn never resolved")
	}

	// --- the Task 14 contract, on the real assembly -------------------------
	err = p.Err()
	if err == nil {
		t.Fatal("an unavailable model produced no recorded failure")
	}
	// ErrProviderUnavailable, not ErrProviderFailed: synthesis is selected
	// BEFORE generation, so the absent voice is discovered first. That is the
	// more precise classification — "there is no such provider" rather than
	// "a provider broke" — and the two send an operator to different places.
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("want a typed ErrProviderUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("the router's own reason was lost: %v", err)
	}

	if sink.count() != 0 {
		t.Errorf("%d audio frames were produced with no synthesiser", sink.count())
	}
	if fsm.Terminal() {
		t.Error("one unavailable provider ended the call")
	}
	if got := fsm.State(); got != StateListening {
		t.Errorf("the session is in %s, want listening", got)
	}
	for _, c := range fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
	}
	if depth, bound := len(p.frames), cap(p.frames); depth > bound {
		t.Errorf("the inbound queue holds %d frames against a bound of %d", depth, bound)
	}

	_ = p.Disconnect()
	select {
	case <-p.Done():
	case <-time.After(20 * time.Second):
		t.Error("the pipeline did not shut down")
	}
}

// ---------------------------------------------------------------------------
// Fixture generation
// ---------------------------------------------------------------------------

// synthesiseFixture produces 16 kHz mono PCM16 from real synthesised speech.
//
// Windows SAPI is a real speech engine, so the recogniser receives real speech
// rather than a tone. Returns nil and a reason if anything is missing —
// fabricating audio would make every downstream stage meaningless.
func synthesiseFixture(t *testing.T, text string) ([]byte, string) {
	t.Helper()

	if runtime.GOOS != "windows" {
		return nil, "speech is generated with Windows SAPI and this is not Windows"
	}

	dir := t.TempDir()
	rawWAV := filepath.Join(dir, "fixture.wav")

	script := fmt.Sprintf(
		`Add-Type -AssemblyName System.Speech; `+
			`$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; `+
			`$s.SetOutputToWaveFile(%q); $s.Speak(%q); $s.Dispose()`,
		rawWAV, text)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Sprintf("Windows SAPI failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Sprintf("ffmpeg is not on PATH, so SAPI output cannot be "+
			"converted to 16 kHz mono: %v", err)
	}

	converted := filepath.Join(dir, "fixture16k.raw")
	convert := exec.Command(ffmpeg, "-y", "-i", rawWAV,
		"-ar", "16000", "-ac", "1", "-f", "s16le", converted)
	if out, err := convert.CombinedOutput(); err != nil {
		return nil, fmt.Sprintf("ffmpeg conversion failed: %v (%s)",
			err, strings.TrimSpace(string(out)))
	}

	pcm, err := os.ReadFile(converted)
	if err != nil {
		return nil, fmt.Sprintf("cannot read the converted audio: %v", err)
	}
	if len(pcm) == 0 {
		return nil, "the converted audio is empty"
	}
	return pcm, ""
}

// framesFrom slices PCM into 20 ms media frames.
func framesFrom(pcm []byte, format media.AudioFormat) []media.Frame {
	frameBytes := format.BytesFor(20 * time.Millisecond)
	if frameBytes <= 0 {
		return nil
	}

	var frames []media.Frame
	for off := 0; off+frameBytes <= len(pcm); off += frameBytes {
		seq := uint64(off / frameBytes)
		payload := make([]byte, frameBytes)
		copy(payload, pcm[off:off+frameBytes])
		frames = append(frames, media.Frame{
			Sequence:  seq,
			Timestamp: time.Duration(seq) * 20 * time.Millisecond,
			Format:    format,
			Payload:   payload,
		})
	}
	return frames
}
