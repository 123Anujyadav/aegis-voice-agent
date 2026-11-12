package deepgram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// resultBuffer bounds segments received but not yet read.
const resultBuffer = 64

// maxFrameDuration bounds one written frame, so that MaxPendingFrames bounds
// the queue's memory as well as its length.
const maxFrameDuration = time.Second

// maxOffset bounds a reported segment offset; larger is malformed.
const maxOffset = 24 * time.Hour

// closeStream is the message that ends the audio: Deepgram flushes its final
// results and then closes the connection.
var closeStream = []byte(`{"type":"CloseStream"}`)

// stream is one recognition session over one WebSocket.
//
// Two goroutines: the sender (queued audio to the socket, then
// CloseStream, then the result watchdog) and the receiver (socket to Results).
// The receiver is the only writer of results and closes it exactly once.
type stream struct {
	p    *Provider
	cfg  speech.STTConfig
	conn *websocket.Conn

	ctx  context.Context
	stop func()

	frames   chan []byte
	results  chan speech.TranscriptSegment
	activity chan struct{}
	wg       sync.WaitGroup

	mu      sync.Mutex
	sentEnd bool // no more Write: CloseSend or Close
	closed  bool // Close was called
	ended   bool // CloseStream was sent
	failure error
	seq     uint64
}

var _ speech.STTStream = (*stream)(nil)

func newStream(ctx context.Context, p *Provider, cfg speech.STTConfig, conn *websocket.Conn, stop func()) *stream {
	s := &stream{
		p: p, cfg: cfg, conn: conn, ctx: ctx, stop: stop,
		frames:   make(chan []byte, p.cfg.MaxPendingFrames),
		results:  make(chan speech.TranscriptSegment, resultBuffer),
		activity: make(chan struct{}, 1),
	}
	s.wg.Add(2)
	go s.send()
	go s.receive()
	return s
}

// Write queues a copy of the frame's audio. It never blocks: a full queue is
// speech.ErrBackpressure.
func (s *stream) Write(f media.Frame) error {
	if f.Format != s.cfg.Format {
		return fmt.Errorf("%w: the frame is %s, the stream is %s", speech.ErrInvalidAudio, f.Format, s.cfg.Format)
	}
	if err := f.Validate(); err != nil {
		return fmt.Errorf("%w: %w", speech.ErrInvalidAudio, err)
	}
	if len(f.Payload) > s.cfg.Format.BytesFor(maxFrameDuration) {
		return fmt.Errorf("%w: a frame is longer than %v", speech.ErrInvalidAudio, maxFrameDuration)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return speech.ErrSpeechSessionClosed
	case s.sentEnd:
		return fmt.Errorf("%w: audio has ended (CloseSend)", speech.ErrSpeechSessionClosed)
	case s.ctx.Err() != nil:
		return fmt.Errorf("%w: the stream has ended", speech.ErrSpeechSessionClosed)
	}
	payload := append([]byte(nil), f.Payload...) // the caller's buffer is borrowed
	select {
	case s.frames <- payload:
		return nil
	default:
		return speech.ErrBackpressure
	}
}

// Results yields interim and final segments. It closes when the stream ends:
// the server closed it, it failed, or Close was called.
func (s *stream) Results() <-chan speech.TranscriptSegment { return s.results }

// CloseSend ends the audio. Queued audio is still sent, then CloseStream;
// results arrive until Deepgram closes the connection. Idempotent.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sentEnd {
		return nil
	}
	s.sentEnd = true
	close(s.frames)
	return nil
}

// Close abandons the stream: queued audio is dropped, the socket is closed
// without waiting for the server, and Results closes. Idempotent. It returns
// the failure that ended the stream, if one did; a caller's Close is not a
// failure.
func (s *stream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if !s.sentEnd {
			s.sentEnd = true
			close(s.frames)
		}
	}
	s.mu.Unlock()
	s.halt()
	s.wg.Wait()
	return s.Err()
}

