package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// stream reads the Messages API's server-sent events.
//
// Close may run concurrently with Recv (the dispatcher relies on it): it
// cancels the request and closes the body without taking Recv's lock, so a
// Recv blocked on the network returns promptly.
type stream struct {
	provider rt.ProviderID
	model    rt.ModelID
	body     io.ReadCloser
	cancel   context.CancelFunc
	ctx      context.Context
	scanner  *bufio.Scanner

	closeMu sync.Mutex
	closed  bool

	mu      sync.Mutex // serialises Recv
	index   int
	done    bool
	pending []rt.Chunk
	usage   rt.Usage
	refused bool
}

var _ rt.TokenStream = (*stream)(nil)

func newStream(ctx context.Context, provider rt.ProviderID, model rt.ModelID, body io.ReadCloser,
	cancel context.CancelFunc, maxEvent int,
) *stream {
	s := &stream{provider: provider, model: model, body: body, cancel: cancel, ctx: ctx}
	s.scanner = bufio.NewScanner(body)
	s.scanner.Buffer(make([]byte, 0, 8<<10), maxEvent)
	return s
}

type wireEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage wireUsage `json:"usage"`
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

type wireUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

func (s *stream) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}

// Recv returns the next chunk, io.EOF after a clean end, or an error.
func (s *stream) Recv() (rt.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed() {
		return rt.Chunk{}, rt.ErrClosed
	}
	if len(s.pending) > 0 {
		c := s.pending[0]
		s.pending = s.pending[1:]
		return s.number(c), nil
	}
	if s.done {
		if s.refused {
			return rt.Chunk{}, s.fail(rt.KindContentFiltered, ErrRefused)
		}
		return rt.Chunk{}, io.EOF
	}
	for {
		data, err := s.nextData()
		if err != nil {
			return rt.Chunk{}, err
		}
		c, ok, err := s.handle(data)
		if err != nil {
			return rt.Chunk{}, err
		}
		if ok {
			return s.number(c), nil
		}
		if len(s.pending) > 0 {
			c := s.pending[0]
			s.pending = s.pending[1:]
			return s.number(c), nil
		}
		if s.done {
			if s.refused {
				return rt.Chunk{}, s.fail(rt.KindContentFiltered, ErrRefused)
			}
			return rt.Chunk{}, io.EOF
		}
	}
}

// nextData returns the payload of the next "data:" line. Event-name lines,
// comments and blank lines carry nothing the JSON type field does not.
func (s *stream) nextData() ([]byte, error) {
	for s.scanner.Scan() {
		line := s.scanner.Bytes()
		if after, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			return bytes.TrimSpace(after), nil
		}
	}
	return nil, s.scanErr()
}

// handle applies one event. It returns a chunk to deliver now, or ok=false.
func (s *stream) handle(data []byte) (rt.Chunk, bool, error) {
	var ev wireEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return rt.Chunk{}, false, s.fail(rt.KindTransport, fmt.Errorf(
			"%w: %d bytes of event data did not parse (content not quoted)", ErrMalformedResponse, len(data)))
	}
	switch ev.Type {
	case "message_start":
		s.usage.InputTokens = ev.Message.Usage.InputTokens
		s.usage.CachedInputTokens = ev.Message.Usage.CacheReadInputTokens
		s.usage.OutputTokens = ev.Message.Usage.OutputTokens
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				return rt.Chunk{Kind: rt.ChunkText, Text: ev.Delta.Text}, true, nil
			}
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				return rt.Chunk{Kind: rt.ChunkThinking, Text: ev.Delta.Thinking}, true, nil
			}
		}
	case "message_delta":
		if ev.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = ev.Usage.OutputTokens // cumulative on the wire
		}
		if ev.Delta.StopReason == "refusal" {
			s.refused = true
		}
	case "message_stop":
		s.done = true
		if s.refused {
			return rt.Chunk{}, false, nil
		}
		// Usage before Done: the runtime accumulates usage and treats Done as
		// the end.
		s.pending = append(s.pending, rt.Chunk{Kind: rt.ChunkUsage, Usage: s.usage}, rt.Chunk{Kind: rt.ChunkDone})
	case "error":
		kind, ok := kindForType(ev.Error.Type)
		if !ok {
			kind = rt.KindTransport
		}
		return rt.Chunk{}, false, s.fail(kind, fmt.Errorf("anthropic: stream error (%s)", typeOrUnknown(ev.Error.Type)))
	}
	// ping, content_block_start, content_block_stop, signature and tool deltas:
	// nothing to deliver.
	return rt.Chunk{}, false, nil
}

func (s *stream) number(c rt.Chunk) rt.Chunk {
	c.Index = s.index
	s.index++
	return c
}

func (s *stream) scanErr() error {
	err := s.scanner.Err()
	switch {
	case errors.Is(s.ctx.Err(), context.Canceled):
		// The caller's decision, not a provider fault.
		return context.Canceled
	case errors.Is(s.ctx.Err(), context.DeadlineExceeded):
		return s.fail(rt.KindTimeout, fmt.Errorf("anthropic: %w", context.DeadlineExceeded))
	case err == nil:
		return s.fail(rt.KindTransport, ErrTruncated)
	case errors.Is(err, bufio.ErrTooLong):
		return s.fail(rt.KindTransport, fmt.Errorf("%w: an event exceeded MaxEventBytes", ErrMalformedResponse))
	}
	return s.fail(rt.KindTransport, fmt.Errorf("anthropic: reading the stream: %w", err))
}

func (s *stream) fail(kind rt.ProviderErrorKind, err error) error {
	return &rt.ProviderError{Provider: s.provider, Model: s.model, Kind: kind, Err: err}
}

// Close cancels the request and releases the body. Idempotent, and safe to
// call while Recv is blocked.
func (s *stream) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	s.closeMu.Unlock()
	s.cancel()
	return s.body.Close()
}
