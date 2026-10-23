package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

// protectedRoutes is edge-api's production route table (internal/httpapi):
// path pattern to handler — today exactly GET /v1/me. Every entry is served
// ONLY behind auth.Middleware — there is no public application route, and no
// way to add one here. Health and readiness stay on the platform's separate
// health listener, which carries no application route.
//
// Because the table is not empty, edge-api REQUIRES authentication to be
// configured: without it, configureAuth refuses to start (errRoutesWithoutAuth)
// rather than serve these routes open or silently drop them.
func protectedRoutes(log *slog.Logger) map[string]http.Handler { return httpapi.Routes(log) }

type runnerRegistrar interface{ Register(platform.Runner) }

type checkRegistrar interface {
	Register(c platform.Checker, critical bool, timeout time.Duration)
}

// replayCheckTimeout bounds the readiness ping; well inside a typical 1s probe.
const replayCheckTimeout = 800 * time.Millisecond

// errRoutesWithoutAuth is a route table with no authentication to put in front
// of it: refused at startup, never served open.
var errRoutesWithoutAuth = errors.New("edge-api: protected routes are registered but authentication is not configured")

// authRuntime is what configureAuth needs of *auth.Runtime.
type authRuntime interface {
	Authenticator() *auth.Authenticator
	CheckDatabase(context.Context) error
	Close()
}

// openAuth builds the authenticator on the shared PostgreSQL replay store
// (auth.Open); replaceable in tests.
var openAuth = func(ctx context.Context, cfg auth.Config, log *slog.Logger) (authRuntime, error) {
	rt, err := auth.Open(ctx, cfg, nil, log)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// configureAuth applies the authentication activation policy (auth package):
// no variable set means the skeleton's behaviour; any set means all are
// required, and the authenticator is built — key file read, verifiers
// validated, replay database reached and its table checked — before the
// service starts. Any failure is returned and nothing is registered.
//
// An application listener is registered only when there are routes to
// protect. With none, the verified runtime is closed again at once: nothing
// would use the pool, and no port is opened.
func configureAuth(ctx context.Context, cfg platform.ServiceConfig, runners runnerRegistrar, health checkRegistrar, routes map[string]http.Handler, log *slog.Logger) error {
	ac, active, err := auth.LoadActivation()
	if err != nil {
		return err
	}
	if !active {
		if len(routes) > 0 {
			return errRoutesWithoutAuth
		}
		log.Info("edge authentication not configured; no application routes, no application listener")
		return nil
	}
	rt, err := openAuth(ctx, ac, log)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		rt.Close()
		log.Info("edge authentication configured and verified against the shared replay store; no protected routes exist, so no application listener is started")
		return nil
	}
	// The pool owner first, the listener LAST: platform.Service shuts runners
	// down in reverse registration order, and HTTPRunner.Shutdown waits for
	// in-flight requests, so every request finishes before the pool its replay
	// check uses is closed.
	runners.Register(&authRunner{rt: rt})
	health.Register(platform.CheckerFunc{CheckName: "edge-replay-store", Fn: rt.CheckDatabase}, true, replayCheckTimeout)
	runners.Register(platform.NewHTTPRunner("edge-api-http", cfg, applicationHandler(rt.Authenticator(), routes), log))
	log.Info("edge application listener registered; every route requires a DPoP-bound access token, replay-checked in PostgreSQL",
		slog.Int("routes", len(routes)), slog.Int("http_port", cfg.HTTPPort))
	return nil
}

// authRunner owns the authentication runtime's lifetime: it does no work of
// its own, and closes the replay store's pool when the service shuts down.
type authRunner struct{ rt authRuntime }

// Name implements platform.Runner.
func (r *authRunner) Name() string { return "edge-auth" }

// Run waits for shutdown; the runner has no work of its own.
func (r *authRunner) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Shutdown closes the authentication runtime, releasing its pool.
func (r *authRunner) Shutdown(context.Context) error {
	r.rt.Close()
	return nil
}

// applicationHandler is the application listener's handler: correlation and
// security headers outermost, then authentication, then the routes. Nothing
// reaches a route without passing auth.Middleware.
func applicationHandler(a *auth.Authenticator, routes map[string]http.Handler) http.Handler {
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.Handle(pattern, h)
	}
	return middleware.SecurityHeadersMiddleware(middleware.CorrelationIDMiddleware(a.Middleware(mux)))
}
