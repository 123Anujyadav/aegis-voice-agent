package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// stream is one StreamingRecognize call.
//
// Two goroutines own the gRPC stream, as gRPC allows: the sender alone calls
// Send and CloseSend, the receiver alone calls Recv. Write only copies audio
// into a bounded channel, so it never waits on the network.
type stream struct {
	id            speech.ProviderID
	model         string
	languages     []speech.Language
	resultTimeout time.Duration

	cfg    speech.STTConfig
	parent context.Context // the caller's: its end is not a provider failure
	ctx    context.Context // the stream's: cancelled by Close, failure or the end
	cancel context.CancelFunc
	rpc    speechpb.Speech_StreamingRecognizeClient
	first  *speechpb.StreamingRecognizeRequest

	audio    chan []byte
	results  chan speech.TranscriptSegment
	activity chan struct{} // a response arrived; resets the result timer
	recvDone chan struct{}
	wg       sync.WaitGroup

	mu      sync.Mutex
	sentEnd bool
	closed  bool
	failure error

	// Owned by the receiver goroutine.
	seq          uint64
	lastFinalEnd time.Duration
}

var _ speech.STTStream = (*stream)(nil)

// Write queues one frame. It never blocks: the payload is copied (the frame's
// is borrowed) into a bounded queue, and a full queue is ErrBackpressure.
func (s *stream) Write(f media.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return speech.ErrSpeechSessionClosed
	case s.sentEnd:
		return fmt.Errorf("%w: audio has ended (CloseSend)", speech.ErrSpeechSessionClosed)
	case s.failure != nil:
		return s.failure
	case s.ctx.Err() != nil:
		return fmt.Errorf("%w: the stream has ended", speech.ErrSpeechSessionClosed)
	case f.Format != s.cfg.Format:
		return fmt.Errorf("%w: frame is %s, the stream takes %s", speech.ErrInvalidAudio, f.Format, s.cfg.Format)
	}
	if err := f.Validate(); err != nil {
		return fmt.Errorf("%w: %w", speech.ErrInvalidAudio, err)
	}
	payload := make([]byte, len(f.Payload))
	copy(payload, f.Payload)
	select {
	case s.audio <- payload:
		return nil
	default:
		return speech.ErrBackpressure
	}
}

// Results yields segments; it closes when the stream ends.
func (s *stream) Results() <-chan speech.TranscriptSegment { return s.results }

// CloseSend ends the audio. Queued audio is still sent, then the gRPC stream
// is half-closed, and results arrive until Google ends the stream (bounded by
// ResultTimeout). Idempotent.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return speech.ErrSpeechSessionClosed
	}
	if !s.sentEnd {
		s.sentEnd = true
		close(s.audio)
	}
	return nil
}

// Close abandons the stream: the call is cancelled, both goroutines finish,
// and Results is closed. Idempotent. It returns the failure that ended
// recognition, if one did; a caller's Close is not a failure.
func (s *stream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	return s.Err()
}

// Err reports why recognition failed, or nil.
//
// The port reports errors only from calls a caller makes. A stream Google
// ends with an error has no such call, so without this a failure would look
// like a caller who said nothing. Check it after Results closes; Close
// returns the same error.
func (s *stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// fail records the first failure. Nothing is recorded after the caller's
// Close, and a nil error (an end that is nobody's fault) records nothing.
func (s *stream) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil && !s.closed {
		s.failure = err
	}
}

// send sends the configuration, then each queued payload, then half-closes.
func (s *stream) send() {
	defer s.wg.Done()
	if err := s.rpc.Send(s.first); err != nil {
		s.sendFailed(err)
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case payload, ok := <-s.audio:
			if !ok {
				s.finish()
				return
			}
			if err := s.sendAudio(payload); err != nil {
				s.sendFailed(err)
				return
			}
		}
	}
}

