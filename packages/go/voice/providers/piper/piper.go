// Package piper adapts the Piper neural text-to-speech engine to the frozen
// Phase 11C synthesis port.
//
// # It implements speech.TTSProvider and adds nothing to it
//
// No Piper type appears in any exported signature. A caller holds a
// speech.TTSProvider and cannot tell what is behind it, which is what makes
// replacing it with a cloud voice a configuration change rather than a code
// change.
//
// # What Piper is, and how honestly it streams
//
// Piper reads a line of text on stdin and writes raw PCM to stdout, one
// utterance per line. That is a stream in the sense that audio arrives while
// the process runs, and NOT a stream in the sense that matters most for a voice
// agent: the model synthesises a whole utterance before emitting its first
// sample, so the first audio of a sentence appears only once that sentence has
// been fully generated.
//
// The practical consequence is that CHUNK SIZE IS THE LATENCY KNOB. Feeding
// Piper a whole paragraph produces nothing until the paragraph is done; feeding
// it clause by clause produces audio a clause at a time. Phase 11C's chunker
// already splits a response into speakable clauses, and this adapter is built
// to be driven that way.
//
// §2 asks that a runtime unable to stream at the desired granularity say so
// rather than pretend. That is what the paragraph above is.
//
// # This package is not executable everywhere, and says so
//
// Piper is a compiled binary with a separate voice model. Where either is
// absent, every entry point returns a typed error naming the path it checked
// and what to install. It fabricates no audio, and no test here passes by
// pretending a voice ran.
package piper

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

// Errors this adapter returns.
var (
	// ErrUnavailable is returned when the binary or voice model is missing.
	ErrUnavailable = errors.New("piper: unavailable")

	// ErrClosed is returned by an operation on a closed stream.
	ErrClosed = errors.New("piper: stream is closed")

	// ErrBackpressure is returned when the chunk queue is full.
	ErrBackpressure = errors.New("piper: chunk queue is full")

	// ErrSynthesisFailed is returned when the engine died mid-utterance.
	ErrSynthesisFailed = errors.New("piper: synthesis failed")

	// ErrUnsafeText is returned for text that cannot be sent to a line-oriented
	// engine.
	ErrUnsafeText = errors.New("piper: unsafe text")
)

// Config describes one Piper installation.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID

	// Executable is the absolute path to the piper binary.
	Executable string

	// ModelPath is the absolute path to the .onnx voice model.
	ModelPath string

	// SpeakerID selects a voice within a multi-speaker model. Negative uses the
	// model's default.
	SpeakerID int

	// Language is the voice's language tag.
	Language string

	// Format is the audio the voice produces.
	//
	// DECLARED, NOT DETECTED. Piper emits headerless PCM whose rate is a
	// property of the model, and there is nothing in the byte stream to read it
	// from. A mismatch between this and the model produces audio that plays at
	// the wrong speed — which sounds like a broken voice rather than a
	// configuration error, so it is validated against the session's format at
	// OpenTTS rather than discovered later.
	Format media.AudioFormat

	// LengthScale slows or speeds delivery. 1 is the model's natural rate;
	// larger is slower.
	LengthScale float64

	// FrameDuration is how much audio one emitted frame carries.
	FrameDuration time.Duration

	// Process configures supervision.
	Process process.Config

	// ChunkTimeout bounds synthesis of one chunk.
	ChunkTimeout time.Duration

	// MaxPendingChunks bounds queued text.
	MaxPendingChunks int

	// MaxPendingFrames bounds queued audio.
	MaxPendingFrames int

	// MaxChunkChars bounds one chunk of text.
	MaxChunkChars int
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
		problems = append(problems, "Format must be mono; Piper emits one channel")
	}
	if c.Format.Format != media.FormatPCM16 {
		problems = append(problems, "Format must be PCM16; Piper emits signed "+
			"16-bit samples")
	}
	if c.LengthScale <= 0 {
		problems = append(problems, "LengthScale must be positive")
	}
	if c.FrameDuration <= 0 {
		problems = append(problems, "FrameDuration must be positive")
	}
	if c.ChunkTimeout <= 0 {
		problems = append(problems, "ChunkTimeout must be positive")
	}
	if c.MaxPendingChunks <= 0 {
		problems = append(problems, "MaxPendingChunks must be positive")
	}
	if c.MaxPendingFrames <= 0 {
		problems = append(problems, "MaxPendingFrames must be positive; an "+
			"unbounded audio queue in front of a slow consumer grows for the "+
			"length of the call")
	}
	if c.MaxChunkChars <= 0 {
		problems = append(problems, "MaxChunkChars must be positive")
	}

	if len(problems) > 0 {
		return fmt.Errorf("piper: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// UnavailableError explains what is missing and how to get it.
type UnavailableError struct {
	Component string
	Path      string
	Remedy    string
	Cause     error
}

func (e *UnavailableError) Error() string {
	s := fmt.Sprintf("piper: %s not found at %q", e.Component, e.Path)
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

// Provider implements speech.TTSProvider over Piper.
type Provider struct {
	cfg Config
}

// Compile-time proof that the frozen port is satisfied.
var _ speech.TTSProvider = (*Provider)(nil)

// New builds a provider, refusing one whose binary or voice is absent.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := checkAvailable(cfg); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg}, nil
}

// Available reports whether this installation could be used.
func Available(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return checkAvailable(cfg)
}

func checkAvailable(cfg Config) error {
	if info, err := os.Stat(cfg.Executable); err != nil || info.IsDir() {
		return &UnavailableError{
			Component: "executable",
			Path:      cfg.Executable,
			Remedy: "install Piper from https://github.com/rhasspy/piper/releases " +
				"and point Executable at the piper binary",
			Cause: err,
		}
	}
	if info, err := os.Stat(cfg.ModelPath); err != nil || info.IsDir() {
		return &UnavailableError{
			Component: "voice model",
			Path:      cfg.ModelPath,
			Remedy: "download a voice from https://huggingface.co/rhasspy/piper-voices " +
				"and point ModelPath at its .onnx file (the .onnx.json config must " +
				"sit beside it)",
			Cause: err,
		}
	}

	// Piper reads its sample rate and phoneme map from a JSON file that must
	// sit beside the model. A missing one produces a confusing runtime failure
	// rather than a clear startup error, so it is checked here.
	configPath := cfg.ModelPath + ".json"
	if info, err := os.Stat(configPath); err != nil || info.IsDir() {
		return &UnavailableError{
			Component: "voice configuration",
			Path:      configPath,
			Remedy: "download the .onnx.json that accompanies the voice model; " +
				"Piper reads its sample rate and phoneme map from it",
			Cause: err,
		}
	}
	return nil
}

// ID returns the authored provider identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// Capabilities describes what this voice can do.
//
// # Streaming is true, with the qualification in the package comment
//
// Audio arrives while the process runs, so a consumer can begin playing before
// synthesis of the whole response finishes. It does NOT mean the first sample
// of a chunk appears before that chunk is fully synthesised — it does not, and
// chunk size is therefore the latency knob.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming:   true,
		Languages:   []speech.Language{speech.Language(p.cfg.Language)},
		SampleRates: []media.SampleRate{p.cfg.Format.Rate},
	}
}

