package callback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-6 — callback admission.
//
// The gate between a raw carrier callback and the [Ingestor]:
//
//	Request ─► 1 authenticate (provider Verifier) ─► 2 normalise (provider Normalizer)
//	        ─► 3 validate ─► 4 resolve (provider, carrier call ID) ─► 5 Ingestor.Ingest
//
// Nothing reaches the lifecycle, and no event key is reserved, until steps 1-4
// have all succeeded.
//
// # What is authenticated today: NOTHING
//
// No carrier's callback-authentication scheme is implemented in this
// repository. [Verifier] is the port a provider-specific adapter implements,
// from that provider's official specification; until one is registered for a
// provider, EVERY callback from that provider is refused with
// [ErrNoVerifier]. There is deliberately no generic signature algorithm here:
// one invented without a specification would be accepted by nothing real, or
// worse, would accept something it should not.
//
// A TLS or mTLS connection is not treated as proof by this gate. A verifier may
// use the connection's verified client certificate as one input if the
// provider's specification says so; the gate itself never does.
//
// # Nothing sensitive leaves this file
//
// Errors name the stage and the provider, never a header value, a signature, a
// credential or the body. Request.String redacts all of them.

// Admission errors. Each identifies the stage that refused the callback.
var (
	// ErrNoVerifier: no authentication adapter is registered for the provider.
	ErrNoVerifier = errors.New("callback: no verifier registered for this provider")

	// ErrUnauthenticated: the provider's verifier did not authenticate the
	// request.
	ErrUnauthenticated = errors.New("callback: request not authenticated")

	// ErrMalformedRequest: the request itself is unusable — missing provider,
	// no body, or larger than the configured bound.
	ErrMalformedRequest = errors.New("callback: malformed request")

	// ErrNoNormalizer: no payload adapter is registered for the provider.
	ErrNoNormalizer = errors.New("callback: no normalizer registered for this provider")
)

// Request is one raw callback as received, before anything is trusted.
//
// Body is the EXACT bytes received. Signature schemes are computed over the
// raw request, and a body that was parsed and re-serialised would not verify.
type Request struct {
	// Provider is the carrier this request claims to come from, taken from
	// the route it arrived on — never from the payload.
	Provider telephony.ProviderID

	Method string
	// URL is the full URL as the carrier addressed it, which some schemes sign.
	URL    string
	Header http.Header
	Body   []byte

	// PeerCertificates are the connection's VERIFIED client certificates, if
	// the transport performed mutual TLS. Informational; see the file comment.
	PeerCertificates [][]byte

	ReceivedAt time.Time
}

// String renders the request without headers, body or certificates.
func (r *Request) String() string {
	return fmt.Sprintf("callback request from %s (%s, %d header(s), %d body bytes)",
		r.Provider, r.Method, len(r.Header), len(r.Body))
}

// Authentication is a verifier's verdict.
type Authentication struct {
	// Provider is the provider the verifier authenticated the request AS. It
	// must equal Request.Provider.
	Provider telephony.ProviderID
	// Verified is true only when the request was cryptographically or
	// otherwise conclusively authenticated per the provider's specification.
	Verified bool
	// Mechanism names what was checked, e.g. a scheme and version. Required:
	// a verdict that cannot say how it was reached is not accepted.
	Mechanism string
}

// Verifier authenticates one provider's callbacks. Implementations are
// provider-specific and must follow that provider's official specification.
//
// It must not log or return secret material. A nil error with Verified=false
// is a refusal, not a pass.
type Verifier interface {
	Verify(ctx context.Context, r *Request) (Authentication, error)
}

// Normalizer turns one provider's AUTHENTICATED callback into an [Event]. It is
// called only after verification succeeds.
type Normalizer interface {
	Normalize(ctx context.Context, r *Request) (Event, error)
}

// Provider adapters, per provider.
type ProviderAdapter struct {
	Verifier   Verifier
	Normalizer Normalizer
}

// AdmissionConfig configures an [Admission].
type AdmissionConfig struct {
	// MaxBodyBytes bounds a callback body. Required, positive.
	MaxBodyBytes int
	// Adapters maps each provider to its verifier and normalizer. A provider
	// absent here, or with a nil Verifier, has every callback refused.
	Adapters map[telephony.ProviderID]ProviderAdapter
	// Recorder receives one admission outcome per Admit call. Optional; nil
	// records nothing. A panic inside it is contained (P1-13).
	Recorder Recorder
	// Logger receives one line per admission that failed for an operational
	// reason (store unavailable, internal error), with no identifiers or
	// error text. Optional; nil logs nothing.
	Logger *slog.Logger
}

// Admission authenticates, normalises and resolves callbacks before handing
// them to the [Ingestor]. Safe for concurrent use; it holds no mutable state.
type Admission struct {
	maxBody  int
	adapters map[telephony.ProviderID]ProviderAdapter
	ingestor *Ingestor
	recorder Recorder
	logger   *slog.Logger
}

