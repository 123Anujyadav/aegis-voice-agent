package piper

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

// ---------------------------------------------------------------------------
// A stand-in engine, because the real one is not installed here
// ---------------------------------------------------------------------------
//
// Piper is absent on this machine (Phase 11E design §4), so these tests drive a
// compiled program that speaks Piper's PROTOCOL — one utterance per stdin line,
// headerless PCM on stdout, progress on stderr — rather than a mock of this
// package's own types.
//
// The distinction matters. What is worth testing in a process adapter is the
// operating-system boundary: what happens when the engine dies mid-utterance,
// when it emits faster than the caller reads, when the caller abandons it. A
// mock of that boundary would test the mock. A real child process cannot be
// talked into pretending.
//
// What this does NOT do is fabricate speech. The audio below is a synthetic
// tone whose sample value encodes which utterance produced it — enough to prove
// ordering and framing, and nothing that could be mistaken for a voice.
// TestAdapter_RealRuntimeAvailability reports the real engine's absence rather
// than papering over it.

var (
	engineOnce sync.Once
	enginePath string
	engineErr  error
)

const engineSource = `package main

// A stand-in for the Piper binary: same command line, same protocol.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]

	var (
		mode         = "voice"
		model        string
		lengthScale  string
		speaker      = "<unset>"
		outputRaw    bool
		argvFile     string
		envFile      string
		heartbeat    string
		bytesPerLine = 1920
	)

	next := func(i *int) string {
		*i++
		if *i < len(args) {
			return args[*i]
		}
		return ""
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			model = next(&i)
		case "--length_scale":
			lengthScale = next(&i)
		case "--speaker":
			speaker = next(&i)
		case "--output_raw":
			outputRaw = true
		case "--mode":
			mode = next(&i)
		case "--argv-file":
			argvFile = next(&i)
		case "--env-file":
			envFile = next(&i)
		case "--heartbeat":
			heartbeat = next(&i)
		case "--bytes-per-line":
			bytesPerLine, _ = strconv.Atoi(next(&i))
		}
	}

	// Recording argv and env is how the tests inspect what the adapter passed
	// without this program having to interpret it.
	if argvFile != "" {
		_ = os.WriteFile(argvFile, []byte(strings.Join(args, "\n")), 0o600)
	}
	if envFile != "" {
		_ = os.WriteFile(envFile, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}

	if !outputRaw {
		fmt.Fprintln(os.Stderr, "fatal: expected --output_raw")
		os.Exit(2)
	}
	if model == "" {
		fmt.Fprintln(os.Stderr, "fatal: no --model")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "piper-stub: model=%s length_scale=%s speaker=%s\n",
		model, lengthScale, speaker)

	switch mode {
	case "hang":
		// Announces nothing and refuses to die when stdin closes. Whether the
		// heartbeat file keeps growing after Close is how the orphan check
		// works: a signal probe is worthless on Windows.
		for {
			if heartbeat != "" {
				f, err := os.OpenFile(heartbeat, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err == nil {
					_, _ = f.WriteString("x")
					_ = f.Close()
				}
			}
			time.Sleep(20 * time.Millisecond)
		}

	case "flood":
		// Emits without ever being asked, faster than any caller drains.
		payload := make([]byte, 4096)
		for i := 0; i+1 < len(payload); i += 2 {
			binary.LittleEndian.PutUint16(payload[i:], uint16(9000))
		}
		for {
			if _, err := os.Stdout.Write(payload); err != nil {
				os.Exit(0)
			}
		}
	}

	utterance := 0
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		utterance++

		switch mode {
		case "die-mid-utterance":
			// Half an utterance, then gone. The caller is left holding a
			// stream that simply stops.
			_, _ = os.Stdout.Write(tone(bytesPerLine/2, utterance))
			fmt.Fprintln(os.Stderr, "fatal: onnx runtime aborted")
			os.Exit(7)

		case "mute":
			// Consumes text and produces nothing.

		case "ragged":
			// Emits a length that is NOT a whole number of frames, so the
			// adapter's trailing partial frame is exercised.
			_, _ = os.Stdout.Write(tone(bytesPerLine+90, utterance))

		default:
			_, _ = os.Stdout.Write(tone(bytesPerLine, utterance))
		}

		fmt.Fprintf(os.Stderr, "piper-stub: synthesised utterance %d (%d chars)\n",
			utterance, len(in.Text()))
	}

	os.Exit(0)
}

// tone returns n bytes of PCM16 whose every sample is the utterance ordinal.
//
// Encoding the ordinal in the SAMPLES is what lets a test prove that audio
// arrived in the order the text was submitted: the bytes themselves say which
// utterance they came from, so ordering is not inferred from timing.
func tone(n, utterance int) []byte {
	if n%2 == 1 {
		n++
	}
	b := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(b[i:], uint16(int16(utterance)))
	}
	return b
}
`

// engine compiles the stand-in engine, once per test binary.
func engine(t *testing.T) string {
	t.Helper()

	engineOnce.Do(func() {
		dir, err := os.MkdirTemp("", "piper-stub")
		if err != nil {
			engineErr = err
			return
		}

		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(engineSource), 0o600); err != nil {
			engineErr = err
			return
		}

		bin := filepath.Join(dir, "piper-stub")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}

		build := exec.Command("go", "build", "-o", bin, src)
		if out, err := build.CombinedOutput(); err != nil {
			engineErr = fmt.Errorf("building the stand-in engine: %v\n%s", err, out)
			return
		}
		enginePath = bin
	})

	if engineErr != nil {
		t.Skipf("cannot build the stand-in engine: %v", engineErr)
	}
	return enginePath
}

