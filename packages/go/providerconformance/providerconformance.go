// Package providerconformance holds behavioural conformance suites for the
// provider ports: speech.STTProvider, speech.TTSProvider and runtime.Provider.
//
// Each suite is a list of checks, and each check enforces one sentence of the
// port's documented contract, quoted in its Contract field. Nothing is checked
// that the port's documentation does not say. A suite runs against any
// implementation: the fakes in speech and runtime today, and every vendor or
// local-model adapter later, which then has a ready acceptance test.
//
// Use it from a test:
//
//	func TestMySTTAdapter(t *testing.T) {
//		providerconformance.RunSTT(t, providerconformance.STTSubject{
//			New:    func(t *testing.T) speech.STTProvider { return newAdapter(t) },
//			Config: myValidConfig,
//		})
//	}
//
// Bounds are deliberately generous (two seconds by default for "does not
// block"): a conformance suite proves a contract, not a latency target. The
// LLM cancellation check uses ADR-0011's 20 ms budget plus a scheduling
// allowance, both overridable.
package providerconformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
)

// DefaultBound is how long a call may take before it counts as blocked.
const DefaultBound = 2 * time.Second

// frameDuration is the length of each audio frame a check writes.
const frameDuration = 20 * time.Millisecond

// Check is one conformance check: the contract sentence it enforces, and a
// function that returns nil when the implementation conforms.
type Check struct {
	Name     string
	Contract string
	Run      func() error
}

func run(t *testing.T, checks []Check) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.Name, func(t *testing.T) {
			if err := c.Run(); err != nil {
				t.Fatalf("%v\ncontract: %s", err, c.Contract)
			}
		})
	}
}

// within runs f and reports whether it returned within d. A call that never
// returns is left running: the check has already failed.
func within(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func bound(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultBound
	}
	return d
}

// closeReporting closes c and returns err, or, if the check had not already
// failed, the Close error: a stream that cannot close cleanly does not conform.
func closeReporting(c io.Closer, err error) error {
	if cerr := c.Close(); cerr != nil && err == nil {
		return fmt.Errorf("Close: %w", cerr)
	}
	return err
}

// stableID reports whether an identifier is set and returns the same value on
// a second call.
func stableID[T ~string](id func() T) error {
	first, second := id(), id()
	if first == "" || first != second {
		return errors.New("ID() is empty or changes between calls")
	}
	return nil
}

