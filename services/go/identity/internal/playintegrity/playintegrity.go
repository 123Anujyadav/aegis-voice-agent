// Package playintegrity is the Google Play Integrity adapter behind
// app.IntegrityVerifier: it calls decodeIntegrityToken and maps the decoded
// verdict (tokenPayloadExternal) into an app.IntegrityVerdict, for
// IntegrityPolicy.Assess to judge (ADR-0010 §5(b), ADR-0031).
//
// WHAT THIS IS, AND WHAT IT IS NOT. This is the HTTP client and the response
// mapping, tested against a local fake of the API. It has NEVER called Google:
// that needs a Google Cloud project linked to the app in Play Console, a
// service account with access, credentials provisioned as a Secret, and
// egress, none of which exist. Obtaining an OAuth access token is the
// injected TokenSource's job (for example workload identity, or a service
// account), so no credential handling lives here. identity is not configured
// with this adapter: until it is, Play Integrity stays unavailable and every
// session is unverified, truthfully.
//
// # Errors
//
// A token Google refuses as invalid (HTTP 400) is app.ErrIntegrityTokenInvalid:
// the client presented something that is not a genuine token. Every other
// failure (no access token, our credentials refused (401/403), quota (429),
// an outage (5xx), a timeout, a malformed or oversized response) is a plain
// error, which the service treats as unavailability: the session is
// unverified, never certified, and never refused because Google could not
// answer. No error ever carries the integrity token, the access token, or
// Google's response text.
//
// A token must be decoded once. Google clears the verdicts of a token decoded
// again, and the verdict's requestHash binds it to one DPoP proof (ADR-0031
// D31.1), whose replay is refused before this is called.
package playintegrity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// DefaultEndpoint is the Play Integrity API's base URL.
const DefaultEndpoint = "https://playintegrity.googleapis.com"

const (
	// DefaultMaxResponseBytes bounds what is read from a response. A decoded
	// verdict is a few hundred bytes.
	DefaultMaxResponseBytes = 64 << 10
	// DefaultTimeout bounds one call when no HTTP client is supplied. The
	// service's IntegrityPolicy.DecodeTimeout bounds it as well.
	DefaultTimeout = 5 * time.Second
)

// TokenSource returns an OAuth 2.0 access token authorised for the Play
// Integrity API. Its implementation (and the credential behind it) belongs to
// the deployment, not to this package.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
}

