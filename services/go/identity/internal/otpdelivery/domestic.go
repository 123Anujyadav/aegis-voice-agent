package otpdelivery

// DELIVERY-ONLY SMS ADAPTERS: the shared rules.
//
// ADR-0033 (Proposed; the owner's preferred OTP architecture): identity
// generates, hashes and verifies every code, and an SMS provider only delivers
// a message identity composed. The adapters in this package (msg91.go,
// twilio.go) follow its §6 boundaries, and these helpers hold the rules they
// share:
//
//   - India only, domestic route: a number outside +91 mobile ranges is refused
//     before anything is sent (owner, 2026-11-10: India-only domestic launch
//     scope; OTP-PROVIDERS.md: "No international-route fallback"). This does
//     not decide ADR-0033 Q2, which is whether identity refuses such numbers at
//     sign-in.
//   - The message is a template registered under TRAI DLT, filled with the
//     code and nothing else; DLT requires the text to match exactly.
//   - Credentials come from mounted files, never the environment. They are
//     sent only to the provider's API host over TLS (or loopback, for tests),
//     and no redirect is followed.
//   - Errors are fixed values: never the provider's text, which may echo the
//     number or the code. Nothing is logged. No retry: Sender owns failover.
//
// NOT ACTIVATED. Bootstrap builds them only when CS_IDENTITY_OTP_PROVIDER
// names them, and then only with the F-1 controls (internal/bootstrap); no
// credential exists, and no DLT entity, header or template is registered
// (R1). ADR-0033 is accepted for M1 only (2026-11-12, rule 8a); MSG91 is the
// owner's selected primary provider (2026-11-13).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixed adapter errors. None carries provider text, the number or the code.
var (
	// ErrNotDomestic is a number outside India's mobile ranges.
	ErrNotDomestic = errors.New("otpdelivery: the number is not an Indian mobile number; only the domestic route is used")
	// ErrBadCode is a code that is not exactly six ASCII digits.
	ErrBadCode = errors.New("otpdelivery: the code is not six digits")
	// ErrRejected is a provider that refused the message.
	ErrRejected = errors.New("otpdelivery: the provider refused the message")
	// ErrProviderUnavailable is a provider that could not be reached, or
	// answered with a server error.
	ErrProviderUnavailable = errors.New("otpdelivery: the provider is unavailable")
	// ErrProviderConfig is an adapter configuration New refuses.
	ErrProviderConfig = errors.New("otpdelivery: invalid provider configuration")
)

// CodePlaceholder marks where the code goes in a message template. A template
// holds it exactly once.
const CodePlaceholder = "{code}"

// domesticNumber returns the national number for an Indian mobile number in
// identity's canonical form (+91 followed by ten digits starting 6-9).
func domesticNumber(msisdn string) (string, error) {
	national, ok := strings.CutPrefix(msisdn, "+91")
	if !ok || len(national) != 10 || national[0] < '6' || national[0] > '9' {
		return "", ErrNotDomestic
	}
	for _, r := range national {
		if r < '0' || r > '9' {
			return "", ErrNotDomestic
		}
	}
	return national, nil
}

func validCode(code string) error {
	if len(code) != 6 {
		return ErrBadCode
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return ErrBadCode
		}
	}
	return nil
}

// Template is a DLT-registered message text holding CodePlaceholder once.
type Template string

func (t Template) validate(name string) error {
	if strings.Count(string(t), CodePlaceholder) != 1 {
		return fmt.Errorf("%w: the %s template must contain %s exactly once", ErrProviderConfig, name, CodePlaceholder)
	}
	if len(t) > 300 {
		return fmt.Errorf("%w: the %s template is too long", ErrProviderConfig, name)
	}
	return nil
}

func (t Template) render(code string) string {
	return strings.Replace(string(t), CodePlaceholder, code, 1)
}

// readSecretFile reads one credential from a mounted file: an absolute path
// holding a single token.
func readSecretFile(what, path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %s must be an absolute path to a mounted Secret file", ErrProviderConfig, what)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is operator configuration naming a mounted Secret, checked absolute above
	if err != nil {
		return "", fmt.Errorf("%w: reading %s: %w", ErrProviderConfig, what, errors.Unwrap(err))
	}
	v := strings.TrimSpace(string(raw))
	if v == "" || strings.ContainsAny(v, "\r\n\t ") {
		return "", fmt.Errorf("%w: %s does not hold a single value", ErrProviderConfig, what)
	}
	return v, nil
}