// voiceFiles creates the model and the .onnx.json that must sit beside it.
func voiceFiles(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	model := filepath.Join(dir, "en_GB-test-medium.onnx")

	if err := os.WriteFile(model, []byte("not a real onnx graph"), 0o600); err != nil {
		t.Fatalf("writing the model: %v", err)
	}
	if err := os.WriteFile(model+".json", []byte(`{"sample_rate":16000}`), 0o600); err != nil {
		t.Fatalf("writing the voice configuration: %v", err)
	}
	return model
}

const (
	testFrame     = 20 * time.Millisecond
	testFrameSize = 640  // 16 kHz · mono · 16-bit · 20 ms
	bytesPerLine  = 1920 // exactly three frames, so no frame straddles two utterances
)

// testConfig returns a working configuration driving the stand-in engine.
func testConfig(t *testing.T, extraArgs ...string) Config {
	t.Helper()

	return Config{
		ID:            speech.ProviderID("piper-local"),
		Executable:    engine(t),
		ModelPath:     voiceFiles(t),
		SpeakerID:     -1,
		Language:      "en-GB",
		Format:        media.PCM16Mono16k(),
		LengthScale:   1.0,
		FrameDuration: testFrame,
		Process: process.Config{
			Args:           append([]string{"--bytes-per-line", "1920"}, extraArgs...),
			StartTimeout:   5 * time.Second,
			StopTimeout:    500 * time.Millisecond,
			MaxStderrBytes: 8 << 10,
		},
		ChunkTimeout:     5 * time.Second,
		MaxPendingChunks: 8,
		MaxPendingFrames: 64,
		MaxChunkChars:    240,
	}
}

func testTTSConfig() speech.TTSConfig {
	return speech.TTSConfig{
		Session:  speech.SessionID("sess-piper-1"),
		Turn:     speech.TurnID("turn-1"),
		Language: speech.Language("en-GB"),
		Format:   media.PCM16Mono16k(),
		Voice:    speech.VoiceID("en_GB-test-medium"),
		Prosody:  speech.DefaultProsody(),
	}
}

// openStream builds a provider and opens one synthesis stream.
func openStream(t *testing.T, cfg Config) speech.TTSStream {
	t.Helper()

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, err := p.OpenTTS(context.Background(), testTTSConfig())
	if err != nil {
		t.Fatalf("OpenTTS: %v", err)
	}
	return s
}

// utteranceOf returns the ordinal the stand-in engine encoded in a frame.
func utteranceOf(t *testing.T, f media.Frame) int {
	t.Helper()

	if len(f.Payload) < 2 {
		t.Fatalf("frame %d has a %d-byte payload", f.Sequence, len(f.Payload))
	}
	return int(int16(binary.LittleEndian.Uint16(f.Payload)))
}

// collect drains audio until the channel closes or the deadline passes.
func collect(t *testing.T, s speech.TTSStream, d time.Duration) []media.Frame {
	t.Helper()

	var frames []media.Frame
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-s.Audio():
			if !ok {
				return frames
			}
			frames = append(frames, f)
		case <-deadline:
			t.Fatalf("timed out after %s with %d frames", d, len(frames))
			return frames
		}
	}
}

// ---------------------------------------------------------------------------
// The frozen port
// ---------------------------------------------------------------------------

func TestAdapter_SatisfiesTheFrozenPort(t *testing.T) {
	t.Parallel()

	// Compile-time, not runtime: the value of these assertions is that the
	// package cannot BUILD if Phase 11C's port drifts from what is implemented.
	var _ speech.TTSProvider = (*Provider)(nil)
	var _ speech.TTSStream = (*stream)(nil)

	// Nothing in the exported surface may leak this package's types, or
	// swapping Piper for a cloud voice stops being a configuration change.
	var p any = &Provider{}
	if _, ok := p.(speech.TTSProvider); !ok {
		t.Fatal("Provider must be usable as a bare speech.TTSProvider")
	}
}

func TestAdapter_DeclaresItsStreamingHonestly(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	caps := p.Capabilities()

	// TRUE, with the qualification the package comment states: audio arrives
	// while the process runs, so playback can begin before the whole response
	// is synthesised. It does NOT claim the first sample of a chunk precedes
	// that chunk's synthesis — it does not, and TestStream_ChunkSizeIsTheLatencyKnob
	// is the executable form of that.
	if !caps.Streaming {
		t.Error("Piper streams audio while the process runs; Streaming must be true")
	}

	// A synthesiser emits no interim transcripts. Declaring otherwise would
	// make the router match this provider for work it cannot do.
	if caps.PartialResults {
		t.Error("PartialResults must be false: a TTS engine produces no transcripts")
	}

	if !caps.Supports(speech.Language(cfg.Language)) {
		t.Errorf("Capabilities must declare the configured language %q, got %v",
			cfg.Language, caps.Languages)
	}
	if !caps.SupportsRate(cfg.Format.Rate) {
		t.Errorf("Capabilities must declare the model's rate %v, got %v",
			cfg.Format.Rate, caps.SampleRates)
	}
}

