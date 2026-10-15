package rdsauth

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// P1-A1 — the SDK-independent adapter core. Offline: no AWS, no network.

const secret = "hunter2"

var validID = Identity{
	Region:               "ap-south-1",
	RoleARN:              "arn:aws:iam::000000000000:role/test-only",
	WebIdentityTokenFile: "/var/run/secrets/eks.amazonaws.com/serviceaccount/token",
}

var _ fence.Credentials = (*Source)(nil)

type call struct{ endpoint, region, user string }

type recorder struct {
	mu    sync.Mutex
	calls []call
	n     atomic.Int64
}

func (r *recorder) BuildToken(_ context.Context, endpoint, region, user string) (string, error) {
	n := r.n.Add(1)
	r.mu.Lock()
	r.calls = append(r.calls, call{endpoint, region, user})
	r.mu.Unlock()
	return "token-" + string(rune('a'+n-1)), nil
}

func TestNew_RefusesAnIncompleteIdentityOrNoBuilder(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]Identity{
		"no region":       {RoleARN: validID.RoleARN, WebIdentityTokenFile: validID.WebIdentityTokenFile},
		"no role":         {Region: validID.Region, WebIdentityTokenFile: validID.WebIdentityTokenFile},
		"no token file":   {Region: validID.Region, RoleARN: validID.RoleARN},
		"padded region":   {Region: " ap-south-1", RoleARN: validID.RoleARN, WebIdentityTokenFile: validID.WebIdentityTokenFile},
		"padded role ARN": {Region: validID.Region, RoleARN: validID.RoleARN + "\n", WebIdentityTokenFile: validID.WebIdentityTokenFile},
	} {
		if _, err := New(id, &recorder{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		} else if strings.Contains(err.Error(), "arn:aws") {
			t.Errorf("%s: error echoes a value: %v", name, err)
		}
	}
	if _, err := New(validID, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("nil builder: %v", err)
	}
}

// The builder receives exactly the dialled endpoint (host:port, IPv6
// bracketed), the explicit region and the database user.
func TestPassword_PassesEndpointRegionAndUser(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	s, err := New(validID, r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Password(ctx, "writer.cluster-x.ap-south-1.rds.amazonaws.com", 5432, "fence_iam"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Password(ctx, "2001:db8::1", 6543, "fence_iam"); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"writer.cluster-x.ap-south-1.rds.amazonaws.com:5432", "ap-south-1", "fence_iam"},
		{"[2001:db8::1]:6543", "ap-south-1", "fence_iam"},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) != len(want) {
		t.Fatalf("calls = %v", r.calls)
	}
	for i := range want {
		if r.calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, r.calls[i], want[i])
		}
	}
}

// Every call builds a new token: nothing is cached, so each connection
// attempt authenticates with its own token.
func TestPassword_BuildsAFreshTokenPerCall(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	s, _ := New(validID, r)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		tok, err := s.Password(context.Background(), "h", 5432, "u")
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Errorf("token %q reused", tok)
		}
		seen[tok] = true
	}
	if r.n.Load() != 3 {
		t.Errorf("builder calls = %d, want 3", r.n.Load())
	}
}

