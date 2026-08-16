package voice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// stubExecutable creates a real file that can stand in for a provider binary.
//
// Path validation stats the file, so a test needs something that exists. It is
// never executed by these tests — process tests build a real Go helper.
func stubExecutable(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("stub"), 0o600); err != nil {
		t.Fatalf("writing stub: %v", err)
	}
	return path
}

func testFormat() media.AudioFormat { return media.PCM16Mono16k() }

// validConfig returns a fully enabled configuration pointing at stub files.
func validConfig(t *testing.T) Config {
	t.Helper()

	cfg := DefaultConfig(testFormat())
	cfg.STT = DefaultSTTConfig("whisper-local",
		stubExecutable(t, "whisper-bin"), stubExecutable(t, "model.bin"),
		"en", testFormat().Rate)
	cfg.TTS = DefaultTTSConfig("piper-local",
		stubExecutable(t, "piper-bin"), stubExecutable(t, "voice.onnx"),
		"en", testFormat().Rate)
	cfg.Model = DefaultModelConfig("local-model", "some-model:tag")
	return cfg
}

func TestConfig_DefaultIsValidAndInert(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the default configuration is invalid: %v", err)
	}
	if cfg.AnyProviderEnabled() {
		t.Error("the default configuration enables a provider; there is no correct " +
			"default path to a local binary and guessing one would be wrong on every " +
			"machine but the author's")
	}
}

func TestConfig_FullyConfiguredIsValid(t *testing.T) {
	t.Parallel()

	cfg := validConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a fully configured runtime is invalid: %v", err)
	}
	if !cfg.AnyProviderEnabled() {
		t.Error("AnyProviderEnabled is false with three providers enabled")
	}
}

// ---------------------------------------------------------------------------
// §18 — path safety
// ---------------------------------------------------------------------------

// TestConfig_RefusesUnsafeExecutablePaths is the §18 executable control.
func TestConfig_RefusesUnsafeExecutablePaths(t *testing.T) {
	t.Parallel()

	real := stubExecutable(t, "ok-bin")
	dir := t.TempDir()

	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "is empty"},
		{"relative", "whisper", "absolute path"},
		{"relative with dots", "../bin/whisper", "absolute path"},
		{"semicolon", filepath.Join(dir, "a;rm -rf b"), "shell metacharacter"},
		{"pipe", filepath.Join(dir, "a|b"), "shell metacharacter"},
		{"backtick", filepath.Join(dir, "a`b`"), "shell metacharacter"},
		{"dollar", filepath.Join(dir, "a$(id)"), "shell metacharacter"},
		{"ampersand", filepath.Join(dir, "a&&b"), "shell metacharacter"},
		{"newline", filepath.Join(dir, "a\nb"), "shell metacharacter"},
		{"missing", filepath.Join(dir, "nope"), "unavailable"},
		{"directory", dir, "is a directory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateExecutablePath("test", tc.path)
			if err == nil {
				t.Fatalf("path %q was accepted", tc.path)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not explain the refusal\n got: %v\nwant: %q",
					err, tc.want)
			}
		})
	}

	if err := validateExecutablePath("test", real); err != nil {
		t.Errorf("a real absolute path was refused: %v", err)
	}
}

// TestConfig_RefusesScriptExecutables guards a genuine Windows hazard.
//
// A .bat or .cmd file is run through cmd.exe, which re-parses the command line
// with rules no escaping scheme fully survives — the root of CVE-2024-24576 and
// of Go's own advisories in the same area. Refused on every platform, so the
// behaviour does not depend on where the tests happen to run.
func TestConfig_RefusesScriptExecutables(t *testing.T) {
	t.Parallel()

	for _, ext := range []string{".bat", ".cmd", ".ps1", ".sh", ".vbs"} {
		t.Run(ext, func(t *testing.T) {
			t.Parallel()

			path := stubExecutable(t, "wrapper"+ext)
			err := validateExecutablePath("test", path)
			if err == nil {
				t.Fatalf("a %s script was accepted as an executable", ext)
			}
			if !strings.Contains(err.Error(), "cannot be escaped reliably") {
				t.Errorf("error does not explain why: %v", err)
			}
			if !strings.Contains(err.Error(), "interpreter") {
				t.Error("error does not offer the safe alternative")
			}
		})
	}
}

