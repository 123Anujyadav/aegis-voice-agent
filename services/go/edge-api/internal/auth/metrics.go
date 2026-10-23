package auth

import "github.com/callscreen/callscreen-platform/packages/go/metrics"

// MetricAuthRequests counts every request Middleware decided, by outcome. It
// is subsystem-prefixed, as metricsexport expects (ADR-0013), because the
// exporter adds no subsystem label.
//
// WHY A COUNTER AND NOT A LOG LINE: a refused request (401) was previously not
// recorded anywhere, so a credential-stuffing or replay attempt was invisible
// to operators. One log line per refusal would make it visible but would let
// any unauthenticated client turn request volume into log volume. A counter
// costs the same at any rate and needs no request detail.
const MetricAuthRequests = "edge_api_auth_requests_total"

// Outcomes recorded under the "outcome" label. The vocabulary is closed: one
// value per decision Middleware can make, so the label cannot grow without
// bound and carries nothing from the request.
const (
	// OutcomeAdmitted is a request whose token and proof verified.
	OutcomeAdmitted = "admitted"
	// OutcomeUnauthenticated is a request without a single DPoP Authorization
	// header (ERR_UNAUTHENTICATED).
	OutcomeUnauthenticated = "unauthenticated"
	// OutcomeInvalidToken is an access token that did not verify
	// (ERR_INVALID_TOKEN).
	OutcomeInvalidToken = "invalid_token"
	// OutcomeInvalidProof is a missing, malformed, mismatched or replayed DPoP
	// proof (ERR_INVALID_DPOP_PROOF).
	OutcomeInvalidProof = "invalid_dpop_proof"
	// OutcomeUnavailable is a request refused because the replay store could
	// not answer (ERR_TEMPORARILY_UNAVAILABLE, 503).
	OutcomeUnavailable = "unavailable"
)

// outcomeForCode maps a refusal's problem code to its outcome. Every code
// refuse can be given is here; an unknown code is counted as unavailable, the
// fail-closed reading.
func outcomeForCode(code string) string {
	switch code {
	case "ERR_UNAUTHENTICATED":
		return OutcomeUnauthenticated
	case "ERR_INVALID_TOKEN":
		return OutcomeInvalidToken
	case "ERR_INVALID_DPOP_PROOF":
		return OutcomeInvalidProof
	default:
		return OutcomeUnavailable
	}
}

// Registry returns the authenticator's instruments, for exposure through
// metricsexport.Source.
func (a *Authenticator) Registry() *metrics.Registry { return a.reg }