func TestPassword_RefusesMissingInputsWithoutCallingTheBuilder(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	s, _ := New(validID, r)
	for name, in := range map[string]struct {
		host string
		port uint16
		user string
	}{"no host": {"", 5432, "u"}, "no port": {"h", 0, "u"}, "no user": {"h", 5432, ""}} {
		if _, err := s.Password(context.Background(), in.host, in.port, in.user); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r.n.Load() != 0 {
		t.Errorf("builder called %d times", r.n.Load())
	}
}

// Credential-provider (STS) and signing failures, and an empty token, all
// become ErrTokenUnavailable with the cause withheld.
func TestPassword_BuilderFailuresAreWithheld(t *testing.T) {
	t.Parallel()
	for name, b := range map[string]TokenBuilderFunc{
		"provider failure": func(context.Context, string, string, string) (string, error) {
			return "", errors.New("operation error STS: AssumeRoleWithWebIdentity, RequestID: 1f2e, arn:aws:iam::123456789012:role/x " + secret)
		},
		"signing failure with partial token": func(context.Context, string, string, string) (string, error) {
			return "h:5432/?Action=connect&X-Amz-Signature=" + secret, errors.New("sign: " + secret)
		},
		"empty token": func(context.Context, string, string, string) (string, error) { return "", nil },
	} {
		s, _ := New(validID, b)
		tok, err := s.Password(context.Background(), "h", 5432, "u")
		if !errors.Is(err, ErrTokenUnavailable) || tok != "" {
			t.Errorf("%s: = %q, %v; want \"\", ErrTokenUnavailable", name, tok, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "arn:aws") || strings.Contains(err.Error(), "RequestID") {
			t.Errorf("%s: error leaks: %v", name, err)
		}
	}
}

// A builder that ignores its context cannot hold the dial past its bound.
func TestPassword_IsBoundedByTheContext(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)
	s, _ := New(validID, TokenBuilderFunc(func(context.Context, string, string, string) (string, error) {
		<-release // ignores ctx on purpose
		return "late-token", nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // TEST bound, not D10
	defer cancel()
	start := time.Now()
	tok, err := s.Password(ctx, "h", 5432, "u")
	if !errors.Is(err, context.DeadlineExceeded) || tok != "" {
		t.Fatalf("= %q, %v; want DeadlineExceeded", tok, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("returned after %v; the context bound was not honoured", d)
	}
}

func TestPassword_CancelledContextNeverCallsTheBuilderOrReturnsAToken(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	s, _ := New(validID, r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tok, err := s.Password(ctx, "h", 5432, "u"); !errors.Is(err, context.Canceled) || tok != "" {
		t.Errorf("= %q, %v", tok, err)
	}
	if r.n.Load() != 0 {
		t.Errorf("builder called after cancellation")
	}

	// A token that arrives together with cancellation is discarded.
	ctx2, cancel2 := context.WithCancel(context.Background())
	s2, _ := New(validID, TokenBuilderFunc(func(context.Context, string, string, string) (string, error) {
		cancel2()
		return "racing-token", nil
	}))
	if tok, err := s2.Password(ctx2, "h", 5432, "u"); err == nil || tok != "" {
		t.Errorf("token used after cancellation: %q, %v", tok, err)
	}
}

// The fence receives the adapter's failure as its own withheld sentinel, so
// no IAM failure can turn into a password or a successful connection.
func TestSource_FailureThroughTheFenceDialerIsWithheldAndNeverConnects(t *testing.T) {
	t.Parallel()
	cfg, err := fence.ParseDSN("postgres://fence_iam@127.0.0.1:1/telephony?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(validID, TokenBuilderFunc(func(context.Context, string, string, string) (string, error) {
		return "", errors.New("sts " + secret)
	}))
	dial, err := fence.PgxCredentialDialer(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // TEST bound, not D10
	defer cancel()
	sess, err := dial(ctx)
	if sess != nil || !errors.Is(err, fence.ErrCredentialsUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatalf("= %v, %v", sess, err)
	}
}

// The SDK-free core stays SDK-free: only aws.go (and its tests) may import
// the AWS SDK.
func TestPackage_OnlyAWSGoImportsTheSDK(t *testing.T) {
	t.Parallel()
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"aws.go": true, "aws_test.go": true}
	fset := token.NewFileSet()
	for _, name := range append(append(pkg.GoFiles, pkg.TestGoFiles...), pkg.XTestGoFiles...) {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "github.com/aws/") && !allowed[name] {
				t.Errorf("%s imports %s; only aws.go may use the SDK", name, imp.Path.Value)
			}
		}
	}
}

// A panicking builder is a failed token, not a crashed gateway.
func TestPassword_APanickingBuilderFailsClosed(t *testing.T) {
	t.Parallel()
	s, _ := New(validID, TokenBuilderFunc(func(context.Context, string, string, string) (string, error) {
		panic("sdk bug " + secret)
	}))
	tok, err := s.Password(context.Background(), "h", 5432, "u")
	var te *TokenError
	if !errors.Is(err, ErrTokenUnavailable) || tok != "" || !errors.As(err, &te) || te.Reason != ReasonBuilderPanic {
		t.Fatalf("= %q, %v", tok, err)
	}
}

// A builder's TokenError reason survives (even when wrapped); any other error
// becomes "unknown"; an empty token is named. No text passes through.
func TestPassword_ReasonsAreFixed(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		err    error
		reason string
	}{
		"typed reason":  {reasonError(ReasonSTSThrottled), ReasonSTSThrottled},
		"plain error":   {errors.New("sts " + secret), ReasonUnknown},
		"wrapped typed": {fmt.Errorf("outer %s: %w", secret, reasonError(ReasonSTSAccessDenied)), ReasonSTSAccessDenied},
		"empty token":   {nil, ReasonEmptyToken},
	} {
		s, _ := New(validID, TokenBuilderFunc(func(context.Context, string, string, string) (string, error) { return "", c.err }))
		_, err := s.Password(context.Background(), "h", 5432, "u")
		var te *TokenError
		if !errors.As(err, &te) || te.Reason != c.reason || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %v, want reason %s", name, err, c.reason)
		}
	}
}
