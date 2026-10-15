package rdsauth

// The AWS SDK binding (A2/A3): IRSA web identity → STS
// AssumeRoleWithWebIdentity → temporary credentials → RDS IAM token. This is
// the ONLY file in the package that imports the SDK
// (TestPackage_OnlyAWSGoImportsTheSDK).
//
// Deliberately narrow (P1-40 §6.1, §7):
//
//   - sts.New(Options), NOT config.LoadDefaultConfig / sts.NewFromConfig: no
//     shared config or credentials files, AWS_PROFILE, static-key variables,
//     container credentials, IMDS or AWS_ENDPOINT_URL* (in sts v1.51.1 those
//     are read only on the NewFromConfig path). The endpoint is the SDK's
//     default regional STS endpoint for the explicit region.
//   - AssumeRoleWithWebIdentity is an anonymous STS operation: the STS client
//     carries no credentials at all.
//   - The temporary STS credentials are cached by aws.CredentialsCache (they
//     are reused until they expire); the RDS token is NOT cached — every
//     Password call signs a new one (Source).

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// SessionName is the STS role-session name, visible in CloudTrail so an
// operator can attribute database logins to the gateway's fence.
const SessionName = "callscreen-telephony-gateway-fence"

// NewAWS returns a Source that builds RDS IAM tokens from the IRSA identity.
// It makes NO network call (R6): STS is first contacted by the first dial.
func NewAWS(id Identity) (*Source, error) {
	return newAWS(id, sts.Options{})
}

// newAWS lets tests point the STS client at a local fake (BaseEndpoint,
// HTTPClient). Production passes zero Options.
func newAWS(id Identity, base sts.Options) (*Source, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	opts := base.Copy()
	opts.Region = id.Region
	opts.Credentials = nil // anonymous operation; never static keys
	client := sts.New(opts)
	provider := aws.NewCredentialsCache(guardedProvider{
		inner: stscreds.NewWebIdentityRoleProvider(client, id.RoleARN,
			tokenFile(id.WebIdentityTokenFile),
			func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = SessionName }),
	})
	return New(id, TokenBuilderFunc(func(ctx context.Context, endpoint, region, user string) (string, error) {
		return buildToken(ctx, provider, endpoint, region, user)
	}))
}

// buildToken obtains the STS credentials and signs one RDS token, as two
// separately timed steps (P1-41 §9). It is exactly what auth.BuildAuthToken
// does internally (Retrieve, then presign) — the signing step is handed the
// credentials just retrieved, through a provider that returns only them.
//
//   - login_prepare_sts: provider.Retrieve — from the call into the
//     credentials cache until it returns (a cache hit, or this call's STS
//     exchange including the token-file read and validation).
//   - login_prepare_sign: auth.BuildAuthToken with those credentials — from
//     the call until the signed token (or an error) returns.
//   - sts_refresh: whether this call ran the exchange (see
//     fence.STSCredentialsOutcome), recorded once Retrieve has returned.
//
// Each is recorded only when its step returns. A dial whose context ends
// first returns without them (Source.Password does not wait); the fence's own
// "login_prepare" phase is always recorded.
func buildToken(ctx context.Context, provider aws.CredentialsProvider, endpoint, region, user string) (string, error) {
	r := &retrieval{dial: ctx}
	start := time.Now()
	// Carry the dial's context (and this call's marker) to guardedProvider:
	// the cache runs a refresh on a context with cancellation suppressed.
	creds, err := provider.Retrieve(context.WithValue(ctx, retrievalKey{}, r))
	fence.RecordCredentialPhase(ctx, fence.PhaseCredentialsSTS, time.Since(start))
	fence.RecordSTSCredentials(ctx, r.outcome(err))
	if err != nil {
		return "", reasonError(classify(err))
	}

	start = time.Now()
	tok, err := auth.BuildAuthToken(ctx, endpoint, region, user,
		aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil }))
	fence.RecordCredentialPhase(ctx, fence.PhaseCredentialsSign, time.Since(start))
	if err != nil {
		return "", reasonError(classify(err))
	}
	return tok, nil
}

// retrieval is one credential preparation's marker. guardedProvider — which
// the SDK's cache runs ONLY for the call that starts a refresh (a cache hit
// never calls it; a concurrent caller joins the running refresh without it) —
// sets the state, so "did THIS call refresh" is observed, not guessed.
type retrieval struct {
	dial  context.Context
	state atomic.Int32
}

const (
	refreshNotRun int32 = iota
	refreshRunning
	refreshSucceeded
	refreshFailed
)

type retrievalKey struct{}