// endpoint checks base against the provider's API host; loopback is allowed
// for tests. It returns the normalised base.
func endpoint(base, apiHost string) (string, error) {
	if base == "" {
		return "https://" + apiHost, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.Scheme != "https" {
		return "", fmt.Errorf("%w: the base URL must be https without credentials", ErrProviderConfig)
	}
	switch u.Hostname() {
	case apiHost, "localhost", "127.0.0.1", "::1":
		return strings.TrimRight(base, "/"), nil
	}
	return "", fmt.Errorf("%w: credentials are sent only to %s", ErrProviderConfig, apiHost)
}

// guardedClient never follows a redirect (the credential would follow it).
func guardedClient(supplied *http.Client) *http.Client {
	c := http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 5 * time.Second, MaxIdleConnsPerHost: 4,
	}}
	if supplied != nil {
		c = *supplied
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("otpdelivery: refusing to follow a redirect")
	}
	return &c
}

// errIPv4OnlyProxy is a request an IPv4-only client refuses because a proxy
// would carry it: the proxy, not this process, would then choose the address
// family and the source address the provider sees.
var errIPv4OnlyProxy = errors.New("otpdelivery: IPv4-only mode does not use a proxy")

// ipv4DialTimeout bounds one IPv4 connection attempt; the caller's context
// (the Sender's attempt bound) is usually shorter.
const ipv4DialTimeout = 5 * time.Second

// restrictToIPv4 makes c, a client from guardedClient, connect over IPv4 only,
// for a provider whose IP security allows only an IPv4 address (MSG91, owner
// approval of 2026-11-13). It changes nothing else: TLS configuration and
// certificate verification, timeouts and the redirect refusal are kept.
//
//   - Every connection is dialled as "tcp4": only IPv4 addresses are resolved
//     and dialled, so there is no IPv6 fallback, and an IPv6 address is
//     refused before any connection is made.
//   - A proxy is refused: a request the environment would send through a
//     proxy fails (errIPv4OnlyProxy), and so does construction when the
//     provider's own address (base) would be proxied.
//   - It fails closed: a client whose transport cannot be restricted (not an
//     *http.Transport, or one with its own TLS dialer) is a configuration
//     error, never silently unrestricted.
//
// c's transport is replaced by a restricted clone; a supplied transport is
// never modified.
func restrictToIPv4(c *http.Client, base string) error {
	var t *http.Transport
	switch tr := c.Transport.(type) {
	case nil:
		dt, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return fmt.Errorf("%w: IPv4-only mode needs a standard HTTP transport", ErrProviderConfig)
		}
		t = dt.Clone()
	case *http.Transport:
		if tr == nil {
			return fmt.Errorf("%w: IPv4-only mode needs a standard HTTP transport", ErrProviderConfig)
		}
		if tr.DialTLSContext != nil || tr.DialTLS != nil { //nolint:staticcheck // DialTLS is checked so that no dialer bypasses tcp4
			return fmt.Errorf("%w: IPv4-only mode cannot restrict a transport with its own TLS dialer", ErrProviderConfig)
		}
		t = tr.Clone()
	default:
		return fmt.Errorf("%w: IPv4-only mode needs a standard HTTP transport", ErrProviderConfig)
	}

	envProxy := t.Proxy
	if envProxy != nil {
		// A proxy decision reads only the request's URL; no request is made.
		target, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("%w: IPv4-only mode: the API address is not a URL", ErrProviderConfig)
		}
		if u, perr := envProxy(&http.Request{Method: http.MethodPost, URL: target, Header: http.Header{}}); perr != nil || u != nil {
			return fmt.Errorf("%w: IPv4-only mode cannot be used with a proxy for the provider's address", ErrProviderConfig)
		}
	}
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		if envProxy == nil {
			return nil, nil
		}
		u, err := envProxy(req)
		if err != nil || u != nil {
			return nil, errIPv4OnlyProxy
		}
		return nil, nil
	}
	dialer := &net.Dialer{Timeout: ipv4DialTimeout, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	t.Dial = nil //nolint:staticcheck // cleared so that only the tcp4 dialer remains
	// A custom dialer would otherwise switch HTTP/2 off; keep what the
	// unrestricted client negotiates.
	t.ForceAttemptHTTP2 = true
	c.Transport = t
	return nil
}

// statusError maps a provider's HTTP status to a fixed error.
func statusError(status int) error {
	if status >= 500 || status == http.StatusTooManyRequests {
		return ErrProviderUnavailable
	}
	return ErrRejected
}

// drain discards a bounded amount of a response body and closes it. The body
// is never read into an error.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10)) //nolint:errcheck // connection reuse is best-effort
	_ = resp.Body.Close()                                         //nolint:errcheck // the status already decided the outcome
}