// ---------------------------------------------------------------------------
// Honest failure: §9 forbids fabricating a voice that is not installed
// ---------------------------------------------------------------------------

func TestAdapter_MissingBinaryIsTypedAndActionable(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.Executable = filepath.Join(t.TempDir(), "piper-does-not-exist")

	_, err := New(cfg)
	if err == nil {
		t.Fatal("New must refuse an installation whose binary is absent")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error must match ErrUnavailable so a router can classify it, got %v", err)
	}

	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error must be an *UnavailableError, got %T", err)
	}
	if unavailable.Path != cfg.Executable {
		t.Errorf("the error must name the path checked: want %q, got %q",
			cfg.Executable, unavailable.Path)
	}
	if unavailable.Remedy == "" {
		t.Error("§9 requires the install requirement, not just the failure")
	}

	// The operator reads the message, so the message must contain both facts.
	if msg := err.Error(); !strings.Contains(msg, cfg.Executable) ||
		!strings.Contains(msg, "install") {
		t.Errorf("message must carry the path and how to fix it, got: %s", msg)
	}
}

func TestAdapter_MissingVoiceModelIsTypedAndActionable(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.ModelPath = filepath.Join(t.TempDir(), "absent-voice.onnx")

	err := Available(cfg)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a missing voice model must report ErrUnavailable, got %v", err)
	}

	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Component != "voice model" {
		t.Errorf("the error must identify WHICH component is missing, got %v", err)
	}
}

func TestAdapter_MissingVoiceConfigurationIsCaughtAtStartup(t *testing.T) {
	t.Parallel()

	// Piper reads its sample rate and phoneme map from a .onnx.json beside the
	// model. Without this check the failure surfaces as a dead engine at the
	// first utterance, which reads like a broken provider rather than an
	// incomplete download.
	cfg := testConfig(t)
	if err := os.Remove(cfg.ModelPath + ".json"); err != nil {
		t.Fatalf("removing the voice configuration: %v", err)
	}

	err := Available(cfg)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Component != "voice configuration" {
		t.Fatalf("a missing .onnx.json must be reported at startup, got %v", err)
	}
	if !strings.Contains(unavailable.Path, ".json") {
		t.Errorf("the error must name the .json path, got %q", unavailable.Path)
	}
}

// TestAdapter_RealRuntimeAvailability reports on the actual engine.
//
// It asserts nothing about whether Piper is installed — Phase 11E requires the
// development loop to work without it. What it forbids is SILENCE: if the real
// runtime is missing, the reason and the remedy appear in the test log rather
// than being discovered in an end-to-end run.
func TestAdapter_RealRuntimeAvailability(t *testing.T) {
	t.Parallel()

	binary, err := exec.LookPath("piper")
	if err != nil {
		t.Logf("LOCAL PROVIDER RUNTIME NOT AVAILABLE\n"+
			"  provider:   piper (speech.TTSProvider)\n"+
			"  missing:    the 'piper' executable is not on PATH (%v)\n"+
			"  needs:      the piper binary from https://github.com/rhasspy/piper/releases\n"+
			"              plus a voice: <name>.onnx AND <name>.onnx.json from\n"+
			"              https://huggingface.co/rhasspy/piper-voices\n"+
			"  effect:     the TTS leg of the local end-to-end run reports unavailable.\n"+
			"              This adapter's contract is proven against a stand-in engine\n"+
			"              speaking Piper's protocol; no audio is fabricated.", err)
		return
	}
	t.Logf("piper found at %s; the adapter is exercised against a stand-in engine "+
		"regardless, because a real voice would make these assertions depend on a "+
		"model download", binary)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestAdapter_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no ID", func(c *Config) { c.ID = "" }, "ID must be set"},
		{"no executable", func(c *Config) { c.Executable = "" }, "Executable must be set"},
		{"no model", func(c *Config) { c.ModelPath = "" }, "ModelPath must be set"},
		{"no language", func(c *Config) { c.Language = "" }, "Language must be set"},
		{"stereo", func(c *Config) { c.Format.Layout = media.LayoutStereo }, "mono"},
		{"zero length scale", func(c *Config) { c.LengthScale = 0 }, "LengthScale"},
		{"zero frame duration", func(c *Config) { c.FrameDuration = 0 }, "FrameDuration"},
		{"zero chunk timeout", func(c *Config) { c.ChunkTimeout = 0 }, "ChunkTimeout"},
		{"unbounded chunks", func(c *Config) { c.MaxPendingChunks = 0 }, "MaxPendingChunks"},
		{"unbounded frames", func(c *Config) { c.MaxPendingFrames = 0 }, "MaxPendingFrames"},
		{"unbounded chunk size", func(c *Config) { c.MaxChunkChars = 0 }, "MaxChunkChars"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig(t)
			tc.mutate(&cfg)

			_, err := New(cfg)
			if err == nil {
				t.Fatalf("New accepted an invalid configuration (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error must say what is wrong: want mention of %q, got %v",
					tc.want, err)
			}
		})
	}
}

