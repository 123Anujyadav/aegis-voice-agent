package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/callscreen/callscreen-platform/packages/go/metrics"
	"github.com/callscreen/callscreen-platform/packages/go/metricsexport"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// MetricRequests counts every response this API decides, by endpoint and
// outcome. It is subsystem-prefixed, as metricsexport expects (ADR-0013), and
// mirrors edge-api's edge_api_auth_requests_total: together they make every
// authentication refusal in the platform observable.
//
// WHY A COUNTER AND NOT A LOG LINE: refusals (400/401/429) are logged nowhere,
// only 5xx are. A log line per refusal would let any unauthenticated client
// turn request volume into log volume; a counter costs the same at any rate
// and needs no request detail.
const MetricRequests = "identity_auth_requests_total"

// Endpoint label values. The vocabulary is closed: the three routes, and
// EndpointUnknown for any other path, which is never itself used as a label
// (a path can carry anything a client sends).
const (
	EndpointOTPRequest  = "otp_request"
	EndpointOTPValidate = "otp_validate"
	EndpointRefresh     = "token_refresh"
	EndpointUnknown     = "unknown"
)

// OutcomeSuccess is a request that received its 2xx response. Every other
// outcome is the response's problem code, lower-cased without its "ERR_"
// prefix (for example invalid_otp, rate_limited): a closed set, because
// problem() only ever emits a code from the problems table.
const OutcomeSuccess = "success"

// Metrics is the identity API's instrument set.
type Metrics struct {
	reg      *metrics.Registry
	requests *metrics.Counter
}

// NewMetrics registers the API's instruments in a fresh registry.
func NewMetrics() *Metrics {
	reg := metrics.NewRegistry()
	return &Metrics{reg: reg, requests: reg.Counter(MetricRequests, "endpoint", "outcome")}
}

// Registry returns the registry to expose, for example through
// metricsexport.Source.
func (m *Metrics) Registry() *metrics.Registry { return m.reg }

// MetricsMount is what accepts the exposition handler: *platform.Service,
// which serves it at GET /metrics on the health listener (ADR-0013), never on
// the application port.
type MetricsMount interface {
	SetMetricsHandler(http.Handler)
}

// MountOn exposes m's registry on target as subsystem's metrics. Keeping this
// here keeps net/http out of cmd/server, whose only HTTP surface is New.
func (m *Metrics) MountOn(target MetricsMount, subsystem string) {
	target.SetMetricsHandler(metricsexport.New(metricsexport.Source{
		Subsystem: subsystem,
		Registry:  m.reg,
	}).Handler())
}

// endpointFor maps a request path to its endpoint label.
func endpointFor(path string) string {
	switch path {
	case routes.OTPRequest:
		return EndpointOTPRequest
	case routes.Enrollment:
		return EndpointOTPValidate
	case routes.Refresh:
		return EndpointRefresh
	default:
		return EndpointUnknown
	}
}

// outcomeFor maps a problem code to its outcome label.
func outcomeFor(code string) string {
	return strings.ToLower(strings.TrimPrefix(code, "ERR_"))
}

// decision ensures a request is counted at most once, whichever of ok,
// problem or the recoverer answers it. A response is written once, so this is
// a backstop: a panic after a response was written must not count it again.
type decision struct{ counted atomic.Bool }

type decisionKey struct{}

// withDecision gives the request a decision record that record reads.
func withDecision(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), decisionKey{}, &decision{})))
	})
}

// record counts the request's outcome, once. A nil m counts nothing.
func (m *Metrics) record(r *http.Request, outcome string) {
	if m == nil {
		return
	}
	if d, ok := r.Context().Value(decisionKey{}).(*decision); ok && !d.counted.CompareAndSwap(false, true) {
		return
	}
	m.requests.Inc(endpointFor(r.URL.Path), outcome)
}