// outcome maps the marker and Retrieve's error to the fixed outcome set.
// "refreshed" requires that the provider returned validated credentials AND
// that Retrieve returned them to this call.
func (r *retrieval) outcome(err error) fence.STSCredentialsOutcome {
	switch r.state.Load() {
	case refreshNotRun:
		if err != nil {
			return fence.STSCredentialsUnavailable
		}
		return fence.STSCredentialsReused
	case refreshSucceeded:
		if err != nil {
			return fence.STSCredentialsRefreshIncomplete
		}
		return fence.STSCredentialsRefreshed
	case refreshFailed:
		return fence.STSCredentialsRefreshFailed
	default: // still running: this call stopped waiting first
		return fence.STSCredentialsRefreshIncomplete
	}
}

var errTokenFileUnreadable = errors.New("rdsauth: web-identity token file unreadable or empty")

// tokenFile replaces stscreds.IdentityTokenFile, whose error embeds the file
// path, with one that returns a fixed sentinel. It re-reads the file on every
// STS exchange, so kubelet's rotation of the projected token is picked up.
type tokenFile string

func (f tokenFile) GetIdentityToken() ([]byte, error) {
	b, err := os.ReadFile(string(f))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, errTokenFileUnreadable
	}
	return b, nil
}

// classify maps an SDK failure to the fixed reason vocabulary. It reads only
// error TYPES and AWS-defined error codes, never messages (which carry ARNs,
// account IDs and request IDs).
func classify(err error) string {
	switch {
	case errors.Is(err, errTokenFileUnreadable):
		return ReasonWebIdentityToken
	case errors.Is(err, errMalformedCredentials):
		return ReasonCredentialsUnusable
	}
	var api interface{ ErrorCode() string }
	if errors.As(err, &api) {
		code := api.ErrorCode()
		for reason, codes := range stsCodes {
			for _, c := range codes {
				if strings.EqualFold(code, c) {
					return reason
				}
			}
		}
		return ReasonSTSOther
	}
	var conn interface{ ConnectionError() bool }
	var ne net.Error
	if (errors.As(err, &conn) && conn.ConnectionError()) || errors.As(err, &ne) {
		return ReasonSTSUnreachable
	}
	return ReasonUnknown
}

// stsCodes are the AssumeRoleWithWebIdentity error codes (sts v1.51.1
// deserializers.go) and common service-wide codes, by reason.
var stsCodes = map[string][]string{
	ReasonSTSAccessDenied:         {"AccessDenied"},
	ReasonSTSInvalidIdentityToken: {"InvalidIdentityToken"},
	ReasonSTSExpiredToken:         {"ExpiredTokenException", "ExpiredToken"},
	ReasonSTSIdentityProvider:     {"IDPCommunicationError", "IDPRejectedClaim"},
	ReasonSTSRegionDisabled:       {"RegionDisabledException"},
	ReasonSTSThrottled:            {"Throttling", "ThrottlingException", "RequestLimitExceeded"},
}

var errMalformedCredentials = errors.New("rdsauth: STS returned unusable credentials")

// guardedProvider sits INSIDE the credentials cache and:
//
//   - bounds the STS exchange by the dial that triggered it (R5). The SDK's
//     cache suppresses the caller's cancellation and deadline for the shared
//     refresh; without this a hung STS call would hold the cache's single
//     in-flight refresh — and so every later dial — indefinitely. No timing
//     value is added: the bound is the dial's own (HeartbeatTimeout; D10).
//   - converts a panic in the SDK provider into an error. The cache's
//     singleflight re-raises provider panics where they cannot be recovered,
//     and stscreds dereferences response fields without nil checks.
//   - refuses incomplete or already-expired credentials, so they are neither
//     cached nor used to sign.
type guardedProvider struct{ inner aws.CredentialsProvider }

func (g guardedProvider) Retrieve(ctx context.Context) (creds aws.Credentials, err error) {
	r, _ := ctx.Value(retrievalKey{}).(*retrieval)
	if r != nil {
		r.state.Store(refreshRunning)
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(r.dial, cancel)
		defer stop()
	}
	defer func() {
		if recover() != nil {
			creds, err = aws.Credentials{}, errMalformedCredentials
		}
		if r != nil {
			if err == nil {
				r.state.Store(refreshSucceeded) // only after validation below passed
			} else {
				r.state.Store(refreshFailed)
			}
		}
	}()
	creds, err = g.inner.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.SessionToken == "" ||
		!creds.CanExpire || !creds.Expires.After(time.Now()) {
		return aws.Credentials{}, errMalformedCredentials
	}
	return creds, nil
}
