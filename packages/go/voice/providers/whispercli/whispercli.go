// Package whispercli adapts the openai-whisper Python command-line tool to the
// frozen Phase 11C recognition port.
//
// # This is a different program from whisper.cpp, and the difference matters
//
// providers/whispercpp targets the C++ streaming front-end: raw PCM on stdin,
// transcription lines out as it goes. This package targets the Python reference
// implementation, which is BATCH ONLY: it takes a finished audio FILE, loads a
// model, and transcribes the whole thing.
//
// Two adapters exist because both programs are called "whisper" and neither can
// stand in for the other. Configuring one where the other is installed produces
// a process that starts, reads nothing, and eventually times out.
//
// # What this adapter cannot do, stated before what it can
//
//   - NO STREAMING INPUT. Audio is buffered until the caller signals the end of
//     speech. Capabilities reports Streaming false, and the orchestrator must
//     plan around it rather than waiting for partials that will never arrive.
//   - NO PARTIAL RESULTS. Segments are final when they appear; nothing is
//     revised.
//   - NOT REAL-TIME. Measured on this machine: see docs/voice/PERFORMANCE.md.
//     Inference begins only after the caller stops speaking, so its latency is
//     added to the turn rather than overlapped with it.
//
// It exists so that at least one recognition path executes end to end on a
// machine where whisper.cpp has not been built. It is a development
// convenience, not a real-time recogniser.
//
// # What it does do, and it is not nothing
//
// The tool prints each segment to stdout AS IT DECODES it, so results arrive
// progressively once inference starts. The adapter forwards them as they come
// rather than waiting for the process to exit, which is the only latency this
// design has any control over.
package whispercli

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// ErrUnavailable is returned when the tool is missing.
	ErrUnavailable = errors.New("whispercli: unavailable")

	// ErrClosed is returned by an operation on a closed stream.
	ErrClosed = errors.New("whispercli: stream is closed")

	// ErrFormat is returned for audio the tool cannot accept.
	ErrFormat = errors.New("whispercli: unsupported audio format")

	// ErrAudioLimit is returned when buffered audio reached its bound.
	//
	// A BOUND, NOT A BUG. This adapter must hold the whole utterance before it
	// can transcribe any of it, so without a limit a caller who never signals
	// the end of speech fills the disk.
	ErrAudioLimit = errors.New("whispercli: buffered audio limit reached")
)

// Config describes one openai-whisper installation.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID

	// Executable is the absolute path to the whisper entry point.
	Executable string

	// Model is the model name the tool loads — tiny, base, small and so on.
	//
	// A NAME, NOT A PATH. The Python tool resolves names against its own cache
	// and downloads on a miss. That download is the one thing this adapter
	// cannot make fail fast, and Available says so.
	Model string

	// Language is the recognition language tag.
	Language string

	// Format is the audio the adapter accepts. Converted to a WAV container
	// unchanged — no resampling, no channel mixing.
	Format media.AudioFormat

	// FP16 enables half precision. Off by default: it is unsupported on CPU and
	// the tool prints a warning and falls back, which pollutes stderr on every
	// run.
	FP16 bool

	// Process configures supervision.
	Process process.Config

	// MaxAudio bounds one utterance. Reaching it fails the stream rather than
	// growing a temporary file without limit.
	MaxAudio time.Duration

	// MaxPendingResults bounds queued transcript segments.
	MaxPendingResults int

	// WorkDir is where the temporary audio file is written. Empty uses the
	// system temporary directory.
	WorkDir string
}

