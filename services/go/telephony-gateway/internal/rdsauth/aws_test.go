package rdsauth

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
	"github.com/jackc/pgx/v5/pgconn"
)

// P1-A1 (A2/A3) — the AWS SDK binding, OFFLINE: a local fake STS and a local
// fake PostgreSQL TLS server. No request reaches AWS. These tests prove the
// SDK wiring (inputs, caching, bounds, failure handling); they do NOT prove
// that real STS or RDS accept the result — that needs staging.

const (
	stsKeyID   = "ASIATESTKEYID0000000"
	stsSecret  = "test-secret-access-key-" + secret
	stsSession = "test-session-token-" + secret
	writerHost = "writer.cluster-x.ap-south-1.rds.amazonaws.com"
)

func identityWithTokenFile(t *testing.T) Identity {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("web-identity-jwt-"+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	id := validID
	id.WebIdentityTokenFile = p
	return id
}

func stsOK(expires time.Time) string {
	return `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleWithWebIdentityResult>
<Credentials><AccessKeyId>` + stsKeyID + `</AccessKeyId><SecretAccessKey>` + stsSecret + `</SecretAccessKey>
<SessionToken>` + stsSession + `</SessionToken><Expiration>` + expires.UTC().Format(time.RFC3339) + `</Expiration></Credentials>
<AssumedRoleUser><Arn>arn:aws:sts::000000000000:assumed-role/test-only/` + SessionName + `</Arn><AssumedRoleId>AROATEST:` + SessionName + `</AssumedRoleId></AssumedRoleUser>
</AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>req-ok</RequestId></ResponseMetadata>
</AssumeRoleWithWebIdentityResponse>`
}

const stsDenied = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type>
<Code>AccessDenied</Code><Message>Not authorized to perform sts:AssumeRoleWithWebIdentity on arn:aws:iam::123456789012:role/x ` + secret + `</Message>
</Error><RequestId>req-denied</RequestId></ErrorResponse>`

// fakeSTS serves AssumeRoleWithWebIdentity with whatever respond returns.
type fakeSTS struct {
	srv      *httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	forms    []url.Values
	authz    []string
	respond  atomic.Value // func(w http.ResponseWriter, r *http.Request)
}

func newFakeSTS(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *fakeSTS {
	t.Helper()
	f := &fakeSTS{}
	f.respond.Store(respond)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.authz = append(f.authz, r.Header.Get("Authorization"))
		f.mu.Unlock()
		f.respond.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func reply(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeSTS) source(t *testing.T, id Identity) *Source {
	t.Helper()
	s, err := newAWS(id, sts.Options{BaseEndpoint: aws.String(f.srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d) // TEST bound, not D10
	t.Cleanup(cancel)
	return ctx
}

// parseToken checks the RDS token's shape and returns its query.
func parseToken(t *testing.T, tok, wantHostPort, wantUser string) url.Values {
	t.Helper()
	u, err := url.Parse("https://" + tok)
	if err != nil {
		t.Fatalf("token is not a URL without scheme: %v", err)
	}
	q := u.Query()
	checks := map[string]bool{
		"host:port":       u.Host == wantHostPort,
		"no path":         u.Path == "" && strings.HasPrefix(tok, wantHostPort+"?"), // BuildAuthToken v1.7.4: "host:port?Action=connect&…"
		"Action":          q.Get("Action") == "connect",
		"DBUser":          q.Get("DBUser") == wantUser,
		"algorithm":       q.Get("X-Amz-Algorithm") == "AWS4-HMAC-SHA256",
		"credential":      strings.HasPrefix(q.Get("X-Amz-Credential"), stsKeyID+"/") && strings.HasSuffix(q.Get("X-Amz-Credential"), "/ap-south-1/rds-db/aws4_request"),
		"session token":   q.Get("X-Amz-Security-Token") == stsSession,
		"expiry (SDK)":    q.Get("X-Amz-Expires") == "900",
		"signed headers":  q.Get("X-Amz-SignedHeaders") == "host",
		"signature":       len(q.Get("X-Amz-Signature")) == 64,
		"no secret value": !strings.Contains(tok, stsSecret),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("token %s check failed", name)
		}
	}
	return q
}

func TestAWS_ExchangesTheWebIdentityAndSignsTheToken(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	id := identityWithTokenFile(t)
	s := f.source(t, id)
	if f.requests.Load() != 0 {
		t.Fatal("constructing the adapter contacted STS (R6)")
	}
	tok, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
	if err != nil {
		t.Fatal(err)
	}
	parseToken(t, tok, writerHost+":5432", "fence_iam")

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) != 1 {
		t.Fatalf("STS requests = %d", len(f.forms))
	}
	form := f.forms[0]
	want := map[string]string{
		"Action": "AssumeRoleWithWebIdentity", "Version": "2011-06-15", "RoleArn": id.RoleARN,
		"RoleSessionName": SessionName, "WebIdentityToken": "web-identity-jwt-" + secret,
	}
	for k, v := range want {
		if form.Get(k) != v {
			t.Errorf("STS %s = %q, want %q", k, form.Get(k), v)
		}
	}
	if f.authz[0] != "" {
		t.Error("the STS request was signed; AssumeRoleWithWebIdentity must be anonymous (no static keys)")
	}
}

// STS credentials are cached until expiry; the RDS token is signed anew for
// every connection attempt.
func TestAWS_CachesSTSCredentialsButSignsANewTokenPerConnection(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	s := f.source(t, identityWithTokenFile(t))
	tok1, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // SigV4 dates have one-second resolution
	tok2, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
	if err != nil {
		t.Fatal(err)
	}
	q1, q2 := parseToken(t, tok1, writerHost+":5432", "fence_iam"), parseToken(t, tok2, writerHost+":5432", "fence_iam")
	if tok1 == tok2 || q1.Get("X-Amz-Date") == q2.Get("X-Amz-Date") {
		t.Error("the second connection reused the first token")
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("STS requests = %d, want 1 (temporary credentials are cached until expiry)", n)
	}
}

func TestAWS_STSAndCredentialFailuresFailClosedWithoutLeaking(t *testing.T) {
	t.Parallel()
	missingCredentials := `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleWithWebIdentityResult><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/x/y</Arn></AssumedRoleUser>
</AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`
	emptyKey := strings.Replace(stsOK(time.Now().Add(time.Hour)), stsKeyID, "", 1)
	stsError := func(status int, code string) func(http.ResponseWriter, *http.Request) {
		return reply(status, strings.Replace(stsDenied, "<Code>AccessDenied</Code>", "<Code>"+code+"</Code>", 1))
	}
	type failure struct {
		respond func(http.ResponseWriter, *http.Request)
		reason  string // "" = any fixed reason
	}
	cases := map[string]failure{
		"access denied":          {reply(403, stsDenied), ReasonSTSAccessDenied},
		"invalid identity token": {stsError(400, "InvalidIdentityToken"), ReasonSTSInvalidIdentityToken},
		"expired identity token": {stsError(400, "ExpiredTokenException"), ReasonSTSExpiredToken},
		"IdP rejected claim":     {stsError(403, "IDPRejectedClaim"), ReasonSTSIdentityProvider},
		"region disabled":        {stsError(403, "RegionDisabledException"), ReasonSTSRegionDisabled},
		"throttled":              {stsError(400, "Throttling"), ReasonSTSThrottled},
		"unlisted STS code":      {stsError(400, "SomethingNew"), ReasonSTSOther},
		"server error":           {reply(500, "internal error "+secret), ""},
		"malformed XML":          {reply(200, "<not-xml "+secret), ""},
		"credentials missing":    {reply(200, missingCredentials), ReasonCredentialsUnusable},
		"credentials expired":    {reply(200, stsOK(time.Now().Add(-time.Minute))), ReasonCredentialsUnusable},
		"empty access key ID":    {reply(200, emptyKey), ReasonCredentialsUnusable},
		"truncated connection":   {func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) }, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeSTS(t, c.respond)
			id := identityWithTokenFile(t)
			s := f.source(t, id)
			tok, err := s.Password(testCtx(t, 20*time.Second), writerHost, 5432, "fence_iam")
			if !errors.Is(err, ErrTokenUnavailable) || tok != "" {
				t.Fatalf("token length %d, err %v; want ErrTokenUnavailable", len(tok), err)
			}
			var te *TokenError
			if !errors.As(err, &te) {
				t.Fatalf("err %v carries no reason", err)
			}
			if c.reason != "" && te.Reason != c.reason {
				t.Errorf("reason = %q, want %q", te.Reason, c.reason)
			}
			t.Logf("reason: %s", te.Reason)
			for _, leak := range []string{secret, "arn:aws", "AccessDenied", "req-", id.WebIdentityTokenFile, stsKeyID} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error leaks %q: %v", leak, err)
				}
			}
			// Nothing bad was cached: the next connection asks STS again.
			before := f.requests.Load()
			_, _ = s.Password(testCtx(t, 20*time.Second), writerHost, 5432, "fence_iam")
			if f.requests.Load() == before {
				t.Error("a failed or unusable credential was cached")
			}
		})
	}
}

func TestAWS_AnUnreadableWebIdentityTokenFileFailsWithoutContactingSTS(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	id := identityWithTokenFile(t)
	s := f.source(t, id)
	if err := os.Remove(id.WebIdentityTokenFile); err != nil {
		t.Fatal(err)
	}
	_, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
	var te *TokenError
	if !errors.As(err, &te) || te.Reason != ReasonWebIdentityToken || strings.Contains(err.Error(), id.WebIdentityTokenFile) {
		t.Fatalf("err = %v", err)
	}
	// An empty (whitespace-only) token file is refused locally, too.
	if err := os.WriteFile(id.WebIdentityTokenFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam"); !errors.As(err, &te) || te.Reason != ReasonWebIdentityToken {
		t.Fatalf("empty token file: %v", err)
	}
	if f.requests.Load() != 0 {
		t.Error("STS contacted without a web-identity token")
	}
}

// The SDK's credentials cache runs the STS exchange with the caller's
// cancellation suppressed. The guardedProvider re-binds it to the dial: a hung
// STS call is abandoned when the dial's context ends, and it does not block
// the next dial. (The tests below prove it.)

// tripContext is a dial context whose deadline the TEST triggers: trip()
// closes Done, and Err then reports context.DeadlineExceeded — exactly what a
// timer deadline does to the code under test, which observes a dial's end
// only through Done and Err. Tripping it once the fake STS HOLDS the request
// makes "the dial ended during the STS exchange" a fact of the test, not a
// race against the scheduler (P1-46, F-9: a 300 ms timer could fire before
// the request reached the server, and the cancellation check then had
// nothing to observe).
type tripContext struct {
	context.Context // Background: no values, no deadline of its own
	done            chan struct{}
	once            sync.Once
}

func newTripContext(t *testing.T) *tripContext {
	t.Helper()
	c := &tripContext{Context: context.Background(), done: make(chan struct{})}
	t.Cleanup(c.trip)
	return c
}

func (c *tripContext) Done() <-chan struct{} { return c.done }

func (c *tripContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *tripContext) trip() { c.once.Do(func() { close(c.done) }) }

// hungSTS is a fake STS whose handler signals when a request has ARRIVED and
// when that request's context was CANCELLED (the client abandoned it), and
// otherwise hangs.
func hungSTS(t *testing.T) (f *fakeSTS, arrived, abandoned <-chan struct{}) {
	t.Helper()
	arr := make(chan struct{})
	abd := make(chan struct{})
	var arrOnce, abdOnce sync.Once
	f = newFakeSTS(t, func(w http.ResponseWriter, r *http.Request) {
		arrOnce.Do(func() { close(arr) })
		select {
		case <-r.Context().Done():
			abdOnce.Do(func() { close(abd) })
		case <-time.After(time.Minute):
		}
	})
	return f, arr, abd
}

// startPassword runs Password in the background and returns its result.
func startPassword(ctx context.Context, s *Source) <-chan error {
	res := make(chan error, 1)
	go func() { _, err := s.Password(ctx, writerHost, 5432, "fence_iam"); res <- err }()
	return res
}

// awaitArrival waits until the fake STS holds the request. Password must not
// have returned: nothing can complete it while the dial is live.
func awaitArrival(t *testing.T, arrived <-chan struct{}, res <-chan error) {
	t.Helper()
	select {
	case <-arrived:
	case err := <-res:
		t.Fatalf("Password returned before the STS request arrived (dial still live): %v", err)
	case <-time.After(30 * time.Second): // liveness bound for a loaded host, not a product timing
		t.Fatal("the STS request never reached the fake STS")
	}
}

// R5: the dial's deadline bounds a hung STS exchange. Once the deadline
// passes WHILE the exchange hangs, Password returns DeadlineExceeded, the
// hung HTTP request is abandoned (its context is cancelled — the cache's
// in-flight refresh is not left holding every later dial), and the next dial
// is not blocked by it.
func TestAWS_AHungSTSIsBoundedByTheDialAndDoesNotBlockTheNext(t *testing.T) {
	t.Parallel()
	f, arrived, abandoned := hungSTS(t)
	s := f.source(t, identityWithTokenFile(t))

	ctx := newTripContext(t)
	res := startPassword(ctx, s)
	awaitArrival(t, arrived, res)
	select {
	case err := <-res:
		t.Fatalf("Password returned while the dial was live and STS hung: %v", err)
	default:
	}

	ctx.trip() // the dial's deadline passes during the exchange
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second): // liveness bound: Password selects on Done
		t.Fatal("Password did not return after the dial's deadline passed")
	}
	select {
	case <-abandoned:
	case <-time.After(10 * time.Second): // liveness bound for the cancellation to propagate
		t.Fatal("the hung STS request was not abandoned when the dial ended")
	}

	// The next dial: STS now answers. A dial that starts while the abandoned
	// refresh is still unwinding may JOIN it and fail at once — that is not
	// blocking. Every attempt must return without hitting its own deadline,
	// and one must succeed.
	f.respond.Store(reply(200, stsOK(time.Now().Add(time.Hour))))
	for attempt := 1; ; attempt++ {
		_, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
		if err == nil {
			break
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: the next dial was blocked by the abandoned refresh", attempt)
		}
		if attempt == 5 {
			t.Fatalf("the next dial never succeeded after the abandoned refresh: %v", err)
		}
	}
}

// The same bound with a real TIMER deadline (as the fence uses): a hung STS
// cannot hold Password past the dial's deadline, wherever the exchange is
// when the timer fires.
func TestAWS_ATimerDeadlineEndsAPasswordOnAHungSTS(t *testing.T) {
	t.Parallel()
	f, _, _ := hungSTS(t)
	s := f.source(t, identityWithTokenFile(t))
	select {
	case err := <-startPassword(testCtx(t, 300*time.Millisecond), s):
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second): // liveness bound; the STS handler hangs for a minute
		t.Fatal("a hung STS held Password past its deadline")
	}
}

// capture is an STS HTTP client that records the request and answers it
// locally, so the PRODUCTION endpoint resolution can be observed offline.
type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (c *capture) Do(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r)
	c.mu.Unlock()
	return &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"text/xml"}}, Request: r,
		Body: io.NopCloser(strings.NewReader(stsOK(time.Now().Add(time.Hour)))),
	}, nil
}

// Static keys, profiles and endpoint overrides in the environment are
// ignored: the STS call goes, unsigned, to the default regional endpoint for
// the explicit region, and the token is signed with the STS credentials.
// (Startup also refuses the static-key variables; this is defence in depth.)
func TestAWS_UsesTheRegionalSTSEndpointAndIgnoresAWSEnvironmentOverrides(t *testing.T) {
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIASTATICKEY0000000", "AWS_SECRET_ACCESS_KEY": "static-" + secret,
		"AWS_SESSION_TOKEN": "static-session", "AWS_PROFILE": "other", "AWS_REGION": "us-east-1",
		"AWS_ENDPOINT_URL": "http://127.0.0.1:1", "AWS_ENDPOINT_URL_STS": "http://127.0.0.1:1",
	} {
		t.Setenv(k, v)
	}
	c := &capture{}
	s, err := newAWS(identityWithTokenFile(t), sts.Options{HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Password(testCtx(t, 10*time.Second), writerHost, 5432, "fence_iam")
	if err != nil {
		t.Fatal(err)
	}
	parseToken(t, tok, writerHost+":5432", "fence_iam")
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) != 1 {
		t.Fatalf("STS requests = %d", len(c.reqs))
	}
	r := c.reqs[0]
	if r.URL.Scheme != "https" || r.URL.Host != "sts.ap-south-1.amazonaws.com" {
		t.Errorf("STS endpoint = %s://%s; want https://sts.ap-south-1.amazonaws.com", r.URL.Scheme, r.URL.Host)
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("the STS request was signed (static keys used)")
	}
}

// ── on the wire: the token as a PostgreSQL password over verify-full TLS ──

func testCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, tmpl *x509.Certificate) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	parent, signer := tmpl, key
	if ca != nil {
		parent, signer = ca, caKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key, der
}

// fakePostgres answers SSLRequest, completes TLS with cert, asks for a
// cleartext password (what RDS IAM authentication uses), records it and
// refuses the login.
func fakePostgres(t *testing.T, cert tls.Certificate) (port int, passwords func() []string, conns *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var got []string
	conns = &atomic.Int64{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.ReadFull(c, make([]byte, 8)); err != nil {
					return
				}
				if _, err := c.Write([]byte("S")); err != nil {
					return
				}
				srv := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if srv.Handshake() != nil {
					return
				}
				r := bufio.NewReader(srv)
				var n uint32
				if binary.Read(r, binary.BigEndian, &n) != nil || n < 8 || n > 10000 {
					return
				}
				if _, err := io.ReadFull(r, make([]byte, n-4)); err != nil { // startup message
					return
				}
				_, _ = srv.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3}) // AuthenticationCleartextPassword
				typ, err := r.ReadByte()
				if err != nil || typ != 'p' || binary.Read(r, binary.BigEndian, &n) != nil || n < 5 || n > 10000 {
					return
				}
				body := make([]byte, n-4)
				if _, err := io.ReadFull(r, body); err != nil {
					return
				}
				mu.Lock()
				got = append(got, strings.TrimRight(string(body), "\x00"))
				mu.Unlock()
				msg := "SFATAL\x00VFATAL\x00C28P01\x00Mtest server refuses every login\x00\x00"
				e := []byte{'E', 0, 0, 0, 0}
				binary.BigEndian.PutUint32(e[1:], uint32(4+len(msg)))
				_, _ = srv.Write(append(e, msg...))
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}, conns
}

// The fence's credential dialer sends a freshly signed token for the dialled
// host, port and user over verify-full TLS — one token per connection, one
// connection per dial — and sends NOTHING to a server whose certificate fails
// verification.
func TestAWS_TokensReachPostgresOnlyOverVerifiedTLS(t *testing.T) {
	t.Parallel()
	caCert, caKey, caDER := testCert(t, nil, nil, &x509.Certificate{
		Subject: pkix.Name{CommonName: "test-ca"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf := func(ca *x509.Certificate, key *ecdsa.PrivateKey, ip net.IP) tls.Certificate {
		_, k, der := testCert(t, ca, key, &x509.Certificate{
			Subject: pkix.Name{CommonName: "writer"}, IPAddresses: []net.IP{ip},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
	}
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	s := f.source(t, identityWithTokenFile(t))
	dialer := func(port int) fence.Dialer {
		cfg, err := fence.ParseDSN(fmt.Sprintf("postgres://fence_iam@127.0.0.1:%d/telephony?sslmode=verify-full&sslrootcert=%s&target_session_attrs=read-write",
			port, url.QueryEscape(caFile)))
		if err != nil {
			t.Fatal(err)
		}
		d, err := fence.PgxCredentialDialer(cfg, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Every dial must end in one of two ways: the server's refusal (SQLSTATE
	// 28P01, so the token reached PostgreSQL over verified TLS), or the
	// HARNESS's per-dial bound (DeadlineExceeded) on a starved host — the
	// fail-closed path, possibly before any token was sent (P1-50, F-10: the
	// old test counted such a dial as a connection that should carry a
	// token). Anything else fails. Dials repeat (bounded) until two dials
	// were REFUSED: the server answers only after it has recorded the token,
	// so a refusal proves that dial's token arrived, whatever the scheduler
	// does afterwards (P1-51: counting recorded tokens instead could stop
	// after a token whose refusal the starved client never read).
	const maxDials = 8 // liveness bound for a loaded host, not a product value
	port, passwords, conns := fakePostgres(t, leaf(caCert, caKey, net.ParseIP("127.0.0.1")))
	dial := dialer(port)
	refused, timedOut := 0, 0
	for attempt := 0; refused < 2; attempt++ {
		if attempt == maxDials {
			t.Fatalf("only %d of %d dials were refused by the server (%d at the harness deadline, %d tokens recorded)",
				refused, maxDials, timedOut, len(passwords()))
		}
		if attempt > 0 {
			time.Sleep(1100 * time.Millisecond) // SigV4 dates have one-second resolution
		}
		sess, err := dial(testCtx(t, 5*time.Second))
		var pgErr *pgconn.PgError
		switch {
		case err == nil || sess != nil:
			t.Fatalf("dial %d succeeded against a server that refuses every login", attempt)
		case errors.As(err, &pgErr) && pgErr.Code == "28P01":
			refused++
		case errors.Is(err, context.DeadlineExceeded):
			timedOut++ // the harness bound, not a product timing: fail closed
		default:
			t.Fatalf("dial %d: neither the server's refusal nor the harness deadline: %v", attempt, err)
		}
	}
	got := passwords()
	// A password is recorded only by a handler of an accepted connection, so
	// reading passwords before conns makes this a strict invariant.
	if n := conns.Load(); int64(len(got)) > n {
		t.Fatalf("passwords = %d over %d connections; at most one token per connection", len(got), n)
	}
	if len(got) < refused {
		t.Fatalf("%d refused dials but only %d recorded tokens; every refusal follows a received token", refused, len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		parseToken(t, p, "127.0.0.1:"+strconv.Itoa(port), "fence_iam")
		if seen[p] {
			t.Error("a connection reused an earlier token")
		}
		seen[p] = true
	}

	// A certificate from another CA: the dial fails at certificate
	// verification and NO token is sent. The test requires a dial that really
	// reached the server and failed verification (not one that only hit the
	// harness deadline first), so it cannot pass without exercising TLS.
	otherCA, otherKey, _ := testCert(t, nil, nil, &x509.Certificate{
		Subject: pkix.Name{CommonName: "other-ca"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	})
	badPort, badPasswords, badConns := fakePostgres(t, leaf(otherCA, otherKey, net.ParseIP("127.0.0.1")))
	verified := false
	for attempt := 0; !verified; attempt++ {
		if attempt == maxDials {
			t.Fatalf("no dial reached certificate verification in %d attempts", maxDials)
		}
		_, err := dialer(badPort)(testCtx(t, 5*time.Second))
		var certErr *tls.CertificateVerificationError
		switch {
		case err == nil:
			t.Fatal("connected to a server with an untrusted certificate")
		case errors.As(err, &certErr):
			verified = true
		case !errors.Is(err, context.DeadlineExceeded):
			t.Fatalf("untrusted server: neither a verification failure nor the harness deadline: %v", err)
		}
		if n := len(badPasswords()); n != 0 {
			t.Fatalf("untrusted server received %d passwords; want 0", n)
		}
	}
	if badConns.Load() < 1 {
		t.Error("the untrusted server saw no connection, yet verification was reported")
	}
}

// STS unreachable (nothing listening): a network reason, nothing else.
func TestAWS_AnUnreachableSTSIsClassifiedWithoutDetails(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s, err := newAWS(identityWithTokenFile(t), sts.Options{BaseEndpoint: aws.String("http://" + addr)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Password(testCtx(t, 20*time.Second), writerHost, 5432, "fence_iam")
	var te *TokenError
	if !errors.As(err, &te) || te.Reason != ReasonSTSUnreachable || strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want reason %s without the address", err, ReasonSTSUnreachable)
	}
}

// The reason survives the fence's credential dialer, so the fence's log line
// can name it; nothing else does.
func TestAWS_TheReasonReachesTheFenceDialer(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, reply(403, stsDenied))
	s := f.source(t, identityWithTokenFile(t))
	cfg, err := fence.ParseDSN("postgres://fence_iam@127.0.0.1:1/telephony?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := fence.PgxCredentialDialer(cfg, s)
	_, err = dial(testCtx(t, 20*time.Second))
	if !errors.Is(err, fence.ErrCredentialsUnavailable) || !strings.Contains(err.Error(), ReasonSTSAccessDenied) {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{secret, "arn:aws", "req-denied", "Not authorized"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("leaks %q: %v", leak, err)
		}
	}
}

// ── P1-41 §9: credential sub-phases (STS vs signing) and refresh outcome ──

func timedCtx(t *testing.T, d time.Duration) (context.Context, *fence.Timings) {
	t.Helper()
	tm := fence.NewTimings()
	return fence.WithTimings(testCtx(t, d), tm), tm
}

func delayed(d time.Duration, h func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { time.Sleep(d); h(w, r) }
}

// The first preparation runs the STS exchange (refreshed; its duration
// includes the exchange); the second reuses the cached credentials (no STS
// request); both sign a token.
func TestAWS_SubPhasesDistinguishARefreshFromReuse(t *testing.T) {
	t.Parallel()
	const stsDelay = 40 * time.Millisecond
	f := newFakeSTS(t, delayed(stsDelay, reply(200, stsOK(time.Now().Add(time.Hour)))))
	s := f.source(t, identityWithTokenFile(t))

	ctx1, tm1 := timedCtx(t, 10*time.Second)
	if _, err := s.Password(ctx1, writerHost, 5432, "fence_iam"); err != nil {
		t.Fatal(err)
	}
	ctx2, tm2 := timedCtx(t, 10*time.Second)
	if _, err := s.Password(ctx2, writerHost, 5432, "fence_iam"); err != nil {
		t.Fatal(err)
	}
	d1, d2 := tm1.Durations(), tm2.Durations()
	if tm1.STSCredentials() != fence.STSCredentialsRefreshed || tm2.STSCredentials() != fence.STSCredentialsReused {
		t.Errorf("outcomes = %q, %q; want refreshed, reused", tm1.STSCredentials(), tm2.STSCredentials())
	}
	if d1[fence.PhaseCredentialsSTS] < stsDelay {
		t.Errorf("refresh login_prepare_sts = %v; it must include the STS exchange (at least %v)", d1[fence.PhaseCredentialsSTS], stsDelay)
	}
	if d2[fence.PhaseCredentialsSTS] >= stsDelay {
		t.Errorf("reuse login_prepare_sts = %v; a cache hit must not wait for STS", d2[fence.PhaseCredentialsSTS])
	}
	for i, d := range []map[string]time.Duration{d1, d2} {
		if _, ok := d[fence.PhaseCredentialsSign]; !ok {
			t.Errorf("preparation %d: login_prepare_sign missing", i+1)
		}
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("STS requests = %d, want 1", n)
	}
}

// Failed refreshes: refresh_failed, the STS duration is recorded, nothing is
// signed — including the SDK-panic path.
func TestAWS_SubPhasesOnRefreshFailures(t *testing.T) {
	t.Parallel()
	missingCredentials := `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`
	for name, respond := range map[string]func(http.ResponseWriter, *http.Request){
		"access denied":               reply(403, stsDenied),
		"expired credentials":         reply(200, stsOK(time.Now().Add(-time.Minute))),
		"credentials missing (panic)": reply(200, missingCredentials),
		"malformed response":          reply(200, "<not-xml"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeSTS(t, respond)
			s := f.source(t, identityWithTokenFile(t))
			ctx, tm := timedCtx(t, 20*time.Second)
			if _, err := s.Password(ctx, writerHost, 5432, "fence_iam"); !errors.Is(err, ErrTokenUnavailable) {
				t.Fatalf("err = %v", err)
			}
			d := tm.Durations()
			if tm.STSCredentials() != fence.STSCredentialsRefreshFailed {
				t.Errorf("outcome = %q, want refresh_failed", tm.STSCredentials())
			}
			if _, ok := d[fence.PhaseCredentialsSTS]; !ok {
				t.Error("login_prepare_sts missing")
			}
			if _, ok := d[fence.PhaseCredentialsSign]; ok {
				t.Error("login_prepare_sign recorded without credentials")
			}
		})
	}
	// An unreadable web-identity token file is a failed refresh too.
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	id := identityWithTokenFile(t)
	s := f.source(t, id)
	_ = os.Remove(id.WebIdentityTokenFile)
	ctx, tm := timedCtx(t, 10*time.Second)
	_, _ = s.Password(ctx, writerHost, 5432, "fence_iam")
	if tm.STSCredentials() != fence.STSCredentialsRefreshFailed {
		t.Errorf("unreadable token file: outcome = %q", tm.STSCredentials())
	}
}

// A dial that times out during a hung exchange never reports "refreshed" and
// never signs.
//
// The dial ends (tripContext) only once the fake STS holds the request, so
// this call's exchange is known to be RUNNING when the deadline passes
// (P1-46, F-9: with a 200 ms timer the dial could end before the exchange
// started, legitimately yielding "unavailable" or no outcome at all).
func TestAWS_SubPhasesWhenTheDialTimesOutDuringARefresh(t *testing.T) {
	t.Parallel()
	f, arrived, _ := hungSTS(t)
	s := f.source(t, identityWithTokenFile(t))
	tm := fence.NewTimings()
	ctx := newTripContext(t)
	res := startPassword(fence.WithTimings(ctx, tm), s)
	awaitArrival(t, arrived, res)
	ctx.trip()
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Password did not return after the dial's deadline passed")
	}
	// The outcome is recorded when the cache returns to this call, which it
	// does on the dial's end; wait for that observable state (bounded).
	deadline := time.Now().Add(10 * time.Second)
	for tm.STSCredentials() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	switch tm.STSCredentials() {
	case fence.STSCredentialsRefreshIncomplete, fence.STSCredentialsRefreshFailed:
	default:
		t.Errorf("outcome = %q, want refresh_incomplete or refresh_failed", tm.STSCredentials())
	}
	if _, ok := tm.Durations()[fence.PhaseCredentialsSign]; ok {
		t.Error("signed after a timed-out refresh")
	}
}

// Every marker state maps to exactly one outcome; "refreshed" needs both a
// validated refresh AND credentials returned to this call.
func TestRetrieval_OutcomeTable(t *testing.T) {
	t.Parallel()
	fail := errors.New("x")
	for _, c := range []struct {
		state int32
		err   error
		want  fence.STSCredentialsOutcome
	}{
		{refreshNotRun, nil, fence.STSCredentialsReused},
		{refreshNotRun, fail, fence.STSCredentialsUnavailable},
		{refreshRunning, fail, fence.STSCredentialsRefreshIncomplete},
		{refreshSucceeded, nil, fence.STSCredentialsRefreshed},
		{refreshSucceeded, fail, fence.STSCredentialsRefreshIncomplete},
		{refreshFailed, fail, fence.STSCredentialsRefreshFailed},
	} {
		r := &retrieval{}
		r.state.Store(c.state)
		if got := r.outcome(c.err); got != c.want {
			t.Errorf("state %d, err %v: %q, want %q", c.state, c.err, got, c.want)
		}
	}
}

// Through the fence's dialer: the sub-phases nest inside "login_prepare"
// (never larger than it), and the connect phases follow.
func TestAWS_SubPhasesNestInsideTheCredentialsPhase(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, delayed(20*time.Millisecond, reply(200, stsOK(time.Now().Add(time.Hour)))))
	s := f.source(t, identityWithTokenFile(t))
	cfg, err := fence.ParseDSN("postgres://fence_iam@127.0.0.1:1/telephony?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := fence.PgxCredentialDialer(cfg, s)
	ctx, tm := timedCtx(t, 10*time.Second)
	_, _ = dial(ctx) // nothing listens on port 1
	d := tm.Durations()
	total, okT := d[fence.PhaseCredentials]
	stsD, okS := d[fence.PhaseCredentialsSTS]
	sign, okG := d[fence.PhaseCredentialsSign]
	// Presence, not size: a local signing step can read 0s on a coarse clock.
	if !okT || !okS || !okG || stsD < 20*time.Millisecond {
		t.Fatalf("phases = %v", d)
	}
	if stsD+sign > total {
		t.Errorf("login_prepare_sts (%v) + login_prepare_sign (%v) exceed login_prepare (%v)", stsD, sign, total)
	}
	if _, ok := d[fence.PhaseTCP]; !ok {
		t.Errorf("tcp phase missing: %v", d)
	}
}

// End to end in a running fence's JSON log: the sub-phases and the outcome
// appear; no credential, token, role, file path, host or user does.
func TestAWS_RunningFenceLogsSubPhasesWithoutSecrets(t *testing.T) {
	t.Parallel()
	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	id := identityWithTokenFile(t)
	s := f.source(t, id)
	cfg, err := fence.ParseDSN("postgres://fence_iam@127.0.0.1:1/telephony?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := fence.PgxCredentialDialer(cfg, s)
	var mu sync.Mutex
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fc, err := fence.New(fence.Config{LockKey: 7, HeartbeatInterval: 300 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond}, dial, log) // TEST values, not D10
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fc.Run(context.Background()) }()
	t.Cleanup(func() { _ = fc.Shutdown(context.Background()) })
	read := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
	// Wait for the first dial whose preparation COMPLETED (a slow host may
	// end earlier dials at the 200 ms test bound first — see
	// checkDialRecords), then check every record seen so far.
	credentialed := awaitCredentialedDials(t, read, 1)
	if credentialed[0][fence.FieldSTSOutcome] == nil {
		t.Errorf("credentialed record without %s", fence.FieldSTSOutcome)
	}
	if fc.Owned() {
		t.Error("owner without a database")
	}
	for _, leak := range []string{stsKeyID, stsSecret, stsSession, secret, "web-identity-jwt", id.RoleARN, id.WebIdentityTokenFile,
		SessionName, "127.0.0.1", "fence_iam", "X-Amz", "Action=connect"} {
		if strings.Contains(read(), leak) {
			t.Errorf("log contains %q", leak)
		}
	}
}

// dialFailurePhases returns the "phase" group of every "cannot open a
// database session" record in a JSON log, in log order.
func dialFailurePhases(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(logs, "\n") {
		if !strings.Contains(l, "cannot open a database session") {
			continue
		}
		var rec struct {
			Phase map[string]any `json:"phase"`
		}
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("unparsable dial-failure record: %v", err)
		}
		out = append(out, rec.Phase)
	}
	return out
}

// checkDialRecords checks a running fence's dial-failure records, in log
// order, against the credential-preparation semantics (P1-41 §9) — whatever
// the scheduler did. A dial may end at its (test) bound before preparation
// completes, the documented "timed-out dial" case, so such a record carries
// login_prepare only, or refresh_incomplete / refresh_failed / unavailable;
// the tests then wait for later dials instead of assuming the first one
// completes (P1-46, F-9). It returns the records that obtained credentials.
//
// Rules: login_prepare is always an integer-nanosecond duration; the outcome,
// when present, is one of the five values; a record that obtained
// credentials (refreshed or reused) has both sub-phases, integer and nested
// within login_prepare; a record without credentials was never signed; at
// most ONE refresh succeeds (the fake STS issues hour-long credentials); and
// "reused" before any "refreshed" requires an earlier dial that gave up
// while its refresh was running (that refresh then filled the cache).
func checkDialRecords(t *testing.T, phases []map[string]any) (credentialed []map[string]any) {
	t.Helper()
	integer := func(v any) (float64, bool) {
		n, ok := v.(float64)
		return n, ok && n >= 0 && n == float64(int64(n))
	}
	gaveUp, refreshed := false, 0
	for i, p := range phases {
		total, ok := integer(p[fence.PhaseCredentials])
		if !ok {
			t.Errorf("record %d: %s = %v, want integer nanoseconds", i, fence.PhaseCredentials, p[fence.PhaseCredentials])
		}
		outcome, _ := p[fence.FieldSTSOutcome].(string)
		switch fence.STSCredentialsOutcome(outcome) {
		case fence.STSCredentialsRefreshed, fence.STSCredentialsReused:
			sts, ok1 := integer(p[fence.PhaseCredentialsSTS])
			sign, ok2 := integer(p[fence.PhaseCredentialsSign])
			if !ok1 || !ok2 {
				t.Errorf("record %d (%s): sub-phases missing or not integer nanoseconds: %v", i, outcome, p)
			} else if sts+sign > total {
				t.Errorf("record %d: sub-phases (%v) exceed %s (%v)", i, sts+sign, fence.PhaseCredentials, total)
			}
			if outcome == string(fence.STSCredentialsRefreshed) {
				refreshed++
				if refreshed > 1 {
					t.Errorf("record %d: a second successful refresh of hour-long credentials", i)
				}
			} else if refreshed == 0 && !gaveUp {
				t.Errorf("record %d: reused before any refresh could have filled the cache", i)
			}
			credentialed = append(credentialed, p)
		case fence.STSCredentialsRefreshIncomplete, fence.STSCredentialsRefreshFailed, fence.STSCredentialsUnavailable, "":
			if _, signed := p[fence.PhaseCredentialsSign]; signed {
				t.Errorf("record %d (%q): signed without credentials", i, outcome)
			}
			gaveUp = true
		default:
			t.Errorf("record %d: %s = %q, not in the fixed set", i, fence.FieldSTSOutcome, outcome)
		}
	}
	return credentialed
}

// awaitCredentialedDials waits (bounded) until the log holds n dials that
// obtained credentials, checking every record, and returns them.
func awaitCredentialedDials(t *testing.T, read func() string, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second) // liveness bound for a loaded host, not a product timing
	for {
		got := checkDialRecords(t, dialFailurePhases(t, read()))
		if t.Failed() {
			t.FailNow() // a rule was broken; do not keep polling
		}
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d dials obtained credentials within the bound; log:\n%s", len(got), n, read())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
