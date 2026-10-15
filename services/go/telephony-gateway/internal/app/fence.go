// Package app wires the telephony-gateway's components into the platform
// lifecycle.
//
// PHASE 19 P1-30B: the only component is the single-active-process fence
// (internal/fence; decision D6). Nothing here admits calls: there is no
// carrier adapter, no offer endpoint and no reconciliation worker yet.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/rdsauth"
	"github.com/jackc/pgx/v5"
)

// Fence configuration variables, appended to the service's env prefix
// (CS_TELEPHONY_GATEWAY_…). Every one is required to run the fence; NONE has
// a default — the heartbeat values are timing decisions (D10) and the lock key
// is a deployment identity.
const (
	// EnvDatabaseDSNFile names a file holding the telephony PostgreSQL
	// connection string (the edge-api/identity pattern: a mounted secret, never
	// an environment value).
	EnvDatabaseDSNFile = "DATABASE_DSN_FILE"
	// EnvFenceLockKey is the advisory-lock key (a non-zero 64-bit integer)
	// shared by every gateway process of one deployment.
	EnvFenceLockKey = "FENCE_LOCK_KEY"
	// EnvFenceHeartbeatInterval is how often ownership is re-confirmed
	// (a Go duration, e.g. what the owner approves under D10).
	EnvFenceHeartbeatInterval = "FENCE_HEARTBEAT_INTERVAL"
	// EnvFenceHeartbeatTimeout bounds one confirmation; shorter than the
	// interval.
	EnvFenceHeartbeatTimeout = "FENCE_HEARTBEAT_TIMEOUT"
	// EnvFenceAuthMode selects how the fence authenticates: "iam" (RDS IAM
	// tokens, C1) or "password" (development only, R2). Required; no default.
	EnvFenceAuthMode = "FENCE_AUTH_MODE"
	// EnvFenceAWSRegion is the AWS region for IAM token generation. Required in
	// IAM mode; must be absent in password mode (M3).
	EnvFenceAWSRegion = "FENCE_AWS_REGION"
	// EnvEnvironment is the platform's environment variable. The fence reads
	// whether it is EXPLICITLY present: the platform's implicit "development"
	// default never enables password mode (R2).
	EnvEnvironment = "ENVIRONMENT"
)

// AuthMode is the fence's database authentication mode.
type AuthMode string

// Authentication modes.
const (
	AuthPassword AuthMode = "password"
	AuthIAM      AuthMode = "iam"
)

// MaxDSNFileBytes bounds the DSN file read (same bound as edge-api).
const MaxDSNFileBytes = 8 << 10

// CheckTimeout bounds the readiness check's call into the fence. The check is
// an in-memory read, so this is a local bound, not a database or carrier
// timing (it is not a D10 value and does not affect fencing).
const CheckTimeout = time.Second

// ErrInvalidConfig is a fence configuration that is present but unusable.
var ErrInvalidConfig = errors.New("telephony-gateway: invalid fence configuration")

// Lookup reads one environment variable (os.LookupEnv in production).
type Lookup func(name string) (string, bool)

// FenceVariables returns the full names of the fence variables for prefix.
func FenceVariables(prefix string) []string {
	return []string{
		prefix + "_" + EnvDatabaseDSNFile,
		prefix + "_" + EnvFenceLockKey,
		prefix + "_" + EnvFenceHeartbeatInterval,
		prefix + "_" + EnvFenceHeartbeatTimeout,
		prefix + "_" + EnvFenceAuthMode,
		prefix + "_" + EnvFenceAWSRegion,
	}
}

// Indexes into FenceVariables.
const (
	varDSNFile = iota
	varLockKey
	varInterval
	varTimeout
	varAuthMode
	varRegion
)

// FenceSettings is the loaded fence configuration.
type FenceSettings struct {
	// Configured is false when NONE of the fence variables is present in the
	// environment. The gateway then starts but is never ready (fail closed).
	Configured bool
	// Missing names every unset variable (names only, never values).
	Missing []string
	Mode    AuthMode
	// Region is the IAM token region (IAM mode only).
	Region string
	// IAM is the statically validated IRSA identity (IAM mode only).
	IAM    rdsauth.Identity
	Config fence.Config
	Conn   *pgx.ConnConfig
}

