package whispercli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

const testFormatRate = media.Rate16kHz

func testFormat() media.AudioFormat { return media.PCM16Mono16k() }

func stubFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ID:         "whisper-cli",
		Executable: stubFile(t, "whisper"),
		Model:      "tiny",
		Language:   "en",
		Format:     testFormat(),
		Process: process.Config{
			StartTimeout:   30 * time.Second,
			StopTimeout:    2 * time.Second,
			MaxStderrBytes: 32 << 10,
		},
		MaxAudio:          30 * time.Second,
		MaxPendingResults: 32,
		WorkDir:           t.TempDir(),
	}
}

// ---------------------------------------------------------------------------
// Contract
// ---------------------------------------------------------------------------

func TestAdapter_SatisfiesTheFrozenPort(t *testing.T) {
	t.Parallel()

	var _ speech.STTProvider = (*Provider)(nil)
	var _ speech.STTStream = (*stream)(nil)
}

// TestAdapter_DeclaresItsLimitationsHonestly is the §2 and §12 honesty rule.
//
// This tool cannot stream. An adapter that claimed otherwise would make the
// orchestrator send audio incrementally and wait for interim results, and the
// resulting silence would look like a provider fault rather than a design
// limitation of a batch program.
func TestAdapter_DeclaresItsLimitationsHonestly(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	caps := p.Capabilities()
	if caps.Streaming {
		t.Error("a batch tool reports Streaming true")
	}
	if caps.PartialResults {
		t.Error("a batch tool reports PartialResults true")
	}
	if caps.MaxSessionAudio == 0 {
		t.Error("no audio bound is declared; this adapter holds a whole utterance " +
			"in memory-backed storage and callers need to know the limit")
	}
	if !caps.Supports("en") || !caps.SupportsRate(testFormatRate) {
		t.Error("the declared language or rate is not reported as supported")
	}
}

func TestAdapter_MissingToolIsTypedAndActionable(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.Executable = filepath.Join(t.TempDir(), "no-whisper")

	_, err := New(cfg)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}

	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err is %T, want *UnavailableError", err)
	}
	if unavailable.Path != cfg.Executable {
		t.Errorf("the error does not name the path checked: %q", unavailable.Path)
	}
	if !strings.Contains(unavailable.Remedy, "pip install openai-whisper") {
		t.Errorf("the remedy does not say what to install: %q", unavailable.Remedy)
	}
}

func TestAdapter_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"no id":             func(c *Config) { c.ID = "" },
		"no executable":     func(c *Config) { c.Executable = "" },
		"no model":          func(c *Config) { c.Model = "" },
		"no language":       func(c *Config) { c.Language = "" },
		"no audio bound":    func(c *Config) { c.MaxAudio = 0 },
		"unbounded results": func(c *Config) { c.MaxPendingResults = 0 },
		"stereo": func(c *Config) {
			c.Format = media.AudioFormat{Format: media.FormatPCM16,
				Layout: media.LayoutStereo, Rate: testFormatRate, Codec: media.CodecPCM}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t)
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("an invalid configuration was accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Parsing — measured against the real tool's output format
// ---------------------------------------------------------------------------

// TestAdapter_ParseOffsetHandlesBothTimeFormats pins the difference that makes
// this adapter's parser incompatible with whisper.cpp's.
//
// The Python tool prints MM:SS.mmm. whisper.cpp prints HH:MM:SS.mmm. Sharing a
// parser would silently misread every timestamp by a factor of sixty.
func TestAdapter_ParseOffsetHandlesBothTimeFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"00:00.000", 0, true},
		{"00:04.000", 4 * time.Second, true},
		{"01:30.500", 90*time.Second + 500*time.Millisecond, true},
		{"12:34.567", 12*time.Minute + 34*time.Second + 567*time.Millisecond, true},
		{"01:02:03.000", time.Hour + 2*time.Minute + 3*time.Second, true},

		{"", 0, false},
		{"90", 0, false},
		{"a:b", 0, false},
		{"1:2:3:4", 0, false},
		{"00:xx", 0, false},
	}

	for _, tc := range cases {
		got, ok := parseOffset(tc.in)
		if ok != tc.ok {
			t.Errorf("parseOffset(%q) ok=%v, want %v", tc.in, ok, tc.ok)
			continue
		}
		// A millisecond of tolerance: the fractional part goes through a float.
		if ok && absDuration(got-tc.want) > time.Millisecond {
			t.Errorf("parseOffset(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestAdapter_ParseSeparatesSpeechFromNoise uses lines captured from the real
// tool.
func TestAdapter_ParseSeparatesSpeechFromNoise(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), session: "ss-1", turn: "st-1"}

	cases := []struct {
		name     string
		line     string
		wantText string
		wantOK   bool
	}{
		{
			// Captured verbatim from this machine.
			name:     "real output line",
			line:     "[00:00.000 --> 00:04.000]  Hello, I would like to check my account balance please.",
			wantText: "Hello, I would like to check my account balance please.",
			wantOK:   true,
		},
		{"devanagari", "[00:00.000 --> 00:02.000]  नमस्ते, मेरा नाम राहुल है", "नमस्ते, मेरा नाम राहुल है", true},
		{"hinglish", "[00:01.500 --> 00:03.000]  mera balance check karna hai please", "mera balance check karna hai please", true},

		{"empty", "", "", false},
		{"fp16 warning", "FP16 is not supported on CPU; using FP32 instead", "", false},
		{"detecting language", "Detecting language using up to the first 30 seconds", "", false},
		{"no bracket", "some bare text", "", false},
		{"no arrow", "[00:00.000] text", "", false},
		{"empty body", "[00:00.000 --> 00:04.000]   ", "", false},
		{"unterminated", "[00:00.000 --> 00:04.000", "", false},
		{"bad offsets", "[aa:bb.ccc --> dd:ee.fff] text", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg, ok := s.parse(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("parse(%q) ok=%v want %v", tc.line, ok, tc.wantOK)
			}
			if ok && seg.Text != tc.wantText {
				t.Errorf("text = %q, want %q", seg.Text, tc.wantText)
			}
		})
	}
}