func (c Config) validate() error {
	var problems []string

	if c.ID == "" {
		problems = append(problems, "ID must be set")
	}
	if c.Executable == "" {
		problems = append(problems, "Executable must be set")
	}
	if c.Model == "" {
		problems = append(problems, "Model must be set")
	}
	if c.Language == "" {
		problems = append(problems, "Language must be set")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Format.Layout != media.LayoutMono {
		problems = append(problems, "Format must be mono")
	}
	if c.Format.Format != media.FormatPCM16 {
		problems = append(problems, "Format must be PCM16")
	}
	if c.MaxAudio <= 0 {
		problems = append(problems, "MaxAudio must be positive; this adapter holds a "+
			"whole utterance before transcribing it, so without a bound a caller "+
			"that never ends its turn fills the disk")
	}
	if c.MaxPendingResults <= 0 {
		problems = append(problems, "MaxPendingResults must be positive")
	}

	if len(problems) > 0 {
		return fmt.Errorf("whispercli: invalid configuration: %s",
			strings.Join(problems, "; "))
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
	s := fmt.Sprintf("whispercli: %s not found at %q", e.Component, e.Path)
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

// Provider implements speech.STTProvider over the openai-whisper tool.
type Provider struct {
	cfg Config
}

// Compile-time proof that the frozen port is satisfied.
var _ speech.STTProvider = (*Provider)(nil)

// New builds a provider, refusing one whose tool is absent.
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
//
// # It cannot check the model, and does not pretend to
//
// The Python tool resolves a model NAME against its own cache and downloads on
// a miss. There is no supported way to ask "is base cached" without invoking
// it, so a first run with an uncached model will pause for a download this
// adapter cannot distinguish from slow inference.
//
// Stating that is better than a check that looks in a cache directory whose
// location is an implementation detail and would silently stop being true.
func Available(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return checkAvailable(cfg)
}

func checkAvailable(cfg Config) error {
	info, err := os.Stat(cfg.Executable)
	if err != nil || info.IsDir() {
		return &UnavailableError{
			Component: "executable",
			Path:      cfg.Executable,
			Remedy: "pip install openai-whisper, then point Executable at the " +
				"whisper entry point in your Python scripts directory",
			Cause: err,
		}
	}
	return nil
}

// ID returns the authored provider identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// Capabilities declares what this tool can do.
//
// # Streaming is false, and that is the honest answer
//
// The tool takes a finished file. An adapter that reported true would make the
// orchestrator send audio incrementally and wait for interim results, and the
// resulting silence would look like a provider fault rather than a design
// limitation.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Streaming:       false,
		PartialResults:  false,
		Languages:       []speech.Language{speech.Language(p.cfg.Language)},
		SampleRates:     []media.SampleRate{p.cfg.Format.Rate},
		MaxSessionAudio: p.cfg.MaxAudio,
	}
}

// OpenSTT starts a recognition stream.
//
// Nothing is spawned here. The tool cannot be given audio incrementally, so the
// process starts when the caller signals the end of speech.
func (p *Provider) OpenSTT(ctx context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Format != p.cfg.Format {
		return nil, fmt.Errorf("%w: session is %s, this provider takes %s",
			ErrFormat, cfg.Format, p.cfg.Format)
	}
	if err := checkAvailable(p.cfg); err != nil {
		return nil, err
	}

	dir := p.cfg.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	workDir, err := os.MkdirTemp(dir, "aegis-stt-")
	if err != nil {
		return nil, fmt.Errorf("whispercli: creating work directory: %w", err)
	}

	audioPath := filepath.Join(workDir, "utterance.wav")
	f, err := os.OpenFile(audioPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("whispercli: creating audio file: %w", err)
	}

	s := &stream{
		cfg:       p.cfg,
		workDir:   workDir,
		audioPath: audioPath,
		file:      f,
		writer:    bufio.NewWriterSize(f, wavWriteBuffer),
		results:   make(chan speech.TranscriptSegment, p.cfg.MaxPendingResults),
		done:      make(chan struct{}),
		session:   cfg.Session,
		turn:      cfg.Turn,
		maxBytes:  p.cfg.Format.BytesFor(p.cfg.MaxAudio),
	}

	// A placeholder header, rewritten with the real length once the utterance
	// ends. Writing it now keeps the sample offset correct as frames arrive.
	if err := writeWAVHeader(s.writer, p.cfg.Format, 0); err != nil {
		_ = s.cleanup()
		return nil, err
	}

	return s, nil
}

const wavWriteBuffer = 64 << 10

// stream is one buffered utterance.
type stream struct {
	cfg       Config
	workDir   string
	audioPath string

	mu        sync.Mutex
	file      *os.File
	writer    *bufio.Writer
	dataBytes int
	maxBytes  int
	closed    bool
	sent      bool

	proc    *process.Process
	results chan speech.TranscriptSegment
	done    chan struct{}
	wg      sync.WaitGroup

	// failure records a provider that died rather than finishing.
	//
	// speech.STTStream has no error channel — a design that suits a provider
	// which reports failures on the call that caused them, and leaves an
	// asynchronous death with nowhere to go. Without this, a crashed recogniser
	// is indistinguishable from a caller who said nothing, and the turn
	// silently produces no transcript.
	failureMu sync.Mutex
	failure   error

	session speech.SessionID
	turn    speech.TurnID
	seq     int
}

// Compile-time proof that the frozen stream port is satisfied.
var _ speech.STTStream = (*stream)(nil)

// Write appends one frame to the buffered utterance.
//
// The frame's payload is BORROWED from Phase 11B's ring, so it is copied to the
// file within this call and never retained.
func (s *stream) Write(f media.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.closed:
		return ErrClosed
	case s.sent:
		return fmt.Errorf("%w: audio already submitted", ErrClosed)
	case f.Format != s.cfg.Format:
		return fmt.Errorf("%w: frame is %s, stream takes %s",
			ErrFormat, f.Format, s.cfg.Format)
	}

	if s.dataBytes+len(f.Payload) > s.maxBytes {
		return fmt.Errorf("%w: %s of audio without an end of speech",
			ErrAudioLimit, s.cfg.MaxAudio)
	}

	n, err := s.writer.Write(f.Payload)
	s.dataBytes += n
	if err != nil {
		return fmt.Errorf("whispercli: buffering audio: %w", err)
	}
	return nil
}

