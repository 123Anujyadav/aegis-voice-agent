// Package platform provides the cross-cutting infrastructure every Go service
// in the CallScreen platform shares: configuration loading, structured logging
// with automatic redaction, health reporting, and lifecycle management.
//
// It contains no business logic and no domain types. See Phase 1 §4 for the
// admission rules that govern what may live here.
package platform

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConfigError reports one or more problems discovered while loading
// configuration. It aggregates every problem rather than failing on the first,
// because an operator correcting a misconfigured deployment needs the complete
// list — reporting one missing variable per restart cycle wastes minutes per
// iteration during an incident.
type ConfigError struct {
	// Problems holds one human-readable description per invalid or missing
	// field, sorted by field name for stable output.
	Problems []string
}

// Error implements the error interface, rendering every problem on its own
// line so the message is readable in a container log.
func (e *ConfigError) Error() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("configuration invalid (%d problem(s)):", len(e.Problems)))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

// ErrNotStructPointer is returned when LoadConfig is called with anything other
// than a non-nil pointer to a struct.
var ErrNotStructPointer = errors.New("platform: target must be a non-nil pointer to a struct")

// LoadConfig populates target from environment variables and validates it.
//
// It implements the environment-variable strategy defined in Phase 1 §11:
// configuration is typed, validated at boot, and a service with invalid
// configuration refuses to start rather than running on a silent default. A
// service that starts with the wrong configuration is more dangerous than one
// that does not start, because the failure surfaces later and further away.
//
// Struct tags:
//
//	env      Name of the environment variable. Required; a field without it is
//	         skipped entirely, which allows computed fields to coexist.
//	default  Value used when the variable is unset. A field with neither a
//	         default nor a value present is reported as missing.
//	required When "true", the field must resolve to a non-zero value even if a
//	         default is present. Use for values that have no safe default.
//	secret   When "true", the value is never echoed in error messages or logs.
//	         Redaction is applied at the point of error construction, not at the
//	         log sink, so a value cannot leak through an unexpected path.
//
// Supported field kinds: string, bool, int, int8..int64, uint, uint8..uint64,
// float32, float64, time.Duration, []string (comma-separated), and nested
// structs, which are traversed recursively so that configuration can be grouped
// by concern without flattening the environment namespace.
//
// The prefix argument is prepended to every env name, separated by an
// underscore. Passing "CS_EDGE_API" and a field tagged env:"PORT" resolves
// CS_EDGE_API_PORT, matching the naming convention in Phase 1 §5.
//
// LoadConfig returns a *ConfigError describing every problem found, or nil on
// success. It never panics and never partially applies: on error, target may
// have been written to and must be discarded.
func LoadConfig(target any, prefix string) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return ErrNotStructPointer
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return ErrNotStructPointer
	}

	problems := make([]string, 0, 8)
	populate(elem, strings.TrimSuffix(prefix, "_"), &problems)

	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}
	return nil
}

// populate walks a struct value, resolving each tagged field from the
// environment and recursing into nested structs. Problems are appended to the
// shared slice so that a single pass collects every fault.
func populate(structVal reflect.Value, prefix string, problems *[]string) {
	structType := structVal.Type()

	for i := 0; i < structVal.NumField(); i++ {
		field := structType.Field(i)
		fieldVal := structVal.Field(i)

		// Unexported fields cannot be set via reflection; skip them silently so
		// that a struct may hold private computed state.
		if !fieldVal.CanSet() {
			continue
		}

		// A nested struct groups related settings. time.Duration is a named
		// int64, not a struct, so it is handled by the scalar path below.
		if fieldVal.Kind() == reflect.Struct && fieldVal.Type() != reflect.TypeOf(time.Time{}) {
			nestedPrefix := prefix
			if sub, ok := field.Tag.Lookup("envPrefix"); ok && sub != "" {
				nestedPrefix = joinEnvName(prefix, sub)
			}
			populate(fieldVal, nestedPrefix, problems)
			continue
		}

		name, ok := field.Tag.Lookup("env")
		if !ok || name == "" {
			continue
		}

		envName := joinEnvName(prefix, name)
		secret := field.Tag.Get("secret") == "true"
		required := field.Tag.Get("required") == "true"
		defaultVal, hasDefault := field.Tag.Lookup("default")

		raw, present := os.LookupEnv(envName)
		switch {
		case present && raw != "":
			// Value supplied by the environment.
		case hasDefault:
			raw = defaultVal
		case required:
			*problems = append(*problems, fmt.Sprintf(
				"%s is required but not set", envName))
			continue
		default:
			*problems = append(*problems, fmt.Sprintf(
				"%s is not set and has no default", envName))
			continue
		}

		if err := assign(fieldVal, raw); err != nil {
			*problems = append(*problems, fmt.Sprintf(
				"%s: %s (got %s)", envName, err.Error(), displayValue(raw, secret)))
			continue
		}

		if required && fieldVal.IsZero() {
			*problems = append(*problems, fmt.Sprintf(
				"%s is required but resolved to a zero value", envName))
		}
	}
}

// joinEnvName concatenates a prefix and a name with a single underscore,
// tolerating an empty prefix so that unprefixed loads work unchanged.
func joinEnvName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}