// TestAdapter_ReportsNoFabricatedConfidence guards against inventing a number
// the model never produced.
//
// The tool reports avg_logprob — a mean log probability over tokens, which is
// not a confidence in [0,1] and cannot be squeezed into one without choosing an
// arbitrary mapping.
func TestAdapter_ReportsNoFabricatedConfidence(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), session: "ss-1", turn: "st-1"}

	seg, ok := s.parse("[00:00.000 --> 00:04.000]  hello")
	if !ok {
		t.Fatal("a real output line was not recognised")
	}
	if seg.Confidence != speech.ConfidenceUnknown {
		t.Errorf("Confidence = %v, want ConfidenceUnknown", seg.Confidence)
	}
	if seg.StartTime != 0 || seg.EndTime != 4*time.Second {
		t.Errorf("times = %s..%s, want 0..4s", seg.StartTime, seg.EndTime)
	}
	if seg.Meta.Model != "tiny" {
		t.Errorf("Meta.Model = %q, want the configured model", seg.Meta.Model)
	}
	if !seg.IsFinal {
		t.Error("a batch tool's segment is not marked final")
	}
}

// ---------------------------------------------------------------------------
// Bounded buffering and cleanup
// ---------------------------------------------------------------------------

func frames(t *testing.T, count int) []media.Frame {
	t.Helper()

	format := testFormat()
	samples := int(format.Rate) / 50 // 20 ms
	out := make([]media.Frame, count)
	for i := range out {
		payload := make([]byte, samples*2)
		for s := 0; s < samples; s++ {
			v := int16(8000 * math.Sin(2*math.Pi*220*float64(i*samples+s)/float64(format.Rate)))
			payload[s*2] = byte(v)
			payload[s*2+1] = byte(v >> 8)
		}
		out[i] = media.Frame{
			Sequence:  uint64(i),
			Timestamp: time.Duration(i) * 20 * time.Millisecond,
			Format:    format,
			Payload:   payload,
		}
	}
	return out
}

// TestAdapter_BufferedAudioIsBounded is the §1 bounded-buffering requirement.
//
// This adapter must hold a whole utterance before it can transcribe any of it.
// Without a bound, a caller who never signals the end of speech fills the disk.
func TestAdapter_BufferedAudioIsBounded(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.MaxAudio = 200 * time.Millisecond // ten frames

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	s, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-1", Turn: "st-1", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	var accepted int
	var limitErr error
	for _, f := range frames(t, 50) {
		if err := s.Write(f); err != nil {
			limitErr = err
			break
		}
		accepted++
	}

	if limitErr == nil {
		t.Fatal("fifty frames were accepted against a ten-frame bound")
	}
	if !errors.Is(limitErr, ErrAudioLimit) {
		t.Errorf("err = %v, want ErrAudioLimit", limitErr)
	}
	if accepted > 12 {
		t.Errorf("accepted %d frames against a %s bound", accepted, cfg.MaxAudio)
	}
	t.Logf("accepted %d frames before the %s bound stopped it", accepted, cfg.MaxAudio)
}