func TestAdapter_ReportsEveryConfigurationProblemAtOnce(t *testing.T) {
	t.Parallel()

	// One problem per run turns a misconfiguration into a guessing game.
	cfg := testConfig(t)
	cfg.ID = ""
	cfg.Language = ""
	cfg.MaxChunkChars = 0

	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted three invalid fields")
	}
	for _, want := range []string{"ID must be set", "Language must be set", "MaxChunkChars"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("every problem must be reported together; %q is missing from: %v",
				want, err)
		}
	}
}

func TestAdapter_RefusesAFormatTheVoiceCannotProduce(t *testing.T) {
	t.Parallel()

	// Piper emits headerless PCM whose rate is a property of the model. A
	// session at another rate cannot be served by resampling here, and playing
	// 16 kHz audio at 8 kHz sounds like a broken voice rather than a
	// configuration error — so it is refused at OpenTTS.
	p, err := New(testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg := testTTSConfig()
	cfg.Format = media.PCM16Mono8k()

	_, err = p.OpenTTS(context.Background(), cfg)
	if err == nil {
		t.Fatal("OpenTTS must refuse a session whose format the model cannot produce")
	}
	if !errors.Is(err, speech.ErrInvalidAudio) {
		t.Errorf("the mismatch must be typed as speech.ErrInvalidAudio, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// The command line: §18's injection surface
// ---------------------------------------------------------------------------

func TestAdapter_ModelTextNeverReachesTheCommandLine(t *testing.T) {
	t.Parallel()

	// This is the one place in Phase 11E where MODEL OUTPUT reaches an external
	// program. If it travelled as an argument, a generated sentence would be a
	// command line, and the sentence is downstream of something a caller said.
	argv := filepath.Join(t.TempDir(), "argv.txt")
	cfg := testConfig(t, "--argv-file", argv)

	const sentence = "Transfer the balance and then rm -rf the whole thing"

	s := openStream(t, cfg)
	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: sentence, IsFinal: true}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
	collect(t, s, 10*time.Second)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recorded, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("the engine did not record its argv: %v", err)
	}

	for _, fragment := range []string{"Transfer the balance", "rm -rf"} {
		if strings.Contains(string(recorded), fragment) {
			t.Errorf("response text reached the command line: %q appears in argv:\n%s",
				fragment, recorded)
		}
	}

	// The flags that MUST be there, since the audio contract depends on them.
	if !strings.Contains(string(recorded), "--output_raw") {
		t.Error("--output_raw is required: without it Piper writes WAV files to " +
			"disk, putting synthesised speech on durable storage")
	}
	if !strings.Contains(string(recorded), cfg.ModelPath) {
		t.Error("the model path must be passed to the engine")
	}
}

func TestAdapter_SpeakerIsOmittedWhenTheModelDefaultIsWanted(t *testing.T) {
	t.Parallel()

	argv := filepath.Join(t.TempDir(), "argv.txt")
	cfg := testConfig(t, "--argv-file", argv)
	cfg.SpeakerID = -1 // "use the model's default"

	s := openStream(t, cfg)
	_ = s.CloseSend()
	collect(t, s, 10*time.Second)
	_ = s.Close()

	recorded, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("reading argv: %v", err)
	}
	if strings.Contains(string(recorded), "--speaker") {
		// Passing --speaker -1 to a single-speaker model is an error, not a
		// no-op.
		t.Errorf("a negative SpeakerID must omit --speaker entirely, got:\n%s", recorded)
	}
}

func TestAdapter_ChildEnvironmentIsAllowlistedNotInherited(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv("PIPER_TEST_SECRET", "sk-live-not-for-the-child")

	envFile := filepath.Join(t.TempDir(), "env.txt")
	cfg := testConfig(t, "--env-file", envFile)

	s := openStream(t, cfg)
	_ = s.CloseSend()
	collect(t, s, 10*time.Second)
	_ = s.Close()

	recorded, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("the engine did not record its environment: %v", err)
	}
	env := string(recorded)

	// A child started with everything the operator exported receives every API
	// key they happen to have.
	if strings.Contains(env, "sk-live-not-for-the-child") {
		t.Error("the engine inherited a secret from the parent environment; " +
			"InheritEnv is an allowlist for exactly this reason")
	}
	// ...and the allowlist must still be enough for the program to start.
	if !strings.Contains(env, "PATH=") {
		t.Error("the child received no PATH; an emptied environment is the other " +
			"failure mode, and it kills a program before it prints anything")
	}
}

// ---------------------------------------------------------------------------
// Ordering, framing and bounds
// ---------------------------------------------------------------------------

