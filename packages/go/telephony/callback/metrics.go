package callback

import (
	"context"
	"errors"
	"log/slog"
	"time"

	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Phase 19 P1-13 — callback observability.
//
// # Two layers, two names, no double counting
//
//   - telephony_callback_http_requests_total / _http_rejected_total: the HTTP
//     handler. Every request it receives, and the ones it refuses BEFORE
//     admission (method, path, header, content type, body).
//   - telephony_callback_admissions_total / _admission_seconds: Admission.Admit.
//     Exactly one outcome per Admit call, whoever called it.
//
// A request refused by the handler is never an admission; a request that
// reaches Admit is counted once there. So http_requests = http_rejected +
// admissions, for requests that came through the handler.
//
// # Bounded labels only
//
// Labels are a provider and one word from a fixed vocabulary. The provider
// label is a CONFIGURED provider: the handler's route provider, or — inside
// Admit — the request's provider only if an adapter is configured for it,
// otherwise "unconfigured". Event IDs, call IDs, URLs, query strings, phone
// numbers, error text and payload content are never labels.
//
// # Instrumentation never fails a callback
//
// Every call into a [Recorder] is guarded: a panic inside it is recovered and
// ignored, so a broken metrics backend cannot change a response. A nil
// Recorder is a no-op.

// Admission outcomes: the complete vocabulary of the "outcome" label.
const (
	OutcomeLabelApplied          = "applied"
	OutcomeLabelAlreadySatisfied = "already_satisfied"
	OutcomeLabelStale            = "stale"
	OutcomeLabelConflictRecorded = "conflict_recorded"
	OutcomeLabelDuplicate        = "duplicate"
	OutcomeLabelInFlight         = "in_flight"
	OutcomeLabelMalformedRequest = "malformed_request"
	OutcomeLabelNoVerifier       = "no_verifier"
	OutcomeLabelUnauthenticated  = "unauthenticated"
	OutcomeLabelNoNormalizer     = "no_normalizer"
	OutcomeLabelInvalidEvent     = "invalid_event"
	OutcomeLabelProviderMismatch = "provider_mismatch"
	OutcomeLabelUnknownCall      = "unknown_call"
	OutcomeLabelEventIDReused    = "event_id_reused"
	OutcomeLabelStoreUnavailable = "store_unavailable"
	OutcomeLabelTimeout          = "timeout"
	OutcomeLabelCancelled        = "cancelled"
	OutcomeLabelInternalError    = "internal_error"
)

// AdmissionOutcomeLabels returns every admission outcome label.
func AdmissionOutcomeLabels() []string {
	return []string{
		OutcomeLabelApplied, OutcomeLabelAlreadySatisfied, OutcomeLabelStale,
		OutcomeLabelConflictRecorded, OutcomeLabelDuplicate, OutcomeLabelInFlight,
		OutcomeLabelMalformedRequest, OutcomeLabelNoVerifier, OutcomeLabelUnauthenticated,
		OutcomeLabelNoNormalizer, OutcomeLabelInvalidEvent, OutcomeLabelProviderMismatch,
		OutcomeLabelUnknownCall, OutcomeLabelEventIDReused, OutcomeLabelStoreUnavailable,
		OutcomeLabelTimeout, OutcomeLabelCancelled, OutcomeLabelInternalError,
	}
}

// HTTP rejection reasons: the complete vocabulary of the "reason" label.
const (
	RejectMethod      = "method"
	RejectPath        = "path"
	RejectHeader      = "header"
	RejectContentType = "content_type"
	RejectBodySize    = "body_size"
	RejectEmptyBody   = "empty_body"
	RejectBodyRead    = "body_read"
	RejectTimeout     = "timeout"
)

// RejectionReasons returns every HTTP rejection reason label.
func RejectionReasons() []string {
	return []string{RejectMethod, RejectPath, RejectHeader, RejectContentType,
		RejectBodySize, RejectEmptyBody, RejectBodyRead, RejectTimeout}
}

// unconfiguredProvider labels an admission for a provider with no adapter.
const unconfiguredProvider = "unconfigured"

// outcomeLabel classifies one Admit result. It is the single classification
// used by both the metrics and the HTTP handler, so the two cannot disagree.
// Order matters: a timeout or an outage wins over any other wrapped sentinel.
func outcomeLabel(res Result, err error) string {
	if err == nil {
		switch res.Outcome {
		case OutcomeApplied:
			return OutcomeLabelApplied
		case OutcomeAlreadySatisfied:
			return OutcomeLabelAlreadySatisfied
		case OutcomeStale:
			return OutcomeLabelStale
		case OutcomeConflict:
			return OutcomeLabelConflictRecorded
		case OutcomeDuplicate:
			return OutcomeLabelDuplicate
		case OutcomeInFlight:
			return OutcomeLabelInFlight
		default:
			return OutcomeLabelInternalError
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeLabelTimeout
	case errors.Is(err, context.Canceled):
		return OutcomeLabelCancelled
	case errors.Is(err, ErrStoreUnavailable):
		return OutcomeLabelStoreUnavailable
	case errors.Is(err, ErrMalformedRequest):
		return OutcomeLabelMalformedRequest
	case errors.Is(err, ErrInvalidEvent):
		return OutcomeLabelInvalidEvent
	case errors.Is(err, ErrProviderMismatch):
		return OutcomeLabelProviderMismatch
	case errors.Is(err, ErrNoVerifier):
		return OutcomeLabelNoVerifier
	case errors.Is(err, ErrUnauthenticated):
		return OutcomeLabelUnauthenticated
	case errors.Is(err, ErrEventIDReused):
		return OutcomeLabelEventIDReused
	case errors.Is(err, ErrUnknownCall):
		return OutcomeLabelUnknownCall
	case errors.Is(err, ErrNoNormalizer):
		return OutcomeLabelNoNormalizer
	default:
		return OutcomeLabelInternalError
	}
}

// Recorder receives callback observations. Every argument is a configured
// provider or a word from the vocabularies above.
type Recorder interface {
	HTTPRequest(provider string)
	HTTPRejected(provider, reason string)
	Admission(provider, outcome string, elapsed time.Duration)
}

// Metrics is the callback instrument set, on its own scoped registry.
type Metrics struct {
	Requests         *metrics.Counter   // provider
	Rejections       *metrics.Counter   // provider, reason
	Admissions       *metrics.Counter   // provider, outcome
	AdmissionLatency *metrics.Histogram // provider

	reg *metrics.Registry
}

var _ Recorder = (*Metrics)(nil)

// NewMetrics builds the instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}
	// Admission is in-process work plus, with durable stores, a few network
	// round trips: milliseconds, with a tail bounded by the handler timeout.
	admissionSecs := []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	m.Requests = m.reg.Counter("telephony_callback_http_requests_total", "provider")
	m.Rejections = m.reg.Counter("telephony_callback_http_rejected_total", "provider", "reason")
	m.Admissions = m.reg.Counter("telephony_callback_admissions_total", "provider", "outcome")
	m.AdmissionLatency = m.reg.Histogram("telephony_callback_admission_seconds", admissionSecs, "provider")
	return m
}

// Registry exposes the registry for an exporter.
func (m *Metrics) Registry() *metrics.Registry { return m.reg }

// HTTPRequest implements [Recorder].
func (m *Metrics) HTTPRequest(provider string) { m.Requests.Inc(provider) }

// HTTPRejected implements [Recorder].
func (m *Metrics) HTTPRejected(provider, reason string) { m.Rejections.Inc(provider, reason) }

// Admission implements [Recorder].
func (m *Metrics) Admission(provider, outcome string, elapsed time.Duration) {
	m.Admissions.Inc(provider, outcome)
	m.AdmissionLatency.ObserveDuration(elapsed, provider)
}

// guarded calls f, containing any panic: instrumentation must never fail a
// callback.
func guarded(f func()) {
	defer func() { _ = recover() }()
	f()
}

// logOutcome writes one structured line for the outcomes an operator must act
// on — durable state unavailable, or an internal error — and nothing else.
// Attributes are the stage, the outcome word and the configured provider
// label: never error text, identifiers, URLs, headers or payload.
func logOutcome(l *slog.Logger, provider, outcome string) {
	if l == nil {
		return
	}
	switch outcome {
	case OutcomeLabelStoreUnavailable, OutcomeLabelInternalError:
		guarded(func() {
			l.Warn("callback admission failed",
				slog.String("stage", "admission"),
				slog.String("outcome", outcome),
				slog.String("provider", provider))
		})
	}
}