// Config configures a Decoder.
type Config struct {
	// PackageName is the Android application id the tokens are for.
	PackageName string
	// Tokens supplies the access token for each call. Required.
	Tokens TokenSource
	// Endpoint is the API base URL; empty is DefaultEndpoint. It must be
	// https, except a loopback http URL, which only tests use.
	Endpoint string
	// HTTPClient is the client used; nil is a client with DefaultTimeout.
	HTTPClient *http.Client
	// MaxResponseBytes bounds the response read; zero is
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Decoder implements app.IntegrityVerifier against the Play Integrity API.
type Decoder struct {
	url    string
	tokens TokenSource
	client *http.Client
	max    int64
}

var _ app.IntegrityVerifier = (*Decoder)(nil)

// ErrInvalidConfig is a Config New refuses.
var ErrInvalidConfig = errors.New("playintegrity: invalid configuration")

// errUnavailable is every failure that is not a rejected token.
var errUnavailable = errors.New("playintegrity: the Play Integrity API could not answer")

// packagePattern is an Android application id: dot-separated segments, each a
// letter then letters, digits or underscores.
var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// New validates cfg and returns a Decoder.
func New(cfg Config) (*Decoder, error) {
	if !packagePattern.MatchString(cfg.PackageName) || len(cfg.PackageName) > 255 {
		return nil, fmt.Errorf("%w: PackageName must be an Android application id", ErrInvalidConfig)
	}
	if cfg.Tokens == nil {
		return nil, fmt.Errorf("%w: a TokenSource is required", ErrInvalidConfig)
	}
	endpoint, err := checkEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if cfg.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("%w: MaxResponseBytes must not be negative", ErrInvalidConfig)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &Decoder{
		url:    strings.TrimRight(endpoint, "/") + "/v1/" + url.PathEscape(cfg.PackageName) + ":decodeIntegrityToken",
		tokens: cfg.Tokens,
		client: client,
		max:    maxBytes,
	}, nil
}

// checkEndpoint returns the base URL to call: empty is DefaultEndpoint, and
// anything else must be a plain https base URL (loopback http for tests).
func checkEndpoint(endpoint string) (string, error) {
	if endpoint == "" {
		return DefaultEndpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%w: Endpoint is not a base URL", ErrInvalidConfig)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
		return "", fmt.Errorf("%w: Endpoint must be https", ErrInvalidConfig)
	}
	return endpoint, nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// Decode implements app.IntegrityVerifier: one decodeIntegrityToken call.
func (d *Decoder) Decode(ctx context.Context, token string) (app.IntegrityVerdict, error) {
	access, err := d.tokens.AccessToken(ctx)
	if err != nil || access == "" {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: no access token", errUnavailable)
	}
	body, err := json.Marshal(map[string]string{"integrity_token": token})
	if err != nil {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: encoding the request", errUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: building the request", errUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		// The transport error can carry the URL but never the tokens; it is
		// still not returned, so nothing request-specific leaves here.
		return app.IntegrityVerdict{}, fmt.Errorf("%w: transport failure", errUnavailable)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // the body has been read; a close failure changes nothing
	raw, err := io.ReadAll(io.LimitReader(resp.Body, d.max+1))
	if err != nil {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: reading the response", errUnavailable)
	}
	switch {
	case resp.StatusCode == http.StatusBadRequest:
		// INVALID_ARGUMENT: the token is not one Google can decode for this
		// app. The client presented it, so it is the client's failure.
		return app.IntegrityVerdict{}, app.ErrIntegrityTokenInvalid
	case resp.StatusCode != http.StatusOK:
		return app.IntegrityVerdict{}, fmt.Errorf("%w: HTTP %d", errUnavailable, resp.StatusCode)
	case int64(len(raw)) > d.max:
		return app.IntegrityVerdict{}, fmt.Errorf("%w: the response exceeds the size limit", errUnavailable)
	}
	return parse(raw)
}

// response is DecodeIntegrityTokenResponse, only the fields Assess judges.
// Unknown fields are ignored: Google adds optional verdicts over time.
type response struct {
	Payload *struct {
		RequestDetails *struct {
			RequestPackageName string `json:"requestPackageName"`
			RequestHash        string `json:"requestHash"`
			TimestampMillis    millis `json:"timestampMillis"`
		} `json:"requestDetails"`
		AppIntegrity struct {
			AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
			PackageName             string   `json:"packageName"`
			CertificateSha256Digest []string `json:"certificateSha256Digest"`
		} `json:"appIntegrity"`
		DeviceIntegrity struct {
			DeviceRecognitionVerdict []string `json:"deviceRecognitionVerdict"`
		} `json:"deviceIntegrity"`
		AccountDetails struct {
			AppLicensingVerdict string `json:"appLicensingVerdict"`
		} `json:"accountDetails"`
	} `json:"tokenPayloadExternal"`
}

// millis is timestampMillis, which the API sends as a JSON string of decimal
// milliseconds (int64 in proto3 JSON); a JSON number is accepted too.
type millis int64

// UnmarshalJSON accepts a positive decimal integer, quoted or not.
func (m *millis) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return errors.New("playintegrity: timestampMillis is not a positive integer")
	}
	*m = millis(n)
	return nil
}

// parse maps a 200 response into a verdict. A response without the payload or
// its requestDetails is not a verdict: unavailable, never a pass.
func parse(raw []byte) (app.IntegrityVerdict, error) {
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: malformed response", errUnavailable)
	}
	if r.Payload == nil || r.Payload.RequestDetails == nil || r.Payload.RequestDetails.TimestampMillis == 0 {
		return app.IntegrityVerdict{}, fmt.Errorf("%w: the response holds no verdict", errUnavailable)
	}
	p := r.Payload
	digests := make([]string, 0, len(p.AppIntegrity.CertificateSha256Digest))
	for _, dg := range p.AppIntegrity.CertificateSha256Digest {
		digests = append(digests, canonicalDigest(dg))
	}
	return app.IntegrityVerdict{
		RequestPackageName: p.RequestDetails.RequestPackageName,
		RequestHash:        p.RequestDetails.RequestHash,
		Timestamp:          time.UnixMilli(int64(p.RequestDetails.TimestampMillis)).UTC(),
		AppRecognition:     p.AppIntegrity.AppRecognitionVerdict,
		AppPackageName:     p.AppIntegrity.PackageName,
		CertificateDigests: digests,
		DeviceRecognition:  p.DeviceIntegrity.DeviceRecognitionVerdict,
		AppLicensing:       p.AccountDetails.AppLicensingVerdict,
	}, nil
}

// canonicalDigest is a certificate digest in the form IntegrityPolicy
// compares: base64url without padding. The API documents URL-safe base64; a
// padded or standard-alphabet value is normalised rather than silently
// failing to match a configured digest.
func canonicalDigest(d string) string {
	d = strings.TrimRight(d, "=")
	return strings.NewReplacer("+", "-", "/", "_").Replace(d)
}