func TestStream_AudioArrivesInTheOrderTextWasSubmitted(t *testing.T) {
	t.Parallel()

	s := openStream(t, testConfig(t))

	const utterances = 4
	for i := 0; i < utterances; i++ {
		chunk := speech.Chunk{
			Sequence: uint64(i),
			Text:     fmt.Sprintf("This is utterance number %d.", i+1),
			IsFinal:  i == utterances-1,
		}
		if err := s.Synthesize(chunk); err != nil {
			t.Fatalf("Synthesize(%d): %v", i, err)
		}
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	frames := collect(t, s, 15*time.Second)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(frames) != utterances*bytesPerLine/testFrameSize {
		t.Fatalf("got %d frames, want %d", len(frames),
			utterances*bytesPerLine/testFrameSize)
	}

	// The engine stamps each sample with the ordinal of the utterance that
	// produced it, so ordering is read out of the AUDIO rather than inferred
	// from arrival timing.
	previous := 0
	for _, f := range frames {
		got := utteranceOf(t, f)
		if got < previous {
			t.Fatalf("frame %d carries utterance %d after utterance %d: audio was "+
				"reordered", f.Sequence, got, previous)
		}
		previous = got
	}
	if previous != utterances {
		t.Errorf("the last frame carries utterance %d; %d utterances were submitted",
			previous, utterances)
	}
}

func TestStream_FrameSequenceIsMonotonicAndGapless(t *testing.T) {
	t.Parallel()

	// Downstream reconstructs playback order from Sequence. A gap is silence
	// the caller never hears about.
	s := openStream(t, testConfig(t))

	for i := 0; i < 3; i++ {
		if err := s.Synthesize(speech.Chunk{Sequence: uint64(i), Text: "Hello there."}); err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
	}
	_ = s.CloseSend()
	frames := collect(t, s, 15*time.Second)
	_ = s.Close()

	if len(frames) == 0 {
		t.Fatal("no audio was produced")
	}
	for i, f := range frames {
		if f.Sequence != uint64(i) {
			t.Fatalf("frame %d has sequence %d; sequences must be gapless", i, f.Sequence)
		}
		if want := time.Duration(i) * testFrame; f.Timestamp != want {
			t.Errorf("frame %d has timestamp %s, want %s", i, f.Timestamp, want)
		}
		if f.Format != media.PCM16Mono16k() {
			t.Errorf("frame %d declares format %s, want the configured one", i, f.Format)
		}
		if len(f.Payload) != testFrameSize {
			t.Errorf("frame %d carries %d bytes, want %d", i, len(f.Payload), testFrameSize)
		}
	}
}

func TestStream_FramesAreOwnedByTheReceiver(t *testing.T) {
	t.Parallel()

	// Phase 11C's contract: unlike media's ring buffer, a provider hands over
	// frames it will not touch again. An adapter that reused its accumulation
	// buffer would corrupt audio already queued for the caller — and the
	// corruption would appear only under load, when the queue is deep.
	s := openStream(t, testConfig(t))

	for i := 0; i < 3; i++ {
		if err := s.Synthesize(speech.Chunk{
			Sequence: uint64(i),
			Text:     fmt.Sprintf("Utterance %d.", i+1),
		}); err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
	}
	_ = s.CloseSend()

	// Deliberately let frames accumulate before reading any of them: if the
	// adapter shared one buffer, the earlier frames would now hold the newest
	// audio.
	time.Sleep(300 * time.Millisecond)
	frames := collect(t, s, 15*time.Second)
	_ = s.Close()

	if len(frames) < 6 {
		t.Fatalf("expected audio from three utterances, got %d frames", len(frames))
	}
	for i := 1; i < len(frames); i++ {
		if &frames[i].Payload[0] == &frames[i-1].Payload[0] {
			t.Fatalf("frames %d and %d share a backing array", i-1, i)
		}
	}
	// First and last must differ, which one shared buffer could not manage.
	if utteranceOf(t, frames[0]) == utteranceOf(t, frames[len(frames)-1]) {
		t.Error("every frame carries the same utterance: the accumulation buffer " +
			"is being reused across deliveries")
	}
}

func TestStream_TrailingPartialFrameIsPaddedNotDropped(t *testing.T) {
	t.Parallel()

	// The engine emits a byte stream with no frame boundaries in it. Dropping
	// the remainder would clip the last few milliseconds off every response —
	// the kind of defect that sounds like a bad line rather than a bug.
	s := openStream(t, testConfig(t, "--mode", "ragged"))

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "An odd length.", IsFinal: true}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	_ = s.CloseSend()
	frames := collect(t, s, 15*time.Second)
	_ = s.Close()

	// 1920 + 90 bytes is three whole frames plus a 90-byte remainder.
	if len(frames) != 4 {
		t.Fatalf("got %d frames; the trailing partial frame was dropped rather "+
			"than padded", len(frames))
	}
	last := frames[len(frames)-1]
	if len(last.Payload) != testFrameSize {
		t.Errorf("the padded frame carries %d bytes; every frame must be one frame "+
			"long, or the media contract breaks", len(last.Payload))
	}
	// Real audio at the front, silence in the pad.
	if utteranceOf(t, last) == 0 {
		t.Error("the trailing frame is entirely silence; the remainder was lost")
	}
	if last.Payload[testFrameSize-1] != 0 || last.Payload[testFrameSize-2] != 0 {
		t.Error("the pad must be silence")
	}
}

func TestStream_QueuedTextIsBoundedAndBackpressureIsReported(t *testing.T) {
	t.Parallel()

	// Blocking would hold the generation loop; growing the queue would defer
	// the problem until memory ran out. Reporting lets the caller decide.
	cfg := testConfig(t, "--mode", "hang")
	cfg.MaxPendingChunks = 2

	s := openStream(t, cfg)
	defer func() { _ = s.Close() }()

	var backpressure error
	for i := 0; i < 200 && backpressure == nil; i++ {
		err := s.Synthesize(speech.Chunk{Sequence: uint64(i), Text: "More text."})
		if err != nil {
			backpressure = err
		}
	}

	if backpressure == nil {
		t.Fatal("200 chunks were accepted against a bound of 2: the text queue is " +
			"unbounded")
	}
	if !errors.Is(backpressure, ErrBackpressure) {
		t.Errorf("a full queue must report ErrBackpressure, got %v", backpressure)
	}
}

