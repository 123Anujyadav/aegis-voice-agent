package voice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// Why every provider number lives in this file
// ---------------------------------------------------------------------------
//
// §11 requires that provider-specific constants not be scattered through the
// code, and the reason is sharper here than usual: a local provider's
// configuration is the difference between a loop that runs on a developer's
// machine and one that does not. "Where do I point this at my model" must be
// answerable by reading one file.
//
// Everything is validated at construction and an invalid configuration fails
// closed. A voice runtime built from a bad path would fail on the first call
// rather than at boot, which is the worst possible moment to discover it.

// ---------------------------------------------------------------------------
// Path and argument safety (§18)
// ---------------------------------------------------------------------------

// shellMetacharacters are refused in any configured path.
//
// # This is defence in depth, not the defence
//
// Nothing in this package ever builds a shell command. Every process is started
// with exec.Command(path, args...), which passes an argv vector directly to the
// operating system and never invokes a shell — so a semicolon in a path could
// not be interpreted as a command separator even if one were there.
//
// They are refused anyway. A path containing one of these is almost certainly a
// misconfiguration — somebody pasted a command line where a path belonged — and
// failing at construction with a clear message beats spawning a process with an
// absurd name and reporting that it does not exist.
//
// # What is deliberately NOT in this set, and why
//
// The backslash and the colon are absent because they are PATH SYNTAX on
// Windows: every absolute path there contains both, starting with the drive
// letter. An earlier version of this constant included the backslash and
// rejected every valid Windows path — the tests caught it immediately, which is
// the argument for testing a security control against real paths rather than
// against the ones an author imagined.
//
// Brackets, braces and the exclamation mark are absent because they are legal
// and reasonably common in real directory names on every platform. Refusing
// them would produce false alarms with no corresponding safety gain, since the
// shell that would interpret them is never invoked.
// Built from a rune list rather than a string literal: the set contains a
// backtick, a double quote and an apostrophe, and every way of writing that
// as one literal is harder to read than this.
var shellMetacharacters = string([]rune{
	'|', '&', ';', '<', '>', '(', ')', '$',
	'`', '"', '\'',
	'\n', '\r', '\t', '*', '?',
})

// refusedExecutableExtensions are script types this package will not launch.
//
// # Batch files on Windows cannot be argument-escaped safely
//
// When CreateProcess is given a .bat or .cmd file it runs it through cmd.exe,
// and cmd.exe re-parses the command line with rules that no escaping scheme
// fully survives. That is the root of CVE-2024-24576 and of Go's own advisories
// in the same area; Go's os/exec now refuses some of these outright.
//
// This package refuses all of them, on every platform. A wrapper script is a
// perfectly reasonable thing to want, and the safe way to have one is a real
// executable — or to configure the interpreter as the executable and the script
// as an argument, where it is argv data rather than a command line.
var refusedExecutableExtensions = []string{".bat", ".cmd", ".ps1", ".sh", ".vbs"}

// validateExecutablePath reports why an executable cannot be used, or nil.
//
// Absolute, present, a regular file, no shell metacharacters, not a script
// type. Checked at construction so a misconfiguration is a boot failure rather
// than a first-call failure.
func validateExecutablePath(field, p string) error {
	if p == "" {
		return fmt.Errorf("%w: %s is empty", ErrUnsafePath, field)
	}
	if strings.ContainsAny(p, shellMetacharacters) {
		return fmt.Errorf(
			"%w: %s %q contains a shell metacharacter — this package never invokes a "+
				"shell, so such a character is a misconfiguration rather than a risk, "+
				"but it will not resolve to a real program",
			ErrUnsafePath, field, p)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf(
			"%w: %s %q must be an absolute path — resolving through PATH makes which "+
				"binary runs depend on the environment the process happened to inherit",
			ErrUnsafePath, field, p)
	}

	lower := strings.ToLower(filepath.Ext(p))
	for _, ext := range refusedExecutableExtensions {
		if lower == ext {
			return fmt.Errorf(
				"%w: %s %q is a %s script; this package will not launch one because "+
					"its arguments cannot be escaped reliably. Configure the interpreter "+
					"as the executable and the script as its first argument instead",
				ErrUnsafePath, field, p, ext)
		}
	}

	info, err := os.Stat(p)
	if err != nil {
		return &UnavailableError{
			Component: "executable",
			Path:      p,
			Cause:     err,
			Remedy:    "install the provider, or point " + field + " at an existing binary",
		}
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %s %q is a directory", ErrUnsafePath, field, p)
	}
	return nil
}