// OpenTTS starts a synthesis stream.
func (p *Provider) OpenTTS(ctx context.Context, cfg speech.TTSConfig) (speech.TTSStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Format != p.cfg.Format {
		return nil, fmt.Errorf(
			"%w: session is %s, this voice produces %s. Piper emits headerless PCM "+
				"whose rate is a property of the model, so this cannot be resolved by "+
				"resampling here",
			speech.ErrInvalidAudio, cfg.Format, p.cfg.Format)
	}
	if err := checkAvailable(p.cfg); err != nil {
		return nil, err
	}

	proc, err := process.New(p.processConfig())
	if err != nil {
		return nil, fmt.Errorf("piper: %w", err)
	}
	if err := proc.Start(ctx); err != nil {
		return nil, fmt.Errorf("piper: %w", err)
	}

	s := &stream{
		cfg:     p.cfg,
		proc:    proc,
		audio:   make(chan media.Frame, p.cfg.MaxPendingFrames),
		chunks:  make(chan speech.Chunk, p.cfg.MaxPendingChunks),
		done:    make(chan struct{}),
		session: cfg.Session,
		turn:    cfg.Turn,
	}

	s.wg.Add(1)
	go s.feed()

	s.wg.Add(1)
	go s.emit()

	return s, nil
}

// processConfig builds the argv vector.
//
// A VECTOR. The response text is NOT here — it goes to the engine on stdin, so
// no caller-derived or model-derived text ever reaches a command line.
func (p *Provider) processConfig() process.Config {
	cfg := p.cfg.Process
	cfg.Executable = p.cfg.Executable
	cfg.InheritEnv = process.DefaultInheritEnv()

	args := []string{
		"--model", p.cfg.ModelPath,
		// Raw PCM on stdout. The alternative writes WAV files to disk, which
		// would put synthesised speech on durable storage for no reason.
		"--output_raw",
		"--length_scale", strconv.FormatFloat(p.cfg.LengthScale, 'f', 3, 64),
	}
	if p.cfg.SpeakerID >= 0 {
		args = append(args, "--speaker", strconv.Itoa(p.cfg.SpeakerID))
	}
	cfg.Args = append(args, p.cfg.Process.Args...)

	// Piper writes progress to stderr and audio to stdout. There is no banner
	// on stdout to wait for, and waiting for one would hang every start.
	// Piper writes headerless PCM to stdout. A line scanner over binary would
	// split on whatever bytes happened to be 0x0A and discard them, corrupting
	// the audio silently.
	cfg.RawStdout = true
	cfg.Ready = nil

	return cfg
}