// LoadFence reads the fence configuration.
//
//   - none of the six fence variables PRESENT: Configured=false, no error (the
//     service is never ready);
//   - otherwise it is a configuration attempt: the first five are required, a
//     present-but-empty value is invalid (never "unconfigured"), the region is
//     required in IAM mode and must be absent in password mode (M3);
//   - password mode requires ENVIRONMENT explicitly set to development (R2);
//   - IAM mode applies the static IAM rules (iam.go);
//   - errors name variables and rules, never values or the DSN file's content.
func LoadFence(prefix string, lookup Lookup) (FenceSettings, error) {
	names := FenceVariables(prefix)
	values, present, anyPresent := readFenceVariables(names, lookup)
	if !anyPresent {
		return FenceSettings{Configured: false, Missing: names}, nil
	}

	missing, problems := requiredFenceProblems(names, values, present)
	if len(missing) > 0 {
		return FenceSettings{}, fmt.Errorf("%w: incomplete; also set %s", ErrInvalidConfig, strings.Join(missing, ", "))
	}
	problems = append(problems, whitespaceProblems(names, values, present)...)
	mode := AuthMode(values[varAuthMode])
	problems = append(problems, authModeProblems(mode, names, values, present)...)
	cfg, timingProblems := parseFenceTimings(names, values)
	problems = append(problems, timingProblems...)
	if len(problems) > 0 {
		return FenceSettings{}, fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	if err := cfg.Validate(); err != nil {
		return FenceSettings{}, fmt.Errorf("%w: %s and %s: %w", ErrInvalidConfig, names[varInterval], names[varTimeout], err)
	}

	conn, err := fenceConnection(prefix, mode, lookup, names, values)
	if err != nil {
		return FenceSettings{}, err
	}
	s := FenceSettings{Configured: true, Mode: mode, Region: values[varRegion], Config: cfg, Conn: conn}
	if mode == AuthIAM {
		role, _ := lookup("AWS_ROLE_ARN")
		tokenFile, _ := lookup("AWS_WEB_IDENTITY_TOKEN_FILE")
		s.IAM = rdsauth.Identity{Region: values[varRegion], RoleARN: role, WebIdentityTokenFile: tokenFile}
		if s.IAM.Validate() != nil {
			return FenceSettings{}, fmt.Errorf("%w: the IRSA identity is incomplete or malformed (AWS_ROLE_ARN, AWS_WEB_IDENTITY_TOKEN_FILE)", ErrInvalidConfig)
		}
	}
	return s, nil
}

// readFenceVariables looks up every fence variable. The anyPresent result
// reports whether at least one is present (set, even if empty).
func readFenceVariables(names []string, lookup Lookup) (values []string, present []bool, anyPresent bool) {
	values = make([]string, len(names))
	present = make([]bool, len(names))
	for i, n := range names {
		values[i], present[i] = lookup(n)
		anyPresent = anyPresent || present[i]
	}
	return values, present, anyPresent
}

// requiredFenceProblems names the required variables (all but the region)
// that are absent, and reports those present but empty.
func requiredFenceProblems(names, values []string, present []bool) (missing, problems []string) {
	for i := varDSNFile; i <= varAuthMode; i++ {
		switch {
		case !present[i]:
			missing = append(missing, names[i])
		case values[i] == "":
			problems = append(problems, names[i]+" is set but empty")
		}
	}
	return missing, problems
}

// whitespaceProblems reports present values with surrounding whitespace.
func whitespaceProblems(names, values []string, present []bool) []string {
	var problems []string
	for i, v := range values {
		if present[i] && strings.TrimSpace(v) != v {
			problems = append(problems, names[i]+" has leading or trailing whitespace")
		}
	}
	return problems
}

// authModeProblems applies the mode rules: the region is required in IAM
// mode and must be absent in password mode (M3); any other non-empty mode is
// invalid (an empty one is already reported as set but empty).
func authModeProblems(mode AuthMode, names, values []string, present []bool) []string {
	switch mode {
	case AuthPassword:
		if present[varRegion] {
			return []string{names[varRegion] + " must not be set in password mode"}
		}
	case AuthIAM:
		if !present[varRegion] {
			return []string{names[varRegion] + " is required in IAM mode"}
		}
		if values[varRegion] == "" {
			return []string{names[varRegion] + " is set but empty"}
		}
	default:
		if values[varAuthMode] != "" {
			return []string{names[varAuthMode] + " must be iam or password"}
		}
	}
	return nil
}

// parseFenceTimings parses the lock key and the two durations. The Config is
// meaningful only when no problem is reported.
func parseFenceTimings(names, values []string) (fence.Config, []string) {
	var problems []string
	key, err := strconv.ParseInt(values[varLockKey], 10, 64)
	if err != nil || key == 0 {
		problems = append(problems, names[varLockKey]+" must be a non-zero 64-bit integer")
	}
	interval, err := time.ParseDuration(values[varInterval])
	if err != nil || interval <= 0 {
		problems = append(problems, names[varInterval]+" must be a positive duration")
	}
	timeout, err := time.ParseDuration(values[varTimeout])
	if err != nil || timeout <= 0 {
		problems = append(problems, names[varTimeout]+" must be a positive duration")
	}
	return fence.Config{LockKey: key, HeartbeatInterval: interval, HeartbeatTimeout: timeout}, problems
}

// fenceConnection applies the mode's environment rules and reads the DSN
// file: password mode only with ENVIRONMENT explicitly development (R2); IAM
// mode with the static IAM rules (iam.go). The mode is already validated.
func fenceConnection(prefix string, mode AuthMode, lookup Lookup, names, values []string) (*pgx.ConnConfig, error) {
	switch mode {
	case AuthPassword:
		envName := prefix + "_" + EnvEnvironment
		if env, set := lookup(envName); !set || env != "development" {
			return nil, fmt.Errorf("%w: password mode requires %s to be explicitly set to development", ErrInvalidConfig, envName)
		}
		return readDSNFile(names[varDSNFile], values[varDSNFile])
	case AuthIAM:
		if err := checkIAMEnvironment(prefix, lookup, values[varRegion]); err != nil {
			return nil, err
		}
		return readIAMDSN(names[varDSNFile], values[varDSNFile], values[varRegion])
	}
	return nil, nil
}

// readDSNFile reads and parses the DSN file (password mode). Errors name the
// variable only.
func readDSNFile(name, path string) (*pgx.ConnConfig, error) {
	dsn, err := readDSNText(name, path)
	if err != nil {
		return nil, err
	}
	conn, err := fence.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: the connection string cannot be parsed (value not shown)", ErrInvalidConfig, name)
	}
	return conn, nil
}