// NewAdmission builds the gate in front of ingestor. It resolves calls with
// the ingestor's own resolver, so the call resolved here is the call the
// ingestor acts on.
func NewAdmission(cfg AdmissionConfig, ingestor *Ingestor) (*Admission, error) {
	var problems []string
	if cfg.MaxBodyBytes <= 0 {
		problems = append(problems, "MaxBodyBytes must be positive")
	}
	if ingestor == nil {
		problems = append(problems, "an ingestor is required")
	}
	adapters := make(map[telephony.ProviderID]ProviderAdapter, len(cfg.Adapters))
	for p, a := range cfg.Adapters {
		if !p.Valid() {
			problems = append(problems, fmt.Sprintf("adapter registered under invalid provider %q", p))
			continue
		}
		adapters[p] = a
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("callback: invalid admission configuration: %s", strings.Join(problems, "; "))
	}
	return &Admission{maxBody: cfg.MaxBodyBytes, adapters: adapters, ingestor: ingestor,
		recorder: cfg.Recorder, logger: cfg.Logger}, nil
}

// Admit runs one callback through the gate.
//
// Every refusal before step 5 returns an error wrapping exactly one of
// [ErrMalformedRequest], [ErrNoVerifier], [ErrUnauthenticated],
// [ErrNoNormalizer], [ErrInvalidEvent], [ErrProviderMismatch] or
// [ErrUnknownCall], and leaves no trace: no event key is reserved and the
// lifecycle is untouched, so a corrected or later redelivery is processed
// normally. From step 5 on, the result and error are the ingestor's.
func (a *Admission) Admit(ctx context.Context, r *Request) (Result, error) {
	start := time.Now()
	res, err := a.admit(ctx, r)
	provider := a.providerLabel(r)
	outcome := outcomeLabel(res, err)
	if a.recorder != nil {
		guarded(func() { a.recorder.Admission(provider, outcome, time.Since(start)) })
	}
	logOutcome(a.logger, provider, outcome)
	return res, err
}

// providerLabel is the request's provider only if an adapter is configured for
// it, so an arbitrary provider string can never become a metric label.
func (a *Admission) providerLabel(r *Request) string {
	if r != nil {
		if _, ok := a.adapters[r.Provider]; ok {
			return string(r.Provider)
		}
	}
	return unconfiguredProvider
}

// admit is the gate itself; see Admit.
func (a *Admission) admit(ctx context.Context, r *Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r == nil || !r.Provider.Valid() {
		return Result{}, fmt.Errorf("%w: a valid provider is required", ErrMalformedRequest)
	}
	if len(r.Body) == 0 {
		return Result{}, fmt.Errorf("%w: empty body from %s", ErrMalformedRequest, r.Provider)
	}
	if len(r.Body) > a.maxBody {
		return Result{}, fmt.Errorf("%w: body from %s exceeds %d bytes", ErrMalformedRequest, r.Provider, a.maxBody)
	}

	// 1. Authenticate. Fail closed on every path that is not an explicit,
	// complete, matching success.
	adapter, ok := a.adapters[r.Provider]
	if !ok || adapter.Verifier == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrNoVerifier, r.Provider)
	}
	auth, err := adapter.Verifier.Verify(ctx, r)
	if err != nil {
		// A verifier cut short by the deadline or a cancellation has not
		// judged the request; report that, not a refusal. Still fail-closed:
		// nothing proceeds.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("callback: %s verification did not finish: %w", r.Provider, ctxErr)
		}
		// The verifier's error is deliberately NOT wrapped: it may describe
		// the material it rejected.
		return Result{}, fmt.Errorf("%w: %s verifier refused the request", ErrUnauthenticated, r.Provider)
	}
	switch {
	case !auth.Verified:
		return Result{}, fmt.Errorf("%w: %s verifier did not verify the request", ErrUnauthenticated, r.Provider)
	case auth.Provider != r.Provider:
		return Result{}, fmt.Errorf("%w: verified as %q, but the request is for %s", ErrUnauthenticated, auth.Provider, r.Provider)
	case strings.TrimSpace(auth.Mechanism) == "":
		return Result{}, fmt.Errorf("%w: %s verifier did not name its mechanism", ErrUnauthenticated, r.Provider)
	}

	// 2. Normalise.
	if adapter.Normalizer == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrNoNormalizer, r.Provider)
	}
	e, err := adapter.Normalizer.Normalize(ctx, r)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("callback: %s normalisation did not finish: %w", r.Provider, ctxErr)
		}
		return Result{}, fmt.Errorf("%w: %s payload could not be normalised", ErrInvalidEvent, r.Provider)
	}

	// 3. Validate. The event must claim the provider that was authenticated:
	// a payload naming another carrier is a cross-provider forgery attempt.
	if e.Provider != r.Provider {
		return Result{}, fmt.Errorf("%w: event names provider %q on a %s callback", ErrProviderMismatch, e.Provider, r.Provider)
	}
	if err := e.Validate(); err != nil {
		return Result{}, err
	}

	// 4. Resolve, before anything is reserved.
	if _, err := a.ingestor.resolver.Resolve(e.Provider, e.ProviderCallID); err != nil {
		if !errors.Is(err, ErrUnknownCall) {
			err = fmt.Errorf("%w: %w", ErrUnknownCall, err)
		}
		return Result{}, err
	}

	// 5. Ingest: deduplication, reconciliation, lifecycle.
	return a.ingestor.Ingest(ctx, e)
}