// declares checks that the provider declares the subject's language and rate.
func declares(caps speech.Capabilities, lang speech.Language, rate media.SampleRate) error {
	if !caps.SupportsRate(rate) {
		return fmt.Errorf("the provider does not declare the config's sample rate %d", rate)
	}
	if len(caps.Languages) > 0 && !caps.Supports(lang) {
		return fmt.Errorf("the provider does not declare the config's language %q", lang)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Speech to text.
// ---------------------------------------------------------------------------

// STTSubject describes one recognition provider under test.
type STTSubject struct {
	// New returns a fresh provider for one check.
	New func(t *testing.T) speech.STTProvider
	// Config returns a valid configuration the provider declares support for.
	Config func() speech.STTConfig
	// Frames is how many 20 ms frames a check writes; zero means 3.
	Frames int
	// Bound is how long a call may take before it counts as blocked; zero
	// means DefaultBound.
	Bound time.Duration
}

// RunSTT runs every STT check as a subtest of t.
func RunSTT(t *testing.T, s STTSubject) {
	t.Helper()
	run(t, STTChecks(t, s))
}

type sttSuite struct {
	t      *testing.T
	s      STTSubject
	frames int
	bound  time.Duration
}

// STTChecks returns the STT checks for s.
func STTChecks(t *testing.T, s STTSubject) []Check {
	frames := s.Frames
	if frames <= 0 {
		frames = 3
	}
	c := sttSuite{t: t, s: s, frames: frames, bound: bound(s.Bound)}
	return []Check{
		{"id is stable and not empty", "STTProvider.ID returns the authored provider identifier.", c.id},
		{
			"the subject's config is one the provider declares",
			"Capabilities describes what the provider declares it can do (Languages, SampleRates).", c.config,
		},
		{
			"write does not block",
			"STTStream.Write MUST NOT BLOCK INDEFINITELY. A provider under pressure returns ErrBackpressure.", c.writeDoesNotBlock,
		},
		{
			"results close after CloseSend",
			"Results: the provider closes it when the stream ends. CloseSend signals end of audio; finals may still arrive afterwards.",
			c.resultsCloseAfterCloseSend,
		},
		{
			"results close after Close",
			"Close abandons the stream and releases its resources; Results is closed when the stream ends.", c.resultsCloseAfterClose,
		},
		{"close is idempotent", "STTStream.Close abandons the stream and releases its resources. Idempotent.", c.closeIdempotent},
	}
}

func (c sttSuite) id() error { return stableID(c.s.New(c.t).ID) }

func (c sttSuite) config() error {
	cfg := c.s.Config()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the subject's config is invalid: %w", err)
	}
	return declares(c.s.New(c.t).Capabilities(), cfg.Language, cfg.Format.Rate)
}

func (c sttSuite) open() (speech.STTStream, error) {
	st, err := c.s.New(c.t).OpenSTT(context.Background(), c.s.Config())
	if err != nil {
		return nil, fmt.Errorf("OpenSTT with a valid config: %w", err)
	}
	return st, nil
}

// write sends the subject's frames, each within the bound.
func (c sttSuite) write(st speech.STTStream) error {
	format := c.s.Config().Format
	size := format.BytesFor(frameDuration)
	if size <= 0 {
		size = 320
	}
	var seq uint64
	var ts time.Duration
	for i := 0; i < c.frames; i++ {
		f := media.Frame{Sequence: seq, Timestamp: ts, Format: format, Payload: make([]byte, size)}
		var werr error
		if !within(c.bound, func() { werr = st.Write(f) }) {
			return fmt.Errorf("Write %d blocked for more than %v", i, c.bound)
		}
		if werr != nil && !errors.Is(werr, speech.ErrBackpressure) {
			return fmt.Errorf("Write %d: %w", i, werr)
		}
		seq++
		ts += frameDuration
	}
	return nil
}

func (c sttSuite) writeDoesNotBlock() (err error) {
	st, err := c.open()
	if err != nil {
		return err
	}
	defer func() { err = closeReporting(st, err) }()
	return c.write(st)
}

func (c sttSuite) resultsCloseAfterCloseSend() (err error) {
	st, err := c.open()
	if err != nil {
		return err
	}
	defer func() { err = closeReporting(st, err) }()
	if werr := c.write(st); werr != nil {
		return werr
	}
	if cerr := st.CloseSend(); cerr != nil {
		return fmt.Errorf("CloseSend: %w", cerr)
	}
	return drain(st.Results(), c.bound)
}

func (c sttSuite) resultsCloseAfterClose() error {
	st, err := c.open()
	if err != nil {
		return err
	}
	if werr := c.write(st); werr != nil {
		return werr
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("Close: %w", cerr)
	}
	return drain(st.Results(), c.bound)
}

func (c sttSuite) closeIdempotent() error {
	st, err := c.open()
	if err != nil {
		return err
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("first Close: %w", cerr)
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("second Close: %w", cerr)
	}
	return nil
}

// drain reads until ch closes, or reports that it did not close within d.
func drain[T any](ch <-chan T, d time.Duration) error {
	_, err := collect(ch, d)
	return err
}

// collect reads until ch closes, returning what it read.
func collect[T any](ch <-chan T, d time.Duration) ([]T, error) {
	var got []T
	deadline := time.After(d)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got, nil
			}
			got = append(got, v)
		case <-deadline:
			return got, fmt.Errorf("the channel did not close within %v", d)
		}
	}
}