// readIAMDSN reads the DSN file and validates it twice: the raw text, then
// pgx's parsed configuration, which must agree exactly (B1).
func readIAMDSN(name, path, region string) (*pgx.ConnConfig, error) {
	dsn, err := readDSNText(name, path)
	if err != nil {
		return nil, err
	}
	want, err := validateIAMDSN(name, dsn)
	if err != nil {
		return nil, err
	}
	if !isWriterEndpoint(want.host, region) {
		return nil, fmt.Errorf("%w: %s: IAM mode requires the Aurora cluster WRITER endpoint in the configured region "+
			"(<cluster>.cluster-<id>.<region>.rds.amazonaws.com); reader, custom, proxy and instance endpoints, "+
			"CNAMEs and IP addresses are refused (C1, C3; host not shown)", ErrInvalidConfig, name)
	}
	conn, err := fence.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: the connection string cannot be parsed (value not shown)", ErrInvalidConfig, name)
	}
	if err := validateIAMParsed(name, want, conn); err != nil {
		return nil, err
	}
	return conn, nil
}

// readDSNText reads the DSN file's single line. Errors name the variable only.
func readDSNText(name, path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator-configured DSN file; reading it is the purpose
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: %s: the file does not exist", ErrInvalidConfig, name)
	case errors.Is(err, fs.ErrPermission):
		return "", fmt.Errorf("%w: %s: permission denied reading the file", ErrInvalidConfig, name)
	case err != nil:
		return "", fmt.Errorf("%w: %s: the file cannot be opened", ErrInvalidConfig, name)
	}
	defer f.Close() //nolint:errcheck // read-only file, already fully read: a Close error cannot lose data
	if info, serr := f.Stat(); serr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s: not a regular file", ErrInvalidConfig, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxDSNFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s: the file cannot be read", ErrInvalidConfig, name)
	}
	if len(data) > MaxDSNFileBytes {
		return "", fmt.Errorf("%w: %s: the file is larger than %d bytes", ErrInvalidConfig, name, MaxDSNFileBytes)
	}
	dsn := strings.TrimSpace(string(data))
	switch {
	case dsn == "":
		return "", fmt.Errorf("%w: %s: the file holds no connection string", ErrInvalidConfig, name)
	case strings.ContainsAny(dsn, "\r\n\x00"):
		return "", fmt.Errorf("%w: %s: the connection string spans more than one line", ErrInvalidConfig, name)
	}
	return dsn, nil
}

