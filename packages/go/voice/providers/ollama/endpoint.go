package ollama

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// ENDPOINT POLICY: LOOPBACK ONLY.
//
// This adapter exists to drive a daemon on the developer's own machine
// (docs/phase19/LOCAL-MODEL-ADAPTER-TESTING.md). A prompt carries a caller's
// transcript, and a transcript is personal data (ADR-0006 §security, ADR-0012),
// so a configuration mistake must not be able to send one to another host.
// Three layers, each closing a gap the one before it leaves:
//
//  1. Configuration: the URL's host must be literally "localhost", "127.0.0.1"
//     or "::1". Anything else, including another loopback spelling, a name
//     that happens to resolve locally, or a URL carrying credentials, is a
//     configuration error at construction.
//  2. Dialling: the adapter's own client refuses to connect to any address
//     that is not a loopback IP, checked on the RESOLVED address. A hosts file
//     or resolver that maps "localhost" elsewhere is refused there.
//  3. Redirects: never followed, by any client. A 307 or 308 would re-send the
//     prompt to wherever the response points.
//
// A remote endpoint needs its own approved policy (residency, a DPA, consent:
// ADR-0012), not a flag on this adapter. docs/phase19/OLLAMA-ENDPOINT-POLICY.md.

// ErrNonLoopbackEndpoint is returned when a connection or a redirect would
// leave this machine.
var ErrNonLoopbackEndpoint = errors.New("ollama: endpoint is not on this machine (loopback only)")

// loopbackHosts are the only hosts a BaseURL may name.
var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// endpointProblem reports why u is outside the policy, or "" when it is inside.
func endpointProblem(u *url.URL) string {
	switch {
	case u.User != nil:
		return "BaseURL must not carry credentials"
	case !loopbackHosts[u.Hostname()]:
		return fmt.Sprintf("BaseURL host %q is not allowed: this adapter talks only to a "+
			"daemon on this machine (localhost, 127.0.0.1 or ::1)", u.Hostname())
	}
	return ""
}

// loopbackDialer dials only loopback addresses. Control runs after name
// resolution, on the address actually being connected to.
func loopbackDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrNonLoopbackEndpoint
			}
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				return ErrNonLoopbackEndpoint
			}
			return nil
		},
	}
}

// refuseRedirects is the CheckRedirect of every client this adapter uses.
func refuseRedirects(*http.Request, []*http.Request) error {
	return fmt.Errorf("%w: refusing to follow a redirect", ErrNonLoopbackEndpoint)
}

// defaultClient is the adapter's own client: loopback dialling, no proxy, no
// redirects.
//
// No client-level Timeout: it applies to the WHOLE exchange including the
// response body, so on a streaming response it would kill a healthy generation
// partway through. Bounding belongs on the context, which is per-request and
// which cancellation already uses.
func defaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // never: a proxy would carry the prompt off this machine
			DialContext:           loopbackDialer(2 * time.Second).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   4,
		},
		CheckRedirect: refuseRedirects,
	}
}

// guardedClient returns the client to use. A caller-supplied client keeps its
// transport, which is the caller's responsibility, but never follows a
// redirect: it is copied, never mutated.
func guardedClient(supplied *http.Client) *http.Client {
	if supplied == nil {
		return defaultClient()
	}
	c := *supplied
	c.CheckRedirect = refuseRedirects
	return &c
}