// ---------------------------------------------------------------------------
// Text to speech.
// ---------------------------------------------------------------------------

// TTSSubject describes one synthesis provider under test.
type TTSSubject struct {
	// New returns a fresh provider for one check.
	New func(t *testing.T) speech.TTSProvider
	// Config returns a valid configuration the provider declares support for.
	Config func() speech.TTSConfig
	// Bound is how long a call may take before it counts as blocked; zero
	// means DefaultBound.
	Bound time.Duration
}

// RunTTS runs every TTS check as a subtest of t.
func RunTTS(t *testing.T, s TTSSubject) {
	t.Helper()
	run(t, TTSChecks(t, s))
}

type ttsSuite struct {
	t     *testing.T
	s     TTSSubject
	bound time.Duration
}

// TTSChecks returns the TTS checks for s.
func TTSChecks(t *testing.T, s TTSSubject) []Check {
	c := ttsSuite{t: t, s: s, bound: bound(s.Bound)}
	return []Check{
		{"id is stable and not empty", "TTSProvider.ID returns the authored provider identifier.", c.id},
		{
			"the subject's config is one the provider declares",
			"Capabilities describes what the provider declares it can do (Languages, SampleRates).", c.config,
		},
		{
			"text yields audio, and the audio closes after CloseSend",
			"Audio yields synthesised frames. The provider closes it when synthesis ends. CloseSend signals end of text; audio may still arrive afterwards.",
			c.textYieldsAudio,
		},
		{
			"frames are owned by the receiver",
			"FRAMES DELIVERED HERE ARE OWNED BY THE RECEIVER. A provider hands over frames it will not touch again.", c.framesOwned,
		},
		{
			"audio closes after Close",
			"Close abandons the stream and releases its resources; Audio is closed when synthesis ends.", c.audioClosesAfterClose,
		},
		{"close is idempotent", "TTSStream.Close abandons the stream and releases its resources. Idempotent.", c.closeIdempotent},
	}
}

func (c ttsSuite) id() error { return stableID(c.s.New(c.t).ID) }

func (c ttsSuite) config() error {
	cfg := c.s.Config()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the subject's config is invalid: %w", err)
	}
	return declares(c.s.New(c.t).Capabilities(), cfg.Language, cfg.Format.Rate)
}

func (c ttsSuite) open() (speech.TTSStream, error) {
	st, err := c.s.New(c.t).OpenTTS(context.Background(), c.s.Config())
	if err != nil {
		return nil, fmt.Errorf("OpenTTS with a valid config: %w", err)
	}
	return st, nil
}

// synthesize sends the given chunks, each within the bound.
func (c ttsSuite) synthesize(st speech.TTSStream, chunks int) error {
	var seq uint64
	for i := 0; i < chunks; i++ {
		ch := speech.Chunk{Sequence: seq, Text: "Hello.", Terminator: '.', IsFinal: i == chunks-1}
		var serr error
		if !within(c.bound, func() { serr = st.Synthesize(ch) }) {
			return fmt.Errorf("Synthesize blocked for more than %v", c.bound)
		}
		if serr != nil {
			return fmt.Errorf("Synthesize: %w", serr)
		}
		seq++
	}
	return nil
}

// audioAfterEnd synthesises chunks, ends the text and collects the audio.
func (c ttsSuite) audioAfterEnd(st speech.TTSStream, chunks int) ([]media.Frame, error) {
	if err := c.synthesize(st, chunks); err != nil {
		return nil, err
	}
	if err := st.CloseSend(); err != nil {
		return nil, fmt.Errorf("CloseSend: %w", err)
	}
	return collect(st.Audio(), c.bound)
}

func (c ttsSuite) textYieldsAudio() (err error) {
	st, err := c.open()
	if err != nil {
		return err
	}
	defer func() { err = closeReporting(st, err) }()
	got, aerr := c.audioAfterEnd(st, 1)
	if aerr != nil {
		return aerr
	}
	if len(got) == 0 {
		return errors.New("no audio frame for a non-empty chunk")
	}
	return nil
}

