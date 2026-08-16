package whispercpp

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

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
		ID:         "whisper-local",
		Executable: stubFile(t, "whisper-stream"),
		ModelPath:  stubFile(t, "ggml-base.bin"),
		Language:   "en",
		Format:     media.PCM16Mono16k(),
		Streaming:  true,
		Process: process.Config{
			StartTimeout:   time.Second,
			StopTimeout:    time.Second,
			MaxStderrBytes: 4096,
		},
		ResultTimeout:     time.Second,
		MaxPendingResults: 16,
	}
}

// TestAdapter_SatisfiesTheFrozenPort is the contract, checked at compile time
// and restated where a reader will look for it.
func TestAdapter_SatisfiesTheFrozenPort(t *testing.T) {
	t.Parallel()

	var _ speech.STTProvider = (*Provider)(nil)
	var _ speech.STTStream = (*stream)(nil)
}

// TestAdapter_NoWhisperTypeEscapes is the §1 boundary: no whisper.cpp-specific
// type may leak into the core speech contracts.
//
// Checked by construction rather than by reflection: the only exported
// constructor returns *Provider, whose exported methods are exactly the port's.
// A caller holding a speech.STTProvider cannot discover what is behind it
// without a type assertion they would have to write deliberately.
func TestAdapter_NoWhisperTypeEscapes(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Used only through the port.
	var provider speech.STTProvider = p

	if provider.ID() != "whisper-local" {
		t.Errorf("ID = %q", provider.ID())
	}
	caps := provider.Capabilities()
	if !caps.Streaming {
		t.Error("a streaming installation reports Streaming false")
	}
	if !caps.Supports("en") {
		t.Error("the declared language is not reported as supported")
	}
	if !caps.SupportsRate(media.Rate16kHz) {
		t.Error("the declared rate is not reported as supported")
	}
}

// TestAdapter_CapabilitiesAreDeclaredNotAspirational pins the honesty rule.
//
// A front-end that cannot stream must not claim partials. An orchestrator told
// to expect them waits, and the wait is dead air on a live call.
func TestAdapter_CapabilitiesAreDeclaredNotAspirational(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.Streaming = false

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	caps := p.Capabilities()
	if caps.Streaming {
		t.Error("a non-streaming installation reports Streaming true")
	}
	if caps.PartialResults {
		t.Error("a non-streaming installation claims partial results; a front-end " +
			"that cannot stream cannot emit an interim result either")
	}
}

// TestAdapter_MissingBinaryIsTypedAndActionable is §1's and §21's honest
// failure.
func TestAdapter_MissingBinaryIsTypedAndActionable(t *testing.T) {
	t.Parallel()

	t.Run("missing executable", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig(t)
		cfg.Executable = filepath.Join(t.TempDir(), "not-built")

		_, err := New(cfg)
		if err == nil {
			t.Fatal("a missing binary was accepted")
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err does not match ErrUnavailable: %v", err)
		}

		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("err is %T, want *UnavailableError", err)
		}
		if unavailable.Path != cfg.Executable {
			t.Errorf("the error does not name the path checked: %q", unavailable.Path)
		}
		if !strings.Contains(unavailable.Remedy, "whisper.cpp") {
			t.Errorf("the remedy does not say what to install: %q", unavailable.Remedy)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig(t)
		cfg.ModelPath = filepath.Join(t.TempDir(), "no-model.bin")

		err := Available(cfg)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}

		var unavailable *UnavailableError
		if errors.As(err, &unavailable) && unavailable.Component != "model" {
			t.Errorf("component = %q, want model", unavailable.Component)
		}
		if !strings.Contains(err.Error(), "download") {
			t.Errorf("the remedy does not say how to get a model: %v", err)
		}
	})
}