func TestStream_AudioQueueIsBoundedAgainstARunawayEngine(t *testing.T) {
	t.Parallel()

	// An engine that emits faster than the caller drains must not be able to
	// grow the host's memory for the length of a call.
	cfg := testConfig(t, "--mode", "flood")
	cfg.MaxPendingFrames = 16

	s := openStream(t, cfg)

	// Read nothing at all while the engine floods.
	time.Sleep(500 * time.Millisecond)

	if got := cap(s.Audio()); got != 16 {
		t.Errorf("the audio queue has capacity %d, want the configured bound of 16", got)
	}
	if got := len(s.Audio()); got > 16 {
		t.Errorf("the audio queue holds %d frames, above its bound of 16", got)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close on a flooding engine: %v", err)
	}
}

func TestStream_RefusesTextThatWouldSplitIntoTwoUtterances(t *testing.T) {
	t.Parallel()

	// Piper is line-oriented, one utterance per line. An embedded newline would
	// silently become two utterances with a gap between them, so it is refused
	// rather than escaped.
	s := openStream(t, testConfig(t))
	defer func() { _ = s.Close() }()

	err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "First line.\nSecond line."})
	if !errors.Is(err, ErrUnsafeText) {
		t.Fatalf("a line break must be refused as ErrUnsafeText, got %v", err)
	}
}

func TestStream_RefusesAChunkAboveTheBound(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.MaxChunkChars = 32

	s := openStream(t, cfg)
	defer func() { _ = s.Close() }()

	err := s.Synthesize(speech.Chunk{Sequence: 0, Text: strings.Repeat("a", 33)})
	if !errors.Is(err, ErrUnsafeText) {
		t.Fatalf("an oversize chunk must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "33") {
		t.Errorf("the error should say how long the chunk was, got %v", err)
	}
}

func TestStream_EmptyChunkIsAcceptedAndSaysNothing(t *testing.T) {
	t.Parallel()

	// A chunker can legitimately produce an empty clause. Sending a bare
	// newline would make Piper emit an empty utterance and a spurious frame
	// boundary, so nothing is sent — but it is not an error either.
	s := openStream(t, testConfig(t))

	for _, text := range []string{"", "   ", "\t"} {
		if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: text}); err != nil {
			t.Fatalf("an empty chunk must not be an error, got %v", err)
		}
	}
	_ = s.CloseSend()

	frames := collect(t, s, 10*time.Second)
	_ = s.Close()

	if len(frames) != 0 {
		t.Errorf("empty chunks produced %d frames", len(frames))
	}
}

// ---------------------------------------------------------------------------
// Cancellation: what a barge-in calls
// ---------------------------------------------------------------------------

func TestStream_CloseOnAHealthyStreamReportsNoFailure(t *testing.T) {
	t.Parallel()

	// A barge-in closes a stream that is working perfectly. If deliberate
	// teardown were reported as a synthesis failure, every interruption would
	// look like a broken provider — and a router that counts failures would
	// eventually open a circuit breaker against a healthy voice.
	s := openStream(t, testConfig(t))

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "The caller interrupts here."}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Take one frame, so synthesis is genuinely under way.
	select {
	case f, ok := <-s.Audio():
		if !ok {
			t.Fatal("audio ended before the first frame")
		}
		_ = f
	case <-time.After(10 * time.Second):
		t.Fatal("no audio within 10s")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close on a healthy stream must report no error, got: %v", err)
	}

	inner, ok := s.(*stream)
	if !ok {
		t.Fatalf("unexpected stream type %T", s)
	}
	if err := inner.Err(); err != nil {
		t.Errorf("abandoning a healthy stream is not a synthesis failure, got: %v", err)
	}
}

func TestStream_CloseStopsAudioPromptly(t *testing.T) {
	t.Parallel()

	// ADR-0004 §12 fixes the abort budget at one frame interval for the
	// CANCELLATION SIGNAL. Reclaiming an operating-system process is a separate,
	// slower thing; what must be immediate is that no further audio is queued.
	cfg := testConfig(t, "--mode", "flood")
	s := openStream(t, cfg)

	<-s.Audio() // synthesis is under way

	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	// Measured and reported, not asserted against the 20 ms signal budget:
	// this figure includes process teardown, and conflating the two would
	// invent an SLA this phase does not have.
	t.Logf("Close on a flooding engine took %s (process teardown included)", elapsed)

	if elapsed > cfg.Process.StopTimeout+2*time.Second {
		t.Errorf("Close took %s, beyond the %s stop timeout plus margin: the "+
			"engine was not being reclaimed promptly", elapsed, cfg.Process.StopTimeout)
	}

	// The property that actually matters for barge-in: nothing more arrives.
	drained := 0
	for range s.Audio() {
		drained++
		if drained > cfg.MaxPendingFrames {
			t.Fatalf("audio kept arriving after Close: %d frames and counting", drained)
		}
	}
}