func (c ttsSuite) framesOwned() (err error) {
	st, err := c.open()
	if err != nil {
		return err
	}
	defer func() { err = closeReporting(st, err) }()
	got, aerr := c.audioAfterEnd(st, 2)
	if aerr != nil {
		return aerr
	}
	return sharedPayload(got)
}

// sharedPayload reports two frames whose payloads share memory: writing into
// one must leave every other unchanged.
func sharedPayload(frames []media.Frame) error {
	for i := range frames {
		for j := range frames {
			if i == j || len(frames[i].Payload) == 0 || len(frames[j].Payload) == 0 {
				continue
			}
			before := frames[j].Payload[0]
			frames[i].Payload[0] ^= 0xFF
			changed := frames[j].Payload[0] != before
			frames[i].Payload[0] ^= 0xFF
			if changed {
				return fmt.Errorf("frames %d and %d share a payload buffer", i, j)
			}
		}
	}
	return nil
}

func (c ttsSuite) audioClosesAfterClose() error {
	st, err := c.open()
	if err != nil {
		return err
	}
	if serr := c.synthesize(st, 1); serr != nil {
		return serr
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("Close: %w", cerr)
	}
	return drain(st.Audio(), c.bound)
}

func (c ttsSuite) closeIdempotent() error {
	st, err := c.open()
	if err != nil {
		return err
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("first Close: %w", cerr)
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("second Close: %w", cerr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Language model.
// ---------------------------------------------------------------------------

// LLMSubject describes one language-model provider under test.
type LLMSubject struct {
	// New returns a fresh provider for one check.
	New func(t *testing.T) rt.Provider
	// Request returns a valid, fully resolved request.
	Request func() rt.GenerateRequest
	// ReturnBound bounds Generate's return and a whole stream's drain; zero
	// means DefaultBound.
	ReturnBound time.Duration
	// CancelBudget is ADR-0011's barge-in budget for Recv after cancellation;
	// zero means 20 ms.
	CancelBudget time.Duration
	// Slack is the scheduling allowance added to CancelBudget; zero means
	// 30 ms. Raise it for a slow local adapter, never to hide a provider that
	// ignores cancellation.
	Slack time.Duration
}

// RunLLM runs every LLM check as a subtest of t.
func RunLLM(t *testing.T, s LLMSubject) {
	t.Helper()
	run(t, LLMChecks(t, s))
}

type llmSuite struct {
	t      *testing.T
	s      LLMSubject
	bound  time.Duration
	prompt time.Duration // CancelBudget + Slack
}

// LLMChecks returns the LLM checks for s.
func LLMChecks(t *testing.T, s LLMSubject) []Check {
	budget, slack := s.CancelBudget, s.Slack
	if budget <= 0 {
		budget = 20 * time.Millisecond
	}
	if slack <= 0 {
		slack = 30 * time.Millisecond
	}
	c := llmSuite{t: t, s: s, bound: bound(s.ReturnBound), prompt: budget + slack}
	return []Check{
		{"id is stable and not empty", "Provider.ID returns the provider's stable identifier; it must not change between releases.", c.id},
		{
			"generate returns promptly and the stream ends with io.EOF",
			"Generate must return promptly. Recv returns io.EOF at normal end.", c.generatePromptly,
		},
		{
			"cancelling the context makes Recv return promptly",
			"Cancelling ctx must terminate the underlying request and cause the returned stream's Recv to return promptly (the 20 ms barge-in budget, ADR-0011).",
			c.cancellation,
		},
		{"probe respects the context deadline", "Probe must be cheap and must respect the context deadline.", c.probeDeadline},
		{
			"after Close, Generate returns ErrClosed",
			"Close releases the provider's resources. After Close, Generate must return ErrClosed.", c.closedGenerate,
		},
		{
			"safe for concurrent use",
			"Implementations must be safe for concurrent use. Stream Close is idempotent and safe to call concurrently with Recv.", c.concurrent,
		},
	}
}

func (c llmSuite) id() error { return stableID(c.s.New(c.t).ID) }

// drainTokens reads a stream to its end, which must be io.EOF.
func drainTokens(st rt.TokenStream, d time.Duration) error {
	var rerr error
	if !within(d, func() {
		for rerr == nil {
			_, rerr = st.Recv()
		}
	}) {
		return fmt.Errorf("the stream did not end within %v", d)
	}
	if !errors.Is(rerr, io.EOF) {
		return fmt.Errorf("the stream ended with %w, not io.EOF", rerr)
	}
	return nil
}

func (c llmSuite) generatePromptly() (err error) {
	p := c.s.New(c.t)
	var st rt.TokenStream
	var gerr error
	if !within(c.bound, func() { st, gerr = p.Generate(context.Background(), c.s.Request()) }) {
		return fmt.Errorf("Generate did not return within %v", c.bound)
	}
	if gerr != nil {
		return fmt.Errorf("Generate: %w", gerr)
	}
	defer func() { err = closeReporting(st, err) }()
	return drainTokens(st, c.bound)
}

func (c llmSuite) cancellation() (err error) {
	p := c.s.New(c.t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, gerr := p.Generate(ctx, c.s.Request())
	if gerr != nil {
		return fmt.Errorf("Generate: %w", gerr)
	}
	defer func() { err = closeReporting(st, err) }()
	cancel()
	if !within(c.prompt, func() { _, _ = st.Recv() }) { //nolint:errcheck // only the time to return is measured here; the result is checked below
		return fmt.Errorf("Recv after cancellation took more than %v", c.prompt)
	}
	// And the stream terminates: no chunk stream outlives its cancelled request.
	var rerr error
	if !within(c.bound, func() {
		for rerr == nil {
			_, rerr = st.Recv()
		}
	}) {
		return fmt.Errorf("the stream did not terminate within %v after cancellation", c.bound)
	}
	return nil
}

func (c llmSuite) probeDeadline() error {
	p := c.s.New(c.t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if !within(c.prompt, func() { _ = p.Probe(ctx) }) { //nolint:errcheck // only the time to return is checked: an expired deadline may or may not be an error
		return fmt.Errorf("Probe with an expired deadline took more than %v", c.prompt)
	}
	return nil
}

func (c llmSuite) closedGenerate() error {
	p := c.s.New(c.t)
	if err := p.Close(); err != nil {
		return fmt.Errorf("Close: %w", err)
	}
	st, err := p.Generate(context.Background(), c.s.Request())
	if st != nil {
		if cerr := st.Close(); cerr != nil {
			return fmt.Errorf("closing a stream returned after Close: %w", cerr)
		}
	}
	if !errors.Is(err, rt.ErrClosed) {
		return fmt.Errorf("Generate after Close returned %w, not ErrClosed", err)
	}
	return nil
}

func (c llmSuite) concurrent() error {
	p := c.s.New(c.t)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.oneConcurrent(p)
		}()
	}
	if !within(c.bound, wg.Wait) {
		return fmt.Errorf("concurrent generations did not finish within %v", c.bound)
	}
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// oneConcurrent generates, closes the stream from another goroutine while
// reading it, and closes it again: Close is idempotent and concurrency-safe.
func (c llmSuite) oneConcurrent(p rt.Provider) error {
	st, err := p.Generate(context.Background(), c.s.Request())
	if err != nil {
		return fmt.Errorf("Generate: %w", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	for {
		if _, rerr := st.Recv(); rerr != nil {
			break
		}
	}
	if cerr := <-closed; cerr != nil {
		return fmt.Errorf("concurrent Close: %w", cerr)
	}
	if cerr := st.Close(); cerr != nil {
		return fmt.Errorf("second Close: %w", cerr)
	}
	return nil
}
