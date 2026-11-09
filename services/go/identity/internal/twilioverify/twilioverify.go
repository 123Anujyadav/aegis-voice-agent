// Package twilioverify is a client for the two Twilio Verify v2 operations
// identity needs: start a verification (Twilio generates the code and sends
// it by SMS) and check one (Twilio compares the code).
//
// DISABLED BY DEFAULT. Nothing builds this client unless
// CS_IDENTITY_OTP_PROVIDER is "twilio" and all four Twilio variables are set
// (internal/bootstrap). No credential, Verify service or route exists for
// this project, and production activation needs the owner's approvals first
// (docs/phase19/OTP-PROVIDERS.md §5, §6). The tests run against a local test
// server only.
//
// # Security rules
//
//   - Credentials stay on the server. The API key secret is read from a
//     mounted file, kept in memory, and sent only as HTTP Basic credentials to
//     Twilio over TLS. It is never logged or returned.
//   - Nothing is logged by this package. Errors are fixed values (below): they
//     never carry the number, the code, a credential, a header or any part of
//     Twilio's response, which may echo the number.
//   - No retries. A start retried after a lost answer could send a second SMS;
//     a check retried could count a second attempt at Twilio. The caller's
//     context and Config.Timeout bound every call.
//   - The response must name the configured account and Verify service, or it
//     is refused: an answer for another account is not an answer for us.
//   - Only the SMS channel is requested. No other channel, and no route
//     choice, is made here: India's domestic route and DLT are Twilio account
//     configuration (docs/phase19/OTP-PROVIDERS.md §5).
package twilioverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is Twilio Verify v2's API root.
const DefaultBaseURL = "https://verify.twilio.com/v2"

// Bounds.
const (
	// DefaultTimeout bounds one call when Config.Timeout is zero. The
	// identity request handler allows 10 s in total.
	DefaultTimeout = 4 * time.Second
	// MaxTimeout is the largest Config.Timeout accepted.
	MaxTimeout = 8 * time.Second
	// At most this much of a response is read.
	maxResponseBytes = 64 << 10
)

// Errors. Every error this package returns is one of these, or wraps one with
// no further text.
var (
	// ErrInvalidConfig is a configuration New refuses.
	ErrInvalidConfig = errors.New("twilioverify: invalid configuration")
	// ErrInvalidRequest is an argument refused before any call: a number
	// that is not E.164, a code that is not 4 to 10 digits, or a malformed
	// verification id.
	ErrInvalidRequest = errors.New("twilioverify: invalid request")
	// ErrRejected is Twilio refusing the request (a 4xx other than 404 and
	// 429): for example an unsupported number or a disabled channel.
	ErrRejected = errors.New("twilioverify: request rejected")
	// ErrNotFound is a verification Twilio no longer has: expired, already
	// approved, or out of check attempts.
	ErrNotFound = errors.New("twilioverify: verification not found")
	// ErrRateLimited is Twilio's own rate limit (429).
	ErrRateLimited = errors.New("twilioverify: rate limited")
	// ErrUnavailable is a transport failure, a timeout, a 5xx, or a response
	// that cannot be trusted (unreadable, or for another account or service).
	ErrUnavailable = errors.New("twilioverify: unavailable")
)

var (
	accountSIDPattern = regexp.MustCompile(`^AC[0-9a-f]{32}$`)
	apiKeySIDPattern  = regexp.MustCompile(`^SK[0-9a-f]{32}$`)
	serviceSIDPattern = regexp.MustCompile(`^VA[0-9a-f]{32}$`)
	verifySIDPattern  = regexp.MustCompile(`^VE[0-9a-f]{32}$`)
	secretPattern     = regexp.MustCompile(`^[0-9A-Za-z]{16,128}$`)
	e164Pattern       = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	codePattern       = regexp.MustCompile(`^\d{4,10}$`)
)

// ValidVerificationSID reports whether s is the shape of a Verify
// verification id ("VE" and 32 lower-case hex digits).
func ValidVerificationSID(s string) bool { return verifySIDPattern.MatchString(s) }

// Config configures a Client.
type Config struct {
	// AccountSID is the Twilio account ("AC" + 32 hex). It is not a secret;
	// responses must name it.
	AccountSID string
	// APIKeySID is the API key ("SK" + 32 hex), the Basic-auth user name.
	APIKeySID string
	// APIKeySecret is the API key's secret, the Basic-auth password. It is
	// read from a mounted file by the caller and never logged.
	APIKeySecret string
	// ServiceSID is the Verify service ("VA" + 32 hex).
	ServiceSID string
	// Timeout bounds each call; zero is DefaultTimeout.
	Timeout time.Duration
	// HTTPClient, if set, carries the requests (tests use a local server's
	// client). Its own Timeout is not relied on: every call has a deadline.
	HTTPClient *http.Client
	// BaseURL is the API root. Empty is DefaultBaseURL, which identity always
	// uses. Tests set a loopback test server. Anything else is refused, so
	// the credentials can only ever be sent to Twilio's API host or to this
	// machine.
	BaseURL string
}