// validateModelPath reports why a model file cannot be used, or nil.
func validateModelPath(field, p string) error {
	if p == "" {
		return fmt.Errorf("%w: %s is empty", ErrUnsafePath, field)
	}
	if strings.ContainsAny(p, shellMetacharacters) {
		return fmt.Errorf("%w: %s %q contains a shell metacharacter",
			ErrUnsafePath, field, p)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%w: %s %q must be an absolute path", ErrUnsafePath, field, p)
	}

	info, err := os.Stat(p)
	if err != nil {
		return &UnavailableError{
			Component: "model",
			Path:      p,
			Cause:     err,
			Remedy:    "download the model, or point " + field + " at an existing file",
		}
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %s %q is a directory", ErrUnsafePath, field, p)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Process configuration
// ---------------------------------------------------------------------------

// ProcessConfig is everything needed to run and supervise one external
// provider process safely.
//
// Shared by every adapter that spawns something, so process supervision is
// configured the same way regardless of which provider is behind it.
type ProcessConfig struct {
	// Executable is the absolute path to the program. Validated at
	// construction.
	Executable string

	// Args are fixed arguments, as a VECTOR.
	//
	// NEVER A COMMAND STRING. These are passed to exec.Command as separate
	// argv entries, so a value containing spaces, quotes or semicolons is one
	// argument containing those characters — not several arguments, and not a
	// shell construct. That property is what makes caller-derived text safe to
	// pass, and TestConfig_ArgvIsDataNotCommandLine checks it directly.
	Args []string

	// Env is the environment the child receives, as KEY=VALUE.
	//
	// EXPLICIT, NOT INHERITED. A process started with the parent's environment
	// receives every API key, token and connection string the operator happens
	// to have exported. A local speech recogniser has no business seeing any of
	// them, and §18 asks for exactly this.
	//
	// [ProcessConfig.BuildEnv] assembles the minimum a program needs plus
	// whatever is listed here.
	Env []string

	// InheritPathVars carries the handful of variables a program genuinely
	// needs to start — PATH, HOME, TEMP and their platform equivalents.
	//
	// Named individually rather than inherited wholesale, so adding one is a
	// visible decision.
	InheritPathVars bool

	// StartTimeout bounds how long the process may take to become ready.
	StartTimeout time.Duration

	// StopTimeout bounds a graceful shutdown before the process is killed.
	StopTimeout time.Duration

	// MaxStderrBytes bounds retained stderr.
	//
	// A provider that writes progress to stderr on every frame would otherwise
	// grow this without limit for the length of a call. Retaining the most
	// recent bytes rather than the first keeps the part that explains a crash.
	MaxStderrBytes int

	// MaxRestarts bounds automatic restarts before the provider is declared
	// failed.
	MaxRestarts int

	// RestartBackoff is the delay before the first restart. Subsequent attempts
	// double it.
	RestartBackoff time.Duration
}

// BuildEnv assembles the child's environment.
//
// Nothing is inherited unless InheritPathVars is set, and even then only the
// named variables — never the whole environment, which is where credentials
// live.
func (c ProcessConfig) BuildEnv() []string {
	env := make([]string, 0, len(c.Env)+len(inheritableEnvVars))

	if c.InheritPathVars {
		for _, key := range inheritableEnvVars {
			if v, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+v)
			}
		}
	}
	return append(env, c.Env...)
}

// inheritableEnvVars are the only variables a child may inherit.
//
// The minimum a program needs to find its libraries and write a temporary file.
// Deliberately short: every addition is a decision to hand a local process
// something it did not have before.
var inheritableEnvVars = []string{
	"PATH", "SystemRoot", "windir", "TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE",
	"LANG", "LC_ALL",
}