// sendAudio sends one payload, one request per maxAudioPerRequest bytes.
func (s *stream) sendAudio(payload []byte) error {
	for len(payload) > 0 {
		n := min(len(payload), maxAudioPerRequest)
		req := &speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_Audio{Audio: payload[:n]},
		}
		if err := s.rpc.Send(req); err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

// sendFailed handles a Send error. An io.EOF means the server ended the
// stream, and the receiver reports why; anything else is recorded here.
func (s *stream) sendFailed(err error) {
	if !errors.Is(err, io.EOF) {
		s.fail(s.classify(err))
	}
	s.cancel()
}

// finish half-closes the stream and waits for the receiver to see its end,
// failing the stream if Google goes quiet for ResultTimeout.
func (s *stream) finish() {
	if err := s.rpc.CloseSend(); err != nil {
		s.sendFailed(err)
		return
	}
	timer := time.NewTimer(s.resultTimeout)
	defer timer.Stop()
	for {
		select {
		case <-s.recvDone:
			return
		case <-s.ctx.Done():
			return
		case <-s.activity:
			timer.Reset(s.resultTimeout)
		case <-timer.C:
			s.fail(fmt.Errorf("%w: google stt sent no end of results within %s of the end of audio",
				speech.ErrProviderTimeout, s.resultTimeout))
			s.cancel()
			return
		}
	}
}

// receive maps responses to segments until the stream ends. It closes
// Results, exactly once, on the way out.
func (s *stream) receive() {
	defer s.wg.Done()
	defer close(s.recvDone)
	defer close(s.results)
	defer s.cancel()
	for {
		resp, err := s.rpc.Recv()
		if errors.Is(err, io.EOF) {
			return // Google ended the stream cleanly
		}
		if err != nil {
			s.fail(s.classify(err))
			return
		}
		select {
		case s.activity <- struct{}{}:
		default:
		}
		for _, r := range resp.GetResults() {
			seg, ok := s.segment(r)
			if !ok {
				continue
			}
			select {
			case s.results <- seg:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

// segment maps one result. A result with no alternatives or an empty top
// transcript says nothing and is skipped.
func (s *stream) segment(r *speechpb.StreamingRecognitionResult) (speech.TranscriptSegment, bool) {
	alts := r.GetAlternatives()
	if len(alts) == 0 {
		return speech.TranscriptSegment{}, false
	}
	text := alts[0].GetTranscript()
	if strings.TrimSpace(text) == "" {
		return speech.TranscriptSegment{}, false
	}
	// Zero is Google's "not set" sentinel; it is set only on final results.
	confidence := float64(alts[0].GetConfidence())
	if confidence <= 0 || confidence > 1 {
		confidence = speech.ConfidenceUnknown
	}
	// A result spans from the end of the last final result to its own end.
	start := s.lastFinalEnd
	end := r.GetResultEndOffset().AsDuration()
	if end < start {
		end = start
	}
	if r.GetIsFinal() {
		s.lastFinalEnd = end
	}
	s.seq++
	return speech.TranscriptSegment{
		Session:    s.cfg.Session,
		Turn:       s.cfg.Turn,
		Segment:    speech.NewSegmentID(),
		Sequence:   s.seq,
		Text:       text,
		IsFinal:    r.GetIsFinal(),
		Confidence: confidence,
		StartTime:  start,
		EndTime:    end,
		Language:   s.language(r.GetLanguageCode()),
		Role:       speech.RoleCaller,
		Meta:       speech.ProviderMeta{Provider: s.id, Model: s.model},
	}, true
}

// language maps Google's detected tag (which it may lower-case) to a declared
// language, so a vendor string never becomes a Language; anything else is the
// stream's own language.
func (s *stream) language(code string) speech.Language {
	for _, l := range s.languages {
		if strings.EqualFold(string(l), code) {
			return l
		}
	}
	return s.cfg.Language
}

// classify maps an error that ended the call. A nil result means it is
// nobody's failure: the caller cancelled (or its deadline passed), or this
// stream cancelled itself after recording why.
func (s *stream) classify(err error) error { return s.classifyCode(status.Code(err)) }

func (s *stream) classifyCode(code codes.Code) error {
	callerEnded := ended(s.parent)
	streamTimedOut := timedOut(s.ctx)
	selfCancelled := code == codes.Canceled && ended(s.ctx)
	switch {
	case callerEnded, selfCancelled && !streamTimedOut:
		return nil
	case streamTimedOut:
		return fmt.Errorf("%w: google stt: the stream's Timeout (%s) passed", speech.ErrProviderTimeout, s.cfg.Timeout)
	}
	return statusError(code)
}

func ended(ctx context.Context) bool { return ctx.Err() != nil }

func timedOut(ctx context.Context) bool { return errors.Is(ctx.Err(), context.DeadlineExceeded) }

// statusError maps a gRPC status by its code alone. Google's message is never
// quoted: it can echo the audio or the transcript.
func statusError(code codes.Code) error {
	switch code {
	case codes.Unauthenticated, codes.PermissionDenied:
		// No credentials sentinel exists in the port; to a router a refused
		// credential is a provider it cannot use.
		return fmt.Errorf("%w: google stt refused the credentials (%s)", speech.ErrProviderUnavailable, code)
	case codes.ResourceExhausted:
		return fmt.Errorf("%w: google stt (%s)", speech.ErrProviderRateLimited, code)
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: google stt (%s)", speech.ErrProviderTimeout, code)
	case codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.OutOfRange,
		codes.Unimplemented, codes.AlreadyExists:
		return fmt.Errorf("%w: google stt rejected the request (%s)", speech.ErrInternalFailure, code)
	}
	return fmt.Errorf("%w: google stt stream failed (%s)", speech.ErrProviderUnavailable, code)
}