// validBaseURL accepts DefaultBaseURL, or plain http on a loopback address
// (a test server).
func validBaseURL(raw string) bool {
	if raw == DefaultBaseURL {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// Client is a Twilio Verify v2 client. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// New validates cfg and returns a client. It makes no request.
func New(cfg Config) (*Client, error) {
	var problems []string
	for _, f := range []struct {
		name  string
		value string
		re    *regexp.Regexp
	}{
		{"account SID", cfg.AccountSID, accountSIDPattern},
		{"API key SID", cfg.APIKeySID, apiKeySIDPattern},
		{"Verify service SID", cfg.ServiceSID, serviceSIDPattern},
		{"API key secret", cfg.APIKeySecret, secretPattern},
	} {
		// The values are never echoed: a misplaced secret must not reach a log.
		if !f.re.MatchString(f.value) {
			problems = append(problems, "the "+f.name+" is missing or malformed")
		}
	}
	if cfg.BaseURL != "" && !validBaseURL(cfg.BaseURL) {
		problems = append(problems, "the base URL must be Twilio Verify's or a loopback test server's")
	}
	if cfg.Timeout < 0 || cfg.Timeout > MaxTimeout {
		problems = append(problems, fmt.Sprintf("the timeout must be within (0, %v]", MaxTimeout))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	// Never follow a redirect: the credentials must reach Twilio's API host
	// only, and a redirect is not an answer.
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &cp}, nil
}

// verificationResponse is the part of Twilio's verification resource read.
type verificationResponse struct {
	SID        string `json:"sid"`
	ServiceSID string `json:"service_sid"`
	AccountSID string `json:"account_sid"`
	Status     string `json:"status"`
	Channel    string `json:"channel"`
}

// StartVerification asks Twilio to generate a code and send it by SMS to
// msisdn (E.164). It returns the verification's id, which CheckVerification
// takes; it is not personal data and is safe to store.
func (c *Client) StartVerification(ctx context.Context, msisdn string) (string, error) {
	if !e164Pattern.MatchString(msisdn) {
		return "", ErrInvalidRequest
	}
	v, err := c.post(ctx, "/Verifications", url.Values{"To": {msisdn}, "Channel": {"sms"}})
	if err != nil {
		return "", err
	}
	if !verifySIDPattern.MatchString(v.SID) || v.Status != "pending" {
		return "", ErrUnavailable
	}
	return v.SID, nil
}

// CheckVerification asks Twilio whether code is right for the verification
// sid. It returns true only for "approved". A wrong code is (false, nil);
// a verification Twilio no longer has is ErrNotFound.
func (c *Client) CheckVerification(ctx context.Context, sid, code string) (bool, error) {
	if !verifySIDPattern.MatchString(sid) || !codePattern.MatchString(code) {
		return false, ErrInvalidRequest
	}
	v, err := c.post(ctx, "/VerificationCheck", url.Values{"VerificationSid": {sid}, "Code": {code}})
	if err != nil {
		return false, err
	}
	if v.SID != sid {
		return false, ErrUnavailable
	}
	switch v.Status {
	case "approved":
		return true, nil
	case "pending", "canceled", "expired", "max_attempts_reached", "failed", "deleted":
		return false, nil
	default:
		return false, ErrUnavailable
	}
}

// post sends one form request to the service's resource path and decodes the
// verification it answers with. See the package's security rules.
func (c *Client) post(ctx context.Context, resource string, form url.Values) (*verificationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/Services/" + c.cfg.ServiceSID + resource
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.SetBasicAuth(c.cfg.APIKeySID, c.cfg.APIKeySecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport's error may carry the URL; it is not returned.
		return nil, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // the body has been read or abandoned
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRateLimited
	case resp.StatusCode >= 500:
		return nil, ErrUnavailable
	case resp.StatusCode >= 400:
		return nil, ErrRejected
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, ErrUnavailable
	}
	if readErr != nil {
		return nil, ErrUnavailable
	}
	var v verificationResponse
	if json.Unmarshal(body, &v) != nil {
		return nil, ErrUnavailable
	}
	if v.AccountSID != c.cfg.AccountSID || v.ServiceSID != c.cfg.ServiceSID {
		return nil, ErrUnavailable
	}
	return &v, nil
}

// Name is the provider's fixed label in identity's observations.
func (*Client) Name() string { return "twilio-verify" }
