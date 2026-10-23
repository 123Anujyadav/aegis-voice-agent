package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/callscreen/callscreen-platform/packages/go/metrics"
	"github.com/callscreen/callscreen-platform/packages/go/metricsexport"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
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

// MetricSecurityEvents counts identity security audit events by type: wrong
// OTP codes, lockouts, and refresh-token reuse (the exfiltration alarm,
// ADR-0010 §9). Metrics implements app.SecurityAuditor, so these are the same
// events the audit records, counted once each; the label is the event type and
// nothing else (no session, request or subscriber id).
const MetricSecurityEvents = "identity_security_events_total"

// SecurityEventOther is the label for an event type this counter does not
// know, so a new type can never add an unbounded label value.
const SecurityEventOther = "other"

// MetricIntegrityOutcomes counts Play Integrity verifications by outcome, and
// MetricIntegrityRestrictions counts the reasons a verdict was restricted:
// ADR-0010 §9's "Play Integrity verdict distribution". No verifier is
// configured today, so in production every call is "unavailable".
const (
	MetricIntegrityOutcomes     = "identity_play_integrity_outcomes_total"
	MetricIntegrityRestrictions = "identity_play_integrity_restrictions_total"
)

// MetricOtpDeliveries counts OTP delivery attempts by provider and outcome:
// ADR-0010 §9's "OTP send failure rate", per provider. Provider names are the
// short labels otpdelivery.New validated; anything else counts as
// ProviderOther. No provider is configured today.
const MetricOtpDeliveries = "identity_otp_deliveries_total"

// ProviderOther is the provider label for a name that is not a valid
// otpdelivery provider name.
const ProviderOther = "other"

var providerLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

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
	reg          *metrics.Registry
	requests     *metrics.Counter
	security     *metrics.Counter
	integrity    *metrics.Counter
	restrictions *metrics.Counter
	deliveries   *metrics.Counter
}

// NewMetrics registers the API's instruments in a fresh registry.
func NewMetrics() *Metrics {
	reg := metrics.NewRegistry()
	return &Metrics{
		reg:          reg,
		requests:     reg.Counter(MetricRequests, "endpoint", "outcome"),
		security:     reg.Counter(MetricSecurityEvents, "type"),
		integrity:    reg.Counter(MetricIntegrityOutcomes, "outcome"),
		restrictions: reg.Counter(MetricIntegrityRestrictions, "reason"),
		deliveries:   reg.Counter(MetricOtpDeliveries, "provider", "outcome"),
	}
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

var _ app.SecurityAuditor = (*Metrics)(nil)

// Record implements app.SecurityAuditor: it counts e under its type. It never
// fails, so it never adds to the service's audit-failure count. A nil m
// counts nothing.
func (m *Metrics) Record(e app.SecurityEvent) error {
	if m == nil {
		return nil
	}
	switch e.Type {
	case app.EventOtpVerificationFailed, app.EventOtpLockout, app.EventRefreshTokenReused:
		m.security.Inc(string(e.Type))
	default:
		m.security.Inc(SecurityEventOther)
	}
	return nil
}

// Instrument makes m the service's security auditor, so its audit events are
// counted (MetricSecurityEvents). Keeping this here keeps internal/app out of
// cmd/server, whose only route to the service is through this package.
func (m *Metrics) Instrument(svc *app.IdentityService) {
	svc.SetSecurityAuditor(m)
	svc.SetIntegrityObserver(m.ObserveIntegrity)
}

// ObserveIntegrity counts one Play Integrity outcome and, for a restricted
// verdict, each reason. Both vocabularies are closed in app. A nil m counts
// nothing.
func (m *Metrics) ObserveIntegrity(o app.IntegrityOutcome, reasons []app.IntegrityReason) {
	if m == nil {
		return
	}
	m.integrity.Inc(string(o))
	for _, r := range reasons {
		m.restrictions.Inc(string(r))
	}
}

// ObserveOtpDelivery counts one OTP delivery attempt. Its signature is
// otpdelivery.Config.Observe's, so a configured Sender reports here. A nil m
// counts nothing.
func (m *Metrics) ObserveOtpDelivery(provider string, o otpdelivery.Outcome) {
	if m == nil {
		return
	}
	if !providerLabelPattern.MatchString(provider) {
		provider = ProviderOther
	}
	m.deliveries.Inc(provider, string(o))
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