func TestConfig_RefusesUnsafeModelPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	for name, path := range map[string]string{
		"empty":     "",
		"relative":  "model.bin",
		"metachar":  filepath.Join(dir, "m;odel.bin"),
		"missing":   filepath.Join(dir, "absent.bin"),
		"directory": dir,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateModelPath("test", path); err == nil {
				t.Errorf("model path %q was accepted", path)
			}
		})
	}
}

// TestConfig_MissingProviderProducesAnActionableError is the §20 and §21
// honest-failure contract at the configuration layer.
func TestConfig_MissingProviderProducesAnActionableError(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "not-installed")

	err := validateExecutablePath("stt.Executable", missing)
	if err == nil {
		t.Fatal("a missing executable was accepted")
	}
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("err does not match ErrProviderUnavailable: %v", err)
	}

	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err is %T, want *UnavailableError", err)
	}
	if unavailable.Path != missing {
		t.Errorf("the error does not name the path it checked: %q", unavailable.Path)
	}
	if unavailable.Remedy == "" {
		t.Error("the error carries no remedy; the first run of this phase will " +
			"almost always hit a missing binary, and an error that does not say " +
			"what to install sends somebody to read source code")
	}
}

// ---------------------------------------------------------------------------
// §18 — argv is data, not a command line
// ---------------------------------------------------------------------------

// TestConfig_ArgvIsDataNotCommandLine is the central §18 claim, checked against
// the standard library rather than asserted.
//
// # Why this executes a real process
//
// The claim "no arbitrary user text becomes a shell command" cannot be proved
// by reading the configuration type. It is a property of how exec.Command
// passes an argv VECTOR to the operating system, and the only way to show it
// holds is to hand a program a string full of shell metacharacters and have it
// report back exactly what it received.
//
// If any layer were splitting on spaces or invoking a shell, the argument would
// come back mangled or the metacharacters would take effect.
func TestConfig_ArgvIsDataNotCommandLine(t *testing.T) {
	t.Parallel()

	helper := buildArgvEcho(t)

	hostile := []string{
		"; rm -rf /",
		"&& shutdown now",
		"| cat /etc/passwd",
		"$(whoami)",
		"`id`",
		"a b c   d",
		`"quoted" 'single'`,
		"trailing\\",
		"%PATH%",
		"$HOME",
		"नमस्ते; echo pwned",
	}

	for _, arg := range hostile {
		t.Run(arg, func(t *testing.T) {
			t.Parallel()

			// EXACTLY how every adapter in this package starts a process: a
			// path and a slice, never a string.
			out, err := exec.Command(helper, arg).Output()
			if err != nil {
				t.Fatalf("running the echo helper: %v", err)
			}

			got := strings.TrimRight(string(out), "\r\n")
			if got != arg {
				t.Errorf("the argument was transformed in transit\n sent: %q\n got: %q\n"+
					"something is splitting or interpreting argv, which is exactly what "+
					"§18 forbids", arg, got)
			}
		})
	}
}