// Results yields transcript segments.
func (s *stream) Results() <-chan speech.TranscriptSegment { return s.results }

// CloseSend finalises the audio and starts transcription.
//
// # This is where the whole utterance's latency lands
//
// A streaming recogniser has already done most of its work by now. This one has
// done none: the model loads and inference runs after this call. That is the
// cost of a batch tool and it is why Capabilities reports Streaming false.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.sent {
		s.mu.Unlock()
		return nil
	}
	s.sent = true
	dataBytes := s.dataBytes
	s.mu.Unlock()

	if err := s.finaliseWAV(dataBytes); err != nil {
		return err
	}

	// No audio at all: nothing to transcribe, and inventing a result would be
	// exactly the fabrication §21 forbids.
	if dataBytes == 0 {
		close(s.results)
		return nil
	}

	proc, err := process.New(s.processConfig())
	if err != nil {
		return fmt.Errorf("whispercli: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Process.StartTimeout)
	defer cancel()

	if err := proc.Start(ctx); err != nil {
		return fmt.Errorf("whispercli: %w", err)
	}

	s.mu.Lock()
	s.proc = proc
	s.mu.Unlock()

	s.wg.Add(1)
	go s.pump(proc)

	return nil
}

// finaliseWAV flushes the samples and rewrites the header with the real length.
func (s *stream) finaliseWAV(dataBytes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.writer == nil || s.file == nil {
		return ErrClosed
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("whispercli: flushing audio: %w", err)
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return fmt.Errorf("whispercli: rewinding audio: %w", err)
	}

	header := bufio.NewWriter(s.file)
	if err := writeWAVHeader(header, s.cfg.Format, dataBytes); err != nil {
		return err
	}
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("whispercli: closing audio: %w", err)
	}
	s.file = nil
	s.writer = nil
	return nil
}

// processConfig builds the argv vector.
//
// A VECTOR. The only variable parts are configuration this adapter validated
// and a temporary path this adapter generated; nothing a caller said reaches it.
func (s *stream) processConfig() process.Config {
	cfg := s.cfg.Process
	cfg.Executable = s.cfg.Executable

	cfg.Args = []string{
		s.audioPath,
		"--model", s.cfg.Model,
		"--language", s.cfg.Language,
		"--output_dir", s.workDir,
		"--output_format", "txt",
		"--fp16", pythonBool(s.cfg.FP16),
	}

	// PYTHONIOENCODING IS NOT OPTIONAL ON WINDOWS.
	//
	// Python writes stdout in the console's code page, which on a Windows
	// system is cp1252. Printing a Devanagari transcript through that encoder
	// raises UnicodeEncodeError and kills the tool mid-transcription — a
	// failure that appears only for non-Latin scripts, which is to say only for
	// the languages Phase 11E must support.
	//
	// Verified: whisper --help crashes this way on this machine when its output
	// contains a CJK character.
	// THE BASE ENVIRONMENT IS REQUIRED, NOT OPTIONAL.
	//
	// A non-nil child environment REPLACES the parent's, so setting the two
	// encoding variables alone leaves Python with no PATH and no SystemRoot. It
	// then dies before printing anything, and the adapter reports an empty
	// transcript with no error — which is precisely what happened here during
	// development, on audio that transcribes perfectly when run by hand.
	cfg.InheritEnv = process.DefaultInheritEnv()
	cfg.Env = append(cfg.Env, "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")

	// Batch inference is the whole cost of the turn, so the readiness probe is
	// nil: there is no banner to wait for, and the process is usable the moment
	// it starts.
	cfg.Ready = nil

	return cfg
}

