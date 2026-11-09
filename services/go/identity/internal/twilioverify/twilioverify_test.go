package twilioverify_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/twilioverify"
)

// NO TEST HERE REACHES TWILIO. Every client is built on a loopback test
// server, and its transport is guarded: a request to any other host fails the
// test before anything is sent. The credentials below are made up, in the
// documented shapes only.

// Made-up identifiers in Twilio's documented shapes (fakeSID).
var (
	accountSID = fakeSID("A", "C")
	apiKeySID  = fakeSID("S", "K")
	serviceSID = fakeSID("V", "A")
	verifySID  = fakeSID("V", "E")
)

const (
	apiSecret = "testSecretNotReal0123456789abcdef"
	number    = "+919876543210"
	code      = "123456"
)

// guardedTransport sends only to allowedHost and counts requests.
type guardedTransport struct {
	t           *testing.T
	allowedHost string
	next        http.RoundTripper
	requests    atomic.Int32
}

func (g *guardedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != g.allowedHost {
		g.t.Errorf("a request to %s was attempted: tests must never leave this machine", r.URL.Host)
		return nil, errors.New("blocked by the test's network guard")
	}
	g.requests.Add(1)
	return g.next.RoundTrip(r)
}

type rig struct {
	srv    *httptest.Server
	guard  *guardedTransport
	client *twilioverify.Client
	last   atomic.Pointer[http.Request]
	form   atomic.Pointer[url.Values]
}

// newRig serves handler on a loopback server and returns a client for it.
func newRig(t *testing.T, timeout time.Duration, handler http.HandlerFunc) *rig {
	t.Helper()
	r := &rig{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		f := req.PostForm
		r.form.Store(&f)
		r.last.Store(req)
		handler(w, req)
	}))
	t.Cleanup(r.srv.Close)
	u, _ := url.Parse(r.srv.URL)
	r.guard = &guardedTransport{t: t, allowedHost: u.Host, next: r.srv.Client().Transport}
	c, err := twilioverify.New(twilioverify.Config{
		AccountSID: accountSID, APIKeySID: apiKeySID, APIKeySecret: apiSecret, ServiceSID: serviceSID,
		Timeout: timeout, HTTPClient: &http.Client{Transport: r.guard}, BaseURL: r.srv.URL + "/v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.client = c
	return r
}

func verification(status string) string {
	return fmt.Sprintf(`{"sid":%q,"service_sid":%q,"account_sid":%q,"status":%q,"channel":"sms","to":%q}`,
		verifySID, serviceSID, accountSID, status, number)
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// noLeak fails if err's text carries anything sensitive.
func noLeak(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{number, strings.TrimPrefix(number, "+"), code, apiSecret, apiKeySID, "127.0.0.1", "Authorization"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q carries %q", err, s)
		}
	}
}

func TestStartVerification_SendsOneWellFormedRequest(t *testing.T) {
	r := newRig(t, 0, reply(http.StatusCreated, verification("pending")))
	sid, err := r.client.StartVerification(context.Background(), number)
	if err != nil || sid != verifySID {
		t.Fatalf("(%q, %v), want %q", sid, err, verifySID)
	}
	req := r.last.Load()
	if req.Method != http.MethodPost || req.URL.Path != "/v2/Services/"+serviceSID+"/Verifications" {
		t.Errorf("%s %s", req.Method, req.URL.Path)
	}
	if user, pass, ok := req.BasicAuth(); !ok || user != apiKeySID || pass != apiSecret {
		t.Error("the API key is not sent as Basic credentials")
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type %q", ct)
	}
	f := *r.form.Load()
	if f.Get("To") != number || f.Get("Channel") != "sms" || len(f) != 2 {
		t.Errorf("form %v, want exactly To and Channel=sms", f)
	}
	if n := r.guard.requests.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestCheckVerification_Outcomes(t *testing.T) {
	for status, want := range map[string]bool{
		"approved": true, "pending": false, "canceled": false, "expired": false, "max_attempts_reached": false,
	} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t, 0, reply(http.StatusOK, verification(status)))
			ok, err := r.client.CheckVerification(context.Background(), verifySID, code)
			if err != nil || ok != want {
				t.Fatalf("(%v, %v), want (%v, nil)", ok, err, want)
			}
			req, f := r.last.Load(), *r.form.Load()
			if req.URL.Path != "/v2/Services/"+serviceSID+"/VerificationCheck" {
				t.Errorf("path %s", req.URL.Path)
			}
			if f.Get("VerificationSid") != verifySID || f.Get("Code") != code || f.Get("To") != "" {
				t.Errorf("form %v: a check names the verification id and the code only, never the number", f)
			}
		})
	}
	r := newRig(t, 0, reply(http.StatusOK, verification("something-new")))
	if _, err := r.client.CheckVerification(context.Background(), verifySID, code); !errors.Is(err, twilioverify.ErrUnavailable) {
		t.Errorf("an unknown status: %v, want ErrUnavailable", err)
	}
}