// TestAdapter_RemovesBufferedAudioOnClose is the one place in Phase 11E where
// raw audio reaches durable storage, and the code that undoes it.
//
// The tool reads a file and nothing else, so buffering to disk is unavoidable.
// What is avoidable is leaving it there.
func TestAdapter_RemovesBufferedAudioOnClose(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-1", Turn: "st-1", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatal(err)
	}

	s, ok := raw.(*stream)
	if !ok {
		t.Fatalf("OpenSTT returned %T", raw)
	}
	audioPath := s.AudioPath()

	for _, f := range frames(t, 5) {
		if err := s.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(audioPath); err != nil {
		t.Fatalf("the buffered audio file does not exist: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(audioPath); !os.IsNotExist(err) {
		t.Errorf("buffered audio survived Close at %q (stat err %v); §16 and §18 "+
			"forbid persisting raw audio", audioPath, err)
	}
	if _, err := os.Stat(filepath.Dir(audioPath)); !os.IsNotExist(err) {
		t.Errorf("the work directory survived Close at %q", filepath.Dir(audioPath))
	}

	// Idempotent.
	if err := s.Close(); err != nil {
		t.Errorf("a second Close returned %v", err)
	}
}

func TestAdapter_RefusesMismatchedAudio(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-1", Turn: "st-1", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	err = s.Write(media.Frame{Format: media.PCM16Mono8k(), Payload: make([]byte, 320)})
	if !errors.Is(err, ErrFormat) {
		t.Errorf("err = %v, want ErrFormat — an 8 kHz frame must not be silently "+
			"accepted by a 16 kHz recogniser", err)
	}
}

func TestAdapter_EmptyUtteranceProducesNothing(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-1", Turn: "st-1", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// No audio at all. Nothing may be spawned and nothing may be invented.
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend on an empty utterance: %v", err)
	}

	select {
	case seg, ok := <-s.Results():
		if ok {
			t.Errorf("an empty utterance produced a transcript: %q", seg.Text)
		}
	case <-time.After(2 * time.Second):
		t.Error("the results channel was never closed")
	}
}

// ---------------------------------------------------------------------------
// The real tool
// ---------------------------------------------------------------------------

// findWhisper locates the installed tool, or skips.
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
	return abs
}

// TestAdapter_RealToolProducesNoFabricatedTranscript runs the installed tool on
// audio that contains no speech.
//
// # The most important test in this file
//
// §21 forbids fabricating provider output. A tone is not speech, and the only
// correct transcript for it is none at all. A recogniser that returned words
// here would be hallucinating, and an adapter that invented them would be
// worse.
//
// It also exercises the whole plumbing against a real process: temporary WAV,
// argv construction, spawn, stdout capture, cleanup.
func TestAdapter_RealToolProducesNoFabricatedTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}

	whisper := findWhisper(t)

	cfg := testConfig(t)
	cfg.Executable = whisper
	cfg.Process.StartTimeout = 5 * time.Minute

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-real", Turn: "st-real", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatalf("OpenSTT: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Two seconds of a 220 Hz tone.
	for _, f := range frames(t, 100) {
		if err := s.Write(f); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	start := time.Now()
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var segments []speech.TranscriptSegment
	deadline := time.After(5 * time.Minute)
	for done := false; !done; {
		select {
		case seg, ok := <-s.Results():
			if !ok {
				done = true
				break
			}
			segments = append(segments, seg)
		case <-deadline:
			t.Fatal("the tool did not finish within five minutes")
		}
	}
	elapsed := time.Since(start)

	for _, seg := range segments {
		t.Logf("tone produced a segment: %q", seg.Text)
	}
	if len(segments) > 0 {
		t.Errorf("a 220 Hz tone produced %d transcript segments; either the model "+
			"hallucinated or the adapter fabricated them", len(segments))
	}

	t.Logf("REAL RUN: openai-whisper model=%s on 2.0s of non-speech audio took %s "+
		"(provider inference, NOT Aegis orchestration overhead)", cfg.Model, elapsed)
}

