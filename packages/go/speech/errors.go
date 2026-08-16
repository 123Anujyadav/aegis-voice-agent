package speech

import (
	"errors"
	"fmt"
	"strings"
)

// The typed error set.
//
// # Control flow uses errors.Is, never a string
//
// A speech pipeline fails in a dozen distinguishable ways, and the caller's
// correct response differs for each: a rate limit should back off, a circuit
// open should route elsewhere immediately, backpressure should slow the
// producer, and a cancelled stream should do nothing at all. Free-form error
// strings collapse those into one bucket and force callers to match on prose
// that changes the next time someone improves a message.
var (
	// ErrProviderUnavailable means no provider could serve the request. The
	// language was supported; nothing healthy was left.
	ErrProviderUnavailable = errors.New("speech: provider unavailable")

	// ErrProviderTimeout means a provider exceeded its deadline.
	ErrProviderTimeout = errors.New("speech: provider timeout")

	// ErrProviderRateLimited means a provider refused for rate reasons.
	ErrProviderRateLimited = errors.New("speech: provider rate limited")

	// ErrProviderCircuitOpen means the breaker refused before trying.
	//
	// Distinct from ErrProviderUnavailable on purpose: this one says we chose
	// not to spend the latency budget discovering a known-down provider is
	// still down.
	ErrProviderCircuitOpen = errors.New("speech: provider circuit open")

	// ErrInvalidAudio means a frame failed validation or carried the wrong
	// format for the session.
	ErrInvalidAudio = errors.New("speech: invalid audio")

	// ErrInvalidTranscript means a transcript segment was malformed.
	ErrInvalidTranscript = errors.New("speech: invalid transcript")

	// ErrTranscriptOutOfOrder means a segment arrived behind the committed
	// sequence and cannot be applied.
	ErrTranscriptOutOfOrder = errors.New("speech: transcript out of order")

	// ErrSpeechSessionClosed means the session is no longer accepting work.
	ErrSpeechSessionClosed = errors.New("speech: session closed")

	// ErrSTTCancelled means recognition was cancelled.
	ErrSTTCancelled = errors.New("speech: stt cancelled")

	// ErrTTSCancelled means synthesis was cancelled — most often by barge-in.
	ErrTTSCancelled = errors.New("speech: tts cancelled")

	// ErrBackpressure means a bounded queue refused work.
	//
	// NOT A FAULT. The caller is being told to slow down, which is the queue
	// doing its job. Treating this as an error to be retried immediately is the
	// classic way to turn backpressure into a livelock.
	ErrBackpressure = errors.New("speech: backpressure")

	// ErrUnsupportedLanguage means no registered provider declares the
	// language.
	//
	// Deliberately distinct from ErrProviderUnavailable: an operator needs to
	// tell "nobody speaks Tamil" from "everything is down", and those have
	// entirely different fixes.
	ErrUnsupportedLanguage = errors.New("speech: unsupported language")

	// ErrInternalFailure is the last resort. Anything returning this without a
	// wrapped cause is a bug in this package.
	ErrInternalFailure = errors.New("speech: internal failure")
)

// ConfigError reports every problem with a configuration at once.
//
// Returning the first problem only means an operator fixes one line, restarts,
// waits, and learns about the next one — a loop that takes as many restarts as
// there are mistakes.
type ConfigError struct{ Problems []string }

// Error implements error.
func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}
	return fmt.Sprintf("%d configuration problems: %s",
		len(e.Problems), strings.Join(e.Problems, "; "))
}

// invariant builds an internal-failure error naming the invariant that broke.
//
// Used where a condition should be impossible given the surrounding code. It
// carries the identifier so a report names the invariant rather than a line
// number that moves.
func invariant(id, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInternalFailure, id, fmt.Sprintf(format, args...))
}