// buildArgvEcho compiles a helper that prints os.Args[1] verbatim.
//
// A real compiled program rather than a shell one-liner, because a shell
// one-liner would be the thing under test.
func buildArgvEcho(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")

	const program = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Print(os.Args[1])
	}
}
`
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatalf("writing helper source: %v", err)
	}

	bin := filepath.Join(dir, "argvecho")
	if _, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32")); err == nil {
		bin += ".exe"
	}

	build := exec.Command("go", "build", "-o", bin, src)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Skipf("cannot build the argv helper (no working Go toolchain here): %v", err)
	}
	return bin
}

// ---------------------------------------------------------------------------
// §18 — environment sanitisation and credential exclusion
// ---------------------------------------------------------------------------

// TestConfig_ChildEnvironmentIsNotInherited is §18's credential exclusion.
//
// A process started with the parent's environment receives every API key the
// operator has exported. A local speech recogniser has no business seeing one,
// and Phase 11E requires no key at all.
func TestConfig_ChildEnvironmentIsNotInherited(t *testing.T) {
	// Not parallel: t.Setenv mutates process state, and the testing package
	// refuses to combine the two for exactly that reason.
	t.Setenv("AEGIS_TEST_FAKE_API_KEY", "sk-should-never-be-inherited")
	t.Setenv("AEGIS_TEST_HARMLESS", "value")

	cfg := DefaultProcessConfig(stubExecutable(t, "bin"))
	env := cfg.BuildEnv()

	for _, entry := range env {
		if strings.Contains(entry, "sk-should-never-be-inherited") {
			t.Fatalf("the child environment carries a credential: %q", entry)
		}
		if strings.HasPrefix(entry, "AEGIS_TEST_HARMLESS=") {
			t.Errorf("an unlisted variable was inherited: %q — the environment is "+
				"an allowlist, so adding one must be a visible decision", entry)
		}
	}

	// The allowlisted variables a program needs to start ARE passed.
	var sawPath bool
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			sawPath = true
		}
	}
	if !sawPath && os.Getenv("PATH") != "" {
		t.Error("PATH was not passed; a program that cannot find its libraries " +
			"will not start")
	}

	// With inheritance off, nothing at all.
	cfg.InheritPathVars = false
	if got := cfg.BuildEnv(); len(got) != 0 {
		t.Errorf("with inheritance disabled the environment is %v, want empty", got)
	}
}

// TestConfig_RefusesCredentialShapedEnvironment stops a key being configured
// deliberately.
func TestConfig_RefusesCredentialShapedEnvironment(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{
		"OPENAI_API_KEY=sk-x",
		"SOME_SECRET=x",
		"AUTH_TOKEN=x",
		"DB_PASSWORD=x",
		"aws_access_key=x",
		"MY_PRIVATE_KEY=x",
	} {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultProcessConfig(stubExecutable(t, "bin"))
			cfg.Env = []string{entry}

			problems := cfg.validate("test")
			var found bool
			for _, p := range problems {
				if strings.Contains(p, "looks like a credential") {
					found = true
				}
			}
			if !found {
				t.Errorf("%q was accepted into a child environment; Phase 11E requires "+
					"no API key and must not hand one to a local process\nproblems: %v",
					entry, problems)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Field validation
// ---------------------------------------------------------------------------

func TestConfig_EveryFieldHasARejectingCase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field  string
		mutate func(*Config)
		want   string
	}{
		{"FrameInterval", func(c *Config) { c.FrameInterval = 0 }, "FrameInterval"},
		{"MaxSessions", func(c *Config) { c.MaxSessions = 0 }, "MaxSessions"},
		{"TurnTimeout", func(c *Config) { c.TurnTimeout = 0 }, "TurnTimeout"},
		{"MaxTranscriptChars", func(c *Config) { c.MaxTranscriptChars = 0 },
			"MaxTranscriptChars"},
		{"MaxResponseChars", func(c *Config) { c.MaxResponseChars = 0 },
			"MaxResponseChars"},

		{"STT.ID", func(c *Config) { c.STT.ID = "Bad ID" }, "not a valid label"},
		{"STT.Language", func(c *Config) { c.STT.Language = "" }, "Language must be set"},
		{"STT.ResultTimeout", func(c *Config) { c.STT.ResultTimeout = 0 },
			"ResultTimeout"},
		{"STT.MaxPendingFrames", func(c *Config) { c.STT.MaxPendingFrames = 0 },
			"MaxPendingFrames"},
		{"STT.Threads", func(c *Config) { c.STT.Threads = -1 }, "Threads"},
		{"STT.SampleRate", func(c *Config) { c.STT.SampleRate = 1 }, "SampleRate"},

		{"TTS.ID", func(c *Config) { c.TTS.ID = "Bad ID" }, "not a valid label"},
		{"TTS.Language", func(c *Config) { c.TTS.Language = "" }, "Language must be set"},
		{"TTS.Speed", func(c *Config) { c.TTS.Speed = 0 }, "Speed"},
		{"TTS.ChunkTimeout", func(c *Config) { c.TTS.ChunkTimeout = 0 }, "ChunkTimeout"},
		{"TTS.MaxPendingChunks", func(c *Config) { c.TTS.MaxPendingChunks = 0 },
			"MaxPendingChunks"},
		{"TTS.MaxPendingFrames", func(c *Config) { c.TTS.MaxPendingFrames = 0 },
			"MaxPendingFrames"},

		{"Model.Endpoint", func(c *Config) { c.Model.Endpoint = "" }, "Endpoint must be set"},
		{"Model.EndpointScheme", func(c *Config) { c.Model.Endpoint = "tcp://x" },
			"http or https"},
		{"Model.Model", func(c *Config) { c.Model.Model = "" }, "Model must be set"},
		{"Model.RequestTimeout", func(c *Config) { c.Model.RequestTimeout = 0 },
			"RequestTimeout"},
		{"Model.FirstTokenTimeout", func(c *Config) { c.Model.FirstTokenTimeout = 0 },
			"FirstTokenTimeout"},
		{"Model.MaxOutputTokens", func(c *Config) { c.Model.MaxOutputTokens = 0 },
			"MaxOutputTokens"},
		{"Model.MaxPendingChunks", func(c *Config) { c.Model.MaxPendingChunks = 0 },
			"MaxPendingChunks"},

		{"Process.StartTimeout", func(c *Config) { c.STT.Process.StartTimeout = 0 },
			"StartTimeout"},
		{"Process.StopTimeout", func(c *Config) { c.STT.Process.StopTimeout = 0 },
			"StopTimeout"},
		{"Process.MaxStderrBytes", func(c *Config) { c.STT.Process.MaxStderrBytes = 0 },
			"MaxStderrBytes"},
		{"Process.MaxRestarts", func(c *Config) { c.STT.Process.MaxRestarts = -1 },
			"MaxRestarts"},
		{"Process.RestartBackoff", func(c *Config) { c.STT.Process.RestartBackoff = 0 },
			"RestartBackoff"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig(t)
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%s accepted an invalid value; it must fail closed", tc.field)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error does not name the problem\n got: %v\nwant: %q",
					tc.field, err, tc.want)
			}

			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("%s: error is %T, want *ConfigError", tc.field, err)
			}
		})
	}
}

// TestConfig_RefusesSilentResampling is §13's explicit-conversion requirement.
func TestConfig_RefusesSilentResampling(t *testing.T) {
	t.Parallel()

	t.Run("stt rate mismatch", func(t *testing.T) {
		t.Parallel()
		cfg := validConfig(t)
		cfg.STT.SampleRate = media.Rate8kHz
		cfg.Format = media.PCM16Mono16k()

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "does not resample silently") {
			t.Errorf("a rate mismatch was accepted: %v", err)
		}
	})

	t.Run("tts rate mismatch", func(t *testing.T) {
		t.Parallel()
		cfg := validConfig(t)
		cfg.TTS.SampleRate = media.Rate24kHz

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "does not resample silently") {
			t.Errorf("a rate mismatch was accepted: %v", err)
		}
	})
}

// TestConfig_FirstTokenTimeoutCannotExceedRequestTimeout catches two
// individually valid settings that contradict each other.
func TestConfig_FirstTokenTimeoutCannotExceedRequestTimeout(t *testing.T) {
	t.Parallel()

	cfg := validConfig(t)
	cfg.Model.RequestTimeout = 5 * time.Second
	cfg.Model.FirstTokenTimeout = 10 * time.Second

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not exceed RequestTimeout") {
		t.Errorf("a first-token timeout beyond the request timeout was accepted: %v", err)
	}
}

func TestConfig_DisabledProviderNeedsNoPaths(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	cfg.STT = STTProviderConfig{Enabled: false, ModelPath: "/nonexistent/nowhere"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a disabled provider was validated: %v\n"+
			"a developer without whisper installed must still be able to start "+
			"the runtime", err)
	}
}

func TestConfig_ReportsEveryProblemNotTheFirst(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	cfg.FrameInterval = 0
	cfg.MaxSessions = 0
	cfg.TurnTimeout = 0
	cfg.MaxTranscriptChars = 0
	cfg.MaxResponseChars = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("five invalid fields were accepted")
	}

	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
	if len(ce.Problems) < 5 {
		t.Errorf("reported %d problems, want at least 5:\n%v", len(ce.Problems), ce.Problems)
	}
}

// ---------------------------------------------------------------------------
// §11 — constants are not scattered
// ---------------------------------------------------------------------------

// configFiles are the only files permitted to hold tunable numbers.
var configFiles = map[string]bool{
	"config.go":   true,
	"defaults.go": true,
	"harness.go":  true,
}

var durationLiteral = regexp.MustCompile(
	`[0-9]\s*\*\s*time\.(Nanosecond|Microsecond|Millisecond|Second|Minute|Hour)`)

// TestConfig_NoDurationLiteralsOutsideConfig enforces §11.
//
// A provider timeout typed into an adapter is a timeout nobody can find and
// nobody can tune. This makes that mistake fail the build.
func TestConfig_NoDurationLiteralsOutsideConfig(t *testing.T) {
	t.Parallel()

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range entries {
		if configFiles[path] || strings.HasSuffix(path, "_test.go") {
			continue
		}

		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if durationLiteral.MatchString(line) {
				t.Errorf("%s:%d holds a duration literal; every tunable belongs in "+
					"config.go or defaults.go\n\t%s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestConfig_NoModelNameIsHardcoded guards the ADR-0006 boundary.
//
// ADR-0006 freezes the production model ladder — four tiers, all on Claude,
// exact identifiers — and explicitly rejected self-hosted open-weight models.
// A local model name appearing as a default in this package would read as an
// endorsement the architecture has not made.
func TestConfig_NoModelNameIsHardcoded(t *testing.T) {
	t.Parallel()

	// Model families that must not appear as defaults in production code.
	families := []string{
		"gemma", "llama", "mistral", "qwen", "phi-", "deepseek",
		"claude-", "gpt-4", "gpt-3",
	}

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			lower := strings.ToLower(line)
			for _, family := range families {
				if strings.Contains(lower, family) {
					t.Errorf("%s:%d names a model family outside a comment: %s\n"+
						"the model identifier is configuration; ADR-0006 owns which "+
						"models this platform uses", path, i+1, trimmed)
				}
			}
		}
	}

	// And the default configuration must genuinely require one.
	cfg := DefaultConfig(testFormat())
	cfg.Model = DefaultModelConfig("local", "")
	if err := cfg.Validate(); err == nil {
		t.Error("an enabled model provider with no model identifier was accepted")
	}
}

// TestConfig_FrozenConstantsMatchTheADRs pins the numbers this phase did not
// choose, and keeps the reference labelled as a reference.
func TestConfig_FrozenConstantsMatchTheADRs(t *testing.T) {
	t.Parallel()

	// ADR-0004 §12, ADR-0011 §5.1, restated in runtime.Provider's contract.
	if FrozenBargeInBudget != 20*time.Millisecond {
		t.Errorf("FrozenBargeInBudget = %s, the ADRs fix it at 20ms", FrozenBargeInBudget)
	}

	// ADR-0011 §5.2 hop 6 / ADR-0006 C1. A REFERENCE for a different system,
	// not a target this phase is held to.
	if ReferenceFirstTokenP50 != 250*time.Millisecond {
		t.Errorf("ReferenceFirstTokenP50 = %s, ADR-0011 hop 6 says 250ms",
			ReferenceFirstTokenP50)
	}
	if ReferenceFirstTokenP95 != 550*time.Millisecond {
		t.Errorf("ReferenceFirstTokenP95 = %s, ADR-0011 hop 6 says 550ms",
			ReferenceFirstTokenP95)
	}

	// The local first-token TIMEOUT must be far above the reference, or it
	// would be functioning as a budget this phase never agreed to.
	if defaultModelFirstTokenTimeout <= ReferenceFirstTokenP95 {
		t.Errorf("defaultModelFirstTokenTimeout (%s) is at or below the ADR-0011 "+
			"reference (%s); a local model on a CPU will not meet that reference, "+
			"and a timeout set there would turn a reference into a de facto SLA",
			defaultModelFirstTokenTimeout, ReferenceFirstTokenP95)
	}
}
