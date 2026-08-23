package main

import (
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/whispercli"
)

// PHASE 16 T10 — THE LOCAL RECOGNISER, WIRED.
//
// # What this is
//
// The missing link Phase 16 T8 found: the provider adapters existed, the
// registry existed, the configuration structs existed, and nothing joined them.
// whispercli.New was called from exactly one test and nowhere else.
//
// This file joins them, and does nothing else. It builds no registry of its
// own, no router, no factory, no cache, no process manager and no session map:
// it constructs the provider the adapter already implements and hands it to the
// registry the platform already has.
//
// # What it deliberately does NOT do
//
// It does not decide whether this process may run a development-class provider.
// [voice.ProviderRegistry] decides that from its deployment mode, and this file
// passes that decision through rather than around — see [LocalSTTConfig.Mode],
// which has no default for exactly that reason.

// LocalSTTConfig configures the local recognition provider.
//
// # Why this is not voice.STTProviderConfig
//
// packages/go/voice already declares an STTProviderConfig, and it does not fit:
// its ModelPath is a FILESYSTEM PATH, validated to exist, because it was shaped
// for whisper.cpp and Piper, which load a model file. The openai-whisper CLI
// takes a model NAME — "tiny", "base" — that the tool resolves against its own
// cache. Passing a name where a path is validated fails at startup; passing a
// path where a name is expected makes the tool download a model, which is the
// one thing this phase must not do.
//
// So the mismatch is real rather than cosmetic, and this configuration is
// service-local. Nothing about the provider port changes.
type LocalSTTConfig struct {
	// Enabled turns the provider on. A disabled provider is not constructed and
	// its paths are not validated, so a developer without the tool installed
	// can still start the service.
	Enabled bool

	// Mode is the deployment mode of the registry this provider joins.
	//
	// REQUIRED, WITH NO DEFAULT. Defaulting it is how a development-class
	// recogniser reaches production traffic: the registry's guard would still
	// run, but against a mode nobody chose. Stating it makes the decision
	// visible in configuration and reviewable in a diff.
	Mode voice.DeploymentMode

	// ID is the authored provider identifier. It reaches metric labels, so it
	// is authored here and never derived from provider output.
	ID speech.ProviderID

	// Executable is the absolute path to the whisper entry point.
	Executable string

	// Model is the model NAME the tool loads — "tiny", "base", and so on.
	//
	// A NAME, NOT A PATH, and with no default. The tool resolves it against its
	// own cache and DOWNLOADS ON A MISS; naming one here would make some
	// particular model an implicit part of the platform and would make a first
	// run fetch multiple gigabytes. The operator names what they have.
	Model string

	// Language is the recognition language, declared to the router.
	Language speech.Language

	// Format is the audio this provider accepts. Refused rather than resampled
	// if the session disagrees.
	Format media.AudioFormat

	// MaxAudio bounds one utterance. Reaching it fails the stream rather than
	// growing a temporary file without limit.
	MaxAudio time.Duration

	// MaxPendingResults bounds queued transcript segments.
	MaxPendingResults int

	// StartTimeout bounds how long inference may take.
	//
	// Batch inference is the whole cost of the turn for this tool, so this is
	// generous by nature; it exists so a wedged process cannot hold a call
	// open for its whole duration.
	StartTimeout time.Duration

	// StopTimeout bounds a graceful stop before the child is killed.
	StopTimeout time.Duration

	// MaxStderrBytes bounds retained stderr. A chatty child overwrites its own
	// oldest output rather than growing this process's memory.
	MaxStderrBytes int

	// WorkDir is where the temporary audio file is written. Empty uses the
	// system temporary directory.
	WorkDir string
}

// validate reports every problem at once.
func (c LocalSTTConfig) validate() []string {
	if !c.Enabled {
		return nil
	}

	var p []string
	if !c.Mode.Valid() {
		p = append(p, fmt.Sprintf(
			"STT.Mode is %q: state %s or %s. There is no default, because "+
				"defaulting it is how a development-class recogniser reaches "+
				"production traffic", c.Mode, voice.ModeProduction, voice.ModeDevelopment))
	}
	if c.ID == "" {
		p = append(p, "STT.ID is required; it becomes a metric label and is authored, "+
			"never derived from provider output")
	}
	if c.Executable == "" {
		p = append(p, "STT.Executable is required when the provider is enabled")
	}
	if c.Model == "" {
		p = append(p, "STT.Model is required and has no default: the tool downloads "+
			"a model on a cache miss, so naming one here would make a first run "+
			"fetch gigabytes")
	}
	if c.Language == "" {
		p = append(p, "STT.Language is required; the router selects on it")
	}
	if err := c.Format.Validate(); err != nil {
		p = append(p, "STT.Format: "+err.Error())
	}
	if c.MaxAudio <= 0 {
		p = append(p, "STT.MaxAudio must be positive; without it one utterance can "+
			"grow a temporary file without limit")
	}
	if c.MaxPendingResults <= 0 {
		p = append(p, "STT.MaxPendingResults must be positive")
	}
	if c.StartTimeout <= 0 {
		p = append(p, "STT.StartTimeout must be positive; without it a wedged "+
			"recogniser holds the call open for its whole duration")
	}
	if c.StopTimeout <= 0 {
		p = append(p, "STT.StopTimeout must be positive; without it an unresponsive "+
			"child is never killed and the process leaks")
	}
	if c.MaxStderrBytes <= 0 {
		p = append(p, "STT.MaxStderrBytes must be positive; an unbounded stderr "+
			"buffer grows for the length of a call")
	}
	return p
}