// Twilio's error answers map to fixed errors, with none of the response's
// text, and are never retried.
func TestErrorsAreFixedAndNeverRetried(t *testing.T) {
	echo := fmt.Sprintf(`{"code":60200,"message":"Invalid parameter To: %s, code %s","status":400}`, number, code)
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, twilioverify.ErrRejected},
		{http.StatusUnauthorized, twilioverify.ErrRejected},
		{http.StatusForbidden, twilioverify.ErrRejected},
		{http.StatusNotFound, twilioverify.ErrNotFound},
		{http.StatusTooManyRequests, twilioverify.ErrRateLimited},
		{http.StatusInternalServerError, twilioverify.ErrUnavailable},
		{http.StatusServiceUnavailable, twilioverify.ErrUnavailable},
		{http.StatusFound, twilioverify.ErrUnavailable},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			r := newRig(t, 0, func(w http.ResponseWriter, req *http.Request) {
				if tc.status == http.StatusFound {
					http.Redirect(w, req, "http://example.invalid/steal", http.StatusFound)
					return
				}
				reply(tc.status, echo)(w, req)
			})
			_, startErr := r.client.StartVerification(context.Background(), number)
			_, checkErr := r.client.CheckVerification(context.Background(), verifySID, code)
			for _, err := range []error{startErr, checkErr} {
				if !errors.Is(err, tc.want) {
					t.Errorf("err %v, want %v", err, tc.want)
				}
				noLeak(t, err)
			}
			if n := r.guard.requests.Load(); n != 2 {
				t.Errorf("%d requests for two calls: a call was retried or a redirect followed", n)
			}
		})
	}
}

