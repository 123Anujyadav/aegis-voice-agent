package platform

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestLoadConfig_RejectsNonStructPointer verifies the guard that prevents a
// caller passing a value instead of a pointer. Without it, reflection would
// silently write to a copy and the caller would receive zero-valued config.
func TestLoadConfig_RejectsNonStructPointer(t *testing.T) {
	t.Parallel()

	type valid struct {
		Field string `env:"FIELD" default:"x"`
	}

	tests := []struct {
		name   string
		target any
	}{
		{name: "nil interface", target: nil},
		{name: "struct value not pointer", target: valid{}},
		{name: "pointer to non-struct", target: new(string)},
		{name: "typed nil pointer", target: (*valid)(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := LoadConfig(tt.target, "CS")
			if !errors.Is(err, ErrNotStructPointer) {
				t.Fatalf("expected ErrNotStructPointer, got %v", err)
			}
		})
	}
}

// TestLoadConfig_ScalarKinds exercises every supported field kind through the
// environment, confirming that parsing and assignment work end to end.
func TestLoadConfig_ScalarKinds(t *testing.T) {
	type cfg struct {
		Str      string        `env:"STR"`
		Bool     bool          `env:"BOOL"`
		Int      int           `env:"INT"`
		Int64    int64         `env:"INT64"`
		Uint     uint          `env:"UINT"`
		Float    float64       `env:"FLOAT"`
		Duration time.Duration `env:"DURATION"`
		List     []string      `env:"LIST"`
	}

	t.Setenv("CS_TEST_STR", "hello")
	t.Setenv("CS_TEST_BOOL", "true")
	t.Setenv("CS_TEST_INT", "-42")
	t.Setenv("CS_TEST_INT64", "9007199254740993")
	t.Setenv("CS_TEST_UINT", "42")
	t.Setenv("CS_TEST_FLOAT", "3.5")
	t.Setenv("CS_TEST_DURATION", "250ms")
	t.Setenv("CS_TEST_LIST", "a, b ,c")

	var c cfg
	if err := LoadConfig(&c, "CS_TEST"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Str != "hello" {
		t.Errorf("Str = %q, want %q", c.Str, "hello")
	}
	if !c.Bool {
		t.Error("Bool = false, want true")
	}
	if c.Int != -42 {
		t.Errorf("Int = %d, want -42", c.Int)
	}
	if c.Int64 != 9007199254740993 {
		t.Errorf("Int64 = %d, want 9007199254740993", c.Int64)
	}
	if c.Uint != 42 {
		t.Errorf("Uint = %d, want 42", c.Uint)
	}
	if c.Float != 3.5 {
		t.Errorf("Float = %v, want 3.5", c.Float)
	}
	if c.Duration != 250*time.Millisecond {
		t.Errorf("Duration = %v, want 250ms", c.Duration)
	}
	// Whitespace around comma-separated entries must be trimmed; operators
	// routinely add spaces for readability in a Kubernetes manifest.
	if len(c.List) != 3 || c.List[0] != "a" || c.List[1] != "b" || c.List[2] != "c" {
		t.Errorf("List = %#v, want [a b c]", c.List)
	}
}

// TestLoadConfig_EmptyListYieldsEmptySlice guards a subtle bug: splitting an
// empty string on "," produces a one-element slice containing "", which reads
// as "one configured value that is blank" rather than "nothing configured".
func TestLoadConfig_EmptyListYieldsEmptySlice(t *testing.T) {
	type cfg struct {
		List []string `env:"LIST" default:""`
	}

	var c cfg
	if err := LoadConfig(&c, "CS_EMPTY"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.List == nil {
		t.Fatal("List is nil, want an initialised empty slice")
	}
	if len(c.List) != 0 {
		t.Errorf("List = %#v, want empty", c.List)
	}
}

// TestLoadConfig_DefaultsApplied confirms that an unset variable falls back to
// its declared default rather than producing an error.
func TestLoadConfig_DefaultsApplied(t *testing.T) {
	type cfg struct {
		Port    int           `env:"PORT" default:"8080"`
		Timeout time.Duration `env:"TIMEOUT" default:"30s"`
	}

	var c cfg
	if err := LoadConfig(&c, "CS_DEFAULTS"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Port != 8080 {
		t.Errorf("Port = %d, want 8080", c.Port)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.Timeout)
	}
}

// TestLoadConfig_EmptyEnvFallsBackToDefault covers the case where a variable is
// present but blank. Kubernetes emits empty strings for unset ConfigMap keys,
// so treating "present but empty" as "unset" avoids a class of deploy failure.
func TestLoadConfig_EmptyEnvFallsBackToDefault(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT" default:"8080"`
	}

	t.Setenv("CS_BLANK_PORT", "")

	var c cfg
	if err := LoadConfig(&c, "CS_BLANK"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Port != 8080 {
		t.Errorf("Port = %d, want 8080 from default", c.Port)
	}
}

// TestLoadConfig_AggregatesAllProblems verifies the design decision that
// loading reports every fault at once. Reporting one per restart would make
// correcting a broken deployment an iterative guessing game.
func TestLoadConfig_AggregatesAllProblems(t *testing.T) {
	type cfg struct {
		A string `env:"A" required:"true"`
		B int    `env:"B" required:"true"`
		C bool   `env:"C" required:"true"`
	}

	var c cfg
	err := LoadConfig(&c, "CS_MISSING")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *ConfigError, got %T", err)
	}
	if len(cfgErr.Problems) != 3 {
		t.Fatalf("expected 3 problems, got %d: %v", len(cfgErr.Problems), cfgErr.Problems)
	}
	for _, name := range []string{"CS_MISSING_A", "CS_MISSING_B", "CS_MISSING_C"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error message missing %s: %s", name, err.Error())
		}
	}
}