func TestAdapter_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"no id":         func(c *Config) { c.ID = "" },
		"no executable": func(c *Config) { c.Executable = "" },
		"no model":      func(c *Config) { c.ModelPath = "" },
		"no language":   func(c *Config) { c.Language = "" },
		"no timeout":    func(c *Config) { c.ResultTimeout = 0 },
		"unbounded":     func(c *Config) { c.MaxPendingResults = 0 },
		"stereo": func(c *Config) {
			c.Format = media.AudioFormat{Format: media.FormatPCM16, Layout: media.LayoutStereo, Rate: media.Rate16kHz, Codec: media.CodecPCM}
		},
		"32-bit samples": func(c *Config) {
			c.Format = media.AudioFormat{Format: media.FormatPCM32, Layout: media.LayoutMono, Rate: media.Rate16kHz, Codec: media.CodecPCM}
		},
	}

	for name, mutate := range cases {
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

// TestAdapter_ParseSeparatesSpeechFromBanners is where a careless adapter turns
// a program's own diagnostics into something the caller apparently said.
//
// The consequence is not cosmetic: a banner treated as a transcript becomes a
// prompt to a language model.
func TestAdapter_ParseSeparatesSpeechFromBanners(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), session: "ss-1", turn: "st-1"}

	cases := []struct {
		name     string
		line     string
		wantText string
		wantOK   bool
	}{
		{"timestamped speech", "[00:00:00.000 --> 00:00:02.000]   hello there", "hello there", true},
		{"bare speech", "just some words", "just some words", true},
		{"devanagari", "[00:00:01.000 --> 00:00:03.000]  नमस्ते", "नमस्ते", true},

		{"empty", "", "", false},
		{"whitespace", "   \t  ", "", false},
		{"whisper banner", "whisper_init_from_file_with_params_no_state: loading model", "", false},
		{"main banner", "main: processing 1 threads", "", false},
		{"system info", "system_info: n_threads = 4", "", false},
		{"error line", "error: failed to initialize", "", false},
		{"usage", "usage: stream [options]", "", false},
		{"start prompt", "[Start speaking]", "", false},
		{"separator", "### transcription 1", "", false},
		{"timestamp only", "[00:00:00.000 --> 00:00:02.000]", "", false},
		{"unterminated bracket", "[00:00:00.000 --> ", "", false},
		{"bracket without arrow", "[something else] words", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg, ok := s.parse(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("parse(%q) recognised=%v, want %v (text %q)",
					tc.line, ok, tc.wantOK, seg.Text)
			}
			if ok && seg.Text != tc.wantText {
				t.Errorf("parse(%q) text = %q, want %q", tc.line, seg.Text, tc.wantText)
			}
		})
	}
}

// TestAdapter_SegmentsCarryNoFabricatedConfidence guards a specific dishonesty.
//
// whisper.cpp emits no per-segment confidence. An adapter that reported 1.0, or
// 0.9, or anything at all would be inventing a number the model never produced
// — and every downstream decision that reads confidence would then be resting
// on it.
func TestAdapter_SegmentsCarryNoFabricatedConfidence(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), session: "ss-1", turn: "st-1"}

	seg, ok := s.parse("[00:00:00.000 --> 00:00:01.000]  hello")
	if !ok {
		t.Fatal("a transcript line was not recognised")
	}
	if seg.Confidence != speech.ConfidenceUnknown {
		t.Errorf("Confidence = %v, want ConfidenceUnknown (%v); whisper.cpp reports "+
			"none and inventing one would mislead every consumer that reads it",
			seg.Confidence, speech.ConfidenceUnknown)
	}
	if seg.Meta.Provider != "whisper-local" {
		t.Errorf("Meta.Provider = %q, want the authored identifier", seg.Meta.Provider)
	}
	if seg.Language != "en" {
		t.Errorf("Language = %q, want the configured language", seg.Language)
	}
}

// TestAdapter_SequenceIsMonotonic is what lets Phase 11C detect a provider that
// repeats or reorders.
func TestAdapter_SequenceIsMonotonic(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), session: "ss-1", turn: "st-1"}

	var last uint64
	for i := 0; i < 20; i++ {
		seg, ok := s.parse("word")
		if !ok {
			t.Fatal("a transcript line was not recognised")
		}
		if seg.Sequence <= last {
			t.Fatalf("sequence went %d then %d", last, seg.Sequence)
		}
		last = seg.Sequence
	}
}