// registerLocalSTT constructs the configured recogniser and registers it.
//
// # It registers, and decides nothing
//
// Availability is checked before construction so a missing tool fails at
// startup with a typed error naming the path and the remedy, rather than on the
// first caller's first word. Classification is stated honestly and the registry
// enforces it: in [voice.ModeProduction] this registration is REFUSED, and that
// refusal is returned unchanged rather than caught, softened or worked around.
//
// A disabled provider is a no-op, which is what lets the deterministic suite and
// a developer without the tool run the same code path.
func registerLocalSTT(reg *voice.ProviderRegistry, cfg LocalSTTConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if reg == nil {
		return fmt.Errorf("voice: no provider registry to register the recogniser on")
	}
	if problems := cfg.validate(); len(problems) > 0 {
		return &voice.ConfigError{Problems: problems}
	}

	adapter := whispercli.Config{
		ID:         cfg.ID,
		Executable: cfg.Executable,
		Model:      cfg.Model,
		Language:   string(cfg.Language),
		Format:     cfg.Format,
		// FP16 stays off: it is unsupported on CPU, and the tool prints a
		// warning and falls back, polluting stderr on every single run.
		FP16:              false,
		MaxAudio:          cfg.MaxAudio,
		MaxPendingResults: cfg.MaxPendingResults,
		WorkDir:           cfg.WorkDir,
		Process: process.Config{
			StartTimeout:   cfg.StartTimeout,
			StopTimeout:    cfg.StopTimeout,
			MaxStderrBytes: cfg.MaxStderrBytes,
		},
	}

	// CHECKED BEFORE CONSTRUCTED. The adapter's own availability check stats
	// the executable and returns an error naming the path it looked at and the
	// command that installs it. Discovering that on a live call instead would
	// cost a caller their turn.
	//
	// It does NOT check the model: the tool resolves that name against its own
	// cache and there is nothing here that can tell a cached model from one
	// that is about to be downloaded. That is the operator's responsibility and
	// is documented as such.
	//
	// Wrapped into voice.ErrProviderUnavailable, and the adapter's own message
	// kept verbatim inside it. The adapter unwraps to a sentinel in its own
	// package, so a caller that wanted to classify this would otherwise have to
	// import whispercli — which would make every consumer of this service know
	// which recogniser is behind the port. The detail an operator needs (the
	// path checked, the command that installs it) survives in the text.
	if err := whispercli.Available(adapter); err != nil {
		return fmt.Errorf("local recogniser %q: %w: %v",
			cfg.ID, voice.ErrProviderUnavailable, err)
	}

	provider, err := whispercli.New(adapter)
	if err != nil {
		return fmt.Errorf("local recogniser %q: %w: %v",
			cfg.ID, voice.ErrProviderUnavailable, err)
	}

	// The classification is the honest one, and the registry enforces it.
	//
	// openai-whisper is a batch Python tool: it cannot stream, it begins
	// inference only once the caller stops speaking, and its latency is added
	// to the turn rather than overlapped with it. Calling that production-class
	// would be a claim about real traffic that nothing here supports — and
	// ADR-0006 would have to be amended to make it true.
	spec := voice.ProviderSpec{
		Class:    voice.ClassDevelopment,
		Tier:     speech.TierPrimary,
		Engine:   "openai-whisper",
		Version:  "cli",
		Locality: voice.LocalityProcess,
		Model:    voice.ModelIdentity{Model: voice.ModelID(cfg.Model)},
	}

	// Returned unchanged. In production mode the registry refuses this, naming
	// ADR-0006, and that refusal must reach the operator as a startup failure
	// rather than being logged and swallowed.
	if err := reg.RegisterSTT(provider, spec); err != nil {
		return fmt.Errorf("local recogniser %q: %w", cfg.ID, err)
	}
	return nil
}