// TestAdapter_RealToolTranscribesRealSpeech runs the tool on genuine synthesised
// speech.
//
// # The speech is real, not a fixture pretending to be
//
// Windows SAPI is a real text-to-speech engine present on this machine. Audio it
// produces contains genuine spoken words, so a transcript of it is a genuine
// recognition result — unlike a tone, which can only ever prove the absence of
// hallucination.
//
// Skipped, never faked, where SAPI is unavailable.
func TestAdapter_RealToolTranscribesRealSpeech(t *testing.T) {
	if testing.Short() {
		t.Skip("real inference is slow; skipped under -short")
	}
	if runtime.GOOS != "windows" {
		t.Skip("speech is generated with Windows SAPI; no generator on this platform")
	}

	whisper := findWhisper(t)

	const spoken = "Hello, I would like to check my account balance please."
	pcm := synthesiseWithSAPI(t, spoken)

	cfg := testConfig(t)
	cfg.Executable = whisper
	cfg.Process.StartTimeout = 5 * time.Minute

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-speech", Turn: "st-speech", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatalf("OpenSTT: %v", err)
	}
	defer func() { _ = s.Close() }()

	format := testFormat()
	const frameBytes = 640 // 20 ms at 16 kHz, 16-bit mono
	for off := 0; off < len(pcm); off += frameBytes {
		end := off + frameBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := s.Write(media.Frame{
			Sequence:  uint64(off / frameBytes),
			Timestamp: time.Duration(off/frameBytes) * 20 * time.Millisecond,
			Format:    format,
			Payload:   pcm[off:end],
		}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	start := time.Now()
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var firstAt time.Duration
	var text strings.Builder
	var count int

	deadline := time.After(5 * time.Minute)
	for done := false; !done; {
		select {
		case seg, ok := <-s.Results():
			if !ok {
				done = true
				break
			}
			if count == 0 {
				firstAt = time.Since(start)
			}
			count++
			text.WriteString(seg.Text)
		case <-deadline:
			t.Fatal("the tool did not finish within five minutes")
		}
	}
	elapsed := time.Since(start)

	got := strings.ToLower(text.String())
	if count == 0 {
		t.Fatal("real speech produced no transcript at all")
	}

	// Recognition quality is the model's business, not this adapter's. What is
	// asserted is that the adapter carried a real result through — checked on
	// content words rather than on an exact match, because demanding one would
	// be asserting model accuracy this phase does not claim.
	for _, word := range []string{"balance", "account"} {
		if !strings.Contains(got, word) {
			t.Errorf("the transcript does not contain %q: %q", word, text.String())
		}
	}

	audioSeconds := float64(len(pcm)) / float64(int(format.Rate)*2)
	t.Logf("REAL RUN: spoke %q", spoken)
	t.Logf("REAL RUN: transcribed %q", text.String())
	t.Logf("REAL RUN: model=%s, %.1fs of speech, %d segments, first at %s, total %s "+
		"(%.1fx real time). PROVIDER INFERENCE, not Aegis orchestration overhead.",
		cfg.Model, audioSeconds, count, firstAt, elapsed,
		elapsed.Seconds()/audioSeconds)
}

// synthesiseWithSAPI produces 16 kHz mono PCM16 speech using Windows SAPI.
//
// Real speech from a real engine. Skips rather than fabricating if anything is
// missing.
func synthesiseWithSAPI(t *testing.T, text string) []byte {
	t.Helper()

	dir := t.TempDir()
	rawWAV := filepath.Join(dir, "sapi.wav")

	script := fmt.Sprintf(
		`Add-Type -AssemblyName System.Speech; `+
			`$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; `+
			`$s.SetOutputToWaveFile(%q); $s.Speak(%q); $s.Dispose()`,
		rawWAV, text)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Windows SAPI is unavailable, so no real speech can be generated: "+
			"%v\n%s", err, out)
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg is not on PATH, so SAPI output cannot be converted to "+
			"16 kHz mono: %v", err)
	}

	converted := filepath.Join(dir, "sapi16k.raw")
	convert := exec.Command(ffmpeg, "-y", "-i", rawWAV,
		"-ar", "16000", "-ac", "1", "-f", "s16le", converted)
	if out, err := convert.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not convert the SAPI output: %v\n%s", err, out)
	}

	pcm, err := os.ReadFile(converted)
	if err != nil {
		t.Skipf("cannot read the converted audio: %v", err)
	}
	if len(pcm) == 0 {
		t.Skip("the converted audio is empty")
	}
	return pcm
}