// Err reports why the stream failed, or nil. The port reports errors only from
// calls a caller makes; a dropped connection has no such call, so without this
// a failure would look like silence.
func (s *stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// fail records the first failure. After Close nothing is recorded: what
// follows a caller's Close is the caller's doing.
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

// halt stops every goroutine: the context is cancelled and the socket closed
// at once, without a close handshake.
func (s *stream) halt() {
	s.stop()
	_ = s.conn.CloseNow() //nolint:errcheck // closing twice reports net.ErrClosed, which is the point
}

// send writes queued audio, then CloseStream, then watches for results.
func (s *stream) send() {
	defer s.wg.Done()
	for payload := range s.frames {
		if err := s.conn.Write(s.ctx, websocket.MessageBinary, payload); err != nil {
			s.writeFailed()
			return
		}
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || s.ctx.Err() != nil {
		return
	}
	if err := s.conn.Write(s.ctx, websocket.MessageText, closeStream); err != nil {
		s.writeFailed()
		return
	}
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	s.watch()
}

// writeFailed closes the socket and leaves the classification to the
// receiver, which then fails to read: it sees the server's close code if one
// arrived (a write usually fails because the server closed), the caller's
// cancellation, or a lost connection. One place classifies, so a rate-limit
// close is not reported as a generic send failure.
func (s *stream) writeFailed() {
	_ = s.conn.CloseNow() //nolint:errcheck // the receiver reports why
}

// watch fails the stream when nothing arrives for ResultTimeout after the end
// of audio was sent. Deepgram answers CloseStream with its finals and then
// closes; a server that does neither would otherwise hold the stream open.
func (s *stream) watch() {
	select { // activity before CloseStream does not count
	case <-s.activity:
	default:
	}
	timer := time.NewTimer(s.p.cfg.ResultTimeout)
	defer timer.Stop()
	for {
		select {
		case <-s.activity:
			// Safe without draining since Go 1.23: Reset discards a pending
			// expiry.
			timer.Reset(s.p.cfg.ResultTimeout)
		case <-timer.C:
			s.fail(fmt.Errorf("%w: deepgram: no result within %v of the end of audio",
				speech.ErrProviderTimeout, s.p.cfg.ResultTimeout))
			s.halt()
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// receive reads messages until the stream ends, then closes Results.
func (s *stream) receive() {
	defer s.wg.Done()
	defer close(s.results)
	defer s.halt() // the stream is over: release the sender and the socket
	for {
		typ, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.fail(s.readFailure(err))
			return
		}
		select {
		case s.activity <- struct{}{}:
		default:
		}
		if typ != websocket.MessageText {
			s.fail(fmt.Errorf("%w: deepgram sent an unexpected binary message", speech.ErrInternalFailure))
			return
		}
		seg, ok, err := s.parse(data)
		if err != nil {
			s.fail(err)
			return
		}
		if !ok {
			continue
		}
		select {
		case s.results <- seg:
		case <-s.ctx.Done():
			s.fail(contextFailure(s.ctx))
			return
		}
	}
}

// readFailure classifies why reading ended; nil is a clean end. A close
// reason is provider text and is never quoted: only the code is reported.
func (s *stream) readFailure(err error) error {
	if s.ctx.Err() != nil {
		return contextFailure(s.ctx)
	}
	code := websocket.CloseStatus(err)
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	switch {
	case code == websocket.StatusNormalClosure && ended:
		return nil
	case code == websocket.StatusNormalClosure:
		return fmt.Errorf("%w: deepgram closed the stream before the end of audio", speech.ErrProviderUnavailable)
	case code == websocket.StatusTryAgainLater:
		return fmt.Errorf("%w: deepgram closed the stream with code %d", speech.ErrProviderRateLimited, int(code))
	case code >= 0:
		return fmt.Errorf("%w: deepgram closed the stream with code %d", speech.ErrProviderUnavailable, int(code))
	case errors.Is(err, websocket.ErrMessageTooBig):
		return fmt.Errorf("%w: deepgram sent a message larger than %d bytes", speech.ErrInternalFailure, s.p.cfg.MaxMessageBytes)
	}
	return fmt.Errorf("%w: deepgram: the connection was lost", speech.ErrProviderUnavailable)
}

// message is the part of a Deepgram message the adapter reads. Every other
// field (speech_final, from_finalize, words, metadata) is ignored: endpointing
// is ours (ADR-0005).
type message struct {
	IsFinal  *bool    `json:"is_final"`
	Start    *float64 `json:"start"`
	Duration *float64 `json:"duration"`
	Channel  *struct {
		Alternatives []struct {
			Transcript string   `json:"transcript"`
			Confidence *float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"channel"`
}

// errMalformed is the one description of a message the adapter cannot use.
// Neither the payload nor the decoder's error (which can quote it) is
// included.
func errMalformed(what string) error {
	return fmt.Errorf("%w: deepgram sent a malformed message (%s)", speech.ErrInternalFailure, what)
}

// parse maps one message. It reports !ok for a message that carries no
// segment: metadata, speech events, and empty transcripts.
func (s *stream) parse(data []byte) (seg speech.TranscriptSegment, ok bool, err error) {
	// The type first: the other messages give "channel" another shape (an
	// array of channel indices in SpeechStarted and UtteranceEnd).
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &kind) != nil {
		return seg, false, errMalformed("not a JSON object with a type")
	}
	switch kind.Type {
	case "Results":
	case "Metadata", "SpeechStarted", "UtteranceEnd":
		return seg, false, nil
	default:
		return seg, false, fmt.Errorf("%w: deepgram sent an unexpected message type", speech.ErrInternalFailure)
	}
	var m message
	if json.Unmarshal(data, &m) != nil {
		return seg, false, errMalformed("a result of the wrong shape")
	}
	if m.Channel == nil || m.IsFinal == nil {
		return seg, false, errMalformed("a result without channel or is_final")
	}
	if len(m.Channel.Alternatives) == 0 || m.Channel.Alternatives[0].Transcript == "" {
		return seg, false, nil // nothing was said
	}
	alt := m.Channel.Alternatives[0]
	confidence := speech.ConfidenceUnknown
	if alt.Confidence != nil {
		confidence = *alt.Confidence
		if confidence < 0 || confidence > 1 {
			return seg, false, errMalformed("confidence outside 0..1")
		}
	}
	start, okStart := offset(m.Start)
	duration, okDuration := offset(m.Duration)
	if !okStart || !okDuration {
		return seg, false, errMalformed("an offset outside 0..24h")
	}

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	seg = speech.TranscriptSegment{
		Session:    s.cfg.Session,
		Turn:       s.cfg.Turn,
		Segment:    speech.NewSegmentID(),
		Sequence:   seq,
		Text:       alt.Transcript,
		IsFinal:    *m.IsFinal,
		Confidence: confidence,
		StartTime:  start,
		EndTime:    start + duration,
		Language:   s.cfg.Language,
		Role:       speech.RoleCaller,
		Meta: speech.ProviderMeta{
			Provider: s.p.cfg.ID,
			Model:    s.p.cfg.Model,
		},
	}
	if seg.Validate() != nil {
		return seg, false, errMalformed("an invalid segment")
	}
	return seg, true, nil
}

// offset converts seconds to a duration; a missing value is zero.
func offset(seconds *float64) (time.Duration, bool) {
	if seconds == nil {
		return 0, true
	}
	if *seconds < 0 || *seconds > maxOffset.Seconds() {
		return 0, false
	}
	return time.Duration(*seconds * float64(time.Second)), true
}