func (c ProcessConfig) validate(field string) []string {
	var p []string

	if err := validateExecutablePath(field+".Executable", c.Executable); err != nil {
		p = append(p, err.Error())
	}
	for i, a := range c.Args {
		if strings.ContainsAny(a, "\x00") {
			p = append(p, fmt.Sprintf("%s.Args[%d] contains a NUL byte", field, i))
		}
	}
	for i, e := range c.Env {
		if !strings.Contains(e, "=") {
			p = append(p, fmt.Sprintf("%s.Env[%d] %q is not KEY=VALUE", field, i, e))
		}
		if looksLikeCredential(e) {
			p = append(p, fmt.Sprintf(
				"%s.Env[%d] looks like a credential; Phase 11E requires no API key and "+
					"must not pass one to a local process", field, i))
		}
	}
	if c.StartTimeout <= 0 {
		p = append(p, field+".StartTimeout must be positive")
	}
	if c.StopTimeout <= 0 {
		p = append(p, field+".StopTimeout must be positive; without it an unresponsive "+
			"child is never killed and the process leaks")
	}
	if c.MaxStderrBytes <= 0 {
		p = append(p, field+".MaxStderrBytes must be positive; an unbounded stderr "+
			"buffer grows for the length of a call")
	}
	if c.MaxRestarts < 0 {
		p = append(p, field+".MaxRestarts must not be negative")
	}
	if c.MaxRestarts > 0 && c.RestartBackoff <= 0 {
		p = append(p, field+".RestartBackoff must be positive when restarts are enabled; "+
			"without it a crash-looping provider is restarted as fast as it dies")
	}

	return p
}

// credentialHints are environment key fragments that suggest a secret.
//
// Heuristic and deliberately blunt. A false positive is a configuration error
// message; a false negative is an API key handed to a subprocess.
var credentialHints = []string{
	"api_key", "apikey", "secret", "token", "password", "passwd",
	"credential", "private_key", "access_key",
}