func TestStream_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	s := openStream(t, testConfig(t))

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// A supervisor and a barge-in can both decide to close the same stream.
	if err := s.Close(); err != nil {
		t.Errorf("second Close must be a no-op, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("third Close must be a no-op, got %v", err)
	}
}

func TestStream_CloseSendIsIdempotentAndEndsTheAudio(t *testing.T) {
	t.Parallel()

	s := openStream(t, testConfig(t))

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "All done.", IsFinal: true}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
	if err := s.CloseSend(); err != nil {
		t.Errorf("a second CloseSend must be a no-op, got %v", err)
	}

	// Audio must END, not hang: the caller waits on this channel to know the
	// response has been fully spoken.
	frames := collect(t, s, 15*time.Second)
	if len(frames) == 0 {
		t.Error("no audio was produced before the stream ended")
	}
	_ = s.Close()
}

func TestStream_SynthesizeAfterEndOfTextIsRefused(t *testing.T) {
	t.Parallel()

	s := openStream(t, testConfig(t))
	defer func() { _ = s.Close() }()

	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// Without this the write would land on a closed channel and panic the
	// caller's goroutine rather than returning an error it can handle.
	err := s.Synthesize(speech.Chunk{Sequence: 1, Text: "One more thing."})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("text after CloseSend must return ErrClosed, got %v", err)
	}
}

func TestStream_SynthesizeAfterCloseIsRefused(t *testing.T) {
	t.Parallel()

	s := openStream(t, testConfig(t))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "Too late."})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("text after Close must return ErrClosed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Failure of the engine itself
// ---------------------------------------------------------------------------

func TestStream_EngineDeathMidUtteranceIsObservable(t *testing.T) {
	t.Parallel()

	// speech.TTSStream reports errors only from calls a caller makes. An engine
	// that dies mid-utterance has no such call to fail, so without Err the only
	// symptom is audio that stops — indistinguishable from a response that
	// simply ended.
	s := openStream(t, testConfig(t, "--mode", "die-mid-utterance"))

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "This will not finish."}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	_ = s.CloseSend()

	collect(t, s, 15*time.Second) // drains until the audio channel closes

	inner, ok := s.(*stream)
	if !ok {
		t.Fatalf("unexpected stream type %T", s)
	}
	err := inner.Err()
	if err == nil {
		t.Fatal("an engine that died mid-utterance must be reported; silent " +
			"truncation is indistinguishable from a finished response")
	}
	if !errors.Is(err, ErrSynthesisFailed) {
		t.Errorf("the failure must be typed ErrSynthesisFailed, got %v", err)
	}
	// The engine's own diagnosis is the only thing that explains WHY.
	if !strings.Contains(err.Error(), "onnx runtime aborted") {
		t.Errorf("the error must carry the engine's stderr, got: %v", err)
	}

	_ = s.Close()
}

func TestStream_SilentEngineEndsRatherThanHanging(t *testing.T) {
	t.Parallel()

	// An engine that consumes text and produces nothing must still terminate
	// the audio channel, or the caller waits for a response that never comes.
	s := openStream(t, testConfig(t, "--mode", "mute"))

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "Say something.", IsFinal: true}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	_ = s.CloseSend()

	frames := collect(t, s, 15*time.Second)
	if len(frames) != 0 {
		t.Errorf("a mute engine produced %d frames", len(frames))
	}
	_ = s.Close()
}

// ---------------------------------------------------------------------------
// Resources: §10's guarantees, at this layer
// ---------------------------------------------------------------------------

func TestStream_CloseLeavesNoOrphanProcess(t *testing.T) {
	t.Parallel()

	// os.FindProcess and a signal is the obvious probe and it is worthless on
	// Windows, where Signal returns EWINDOWS for anything but Kill — the check
	// reports "not alive" for every process, including live ones. This watches
	// the child's SIDE EFFECTS instead: a file it appends to every 20 ms.
	beat := filepath.Join(t.TempDir(), "heartbeat")
	s := openStream(t, testConfig(t, "--mode", "hang", "--heartbeat", beat))

	time.Sleep(300 * time.Millisecond) // let it establish a rhythm

	size := func() int64 {
		info, err := os.Stat(beat)
		if err != nil {
			return -1
		}
		return info.Size()
	}
	if size() <= 0 {
		t.Fatal("the engine never started beating; the orphan check would pass " +
			"without testing anything")
	}

	// This engine ignores the closing of its stdin, so it has to be killed.
	// Close REPORTS that rather than swallowing it: the child is dead either
	// way, but "it needed killing" is a fact about the provider worth counting,
	// because a voice that never exits cleanly will eventually be killed
	// mid-utterance.
	err := s.Close()
	if err != nil && !errors.Is(err, process.ErrStopTimeout) {
		t.Fatalf("Close: %v", err)
	}
	if err == nil {
		t.Error("an engine that ignored the end of its input was reclaimed " +
			"silently; needing to be killed must be reported")
	}

	time.Sleep(150 * time.Millisecond) // let any write in flight land
	before := size()
	time.Sleep(300 * time.Millisecond)

	if after := size(); after != before {
		t.Errorf("the engine is still running after Close: its heartbeat grew from "+
			"%d to %d bytes. An abandoned stream must not leave a voice process "+
			"behind for the length of a call", before, after)
	}
}