// A response that names another account or service, or cannot be read, is
// not trusted.
func TestUntrustworthyResponsesAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"another account":      strings.Replace(verification("approved"), accountSID, "AC"+strings.Repeat("f", 32), 1),
		"another service":      strings.Replace(verification("approved"), serviceSID, "VA"+strings.Repeat("f", 32), 1),
		"another verification": strings.Replace(verification("approved"), verifySID, "VE"+strings.Repeat("f", 32), 1),
		"not json":             "<html>gateway</html>",
		"empty":                "",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 0, reply(http.StatusOK, body))
			if ok, err := r.client.CheckVerification(context.Background(), verifySID, code); ok || !errors.Is(err, twilioverify.ErrUnavailable) {
				t.Errorf("(%v, %v), want (false, ErrUnavailable)", ok, err)
			}
		})
	}
	r := newRig(t, 0, reply(http.StatusCreated, `{"sid":"not-a-sid","service_sid":"`+serviceSID+`","account_sid":"`+accountSID+`","status":"pending"}`))
	if _, err := r.client.StartVerification(context.Background(), number); !errors.Is(err, twilioverify.ErrUnavailable) {
		t.Errorf("a malformed verification id: %v, want ErrUnavailable", err)
	}
	r = newRig(t, 0, reply(http.StatusCreated, verification("approved")))
	if _, err := r.client.StartVerification(context.Background(), number); !errors.Is(err, twilioverify.ErrUnavailable) {
		t.Errorf("a new verification that is not pending: %v, want ErrUnavailable", err)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	r := newRig(t, 300*time.Millisecond, func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
		case <-req.Context().Done():
		}
	})
	defer close(release)
	start := time.Now()
	_, err := r.client.StartVerification(context.Background(), number)
	if !errors.Is(err, twilioverify.ErrUnavailable) {
		t.Fatalf("err %v, want ErrUnavailable", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("a stalled provider held the call for %v", took)
	}
	noLeak(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.client.CheckVerification(ctx, verifySID, code); !errors.Is(err, twilioverify.ErrUnavailable) {
		t.Errorf("a cancelled check: %v, want ErrUnavailable", err)
	}
}

// Arguments are checked before any request.
func TestInvalidArgumentsMakeNoRequest(t *testing.T) {
	r := newRig(t, 0, reply(http.StatusOK, verification("approved")))
	for _, n := range []string{"", "9876543210", "+91 98765 43210", "+0123456789", "+9198765432101234"} {
		if _, err := r.client.StartVerification(context.Background(), n); !errors.Is(err, twilioverify.ErrInvalidRequest) {
			t.Errorf("StartVerification(%q): %v", n, err)
		}
	}
	for _, c := range [][2]string{{verifySID, ""}, {verifySID, "12a456"}, {verifySID, "123"}, {verifySID, "12345678901"}, {"VEshort", code}, {"", code}} {
		if _, err := r.client.CheckVerification(context.Background(), c[0], c[1]); !errors.Is(err, twilioverify.ErrInvalidRequest) {
			t.Errorf("CheckVerification(%q, %q): %v", c[0], c[1], err)
		}
	}
	if n := r.guard.requests.Load(); n != 0 {
		t.Errorf("%d requests for invalid arguments, want 0", n)
	}
}

// New validates every value without echoing it, and makes no request.
func TestNew_ValidatesWithoutEchoingOrConnecting(t *testing.T) {
	valid := twilioverify.Config{AccountSID: accountSID, APIKeySID: apiKeySID, APIKeySecret: apiSecret, ServiceSID: serviceSID}
	for name, mutate := range map[string]func(*twilioverify.Config){
		"account SID missing":     func(c *twilioverify.Config) { c.AccountSID = "" },
		"account SID upper case":  func(c *twilioverify.Config) { c.AccountSID = strings.ToUpper(accountSID) },
		"API key SID is an AC":    func(c *twilioverify.Config) { c.APIKeySID = accountSID },
		"service SID missing":     func(c *twilioverify.Config) { c.ServiceSID = "" },
		"secret missing":          func(c *twilioverify.Config) { c.APIKeySecret = "" },
		"secret with a newline":   func(c *twilioverify.Config) { c.APIKeySecret = apiSecret + "\n" },
		"secret too short":        func(c *twilioverify.Config) { c.APIKeySecret = "short" },
		"negative timeout":        func(c *twilioverify.Config) { c.Timeout = -time.Second },
		"timeout too long":        func(c *twilioverify.Config) { c.Timeout = time.Minute },
		"base URL elsewhere":      func(c *twilioverify.Config) { c.BaseURL = "https://attacker.example/v2" },
		"base URL plain http":     func(c *twilioverify.Config) { c.BaseURL = "http://verify.twilio.com/v2" },
		"base URL with user info": func(c *twilioverify.Config) { c.BaseURL = "http://u:p@127.0.0.1:1/v2" },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			_, err := twilioverify.New(c)
			if !errors.Is(err, twilioverify.ErrInvalidConfig) {
				t.Fatalf("err %v, want ErrInvalidConfig", err)
			}
			for _, secret := range []string{apiSecret, accountSID, apiKeySID, serviceSID, "attacker"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error echoes %q", secret)
				}
			}
		})
	}
	// The default configuration targets Twilio Verify, and constructing it
	// sends nothing: its transport would fail the test.
	c := valid
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("New made a request")
		return nil, errors.New("blocked")
	})}
	if _, err := twilioverify.New(c); err != nil {
		t.Fatal(err)
	}
	if twilioverify.DefaultBaseURL != "https://verify.twilio.com/v2" {
		t.Errorf("DefaultBaseURL %q", twilioverify.DefaultBaseURL)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestValidVerificationSID(t *testing.T) {
	if !twilioverify.ValidVerificationSID(verifySID) {
		t.Error("a valid id was refused")
	}
	for _, s := range []string{"", "VE123", strings.ToUpper(verifySID), "VA" + verifySID[2:], verifySID + "0"} {
		if twilioverify.ValidVerificationSID(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

// fakeSID builds a made-up Twilio identifier of the documented shape (two
// letters and 32 lower-case hex digits) at run time, so that no
// credential-shaped literal sits in the source. None is a real identifier.
func fakeSID(a, b string) string { return a + b + strings.Repeat("0123456789abcdef", 2) }
