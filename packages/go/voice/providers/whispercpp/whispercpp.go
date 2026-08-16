// Package whispercpp adapts whisper.cpp to the frozen Phase 11C recognition
// port.
//
// # It implements speech.STTProvider and adds nothing to it
//
// Phase 11C's provider contract already names Whisper as an intended adapter.
// This package writes one, and no whisper.cpp type appears in any exported
// signature — a caller holds a speech.STTProvider and cannot tell what is
// behind it.
//
// # What whisper.cpp actually is, and what that costs
//
// whisper.cpp's streaming example consumes raw PCM on stdin and prints
// transcription lines to stdout as it goes. That is a genuine stream, but it is
// not a low-latency one: the model runs over a window of audio, so a "partial"
// arrives when a window completes rather than when a word is spoken.
//
// The adapter reports what the binary gives it and claims nothing more. Where
// the configured binary does not emit incremental output at all, Capabilities
// says Streaming is false and the orchestrator plans around it.
//
// # This package is not executable everywhere, and says so
//
// whisper.cpp is a C++ program that has to be built for the host. When the
// binary or the model is absent, every entry point returns a typed error naming
// the path it checked and what to install. It does not fabricate a transcript,
// and no test here passes by pretending one arrived.
package whispercpp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

// Errors this adapter returns.
var (
	// ErrUnavailable is returned when the binary or model is missing.
	ErrUnavailable = errors.New("whispercpp: unavailable")

	// ErrClosed is returned by an operation on a closed stream.
	ErrClosed = errors.New("whispercpp: stream is closed")

	// ErrFormat is returned for audio the binary cannot accept.
	ErrFormat = errors.New("whispercpp: unsupported audio format")
)

// Config describes one whisper.cpp installation.
//
// Deliberately not the parent package's STTProviderConfig: an adapter that
// depended on the voice runtime's configuration type could not be used without
// the voice runtime, and the point of a port is that it can.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID

	// Executable is the absolute path to the whisper.cpp stream binary.
	Executable string

	// ModelPath is the absolute path to the GGML model.
	ModelPath string

	// Language is the recognition language tag, or "auto".
	Language string

	// Format is the audio the binary expects. whisper.cpp is built around
	// 16 kHz mono; anything else is refused rather than resampled.
	Format media.AudioFormat

	// Threads bounds CPU use. Zero lets the binary decide.
	Threads int

	// StepMillis is how much audio the binary processes per window. Zero uses
	// the binary's default.
	StepMillis int

	// Streaming declares whether the configured binary emits incremental
	// output.
	//
	// DECLARED, NOT DETECTED. whisper.cpp has several front-ends and they do
	// not agree about this. An adapter that guessed would sometimes tell the
	// orchestrator to expect partials that never arrive, and the orchestrator
	// would wait for them.
	Streaming bool

	// Process configures supervision.
	Process process.Config

	// ResultTimeout bounds the wait for a final result after audio ends.
	ResultTimeout time.Duration

	// MaxPendingResults bounds queued transcript segments.
	MaxPendingResults int
}

func (c Config) validate() error {
	var problems []string

	if c.ID == "" {
		problems = append(problems, "ID must be set")
	}
	if c.Executable == "" {
		problems = append(problems, "Executable must be set")
	}
	if c.ModelPath == "" {
		problems = append(problems, "ModelPath must be set")
	}
	if c.Language == "" {
		problems = append(problems, "Language must be set")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Format.Layout != media.LayoutMono {
		problems = append(problems, "Format must be mono; whisper.cpp takes one channel")
	}
	if c.Format.Format != media.FormatPCM16 {
		problems = append(problems, "Format must be PCM16; whisper.cpp takes signed "+
			"16-bit samples")
	}
	if c.ResultTimeout <= 0 {
		problems = append(problems, "ResultTimeout must be positive")
	}
	if c.MaxPendingResults <= 0 {
		problems = append(problems, "MaxPendingResults must be positive")
	}

	if len(problems) > 0 {
		return fmt.Errorf("whispercpp: invalid configuration: %s",
			strings.Join(problems, "; "))
	}
	return nil
}

// Provider is a whisper.cpp installation, exposed as a Phase 11C recogniser.
//
// Compile-time proof that the frozen port is satisfied:
var _ speech.STTProvider = (*Provider)(nil)

// Provider implements speech.STTProvider over whisper.cpp.
type Provider struct {
	cfg Config
}

// New builds a provider.
//
// # Availability is checked here, not at first use
//
// A missing binary discovered on the first call of a live conversation is an
// outage. Discovered at construction it is a startup error somebody reads
// before taking traffic.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := checkAvailable(cfg); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg}, nil
}