// TestLoadConfig_SecretsAreRedactedInErrors is a security regression guard.
// A parse failure must never echo the offending value when the field is marked
// secret, because error text reaches logs, crash reports and support tickets.
func TestLoadConfig_SecretsAreRedactedInErrors(t *testing.T) {
	type cfg struct {
		Token int `env:"TOKEN" secret:"true"`
	}

	const leaked = "super-secret-value"
	t.Setenv("CS_SEC_TOKEN", leaked)

	var c cfg
	err := LoadConfig(&c, "CS_SEC")
	if err == nil {
		t.Fatal("expected a parse error, got nil")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Fatalf("secret value leaked into error message: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker, got: %s", err.Error())
	}
}

// TestLoadConfig_NonSecretValueIsShown confirms redaction is targeted rather
// than blanket — an operator debugging a malformed port needs to see the value.
func TestLoadConfig_NonSecretValueIsShown(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT"`
	}

	t.Setenv("CS_SHOW_PORT", "not-a-number")

	var c cfg
	err := LoadConfig(&c, "CS_SHOW")
	if err == nil {
		t.Fatal("expected a parse error, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("expected the offending value in the message, got: %s", err.Error())
	}
}

// TestLoadConfig_NestedStructs verifies recursion into grouped configuration
// and that envPrefix extends the namespace correctly.
func TestLoadConfig_NestedStructs(t *testing.T) {
	type database struct {
		Host string `env:"HOST" default:"localhost"`
		Port int    `env:"PORT" default:"5432"`
	}
	type cfg struct {
		Service string   `env:"NAME" default:"svc"`
		DB      database `envPrefix:"DB"`
	}

	t.Setenv("CS_NEST_DB_HOST", "db.internal")

	var c cfg
	if err := LoadConfig(&c, "CS_NEST"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DB.Host != "db.internal" {
		t.Errorf("DB.Host = %q, want db.internal", c.DB.Host)
	}
	if c.DB.Port != 5432 {
		t.Errorf("DB.Port = %d, want 5432 from default", c.DB.Port)
	}
}

// TestLoadConfig_UnsupportedKind ensures an unsupported field type is reported
// clearly at boot rather than being silently ignored, which would leave the
// field at its zero value and produce confusing behaviour much later.
func TestLoadConfig_UnsupportedKind(t *testing.T) {
	type cfg struct {
		Weird map[string]string `env:"WEIRD" default:"x"`
	}

	var c cfg
	err := LoadConfig(&c, "CS_BAD")
	if err == nil {
		t.Fatal("expected an error for an unsupported kind, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported field kind") {
		t.Errorf("unexpected message: %s", err.Error())
	}
}

// TestLoadConfig_UntaggedFieldsIgnored confirms that fields without an env tag
// are left alone, permitting computed or injected state on a config struct.
func TestLoadConfig_UntaggedFieldsIgnored(t *testing.T) {
	type cfg struct {
		Tagged   string `env:"TAGGED" default:"set"`
		Untagged string
		private  string //nolint:unused // exercises the CanSet guard
	}

	var c cfg
	if err := LoadConfig(&c, "CS_TAGS"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Tagged != "set" {
		t.Errorf("Tagged = %q, want set", c.Tagged)
	}
	if c.Untagged != "" {
		t.Errorf("Untagged = %q, want empty", c.Untagged)
	}
}

// TestServiceConfig_Validate covers each invariant that cannot be expressed as
// a struct tag, including the production-only rules.
func TestServiceConfig_Validate(t *testing.T) {
	t.Parallel()

	base := func() ServiceConfig {
		return ServiceConfig{
			Name:              "edge-api",
			Environment:       "development",
			Region:            "in-south-1",
			Version:           "dev",
			HTTPPort:          8080,
			HealthPort:        8081,
			LogLevel:          "info",
			LogFormat:         "json",
			ShutdownTimeout:   25 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
		}
	}

	tests := []struct {
		name      string
		mutate    func(*ServiceConfig)
		wantValid bool
		wantHint  string
	}{
		{name: "valid baseline", mutate: func(*ServiceConfig) {}, wantValid: true},
		{
			name:     "unknown environment",
			mutate:   func(c *ServiceConfig) { c.Environment = "qa" },
			wantHint: "ENVIRONMENT",
		},
		{
			name:     "unknown log level",
			mutate:   func(c *ServiceConfig) { c.LogLevel = "trace" },
			wantHint: "LOG_LEVEL",
		},
		{
			name:     "unknown log format",
			mutate:   func(c *ServiceConfig) { c.LogFormat = "xml" },
			wantHint: "LOG_FORMAT",
		},
		{
			name:     "http port out of range",
			mutate:   func(c *ServiceConfig) { c.HTTPPort = 70000 },
			wantHint: "HTTP_PORT",
		},
		{
			name:     "health port zero",
			mutate:   func(c *ServiceConfig) { c.HealthPort = 0 },
			wantHint: "HEALTH_PORT",
		},
		{
			name:     "ports collide",
			mutate:   func(c *ServiceConfig) { c.HealthPort = c.HTTPPort },
			wantHint: "must differ",
		},
		{
			name:     "non positive shutdown timeout",
			mutate:   func(c *ServiceConfig) { c.ShutdownTimeout = 0 },
			wantHint: "SHUTDOWN_TIMEOUT",
		},
		{
			name: "text logging rejected in production",
			mutate: func(c *ServiceConfig) {
				c.Environment = "production"
				c.LogFormat = "text"
			},
			wantHint: "json in production",
		},
		{
			name: "json logging accepted in production",
			mutate: func(c *ServiceConfig) {
				c.Environment = "production"
				c.LogFormat = "json"
			},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := base()
			tt.mutate(&c)
			err := c.Validate()

			if tt.wantValid {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			if tt.wantHint != "" && !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.wantHint)
			}
		})
	}
}

// TestServiceConfig_IsProduction locks in the exact string comparison, since
// security-relevant behaviour gates on this method.
func TestServiceConfig_IsProduction(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"production":  true,
		"staging":     false,
		"development": false,
		"Production":  false, // case-sensitive by design; Validate rejects this
		"":            false,
	}

	for env, want := range tests {
		c := ServiceConfig{Environment: env}
		if got := c.IsProduction(); got != want {
			t.Errorf("IsProduction() with Environment=%q = %v, want %v", env, got, want)
		}
	}
}

// TestConfigError_ErrorFormatsEveryProblem confirms the aggregate message lists
// each problem on its own line for readability in a container log.
func TestConfigError_ErrorFormatsEveryProblem(t *testing.T) {
	t.Parallel()

	err := &ConfigError{Problems: []string{"first problem", "second problem"}}
	msg := err.Error()

	if !strings.Contains(msg, "2 problem(s)") {
		t.Errorf("missing problem count: %s", msg)
	}
	for _, p := range []string{"first problem", "second problem"} {
		if !strings.Contains(msg, p) {
			t.Errorf("missing %q in: %s", p, msg)
		}
	}
	if strings.Count(msg, "\n  - ") != 2 {
		t.Errorf("expected two bulleted lines, got: %s", msg)
	}
}