// pythonBool renders a boolean the way Python argparse's str2bool expects it.
//
// strconv.FormatBool gives "false"; the tool's str2bool accepts only "True" and
// "False" with a leading capital and rejects anything else with exit code 2.
// The failure is silent from the outside — argparse prints usage to stderr and
// produces no transcript — so this cost a real debugging session.
func pythonBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// pump forwards segments as the tool decodes them.
func (s *stream) pump(proc *process.Process) {
	defer s.wg.Done()
	defer close(s.results)

	var produced int

	lines := proc.Lines()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				s.noteExit(proc, produced)
				return
			}
			seg, isResult := s.parse(line)
			if !isResult {
				continue
			}
			produced++
			select {
			case s.results <- seg:
			case <-s.done:
				return
			}

		case <-s.done:
			return
		}
	}
}

// noteExit records a child that failed rather than finishing.
//
// A non-zero exit is always a failure. A CLEAN exit that produced nothing is
// not necessarily one — silence is a legitimate transcript of silence — so it
// is left alone, and the caller distinguishes the two by asking Err.
func (s *stream) noteExit(proc *process.Process, produced int) {
	// WAIT FOR THE REAPER FIRST.
	//
	// The stdout pipe closes before Wait returns, so reading ExitError the
	// moment Lines() closes races the goroutine that sets it — and loses often
	// enough that a child which exited 2 on an argparse error reported no error
	// at all. That is exactly the "crashed provider looks like silence" failure
	// this function exists to prevent, so it failed at its one job.
	//
	// Exited is closed by the reaper AFTER the exit status is stored, so
	// waiting on it makes the read ordered rather than lucky.
	<-proc.Exited()

	exitErr := proc.ExitError()
	if exitErr == nil {
		return
	}

	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failure == nil {
		s.failure = fmt.Errorf(
			"whispercli: the recogniser exited with %w after %d segments; "+
				"stderr: %s",
			exitErr, produced, proc.StderrTail())
	}
}

// Err returns why recognition failed, or nil.
//
// # Why this exists outside the port
//
// speech.STTStream reports errors only from calls a caller makes. A provider
// that dies while transcribing has no such call to fail, so without this the
// only observable symptom is an empty transcript — and an empty transcript is
// also what a caller who said nothing produces.
//
// The orchestrator checks this after the results channel closes. Close returns
// the same error, for a caller that does not.
func (s *stream) Err() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failure
}

// parse turns one stdout line into a segment.
//
// # The format, measured rather than assumed
//
// The tool prints "[MM:SS.mmm --> MM:SS.mmm]  text", one line per segment, as
// each is decoded. Note MM:SS — whisper.cpp uses HH:MM:SS, which is why the two
// adapters cannot share a parser.
//
// Anything that does not match is ignored. A warning mistaken for speech
// becomes a prompt to a language model; a missed segment is an utterance the
// caller repeats.
func (s *stream) parse(line string) (speech.TranscriptSegment, bool) {
	text := strings.TrimSpace(line)
	if text == "" || !strings.HasPrefix(text, "[") {
		return speech.TranscriptSegment{}, false
	}

	end := strings.Index(text, "]")
	if end < 0 {
		return speech.TranscriptSegment{}, false
	}

	span := text[1:end]
	arrow := strings.Index(span, "-->")
	if arrow < 0 {
		return speech.TranscriptSegment{}, false
	}

	start, okStart := parseOffset(strings.TrimSpace(span[:arrow]))
	stop, okStop := parseOffset(strings.TrimSpace(span[arrow+3:]))
	if !okStart || !okStop {
		return speech.TranscriptSegment{}, false
	}

	body := strings.TrimSpace(text[end+1:])
	if body == "" {
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
		Text:     body,
		IsFinal:  true,
		// The tool reports avg_logprob, which is a mean log probability over
		// tokens and NOT a confidence in [0,1]. Squeezing one into the other
		// would be inventing a number the model never produced, and every
		// consumer that reads Confidence would then rest on it.
		Confidence: speech.ConfidenceUnknown,
		StartTime:  start,
		EndTime:    stop,
		Language:   speech.Language(s.cfg.Language),
		Role:       speech.RoleCaller,
		Meta: speech.ProviderMeta{
			Provider: s.cfg.ID,
			Model:    s.cfg.Model,
		},
	}, true
}