// displayValue renders a value for inclusion in an error message, substituting
// a marker for anything tagged secret. Redacting here rather than at the log
// sink guarantees the value cannot escape through an error that is formatted
// somewhere unexpected.
func displayValue(raw string, secret bool) string {
	if secret {
		return "[REDACTED]"
	}
	return strconv.Quote(raw)
}

// assign parses raw into the given field according to its kind, returning a
// descriptive error when the text cannot be represented in the target type.
func assign(field reflect.Value, raw string) error {
	// time.Duration is an int64 with a bespoke textual form, so it must be
	// matched before the generic integer path.
	if field.Type() == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return errors.New("expected a duration such as 250ms, 5s or 2m")
		}
		field.SetInt(int64(d))
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)

	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.New("expected a boolean such as true, false, 1 or 0")
		}
		field.SetBool(b)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("expected a %d-bit signed integer", field.Type().Bits())
		}
		field.SetInt(n)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("expected a %d-bit unsigned integer", field.Type().Bits())
		}
		field.SetUint(n)

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, field.Type().Bits())
		if err != nil {
			return errors.New("expected a decimal number")
		}
		field.SetFloat(f)

	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element type %s", field.Type().Elem())
		}
		// An empty string yields an empty slice rather than a slice holding one
		// empty string, which is almost never what an operator intends.
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			field.Set(reflect.MakeSlice(field.Type(), 0, 0))
			return nil
		}
		parts := strings.Split(trimmed, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		field.Set(reflect.ValueOf(out))

	default:
		return fmt.Errorf("unsupported field kind %s", field.Kind())
	}

	return nil
}

// ServiceConfig is the configuration block every service embeds.
//
// It exists so that operational behaviour — port binding, log verbosity,
// shutdown grace, environment identity — is identical across all eight Go
// services. Divergence in these settings is a recurring source of confusion
// during incidents, where an engineer assumes one service behaves like its
// neighbour and it does not.
type ServiceConfig struct {
	// Name identifies the service, matching its directory under services/go.
	// It is attached to every log record, metric and trace span.
	Name string `env:"SERVICE_NAME" required:"true"`

	// Environment is one of development, staging or production. It gates
	// behaviour that must differ by tier, such as whether stack traces may be
	// returned to a client.
	Environment string `env:"ENVIRONMENT" default:"development"`

	// Region names the deployment region, for example "in-south-1". Recorded on
	// telemetry so that residency can be audited from logs alone.
	Region string `env:"REGION" default:"in-south-1"`

	// Version is the build version injected at container build time. Used to
	// attribute error-rate changes to a specific rollout.
	Version string `env:"VERSION" default:"dev"`

	// HTTPPort is the port the service's HTTP listener binds.
	HTTPPort int `env:"HTTP_PORT" default:"8080"`

	// HealthPort is a separate port for liveness and readiness probes. It is
	// deliberately distinct from HTTPPort so that probes keep answering when the
	// main listener is saturated or intentionally drained — a health check that
	// queues behind application traffic reports the wrong answer precisely when
	// the answer matters most.
	HealthPort int `env:"HEALTH_PORT" default:"8081"`

	// LogLevel is one of debug, info, warn or error.
	LogLevel string `env:"LOG_LEVEL" default:"info"`

	// LogFormat is either "json" for machine ingestion or "text" for local
	// development readability.
	LogFormat string `env:"LOG_FORMAT" default:"json"`

	// ShutdownTimeout bounds how long graceful shutdown may take before
	// in-flight work is abandoned. It must be shorter than the orchestrator's
	// termination grace period, or the process is killed mid-drain and the
	// graceful path never completes.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" default:"25s"`

	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers. Without it the service is trivially exhausted by slow-loris
	// connections, which is why Go's own linters flag a server that omits it.
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" default:"5s"`
}

// Validate checks the invariants that cannot be expressed in struct tags and
// returns a *ConfigError listing every violation.
func (c *ServiceConfig) Validate() error {
	problems := make([]string, 0, 4)

	switch c.Environment {
	case "development", "staging", "production":
	default:
		problems = append(problems, fmt.Sprintf(
			"ENVIRONMENT must be development, staging or production, got %q", c.Environment))
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf(
			"LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel))
	}

	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		problems = append(problems, fmt.Sprintf(
			"LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}

	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		problems = append(problems, fmt.Sprintf(
			"HTTP_PORT must be between 1 and 65535, got %d", c.HTTPPort))
	}
	if c.HealthPort < 1 || c.HealthPort > 65535 {
		problems = append(problems, fmt.Sprintf(
			"HEALTH_PORT must be between 1 and 65535, got %d", c.HealthPort))
	}
	if c.HTTPPort == c.HealthPort {
		problems = append(problems, fmt.Sprintf(
			"HTTP_PORT and HEALTH_PORT must differ, both are %d", c.HTTPPort))
	}

	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be positive")
	}

	// Text logging in production defeats log ingestion and is almost always an
	// accident inherited from a copied local configuration.
	if c.Environment == "production" && strings.EqualFold(c.LogFormat, "text") {
		problems = append(problems, "LOG_FORMAT must be json in production")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}
	return nil
}

// IsProduction reports whether the service is running in the production tier.
// Behaviour that must never be enabled in production — verbose error bodies,
// permissive CORS, seeded test data — gates on this.
func (c *ServiceConfig) IsProduction() bool {
	return c.Environment == "production"
}