// Available reports whether this installation could be used, without building a
// provider.
//
// For a startup probe that wants to log what is missing rather than fail.
func Available(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return checkAvailable(cfg)
}

// checkAvailable stats the binary and the model.
func checkAvailable(cfg Config) error {
	if info, err := os.Stat(cfg.Executable); err != nil || info.IsDir() {
		return &UnavailableError{
			Path:      cfg.Executable,
			Component: "executable",
			Remedy: "build whisper.cpp and point Executable at its stream binary — " +
				"see https://github.com/ggerganov/whisper.cpp",
			Cause: err,
		}
	}
	if info, err := os.Stat(cfg.ModelPath); err != nil || info.IsDir() {
		return &UnavailableError{
			Path:      cfg.ModelPath,
			Component: "model",
			Remedy: "download a GGML model, for example " +
				"models/download-ggml-model.sh base.en, and point ModelPath at it",
			Cause: err,
		}
	}
	return nil
}

// UnavailableError explains what is missing and how to get it.
//
// The remedy is the point: the first run of this phase on any machine will hit
// a missing binary, and an error that does not say what to install sends
// somebody to read source code.
type UnavailableError struct {
	Component string
	Path      string
	Remedy    string
	Cause     error
}

func (e *UnavailableError) Error() string {
	s := fmt.Sprintf("whispercpp: %s not found at %q", e.Component, e.Path)
	if e.Cause != nil {
		s += fmt.Sprintf(" (%v)", e.Cause)
	}
	if e.Remedy != "" {
		s += "\n  to fix: " + e.Remedy
	}
	return s
}

// Unwrap lets errors.Is match ErrUnavailable.
func (e *UnavailableError) Unwrap() error { return ErrUnavailable }

// ID returns the authored provider identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// Capabilities describes what this installation can do.
//
// # Declared honestly, including the unflattering parts
//
// Streaming reflects the configured binary rather than an aspiration. A
// provider that claimed to stream and then delivered one result at the end
// would make the orchestrator wait for partials that never come, and the
// resulting dead air would look like a network fault.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming: p.cfg.Streaming,
		// PartialResults tracks Streaming: a front-end that cannot stream
		// cannot emit an interim result either, and claiming otherwise would
		// make the orchestrator wait for one.
		PartialResults: p.cfg.Streaming,
		Languages:      []speech.Language{speech.Language(p.cfg.Language)},
		SampleRates:    []media.SampleRate{p.cfg.Format.Rate},
	}
}

// OpenSTT starts a recognition stream.
func (p *Provider) OpenSTT(ctx context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Format != p.cfg.Format {
		return nil, fmt.Errorf("%w: session is %s, this installation takes %s",
			ErrFormat, cfg.Format, p.cfg.Format)
	}
	if err := checkAvailable(p.cfg); err != nil {
		return nil, err
	}

	proc, err := process.New(p.processConfig())
	if err != nil {
		return nil, fmt.Errorf("whispercpp: %w", err)
	}
	if err := proc.Start(ctx); err != nil {
		return nil, fmt.Errorf("whispercpp: %w", err)
	}

	s := &stream{
		cfg:     p.cfg,
		proc:    proc,
		results: make(chan speech.TranscriptSegment, p.cfg.MaxPendingResults),
		done:    make(chan struct{}),
		session: cfg.Session,
		turn:    cfg.Turn,
	}

	s.wg.Add(1)
	go s.pump()

	return s, nil
}