// stream is one live synthesis.
type stream struct {
	cfg  Config
	proc *process.Process

	chunks chan speech.Chunk
	audio  chan media.Frame
	done   chan struct{}
	wg     sync.WaitGroup

	session speech.SessionID
	turn    speech.TurnID

	mu       sync.Mutex
	closed   bool
	sentSend bool

	// sequence numbers the frames this stream emits.
	sequence atomic.Uint64

	// emitted counts frames delivered, for the caller's own accounting.
	emitted atomic.Uint64

	failureMu sync.Mutex
	failure   error
}

// Compile-time proof that the frozen stream port is satisfied.
var _ speech.TTSStream = (*stream)(nil)

// Synthesize submits one chunk of text.
//
// # Text goes to stdin, never to an argument
//
// This is the one place in Phase 11E where MODEL OUTPUT — text a language model
// generated, downstream of something a caller said — reaches an external
// program. It travels on stdin as data. Nothing here builds a command line, so
// there is no construction in which a generated sentence becomes a command.
//
// Newlines are refused rather than escaped: Piper is line-oriented, one
// utterance per line, so an embedded newline would silently split one chunk
// into two utterances with a gap between them.
func (s *stream) Synthesize(c speech.Chunk) error {
	s.mu.Lock()
	closed, sent := s.closed, s.sentSend
	s.mu.Unlock()

	switch {
	case closed:
		return ErrClosed
	case sent:
		return fmt.Errorf("%w: text already ended", ErrClosed)
	}

	text := strings.TrimSpace(c.Text)
	if text == "" {
		// Nothing to say. Not an error — a chunker can legitimately produce an
		// empty clause — but nothing is sent, because a bare newline makes
		// Piper emit an empty utterance and a spurious frame boundary.
		return nil
	}
	if strings.ContainsAny(text, "\n\r") {
		return fmt.Errorf(
			"%w: chunk %d contains a line break; Piper is line-oriented and would "+
				"split it into two utterances", ErrUnsafeText, c.Sequence)
	}
	if len([]rune(text)) > s.cfg.MaxChunkChars {
		return fmt.Errorf("%w: chunk %d is %d characters, the bound is %d",
			ErrUnsafeText, c.Sequence, len([]rune(text)), s.cfg.MaxChunkChars)
	}

	select {
	case s.chunks <- speech.Chunk{
		Sequence:   c.Sequence,
		Text:       text,
		Terminator: c.Terminator,
		IsFinal:    c.IsFinal,
	}:
		return nil
	case <-s.done:
		return ErrClosed
	default:
		// BACKPRESSURE, REPORTED RATHER THAN ABSORBED. Blocking here would hold
		// the generation loop; growing the queue would defer the problem until
		// memory ran out.
		return fmt.Errorf("%w: %d chunks queued", ErrBackpressure, s.cfg.MaxPendingChunks)
	}
}

// feed writes queued text to the engine, one utterance per line.
func (s *stream) feed() {
	defer s.wg.Done()

	for {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				_ = s.proc.CloseStdin()
				return
			}
			if _, err := s.proc.Write([]byte(chunk.Text + "\n")); err != nil {
				// As in emit: a write that fails because this stream is being
				// torn down is the teardown working, not the engine failing.
				if !s.closing() {
					s.note(fmt.Errorf("%w: writing chunk %d: %v",
						ErrSynthesisFailed, chunk.Sequence, err))
				}
				return
			}

		case <-s.done:
			return
		}
	}
}

// emit reads raw PCM from the engine and frames it.
//
// # Piper emits a byte stream, not frames
//
// The engine writes headerless PCM with no utterance boundaries in it. This
// accumulates whole frames of the configured duration and forwards them; a
// partial frame at the end of the stream is padded with silence rather than
// dropped, because dropping it would clip the last few milliseconds of every
// response.
func (s *stream) emit() {
	defer s.wg.Done()
	defer close(s.audio)

	frameBytes := s.cfg.Format.BytesFor(s.cfg.FrameDuration)
	if frameBytes <= 0 {
		s.note(fmt.Errorf("%w: frame duration %s yields no bytes at %s",
			ErrSynthesisFailed, s.cfg.FrameDuration, s.cfg.Format))
		return
	}

	buf := make([]byte, frameBytes)
	filled := 0
	reader := s.proc.Stdout()

	for {
		select {
		case <-s.done:
			return
		default:
		}

		n, err := reader.Read(buf[filled:])
		filled += n

		if filled == frameBytes {
			if !s.deliver(buf, frameBytes) {
				return
			}
			filled = 0
		}

		if err != nil {
			// A DELIBERATE TEARDOWN IS NOT A FAILURE. Close stops the engine
			// and closes the stdout pipe under this read, so the read fails —
			// but a barge-in closing a perfectly healthy stream must not be
			// reported as a broken voice, or a router counting failures
			// eventually opens a circuit breaker against a provider that never
			// misbehaved.
			if s.closing() {
				return
			}

			// A partial frame at the end is real audio. Padding it to a whole
			// frame keeps the media contract — every frame is one frame long —
			// and loses nothing the caller said.
			if filled > 0 {
				for i := filled; i < frameBytes; i++ {
					buf[i] = 0
				}
				s.deliver(buf, frameBytes)
			}
			if !errors.Is(err, io.EOF) {
				s.note(fmt.Errorf("%w: reading audio: %v", ErrSynthesisFailed, err))
			}
			s.noteExit()
			return
		}
	}
}