// notConfigured keeps readiness false while the fence is unconfigured.
type notConfigured struct{ missing []string }

// Name implements platform.Checker: the fence's health-check name.
func (notConfigured) Name() string { return fence.Name }

// Check implements platform.Checker: always not ready (fence.ErrNotOwner),
// naming the unset variables.
func (n notConfigured) Check(context.Context) error {
	return fmt.Errorf("%w: not configured; set %s", fence.ErrNotOwner, strings.Join(n.missing, ", "))
}

// WireFence adds the fence to the service lifecycle. Call it BEFORE
// registering any other runner: runners shut down in reverse order, so the
// fence — the lowest-level component — stops last, after anything that does
// work under it.
//
// The fence is a CRITICAL readiness check: /readyz stays 503 until this
// process owns the fence, and returns to 503 the moment ownership is lost or
// becomes uncertain. Unconfigured, the service still starts (health is served,
// so it can be diagnosed) but is never ready. A present-but-invalid
// configuration is returned as an error: the service must not start.
//
// It returns the fence (nil when unconfigured) so later work can check
// Owned() immediately before acting. Ownership is NOT a guarantee about work
// already started: a carrier request issued before a loss is not recalled.
func WireFence(svc *platform.Service, health *platform.Health, prefix string, lookup Lookup, log *slog.Logger) (*fence.Fence, error) {
	if svc == nil || health == nil || lookup == nil || log == nil {
		return nil, errors.New("telephony-gateway: WireFence requires a service, health, lookup and logger")
	}
	s, err := LoadFence(prefix, lookup)
	if err != nil {
		return nil, err
	}
	if !s.Configured {
		log.Error("single-active-process fence NOT configured; this instance will never report ready",
			slog.Any("missing", s.Missing))
		health.Register(notConfigured{missing: s.Missing}, true, CheckTimeout)
		return nil, nil
	}
	var dial fence.Dialer
	switch s.Mode {
	case AuthPassword:
		dial = fence.PgxDialer(s.Conn)
	case AuthIAM:
		// No fallback to any other authentication, ever (R6).
		if iamCredentials == nil {
			return nil, ErrIAMAdapterUnavailable
		}
		creds, cerr := iamCredentials(s.IAM)
		if cerr != nil {
			return nil, fmt.Errorf("%w: the IAM credential source could not be constructed (details withheld)", ErrInvalidConfig)
		}
		if dial, err = fence.PgxCredentialDialer(s.Conn, creds); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: unknown authentication mode", ErrInvalidConfig)
	}
	f, err := fence.New(s.Config, dial, log)
	if err != nil {
		return nil, err
	}
	svc.Register(f)
	health.Register(f, true, CheckTimeout)
	// The lock key is not a secret; logging it lets an operator confirm that
	// every gateway process of one deployment uses the SAME key — processes
	// with different keys would each own "the" fence (P1-31).
	log.Info("single-active-process fence configured; ready only while this instance owns it",
		slog.Int64("lock_key", s.Config.LockKey),
		slog.String("auth_mode", string(s.Mode)),
		slog.Duration("heartbeat_interval", s.Config.HeartbeatInterval),
		slog.Duration("heartbeat_timeout", s.Config.HeartbeatTimeout),
		slog.Duration("acquisition_grace", s.Config.Validity()))
	return f, nil
}
