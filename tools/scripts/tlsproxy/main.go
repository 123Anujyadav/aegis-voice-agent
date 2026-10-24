// Command tlsproxy terminates TLS on a loopback port and forwards every request
// to a plain-HTTP upstream on loopback. It exists only for
// tools/scripts/android-identity-contract.sh: identity serves plain HTTP
// behind a TLS-terminating load balancer, and the Android client signs DPoP
// proofs only for https URLs, so the contract test needs TLS in front of
// identity, as production has.
//
// Standard library only; built with GOWORK=off from this file alone. It binds
// and forwards to 127.0.0.1 only, and refuses anything else.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func main() {
	listen := flag.String("listen", "", "loopback address to serve TLS on, for example 127.0.0.1:8443")
	upstream := flag.String("upstream", "", "plain-HTTP loopback origin to forward to, for example http://127.0.0.1:8080")
	cert := flag.String("cert", "", "server certificate (PEM)")
	key := flag.String("key", "", "server private key (PEM)")
	flag.Parse()

	target, err := loopbackOrigin(*upstream)
	if err != nil {
		log.Fatalf("tlsproxy: -upstream: %v", err)
	}
	if host, _, splitErr := net.SplitHostPort(*listen); splitErr != nil || host != "127.0.0.1" {
		log.Fatalf("tlsproxy: -listen must be 127.0.0.1:<port>")
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           httputil.NewSingleHostReverseProxy(target),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("tlsproxy: https://%s -> %s", *listen, target)
	if err := server.ListenAndServeTLS(*cert, *key); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("tlsproxy: %v", err)
	}
}

// loopbackOrigin parses raw as an http origin on 127.0.0.1.
func loopbackOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("must be http://127.0.0.1:<port>")
	}
	return u, nil
}