// deliver sends one frame, reporting whether the stream is still live.
func (s *stream) deliver(buf []byte, n int) bool {
	// COPIED. Phase 11C's contract says frames delivered on this channel are
	// owned by the receiver — unlike media's ring buffer, a provider hands over
	// audio it will not touch again. Reusing the accumulation buffer would
	// corrupt frames already queued.
	payload := make([]byte, n)
	copy(payload, buf[:n])

	seq := s.sequence.Add(1) - 1
	frame := media.Frame{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * s.cfg.FrameDuration,
		Format:    s.cfg.Format,
		Payload:   payload,
	}

	select {
	case s.audio <- frame:
		s.emitted.Add(1)
		return true
	case <-s.done:
		return false
	}
}

// Audio yields synthesised frames, closed when synthesis ends.
func (s *stream) Audio() <-chan media.Frame { return s.audio }

// CloseSend signals end of text. Audio may still arrive.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	if s.sentSend || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.sentSend = true
	s.mu.Unlock()

	// Closing the chunk queue makes feed drain what is left and then close the
	// engine's stdin, which is how Piper is told there is no more text.
	close(s.chunks)
	return nil
}

// Close abandons synthesis and releases the engine. Idempotent.
//
// # This is what a barge-in calls, and it must be prompt
//
// ADR-0004 §12 fixes the abort budget at one frame interval. Closing done first
// stops the emit loop from queueing further audio immediately; stopping the
// process then reclaims it.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.done)

	err := s.proc.Stop(context.Background())
	s.wg.Wait()

	if failure := s.Err(); failure != nil {
		return failure
	}
	return err
}

// Err returns why synthesis failed, or nil.
//
// speech.TTSStream reports errors only from calls a caller makes. An engine
// that dies mid-utterance has no such call to fail, so without this the only
// symptom is audio that stops — indistinguishable from a response that ended.
func (s *stream) Err() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failure
}

// Emitted returns how many frames this stream has delivered.
func (s *stream) Emitted() uint64 { return s.emitted.Load() }

// closing reports whether this stream has been abandoned by its caller.
//
// It distinguishes "the engine broke" from "we stopped the engine", which are
// the same observation — a failed read or write — from inside the goroutines.
func (s *stream) closing() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *stream) note(err error) {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
}

// noteExit records an engine that died rather than finishing.
func (s *stream) noteExit() {
	// Wait for the reaper before reading the exit status: the stdout pipe
	// closes before Wait returns, and reading it early races the goroutine that
	// stores it. That race cost a debugging session in the recognition adapter.
	<-s.proc.Exited()

	if exitErr := s.proc.ExitError(); exitErr != nil {
		s.note(fmt.Errorf("%w: the engine exited with %v; stderr: %s",
			ErrSynthesisFailed, exitErr, s.proc.StderrTail()))
	}
}

// ---------------------------------------------------------------------------
// Audio helpers
// ---------------------------------------------------------------------------

// SilenceFrame builds one frame of silence in the given format.
//
// Zero bytes are silence in signed PCM of either width, which is why this needs
// no per-format special case.
func SilenceFrame(format media.AudioFormat, d time.Duration, seq uint64) media.Frame {
	return media.Frame{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * d,
		Format:    format,
		Payload:   make([]byte, format.BytesFor(d)),
	}
}

// PCMLevel returns the mean absolute sample level of a PCM16 payload, in
// [0,1].
//
// For tests and diagnostics that need to distinguish audio from silence without
// reimplementing sample decoding. Not part of the port.
func PCMLevel(payload []byte) float64 {
	if len(payload) < 2 {
		return 0
	}
	var total float64
	n := len(payload) / 2
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(payload[i*2:]))
		if v < 0 {
			total += float64(-int(v))
		} else {
			total += float64(v)
		}
	}
	return total / float64(n) / 32768.0
}