// TestAdapter_WAVHeaderIsCorrect covers the one explicit conversion this
// adapter performs.
//
// §13 forbids silent conversion. This converts CONTAINER only — no resampling,
// no channel mixing — and the bytes are checked against the RIFF layout rather
// than against the function's own idea of it.
func TestAdapter_WAVHeaderIsCorrect(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	const dataBytes = 32000
	if err := WriteWAVHeader(w, media.PCM16Mono16k(), dataBytes); err != nil {
		t.Fatalf("WriteWAVHeader: %v", err)
	}

	got := buf.Bytes()
	if len(got) != 44 {
		t.Fatalf("header is %d bytes, want 44", len(got))
	}

	le32 := func(off int) uint32 {
		return uint32(got[off]) | uint32(got[off+1])<<8 |
			uint32(got[off+2])<<16 | uint32(got[off+3])<<24
	}
	le16 := func(off int) uint16 {
		return uint16(got[off]) | uint16(got[off+1])<<8
	}

	if string(got[0:4]) != "RIFF" {
		t.Errorf("bytes 0-3 = %q, want RIFF", got[0:4])
	}
	if string(got[8:12]) != "WAVE" {
		t.Errorf("bytes 8-11 = %q, want WAVE", got[8:12])
	}
	if string(got[12:16]) != "fmt " {
		t.Errorf("bytes 12-15 = %q, want 'fmt '", got[12:16])
	}
	if string(got[36:40]) != "data" {
		t.Errorf("bytes 36-39 = %q, want data", got[36:40])
	}

	if v := le32(4); v != 44-8+dataBytes {
		t.Errorf("RIFF size = %d, want %d", v, 44-8+dataBytes)
	}
	if v := le16(20); v != 1 {
		t.Errorf("audio format = %d, want 1 (PCM)", v)
	}
	if v := le16(22); v != 1 {
		t.Errorf("channels = %d, want 1", v)
	}
	if v := le32(24); v != 16000 {
		t.Errorf("sample rate = %d, want 16000", v)
	}
	if v := le32(28); v != 32000 {
		t.Errorf("byte rate = %d, want 32000", v)
	}
	if v := le16(32); v != 2 {
		t.Errorf("block align = %d, want 2", v)
	}
	if v := le16(34); v != 16 {
		t.Errorf("bits per sample = %d, want 16", v)
	}
	if v := le32(40); v != dataBytes {
		t.Errorf("data size = %d, want %d", v, dataBytes)
	}
}

// TestAdapter_WAVHeaderRefusesUnsupportedFormats keeps the conversion from
// silently mis-describing audio.
func TestAdapter_WAVHeaderRefusesUnsupportedFormats(t *testing.T) {
	t.Parallel()

	for name, format := range map[string]media.AudioFormat{
		"stereo": {Format: media.FormatPCM16, Layout: media.LayoutStereo,
			Rate: media.Rate16kHz, Codec: media.CodecPCM},
		"pcm32": {Format: media.FormatPCM32, Layout: media.LayoutMono,
			Rate: media.Rate16kHz, Codec: media.CodecPCM},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			err := WriteWAVHeader(bufio.NewWriter(&buf), format, 100)
			if !errors.Is(err, ErrFormat) {
				t.Errorf("err = %v, want ErrFormat", err)
			}
		})
	}
}

// TestAdapter_RefusesMismatchedAudio is §13's no-silent-resampling rule at the
// stream boundary.
func TestAdapter_RefusesMismatchedAudio(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t)}

	err := s.Write(media.Frame{
		Format:  media.PCM16Mono8k(),
		Payload: make([]byte, 320),
	})
	if !errors.Is(err, ErrFormat) {
		t.Errorf("err = %v, want ErrFormat — an 8 kHz frame must not be silently "+
			"accepted by a 16 kHz recogniser", err)
	}
}

func TestAdapter_ClosedStreamRefusesWrites(t *testing.T) {
	t.Parallel()

	s := &stream{cfg: testConfig(t), closed: true}

	if err := s.Write(media.Frame{Format: media.PCM16Mono16k(),
		Payload: make([]byte, 320)}); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}