func TestStream_CloseLeavesNoGoroutineBehind(t *testing.T) {
	// Not parallel: goroutine counting is meaningless while other tests run.

	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			n = runtime.NumGoroutine()
			time.Sleep(20 * time.Millisecond)
			if runtime.NumGoroutine() == n {
				return n
			}
		}
		return n
	}

	before := settle()

	for i := 0; i < 5; i++ {
		s := openStream(t, testConfig(t))
		if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "A short utterance."}); err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
		<-s.Audio()
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	after := settle()

	// Every goroutine this adapter starts is owned by a WaitGroup that Close
	// waits on. A leak here is one voice process's worth of goroutines per
	// interrupted response.
	if after > before+2 {
		t.Errorf("goroutines went from %d to %d across five opened and closed "+
			"streams: Close is not reclaiming them", before, after)
	}
}

func TestStream_ManyStreamsAreIndependent(t *testing.T) {
	t.Parallel()

	// One engine process per stream, and no shared state between them: audio
	// from one session must never appear in another.
	const streams = 4

	var wg sync.WaitGroup
	errs := make(chan error, streams)

	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			s := openStream(t, testConfig(t))
			defer func() { _ = s.Close() }()

			text := fmt.Sprintf("Session %d speaking.", n)
			if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: text, IsFinal: true}); err != nil {
				errs <- fmt.Errorf("stream %d: Synthesize: %w", n, err)
				return
			}
			if err := s.CloseSend(); err != nil {
				errs <- fmt.Errorf("stream %d: CloseSend: %w", n, err)
				return
			}

			var frames int
			for range s.Audio() {
				frames++
			}
			if want := bytesPerLine / testFrameSize; frames != want {
				// The stream's own failure is the only thing that distinguishes
				// "the engine died" from "audio was silently truncated".
				var why error
				if inner, ok := s.(*stream); ok {
					why = inner.Err()
				}
				errs <- fmt.Errorf("stream %d produced %d frames, want %d (stream error: %v)",
					n, frames, want, why)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// The latency claim, stated as an ordering property
// ---------------------------------------------------------------------------

func TestStream_ChunkSizeIsTheLatencyKnob(t *testing.T) {
	t.Parallel()

	// The honest form of "Piper streams": audio for the FIRST clause arrives
	// while later clauses are still being submitted. It is asserted by
	// ORDERING, not by wall-clock, so it does not become a latency SLA that
	// this phase has not measured and does not own.
	s := openStream(t, testConfig(t))
	defer func() { _ = s.Close() }()

	if err := s.Synthesize(speech.Chunk{Sequence: 0, Text: "The first clause."}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	select {
	case f, ok := <-s.Audio():
		if !ok {
			t.Fatal("audio ended before the first frame")
		}
		if got := utteranceOf(t, f); got != 1 {
			t.Errorf("the first frame carries utterance %d, want the first", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no audio for the first clause while the response was still being " +
			"submitted: nothing is streaming")
	}

	// Only NOW is the rest of the response submitted. Audio for clause one has
	// already been delivered, which is the whole claim.
	if err := s.Synthesize(speech.Chunk{Sequence: 1, Text: "The second clause.", IsFinal: true}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers this package exports for diagnostics
// ---------------------------------------------------------------------------

func TestPCMLevel_DistinguishesAudioFromSilence(t *testing.T) {
	t.Parallel()

	silence := make([]byte, 640)
	if got := PCMLevel(silence); got != 0 {
		t.Errorf("silence measured %v, want 0", got)
	}

	loud := make([]byte, 640)
	for i := 0; i+1 < len(loud); i += 2 {
		binary.LittleEndian.PutUint16(loud[i:], uint16(int16(16384)))
	}
	if got := PCMLevel(loud); got < 0.4 || got > 0.6 {
		t.Errorf("half-scale audio measured %v, want about 0.5", got)
	}

	// Negative samples must contribute their magnitude, not cancel out: a
	// symmetric waveform would otherwise measure as silence.
	negative := make([]byte, 640)
	quiet := int16(-16384) // a variable: converting the constant would not compile
	for i := 0; i+1 < len(negative); i += 2 {
		binary.LittleEndian.PutUint16(negative[i:], uint16(quiet))
	}
	if got := PCMLevel(negative); got < 0.4 {
		t.Errorf("a negative waveform measured %v; magnitudes must not cancel", got)
	}

	if got := PCMLevel(nil); got != 0 {
		t.Errorf("an empty payload measured %v, want 0", got)
	}
}

func TestSilenceFrame_IsSilentAndWellFormed(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono16k()
	f := SilenceFrame(format, testFrame, 7)

	if len(f.Payload) != testFrameSize {
		t.Errorf("a %s silence frame carries %d bytes, want %d",
			testFrame, len(f.Payload), testFrameSize)
	}
	if f.Sequence != 7 {
		t.Errorf("Sequence is %d, want 7", f.Sequence)
	}
	if want := 7 * testFrame; f.Timestamp != want {
		t.Errorf("Timestamp is %s, want %s", f.Timestamp, want)
	}
	if PCMLevel(f.Payload) != 0 {
		t.Error("a silence frame is not silent")
	}
}