// ---------------------------------------------------------------------------
// Regressions from running the real tool
// ---------------------------------------------------------------------------

// TestAdapter_BooleanArgsUsePythonCapitalisation is a regression.
//
// strconv.FormatBool renders "false"; the tool's argparse str2bool accepts only
// "True" and "False" and rejects anything else with exit code 2. The failure is
// invisible from outside — argparse prints usage to stderr and produces no
// transcript — so it looked exactly like a caller who said nothing.
func TestAdapter_BooleanArgsUsePythonCapitalisation(t *testing.T) {
	t.Parallel()

	if got := pythonBool(false); got != "False" {
		t.Errorf("pythonBool(false) = %q, want %q — argparse rejects the lowercase "+
			"form and exits 2", got, "False")
	}
	if got := pythonBool(true); got != "True" {
		t.Errorf("pythonBool(true) = %q, want %q", got, "True")
	}

	// And the argv the adapter actually builds must carry it.
	s := &stream{cfg: testConfig(t), workDir: t.TempDir(), audioPath: "a.wav"}
	args := s.processConfig().Args

	var fp16 string
	for i, a := range args {
		if a == "--fp16" && i+1 < len(args) {
			fp16 = args[i+1]
		}
	}
	if fp16 != "False" {
		t.Errorf("--fp16 argument is %q, want %q", fp16, "False")
	}
}

// TestAdapter_ChildEnvironmentIsNotEmptied is a regression.
//
// A non-nil child environment REPLACES the parent's. Setting only the two
// encoding variables left Python with no PATH and no SystemRoot; it died before
// printing anything, and the adapter reported an empty transcript with no error
// on audio that transcribes perfectly by hand.
func TestAdapter_ChildEnvironmentIsNotEmptied(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), workDir: t.TempDir(), audioPath: "a.wav"}
	cfg := s.processConfig()

	if len(cfg.InheritEnv) == 0 {
		t.Fatal("no environment is inherited; a Python child with no PATH or " +
			"SystemRoot dies before printing anything")
	}

	need := map[string]bool{"PATH": false, "SystemRoot": false}
	for _, key := range cfg.InheritEnv {
		if _, ok := need[key]; ok {
			need[key] = true
		}
	}
	for key, found := range need {
		if !found {
			t.Errorf("%s is not inherited by the child", key)
		}
	}

	// The encoding variables must still be set: Python writes stdout in the
	// console code page, and a Devanagari transcript through cp1252 raises
	// UnicodeEncodeError and kills the tool mid-run.
	var sawEncoding bool
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "PYTHONIOENCODING=") {
			sawEncoding = true
		}
	}
	if !sawEncoding {
		t.Error("PYTHONIOENCODING is not set; non-Latin transcripts crash the tool " +
			"on Windows, which is to say exactly the languages this phase must support")
	}
}

// TestAdapter_FailedChildIsReportedNotSilent is the regression that matters
// most.
//
// A recogniser that dies must not be indistinguishable from a caller who said
// nothing. The first version of the failure check read the exit status the
// moment stdout closed, which races the reaper and lost — so a child that
// exited 2 reported no error at all.
func TestAdapter_FailedChildIsReportedNotSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real process; skipped under -short")
	}

	whisper := findWhisper(t)

	cfg := testConfig(t)
	cfg.Executable = whisper
	cfg.Process.StartTimeout = 2 * time.Minute
	// A language the tool rejects during argument parsing.
	//
	// processConfig rebuilds the argument vector from configuration, so an
	// extra bad flag in Process.Args would simply be discarded — which is what
	// an earlier version of this test did, and it passed while testing nothing.
	// --language has a fixed choice list, so an unknown tag fails argparse
	// immediately with exit code 2 and never touches the network.
	cfg.Language = "zz"

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-fail", Turn: "st-fail", Language: "en", Format: testFormat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*stream)

	for _, f := range frames(t, 5) {
		if err := s.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// Drain to completion.
	for range s.Results() {
	}

	if s.Err() == nil {
		t.Fatal("a recogniser that exited on a bad argument reported no error; " +
			"a crashed provider is indistinguishable from silence")
	}
	if !strings.Contains(s.Err().Error(), "stderr") {
		t.Errorf("the error carries no diagnostics: %v", s.Err())
	}

	// Close surfaces the same failure, for a caller that does not check Err.
	if err := s.Close(); err == nil {
		t.Error("Close returned nil after the provider failed")
	}
	t.Logf("failed child reported: %v", s.Err())
}