// parseOffset reads "MM:SS.mmm" or "HH:MM:SS.mmm" into a duration.
func parseOffset(s string) (time.Duration, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}

	// The leading fields are a base-60 number of units ABOVE seconds: for
	// "MM:SS" that is minutes, for "HH:MM:SS" it is hours then minutes.
	// Accumulating them as SECONDS and rescaling afterwards is the obvious
	// approach and it is wrong — it reads 01:30.500 as 31.5 seconds instead of
	// 90.5, which a test caught.
	var units int
	for _, p := range parts[:len(parts)-1] {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		units = units*60 + v
	}

	// The final component carries fractional seconds.
	secs, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, false
	}

	return time.Duration(units)*time.Minute +
		time.Duration(secs*float64(time.Second)), true
}

// Close abandons the stream and removes every temporary file. Idempotent.
//
// # The temporary audio file is deleted here, and that is load-bearing
//
// This adapter must write a caller's speech to disk, because the tool it drives
// reads a file and nothing else. That is the one place in Phase 11E where raw
// audio touches durable storage, it is documented in
// docs/voice/SECURITY_REVIEW.md, and this is the code that undoes it.
//
// The whole working directory goes, so the tool's own output files leave with
// it.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	proc := s.proc
	s.mu.Unlock()

	close(s.done)

	var stopErr error
	if proc != nil {
		stopErr = proc.Stop(context.Background())
	}
	s.wg.Wait()

	if err := s.cleanup(); err != nil && stopErr == nil {
		stopErr = err
	}
	// A provider that died mid-transcription outranks a stop error: the stop
	// error describes tidying up after a failure the caller has not been told
	// about yet.
	if failure := s.Err(); failure != nil {
		return failure
	}
	return stopErr
}

// cleanup removes the working directory and everything in it.
func (s *stream) cleanup() error {
	s.mu.Lock()
	file, dir := s.file, s.workDir
	s.file, s.writer = nil, nil
	s.mu.Unlock()

	if file != nil {
		_ = file.Close()
	}
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("whispercli: removing buffered audio at %q: %w", dir, err)
	}
	return nil
}

// AudioPath returns where the utterance is buffered.
//
// For tests that verify the file is removed. Not otherwise useful, and
// deliberately not part of the port.
func (s *stream) AudioPath() string { return s.audioPath }

// writeWAVHeader writes a 44-byte RIFF header.
//
// Container only: no resampling, no channel mixing, no sample-format change.
// A format the tool cannot take is refused by Config.validate.
func writeWAVHeader(w *bufio.Writer, format media.AudioFormat, dataBytes int) error {
	if format.Format != media.FormatPCM16 || format.Layout != media.LayoutMono {
		return fmt.Errorf("%w: WAV header supports mono PCM16 only, got %s",
			ErrFormat, format)
	}

	const (
		headerSize    = 44
		bitsPerSample = 16
		channels      = 1
	)
	byteRate := int(format.Rate) * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	put32 := func(v int) error { return binary.Write(w, binary.LittleEndian, uint32(v)) }
	put16 := func(v int) error { return binary.Write(w, binary.LittleEndian, uint16(v)) }

	for _, step := range []func() error{
		func() error { _, err := w.WriteString("RIFF"); return err },
		func() error { return put32(headerSize - 8 + dataBytes) },
		func() error { _, err := w.WriteString("WAVEfmt "); return err },
		func() error { return put32(16) },
		func() error { return put16(1) },
		func() error { return put16(channels) },
		func() error { return put32(int(format.Rate)) },
		func() error { return put32(byteRate) },
		func() error { return put16(blockAlign) },
		func() error { return put16(bitsPerSample) },
		func() error { _, err := w.WriteString("data"); return err },
		func() error { return put32(dataBytes) },
	} {
		if err := step(); err != nil {
			return fmt.Errorf("whispercli: writing WAV header: %w", err)
		}
	}
	return w.Flush()
}