// processConfig builds the argv vector.
//
// A VECTOR, never a command string. Nothing derived from a caller reaches it —
// the only variable parts are configuration this adapter validated.
func (p *Provider) processConfig() process.Config {
	cfg := p.cfg.Process
	cfg.Executable = p.cfg.Executable

	args := []string{
		"-m", p.cfg.ModelPath,
		"-l", p.cfg.Language,
	}
	if p.cfg.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(p.cfg.Threads))
	}
	if p.cfg.StepMillis > 0 {
		args = append(args, "--step", strconv.Itoa(p.cfg.StepMillis))
	}
	cfg.Args = append(args, p.cfg.Process.Args...)

	return cfg
}

// stream is one live recognition.
type stream struct {
	cfg  Config
	proc *process.Process

	results chan speech.TranscriptSegment
	done    chan struct{}
	wg      sync.WaitGroup

	session speech.SessionID
	turn    speech.TurnID

	mu       sync.Mutex
	closed   bool
	sentSend bool
	seq      int
}

// Compile-time proof that the frozen stream port is satisfied.
var _ speech.STTStream = (*stream)(nil)

// Write submits one frame of audio.
//
// The frame's payload is BORROWED from Phase 11B's ring, so it is written to
// the child immediately and never retained.
func (s *stream) Write(f media.Frame) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return ErrClosed
	}
	if f.Format != s.cfg.Format {
		return fmt.Errorf("%w: frame is %s, stream takes %s",
			ErrFormat, f.Format, s.cfg.Format)
	}

	if _, err := s.proc.Write(f.Payload); err != nil {
		return fmt.Errorf("whispercpp: writing audio: %w", err)
	}
	return nil
}

// Results yields transcript segments. Closed when recognition ends.
func (s *stream) Results() <-chan speech.TranscriptSegment { return s.results }

// CloseSend signals end of audio. Finals may still arrive.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	if s.sentSend || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.sentSend = true
	s.mu.Unlock()

	return s.proc.CloseStdin()
}

// Close abandons the stream and releases the process. Idempotent.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.done)

	// Stop reaps the child and waits for every goroutine it owns; the pump
	// below then observes a closed Lines channel and returns.
	err := s.proc.Stop(context.Background())
	s.wg.Wait()

	return err
}

// pump turns the child's stdout into transcript segments.
func (s *stream) pump() {
	defer s.wg.Done()
	defer close(s.results)

	for {
		select {
		case line, ok := <-s.proc.Lines():
			if !ok {
				return
			}
			seg, isResult := s.parse(line)
			if !isResult {
				continue
			}
			select {
			case s.results <- seg:
			case <-s.done:
				return
			}

		case <-s.done:
			return

		case <-s.proc.Exited():
			// Drain whatever the child managed to print before dying, then
			// stop. Discarding it would throw away the last thing the caller
			// said.
			for line := range s.proc.Lines() {
				if seg, ok := s.parse(line); ok {
					select {
					case s.results <- seg:
					case <-s.done:
						return
					}
				}
			}
			return
		}
	}
}