func looksLikeCredential(entry string) bool {
	key, _, _ := strings.Cut(entry, "=")
	lower := strings.ToLower(key)
	for _, hint := range credentialHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Provider configuration
// ---------------------------------------------------------------------------

// STTProviderConfig configures a local recognition provider.
type STTProviderConfig struct {
	// Enabled turns the provider on. A disabled provider is not registered and
	// its paths are not validated, so a developer without whisper installed can
	// still start the runtime.
	Enabled bool

	// ID is the authored provider identifier. Reaches metrics.
	ID ProviderID

	// Process configures the external program.
	Process ProcessConfig

	// ModelPath is the recognition model. Validated when Enabled.
	ModelPath string

	// Language is the expected language tag.
	Language string

	// SampleRate is what the provider requires. Audio at any other rate is
	// refused rather than silently resampled — see [audioConversionPolicy].
	SampleRate media.SampleRate

	// Threads bounds provider CPU use. Zero lets the provider decide.
	Threads int

	// ResultTimeout bounds how long to wait for a result after audio ends.
	ResultTimeout time.Duration

	// MaxPendingFrames bounds buffered inbound audio.
	MaxPendingFrames int
}

// TTSProviderConfig configures a local synthesis provider.
type TTSProviderConfig struct {
	// Enabled turns the provider on.
	Enabled bool

	// ID is the authored provider identifier.
	ID ProviderID

	// Process configures the external program.
	Process ProcessConfig

	// ModelPath is the voice model. Validated when Enabled.
	ModelPath string

	// SpeakerID selects a voice within a multi-speaker model. Negative means
	// the model's default.
	SpeakerID int

	// Language is the voice's language tag.
	Language string

	// Speed is the synthesis rate multiplier. 1 is the model's natural rate.
	Speed float64

	// SampleRate is what the model produces. Declared rather than detected,
	// because a mismatch must be an explicit conversion and not a surprise.
	SampleRate media.SampleRate

	// ChunkTimeout bounds synthesis of one text chunk.
	ChunkTimeout time.Duration

	// MaxPendingChunks bounds queued text.
	MaxPendingChunks int

	// MaxPendingFrames bounds buffered outbound audio.
	MaxPendingFrames int
}

// ModelProviderConfig configures a local language model provider.
//
// # No model name appears here as a default
//
// ADR-0006 freezes the production model ladder — four tiers, all on Claude,
// exact identifiers — and explicitly rejected self-hosted open-weight models.
// This configuration exists for a DEVELOPMENT loop that must run without an API
// key, and naming a specific local model as a default would read as an
// endorsement the architecture has not made.
//
// The operator names their model. There is no fallback.
type ModelProviderConfig struct {
	// Enabled turns the provider on.
	Enabled bool

	// ID is the authored provider identifier.
	ID ProviderID

	// Endpoint is the local daemon's base URL.
	//
	// AN HTTP CLIENT, NOT A SUPERVISED PROCESS. The daemon's lifecycle belongs
	// to the operator, which is why no ProcessConfig appears here — and it
	// removes the argv injection surface entirely, because caller text travels
	// as a JSON field rather than as a command-line argument.
	Endpoint string

	// Model is the model identifier the daemon knows. Required when Enabled;
	// no default.
	Model ModelID

	// RequestTimeout bounds a whole generation.
	RequestTimeout time.Duration

	// FirstTokenTimeout bounds the wait for the first token.
	//
	// A TIMEOUT, NOT A BUDGET. ADR-0011 hop 6 budgets time-to-first-token at
	// 250 ms p50 / 550 ms p95 for claude-sonnet-5 over a network; a local model
	// on a developer's CPU is not held to that and this phase creates no
	// target of its own. This value exists so a hung daemon does not hang a
	// call, and is set generously for that reason.
	FirstTokenTimeout time.Duration

	// MaxOutputTokens bounds a response.
	MaxOutputTokens int

	// MaxPendingChunks bounds the token stream's buffer.
	MaxPendingChunks int
}

func (c STTProviderConfig) validate() []string {
	if !c.Enabled {
		return nil
	}

	var p []string
	if !c.ID.Valid() {
		p = append(p, fmt.Sprintf("stt: ID %q is not a valid label", c.ID))
	}
	p = append(p, c.Process.validate("stt.Process")...)

	if err := validateModelPath("stt.ModelPath", c.ModelPath); err != nil {
		p = append(p, err.Error())
	}
	if c.Language == "" {
		p = append(p, "stt: Language must be set; a recogniser given no language "+
			"guesses, and a wrong guess is a whole call of nonsense")
	}
	if !c.SampleRate.Valid() {
		p = append(p, fmt.Sprintf("stt: SampleRate %s is outside the supported range",
			c.SampleRate))
	}
	if c.Threads < 0 {
		p = append(p, "stt: Threads must not be negative")
	}
	if c.ResultTimeout <= 0 {
		p = append(p, "stt: ResultTimeout must be positive")
	}
	if c.MaxPendingFrames <= 0 {
		p = append(p, "stt: MaxPendingFrames must be positive; an unbounded audio "+
			"queue in front of a slow recogniser grows for the length of the call")
	}
	return p
}

func (c TTSProviderConfig) validate() []string {
	if !c.Enabled {
		return nil
	}

	var p []string
	if !c.ID.Valid() {
		p = append(p, fmt.Sprintf("tts: ID %q is not a valid label", c.ID))
	}
	p = append(p, c.Process.validate("tts.Process")...)

	if err := validateModelPath("tts.ModelPath", c.ModelPath); err != nil {
		p = append(p, err.Error())
	}
	if c.Language == "" {
		p = append(p, "tts: Language must be set")
	}
	if c.Speed <= 0 {
		p = append(p, fmt.Sprintf("tts: Speed %g must be positive", c.Speed))
	}
	if !c.SampleRate.Valid() {
		p = append(p, fmt.Sprintf("tts: SampleRate %s is outside the supported range",
			c.SampleRate))
	}
	if c.ChunkTimeout <= 0 {
		p = append(p, "tts: ChunkTimeout must be positive")
	}
	if c.MaxPendingChunks <= 0 {
		p = append(p, "tts: MaxPendingChunks must be positive")
	}
	if c.MaxPendingFrames <= 0 {
		p = append(p, "tts: MaxPendingFrames must be positive")
	}
	return p
}

func (c ModelProviderConfig) validate() []string {
	if !c.Enabled {
		return nil
	}

	var p []string
	if !c.ID.Valid() {
		p = append(p, fmt.Sprintf("model: ID %q is not a valid label", c.ID))
	}
	if c.Endpoint == "" {
		p = append(p, "model: Endpoint must be set")
	} else if !strings.HasPrefix(c.Endpoint, "http://") &&
		!strings.HasPrefix(c.Endpoint, "https://") {
		p = append(p, fmt.Sprintf("model: Endpoint %q must be an http or https URL",
			c.Endpoint))
	}
	if c.Model == "" {
		p = append(p, "model: Model must be set; there is deliberately no default, "+
			"because ADR-0006 freezes the production model ladder and naming a local "+
			"model here would read as an endorsement this phase has not made")
	} else if !c.Model.Valid() {
		p = append(p, fmt.Sprintf("model: Model %q is not a valid label", c.Model))
	}
	if c.RequestTimeout <= 0 {
		p = append(p, "model: RequestTimeout must be positive")
	}
	if c.FirstTokenTimeout <= 0 {
		p = append(p, "model: FirstTokenTimeout must be positive")
	}
	if c.FirstTokenTimeout > c.RequestTimeout {
		p = append(p, fmt.Sprintf(
			"model: FirstTokenTimeout (%s) must not exceed RequestTimeout (%s), or the "+
				"whole request expires before the first token is judged late",
			c.FirstTokenTimeout, c.RequestTimeout))
	}
	if c.MaxOutputTokens <= 0 {
		p = append(p, "model: MaxOutputTokens must be positive")
	}
	if c.MaxPendingChunks <= 0 {
		p = append(p, "model: MaxPendingChunks must be positive")
	}
	return p
}

// ---------------------------------------------------------------------------
// Runtime configuration
// ---------------------------------------------------------------------------

// Config is the complete, immutable configuration of a voice runtime.
type Config struct {
	// Format is the audio the loop carries.
	Format media.AudioFormat

	// FrameInterval is the expected frame cadence.
	FrameInterval time.Duration

	// MaxSessions bounds concurrent voice sessions.
	MaxSessions int

	// TurnTimeout bounds one whole turn, transcript to final audio.
	TurnTimeout time.Duration

	// STT, TTS and Model configure the local providers. Any may be disabled.
	STT   STTProviderConfig
	TTS   TTSProviderConfig
	Model ModelProviderConfig

	// MaxTranscriptChars bounds a single transcript before it is refused.
	//
	// A recogniser that malfunctions can emit unbounded text, and that text
	// becomes a prompt. Bounding it here is cheaper than discovering the bound
	// in a token bill.
	MaxTranscriptChars int

	// MaxResponseChars bounds a generated response.
	MaxResponseChars int
}

// Validate checks the whole configuration, reporting every problem.
func (c Config) Validate() error {
	if problems := c.validate(); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

func (c Config) validate() []string {
	var p []string

	if err := c.Format.Validate(); err != nil {
		p = append(p, err.Error())
	}
	if c.FrameInterval <= 0 {
		p = append(p, "config: FrameInterval must be positive")
	}
	if c.MaxSessions <= 0 {
		p = append(p, "config: MaxSessions must be positive; an unbounded session "+
			"registry is a memory leak that presents as a slow crash days later")
	}
	if c.TurnTimeout <= 0 {
		p = append(p, "config: TurnTimeout must be positive; without it a stuck "+
			"provider holds a turn open for the length of the call")
	}
	if c.MaxTranscriptChars <= 0 {
		p = append(p, "config: MaxTranscriptChars must be positive")
	}
	if c.MaxResponseChars <= 0 {
		p = append(p, "config: MaxResponseChars must be positive")
	}

	p = append(p, c.STT.validate()...)
	p = append(p, c.TTS.validate()...)
	p = append(p, c.Model.validate()...)

	// Cross-section invariants: individually valid settings that contradict.
	if c.STT.Enabled && c.STT.SampleRate != c.Format.Rate {
		p = append(p, fmt.Sprintf(
			"config: STT.SampleRate (%s) differs from Format.Rate (%s). This engine "+
				"does not resample silently — configure the provider for the stream's "+
				"rate, or convert explicitly upstream",
			c.STT.SampleRate, c.Format.Rate))
	}
	if c.TTS.Enabled && c.TTS.SampleRate != c.Format.Rate {
		p = append(p, fmt.Sprintf(
			"config: TTS.SampleRate (%s) differs from Format.Rate (%s). This engine "+
				"does not resample silently — configure the voice for the stream's "+
				"rate, or convert explicitly downstream",
			c.TTS.SampleRate, c.Format.Rate))
	}

	return p
}

// AnyProviderEnabled reports whether at least one local provider is configured.
//
// A runtime with none is legal — it is what a unit test of the orchestration
// uses — but an end-to-end path needs to say so clearly rather than failing
// three layers down.
func (c Config) AnyProviderEnabled() bool {
	return c.STT.Enabled || c.TTS.Enabled || c.Model.Enabled
}
