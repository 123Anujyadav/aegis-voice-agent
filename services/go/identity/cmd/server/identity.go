package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/telemetry"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/bootstrap"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/httpapi"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
)

// Identity activation. See bootstrap's activation documentation for the
// contract; in short: no identity variable set means the skeleton boots as it
// always has (health listener only, no application port), some set means all
// are required, and all set builds the service on PostgreSQL and serves the
// identity API (internal/httpapi) on the application port.

// errIdentityInactive is the readiness message while identity is inactive. The
// check is non-critical, so readiness stays 200 ("degraded") exactly as the
// skeleton's did, while saying plainly that no identity function is available.
var errIdentityInactive = errors.New("identity inactive: no CS_IDENTITY_* security configuration supplied")

// dbCheckTimeout bounds the readiness ping; well inside a typical 1s probe.
const dbCheckTimeout = 800 * time.Millisecond

// runnerRegistrar is what configureIdentity needs of *platform.Service:
// runner registration, and the GET /metrics handler on the health listener.
type runnerRegistrar interface {
	Register(platform.Runner)
	httpapi.MetricsMount
}

type checkRegistrar interface {
	Register(c platform.Checker, critical bool, timeout time.Duration)
}

// identityTracer builds the tracer for the API listener's edge spans
// (ADR-0026). Spans are written through the service's redacting logger by
// telemetry.LogExporter; no external exporter is configured (ADR-0028 is
// conditional on SRE checks). Replaceable in tests.
var identityTracer = func(log *slog.Logger) runtime.Tracer {
	return telemetry.NewRecordingTracer(telemetry.NewLogExporter(log))
}

// openIdentity builds the activated runtime; replaceable in tests.
var openIdentity = bootstrap.Open

// configureIdentity applies the activation policy. It returns an error — and
// registers nothing — for partial or invalid configuration and for any
// construction failure, including an unreachable database.
func configureIdentity(ctx context.Context, svcCfg platform.ServiceConfig, runners runnerRegistrar, health checkRegistrar, log *slog.Logger) error {
	cfg, active, err := bootstrap.LoadActivation()
	if err != nil {
		return err
	}
	if !active {
		health.Register(platform.CheckerFunc{
			CheckName: "identity",
			Fn:        func(context.Context) error { return errIdentityInactive },
		}, false, dbCheckTimeout)
		log.Warn("identity inactive: no identity configuration supplied; no identity service constructed")
		return nil
	}
	rt, err := openIdentity(ctx, cfg, nil)
	if err != nil {
		return err
	}
	// The pool is released at shutdown by the runner; readiness fails while the
	// database cannot be reached.
	runners.Register(&identityRunner{rt: rt})
	health.Register(platform.CheckerFunc{CheckName: "identity-postgres", Fn: rt.CheckDatabase}, true, dbCheckTimeout)
	// Registered AFTER the pool owner, so shutdown — which runs in reverse —
	// stops housekeeping before the pool it uses is closed. Its readiness check
	// is non-critical: see bootstrap.HousekeepingRunner.
	hk := bootstrap.NewHousekeepingRunner(rt.Housekeeper(), bootstrap.HousekeepingInterval, log)
	runners.Register(hk)
	health.Register(platform.CheckerFunc{CheckName: "identity-housekeeping", Fn: hk.Check}, false, dbCheckTimeout)
	// The application listener, registered LAST: platform.Service shuts
	// runners down in reverse registration order, and HTTPRunner.Shutdown
	// stops accepting connections and waits for in-flight requests. So intake
	// stops, and every request in progress finishes, before housekeeping stops
	// and before the pool the handlers use is closed. It exists only here — an
	// inactive identity binds no application port.
	apiMetrics := httpapi.NewMetrics()
	runners.Register(platform.NewHTTPRunner("identity-http", svcCfg, httpapi.New(rt.Service, log, apiMetrics, identityTracer(log)), log))
	// GET /metrics on the health listener (ADR-0013): every response the API
	// decides, by endpoint and outcome (httpapi.MetricRequests). It shares
	// HealthPort and is never served on the application port.
	apiMetrics.MountOn(runners, serviceName)
	// Security audit events (wrong codes, lockouts, refresh-token reuse) are
	// counted in the same registry (httpapi.MetricSecurityEvents).
	apiMetrics.Instrument(rt.Service)
	log.Info("identity service constructed on PostgreSQL; HTTP API registered",
		slog.Int("http_port", svcCfg.HTTPPort))
	return nil
}

// identityRunner owns the activated runtime's lifetime: it does no work of its
// own, and closes the database pool when the service shuts down.
type identityRunner struct{ rt *bootstrap.Runtime }

// Name implements platform.Runner.
func (r *identityRunner) Name() string { return "identity" }

// Run implements platform.Runner: it waits for shutdown.
func (r *identityRunner) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Shutdown implements platform.Runner: it closes the runtime's pool.
func (r *identityRunner) Shutdown(context.Context) error {
	r.rt.Close()
	return nil
}

// runMigrate is `service migrate`: it applies the identity schema with
// pgstore.Migrate, using the same CS_IDENTITY_DATABASE_DSN_FILE as the
// service, and needs no other identity variable. It is the ONLY path that
// applies DDL; the service never migrates at startup. It prints migration
// names and the resulting version — never the DSN — and any error is returned,
// so the process exits non-zero.
func runMigrate(ctx context.Context, out io.Writer) error {
	const name = bootstrap.EnvPrefix + "_DATABASE_DSN_FILE"
	path, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(path) == "" {
		return fmt.Errorf("migrate: %s is required", name)
	}
	dsn, err := bootstrap.ReadDSNFile(path)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	connectCtx, cancel := context.WithTimeout(ctx, bootstrap.ConnectTimeout)
	defer cancel()
	policy := pgshared.PolicyForEnvironment(os.LookupEnv(bootstrap.EnvPrefix + "_ENVIRONMENT"))
	pool, err := pgstore.OpenPool(connectCtx, dsn, policy)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer pool.Close()
	res, err := pgstore.Migrate(ctx, pool)
	if werr := reportMigration(out, res); werr != nil {
		return werr
	}
	if err != nil {
		return fmt.Errorf("migrate: %w", pgstore.Redact(err))
	}
	if _, werr := fmt.Fprintf(out, "identity schema at version %d\n", res.Version); werr != nil {
		return fmt.Errorf("migrate: writing the result: %w", werr)
	}
	return nil
}

// reportMigration prints which migrations were already applied and which were
// applied now. A write that fails is an error: an operator must not be left
// believing a migration report they never saw.
func reportMigration(out io.Writer, res pgstore.MigrationResult) error {
	for _, m := range res.AlreadyApplied {
		if _, err := fmt.Fprintf(out, "already applied: %s\n", m); err != nil {
			return fmt.Errorf("migrate: writing the result: %w", err)
		}
	}
	for _, m := range res.Applied {
		if _, err := fmt.Fprintf(out, "applied: %s\n", m); err != nil {
			return fmt.Errorf("migrate: writing the result: %w", err)
		}
	}
	return nil
}