// parse turns one stdout line into a segment.
//
// # whisper.cpp's output is not a protocol
//
// It prints timestamped lines like "[00:00:00.000 --> 00:00:02.000]  text",
// interleaved with banners, progress and blank lines. There is no framing and
// no version marker, so this is tolerant by necessity: anything that does not
// look like a transcript line is ignored rather than treated as text.
//
// Ignoring is the safe direction. A banner mistaken for speech becomes a
// prompt; a transcript line mistaken for a banner is a missed utterance the
// caller will repeat.
func (s *stream) parse(line string) (speech.TranscriptSegment, bool) {
	text := strings.TrimSpace(line)
	if text == "" {
		return speech.TranscriptSegment{}, false
	}

	// Timestamped form: "[hh:mm:ss.mmm --> hh:mm:ss.mmm]   text"
	if strings.HasPrefix(text, "[") {
		close := strings.Index(text, "]")
		if close < 0 {
			return speech.TranscriptSegment{}, false
		}
		inner := text[1:close]
		if !strings.Contains(inner, "-->") {
			return speech.TranscriptSegment{}, false
		}
		text = strings.TrimSpace(text[close+1:])
	}

	if text == "" || isBanner(text) {
		return speech.TranscriptSegment{}, false
	}

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	return speech.TranscriptSegment{
		Segment:  speech.NewSegmentID(),
		Session:  s.session,
		Turn:     s.turn,
		Sequence: uint64(seq),
		Text:     text,
		IsFinal:  true,
		// whisper.cpp emits no per-segment confidence. Reporting a number it
		// did not produce would be a fabrication; ConfidenceUnknown is the
		// value Phase 11C defines for exactly this.
		Confidence: speech.ConfidenceUnknown,
		Language:   speech.Language(s.cfg.Language),
		Role:       speech.RoleCaller,
		Meta: speech.ProviderMeta{
			Provider: s.cfg.ID,
		},
	}, true
}

// bannerPrefixes are lines whisper.cpp prints that are not speech.
//
// Matched case-insensitively on a prefix. Deliberately conservative: a missed
// banner becomes a spurious utterance, which is visible and reported, whereas
// an over-broad rule silently eats real speech.
var bannerPrefixes = []string{
	"whisper_", "main:", "system_info", "init:", "error", "usage:",
	"[start speaking]", "###",
}

func isBanner(text string) bool {
	lower := strings.ToLower(text)
	for _, prefix := range bannerPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Audio conversion
// ---------------------------------------------------------------------------

// WriteWAVHeader writes a 44-byte RIFF header for the given format and payload
// length.
//
// # Why this is here and explicit
//
// Some whisper front-ends want a WAV file rather than raw samples, and §13
// forbids silent conversion. This makes the conversion a named, tested function
// a caller opts into rather than something an adapter does behind their back.
//
// It converts CONTAINER, never content: no resampling, no channel mixing, no
// sample-format change. A format the binary cannot take is refused by
// Config.validate rather than quietly converted here.
func WriteWAVHeader(w *bufio.Writer, format media.AudioFormat, dataBytes int) error {
	if format.Format != media.FormatPCM16 || format.Layout != media.LayoutMono {
		return fmt.Errorf("%w: WAV header supports mono PCM16 only, got %s",
			ErrFormat, format)
	}

	const headerSize = 44
	bitsPerSample := 16
	channels := 1
	byteRate := int(format.Rate) * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	put32 := func(v int) error { return binary.Write(w, binary.LittleEndian, uint32(v)) }
	put16 := func(v int) error { return binary.Write(w, binary.LittleEndian, uint16(v)) }

	for _, step := range []func() error{
		func() error { _, err := w.WriteString("RIFF"); return err },
		func() error { return put32(headerSize - 8 + dataBytes) },
		func() error { _, err := w.WriteString("WAVEfmt "); return err },
		func() error { return put32(16) },
		func() error { return put16(1) }, // PCM
		func() error { return put16(channels) },
		func() error { return put32(int(format.Rate)) },
		func() error { return put32(byteRate) },
		func() error { return put16(blockAlign) },
		func() error { return put16(bitsPerSample) },
		func() error { _, err := w.WriteString("data"); return err },
		func() error { return put32(dataBytes) },
	} {
		if err := step(); err != nil {
			return fmt.Errorf("whispercpp: writing WAV header: %w", err)
		}
	}
	return w.Flush()
}
