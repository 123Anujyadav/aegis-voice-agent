// Package rdsauth supplies RDS IAM authentication tokens as fence.Credentials
// (Phase 19 P1-A1; decisions C1, R1–R6 — see
// docs/telephony/P1-40_A1_DECISIONS_AND_IMPLEMENTATION.md).
//
// # Layout
//
// This file is the SDK-free core: input validation, the "host:port" endpoint,
// the dial-context bound, the fresh token per connection and the refusal to
// pass on any error text. The token itself comes from a TokenBuilder. aws.go
// is the AWS SDK binding (A2/A3: service/sts + credentials/stscreds for IRSA
// web identity, feature/rds/auth.BuildAuthToken) and the only file that
// imports the SDK. Verified offline against fakes only; real AWS
// authentication is unproven until staging.
//
// # Rules this package keeps
//
//   - The region is the explicit FENCE_AWS_REGION value (R4a); it is never
//     read from the environment here.
//   - Identity is IRSA only: a role ARN and a web-identity token FILE. There is
//     no field for, and no code path to, static access keys.
//   - Every call builds a NEW token (one per connection attempt); nothing is
//     cached here. Credential caching, if any, belongs to the SDK provider.
//   - Every call is bounded by the caller's context — the fence's per-dial
//     HeartbeatTimeout (R5). No timing value is defined here (D10 unresolved).
//   - Failures return sentinel errors only. A builder's error can carry
//     account IDs, role ARNs, STS request IDs or token fragments; it is never
//     returned, wrapped or logged (R3).
package rdsauth

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
)

// ErrTokenUnavailable is returned when no token could be built. The cause is
// deliberately withheld (R3).
var ErrTokenUnavailable = errors.New("rdsauth: RDS IAM token unavailable (details withheld)")

// Failure reasons: a FIXED vocabulary, never derived from error text, so it
// can be logged (fence.CredentialsReason) while every detail stays withheld.
const (
	ReasonSTSAccessDenied         = "sts_access_denied"
	ReasonSTSInvalidIdentityToken = "sts_invalid_identity_token" //nolint:gosec // G101: a failure-reason label, not a credential
	ReasonSTSExpiredToken         = "sts_expired_token"          //nolint:gosec // G101: a failure-reason label, not a credential
	ReasonSTSIdentityProvider     = "sts_identity_provider_error"
	ReasonSTSRegionDisabled       = "sts_region_disabled"
	ReasonSTSThrottled            = "sts_throttled"
	ReasonSTSOther                = "sts_error"
	ReasonSTSUnreachable          = "sts_unreachable"
	ReasonWebIdentityToken        = "web_identity_token_unreadable"
	ReasonCredentialsUnusable     = "sts_credentials_unusable"
	ReasonBuilderPanic            = "token_builder_panic"
	ReasonEmptyToken              = "token_empty"
	ReasonUnknown                 = "unknown"
)

// TokenError is ErrTokenUnavailable with a Reason from the fixed vocabulary.
type TokenError struct{ Reason string }

func (e *TokenError) Error() string {
	return "rdsauth: RDS IAM token unavailable: " + e.Reason + " (details withheld)"
}

// Is makes a TokenError match ErrTokenUnavailable.
func (e *TokenError) Is(target error) bool { return target == ErrTokenUnavailable }

// CredentialsReason implements fence.CredentialsReason.
func (e *TokenError) CredentialsReason() string { return e.Reason }

func reasonError(r string) error { return &TokenError{Reason: r} }

// ErrInvalidInput is returned for an unusable identity, endpoint or user.
var ErrInvalidInput = errors.New("rdsauth: invalid input")

// Identity is the IRSA workload identity the SDK binding uses. The values
// come from configuration that app.LoadFence has already validated
// statically (R6); nothing here contacts AWS.
type Identity struct {
	// Region is the explicit FENCE_AWS_REGION (R4a/R4b).
	Region string
	// RoleARN is AWS_ROLE_ARN, injected by EKS IRSA.
	RoleARN string
	// WebIdentityTokenFile is AWS_WEB_IDENTITY_TOKEN_FILE, the projected
	// service-account token. Its CONTENT is a credential and is never read or
	// logged by this package.
	WebIdentityTokenFile string
}

// Validate checks that every field is present and free of surrounding
// whitespace. Errors name fields, never values.
func (id Identity) Validate() error {
	for _, f := range []struct{ name, v string }{
		{"region", id.Region}, {"role ARN", id.RoleARN}, {"web-identity token file", id.WebIdentityTokenFile},
	} {
		if f.v == "" || strings.TrimSpace(f.v) != f.v {
			return errors.Join(ErrInvalidInput, errors.New("rdsauth: the "+f.name+" is missing or malformed"))
		}
	}
	return nil
}

// TokenBuilder builds ONE RDS IAM authentication token for user at endpoint
// ("host:port") in region. It must honour ctx. The SDK binding is
// auth.BuildAuthToken(ctx, endpoint, region, user, provider).
type TokenBuilder interface {
	BuildToken(ctx context.Context, endpoint, region, user string) (string, error)
}

// TokenBuilderFunc adapts a function to TokenBuilder.
type TokenBuilderFunc func(ctx context.Context, endpoint, region, user string) (string, error)

// BuildToken calls f.
func (f TokenBuilderFunc) BuildToken(ctx context.Context, endpoint, region, user string) (string, error) {
	return f(ctx, endpoint, region, user)
}

// Source is a fence.Credentials that returns a fresh RDS IAM token per call.
type Source struct {
	region string
	build  TokenBuilder
}

// New returns a Source for id that builds tokens with build.
func New(id Identity, build TokenBuilder) (*Source, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if build == nil {
		return nil, errors.Join(ErrInvalidInput, errors.New("rdsauth: a token builder is required"))
	}
	return &Source{region: id.Region, build: build}, nil
}

// Password returns a newly built token for one connection to host:port as
// user. It returns within ctx's bound even if the builder does not: a builder
// still running when ctx ends finishes in the background and its result is
// discarded (it can never be used for a later connection).
func (s *Source) Password(ctx context.Context, host string, port uint16, user string) (string, error) {
	if host == "" || port == 0 || user == "" {
		return "", errors.Join(ErrInvalidInput, errors.New("rdsauth: host, port and user are required"))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	endpoint := net.JoinHostPort(host, strconv.Itoa(int(port)))

	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1) // buffered: a late builder never blocks
	go func() {
		defer func() {
			if recover() != nil { // a panicking builder is a failed token, not a crashed gateway
				done <- result{err: reasonError(ReasonBuilderPanic)}
			}
		}()
		t, err := s.build.BuildToken(ctx, endpoint, s.region, user)
		done <- result{t, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case r.err != nil:
			// Only a TokenError's fixed reason survives; any other error
			// (and its text) is replaced.
			var te *TokenError
			if errors.As(r.err, &te) {
				return "", reasonError(te.Reason)
			}
			return "", reasonError(ReasonUnknown)
		case r.token == "":
			return "", reasonError(ReasonEmptyToken)
		}
		return r.token, nil
	}
}
