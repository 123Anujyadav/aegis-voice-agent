package voice

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// The provider registry
// ---------------------------------------------------------------------------
//
// # This is a descriptor layer. It does not route.
//
// Selection, tiers, health, the circuit breaker and switch accounting all live
// in speech.ProviderRouter and stay there. Every routing call below is a
// one-line delegation, and TestRegistry_RoutesIdenticallyToTheBareRouter drives
// the same scenario through both to prove the registry cannot have acquired
// selection logic of its own.
//
// What the router deliberately does NOT have is a vocabulary for describing
// providers. It selects on [speech.Capabilities] and health and cannot ask "is
// this Deepgram" — that is what makes a provider swap a configuration change.
// But an operator, a readiness endpoint and a support engineer all need exactly
// the question the router refuses to answer: what is registered, which engine
// is behind it, which model, which version, and is it something we are allowed
// to run in production.
//
// Answering that WITHOUT giving the router an identity vocabulary is the whole
// job of this file. Descriptors are read; nothing selects on them.
//
// # Capabilities are derived, never authored
//
// Every capability and language in a descriptor is read from the provider's own
// Capabilities() at registration. Nothing here lets an operator type them in.
// A hand-written duplicate would drift from the provider it describes, and the
// drift would surface as a router that declines work the provider could do —
// or, worse, sends it work it cannot.
//
// # No credentials, structurally
//
// A descriptor has no field for one, and [ProviderSpec] is validated against
// the same credential heuristic the process configuration uses.
// TestDescriptor_HasNowhereToPutACredential checks the struct itself by
// reflection, so a future field called APIKey fails the build's tests rather
// than review.

// ProviderClass is whether a provider may serve production traffic.
//
// # There is no safe default, so there is no default
//
// [ClassUnspecified] is the zero value and it is INVALID. The alternative —
// defaulting to production — means a forgotten field silently promotes a
// developer's laptop model into the production path, which is precisely the
// failure ADR-0006's tier ladder exists to prevent. A forgotten field must fail
// loudly instead.
type ProviderClass string

// The provider classes.
const (
	// ClassUnspecified is the zero value and is never valid.
	ClassUnspecified ProviderClass = ""

	// ClassProduction is a provider permitted to serve real traffic.
	ClassProduction ProviderClass = "production"

	// ClassDevelopment is a provider for local development only.
	//
	// ADR-0006 freezes the production model ladder — four tiers, all on Claude,
	// exact identifiers — and explicitly REJECTED self-hosted open-weight
	// models as its Option 5. The local model adapter this phase adds is a
	// development convenience so the loop runs without an API key. It is not a
	// tier, and [ModeProduction] refuses to register one.
	ClassDevelopment ProviderClass = "development"
)

// AllProviderClasses returns every declared class, excluding the invalid zero.
func AllProviderClasses() []ProviderClass {
	return []ProviderClass{ClassProduction, ClassDevelopment}
}

// Valid reports whether the class was explicitly declared.
func (c ProviderClass) Valid() bool {
	return c == ClassProduction || c == ClassDevelopment
}

// String implements fmt.Stringer.
func (c ProviderClass) String() string { return string(c) }

// DeploymentMode is what this process is permitted to run.
type DeploymentMode string

// The deployment modes.
const (
	// ModeDevelopment permits development-class providers.
	ModeDevelopment DeploymentMode = "development"

	// ModeProduction refuses them.
	ModeProduction DeploymentMode = "production"
)

// Valid reports whether the mode is declared.
func (m DeploymentMode) Valid() bool {
	return m == ModeDevelopment || m == ModeProduction
}

// String implements fmt.Stringer.
func (m DeploymentMode) String() string { return string(m) }

// ProviderLocality is where a provider executes.
//
// Operationally load-bearing rather than decorative: a supervised child process
// competes with this one for CPU and can be orphaned, an external daemon has a
// lifecycle nobody here controls, and a remote service can fail while every
// local thing is healthy. They are three different pager responses.
type ProviderLocality string

// The localities.
const (
	// LocalityProcess is a program this runtime spawns and supervises.
	LocalityProcess ProviderLocality = "local_process"

	// LocalityDaemon is a locally reachable service with an externally managed
	// lifecycle. Phase 11E's process supervision does NOT apply to it.
	LocalityDaemon ProviderLocality = "local_daemon"

	// LocalityRemote is a service reached over a network this runtime does not
	// own.
	LocalityRemote ProviderLocality = "remote"
)

// Valid reports whether the locality is declared.
func (l ProviderLocality) Valid() bool {
	return l == LocalityProcess || l == LocalityDaemon || l == LocalityRemote
}

// String implements fmt.Stringer.
func (l ProviderLocality) String() string { return string(l) }

// ModelIdentity names the model behind a provider.
//
// Identity, NOT LOCATION. There is deliberately no path field: a descriptor is
// meant to be read by operators and served from a status endpoint, and a
// filesystem path describes this machine's layout rather than the model. The
// configuration that loads the model keeps the path; this says what was loaded.
type ModelIdentity struct {
	// Model is the model identifier. Configuration, never a constant — see
	// [ModelID].
	Model ModelID

	// Revision distinguishes two builds of the same model, where the provider
	// or the operator knows one. Optional.
	Revision string

	// Quantization records the weight format where it matters to behaviour,
	// for example "q4_0". Optional, and free text because every engine spells
	// it differently.
	Quantization string
}

// String renders the identity for logs.
func (m ModelIdentity) String() string {
	s := string(m.Model)
	if m.Quantization != "" {
		s += " (" + m.Quantization + ")"
	}
	if m.Revision != "" {
		s += " @" + m.Revision
	}
	return s
}

// ProviderCapabilities is the descriptor's view of what a provider can do.
//
// A UNION OF TWO PORTS, read from whichever applies. speech.Capabilities and
// runtime.Capabilities describe different jobs and neither is a superset, so a
// registry that holds both kinds needs one shape to report. Fields that do not
// apply to a kind are zero, which is why [ProviderDescriptor.Kind] is the field
// to read first.
type ProviderCapabilities struct {
	// Streaming reports incremental delivery. Meaningful for every kind.
	Streaming bool

	// PartialResults reports interim transcripts. Recognition only.
	PartialResults bool

	// SampleRates the provider accepts or produces. Speech only.
	SampleRates []media.SampleRate

	// Thinking reports extended thinking. Generation only.
	Thinking bool

	// ToolCalling reports structured tool calls. Generation only.
	ToolCalling bool

	// MaxContextTokens bounds the context. Generation only.
	MaxContextTokens int

	// MaxOutputTokens bounds a completion. Generation only.
	MaxOutputTokens int
}

// ProviderSpec is what an operator authors about a provider.
//
// Everything a provider can report about ITSELF — its identifier, its
// capabilities, its languages — is read from the provider and is absent here on
// purpose. What remains is what no adapter can know: which engine build this
// is, and whether we are allowed to run it in production.
type ProviderSpec struct {
	// Class is the production/development classification. Required; there is
	// no default.
	Class ProviderClass

	// Tier is the routing preference, delegated to the router unchanged.
	// Ignored for [KindModel], which this registry does not route.
	Tier speech.Tier

	// Engine names the implementation behind the port, for example
	// "whisper.cpp" or "piper". Required.
	//
	// An operational label, not a routing input. Nothing selects on it — the
	// router cannot see it — but "which engine is serving" is the first
	// question asked when a provider misbehaves.
	Engine string

	// Version is the engine or adapter build. Required.
	//
	// Authored because none of these engines reports a version over its port,
	// and a version invented from the binary's mtime would be worse than
	// asking.
	Version string

	// Model identifies the loaded model. Required for [KindModel]; optional
	// elsewhere, since a recogniser or voice may be a single-model program.
	Model ModelIdentity

	// Locality is where the provider executes. Required.
	Locality ProviderLocality
}

// ProviderDescriptor is the readable account of one registered provider.
//
// Returned by value, with its slices copied, so a caller inspecting the
// registry cannot mutate what the next caller reads.
type ProviderDescriptor struct {
	// ID is the provider's identifier, taken from the provider itself.
	ID ProviderID

	// Kind is which port it implements.
	Kind ProviderKind

	// Class is the production/development classification.
	Class ProviderClass

	// Engine names the implementation.
	Engine string

	// Version is the engine or adapter build.
	Version string

	// Model identifies the loaded model.
	Model ModelIdentity

	// Locality is where it executes.
	Locality ProviderLocality

	// Languages it declares. Derived from the provider.
	Languages []speech.Language

	// Capabilities it declares. Derived from the provider.
	Capabilities ProviderCapabilities

	// routed records whether this registry handed the provider to the router,
	// and at which tier. Unexported so the pair cannot be read apart: a tier
	// read without its routed flag is how a model provider would come to look
	// like a primary tier.
	routed bool
	tier   speech.Tier
}

// RoutingTier returns the tier this provider is routed at, and whether this
// registry routes it at all.
//
// # A model provider is never routed here, and never reports a tier
//
// ADR-0006 owns the model ladder. This registry describes a model provider so
// an operator can see what is loaded; it does not select models, and returning
// "primary" for one would be this phase quietly claiming a tier it was told not
// to claim. Generation routing is runtime.Kernel's, unchanged.
func (d ProviderDescriptor) RoutingTier() (speech.Tier, bool) {
	if !d.routed {
		return 0, false
	}
	return d.tier, true
}

// Routable reports whether this registry delegates selection of this provider
// to the router.
func (d ProviderDescriptor) Routable() bool { return d.routed }

// String renders the descriptor for logs and diagnostics.
func (d ProviderDescriptor) String() string {
	tier := "unrouted"
	if t, ok := d.RoutingTier(); ok {
		tier = t.String()
	}
	s := fmt.Sprintf("%s %s/%s %s v%s [%s %s]",
		d.ID, d.Kind, d.Class, d.Engine, d.Version, tier, d.Locality)
	if d.Model.Model != "" {
		s += " model=" + d.Model.String()
	}
	return s
}

// ProviderRegistry describes registered providers and delegates their routing.
//
// Safe for concurrent use. Registration happens at assembly; description and
// routing happen throughout a call.
type ProviderRegistry struct {
	mode   DeploymentMode
	router *speech.ProviderRouter

	mu    sync.RWMutex
	byID  map[ProviderID]ProviderDescriptor
	order []ProviderID
	// models holds generation providers, which the router has no concept of.
	// A LOOKUP, NOT A ROUTING TABLE — see [ProviderRegistry.Model].
	models map[ProviderID]rt.Provider
}

// NewProviderRegistry builds a registry over an existing router.
//
// The router is REQUIRED and is not constructed here. Building one internally
// would make this type the owner of routing configuration — thresholds,
// cooldowns, tiers — and the second place those are decided. It is handed one
// that has already been configured.
func NewProviderRegistry(mode DeploymentMode, router *speech.ProviderRouter) (*ProviderRegistry, error) {
	var problems []string
	if !mode.Valid() {
		problems = append(problems, fmt.Sprintf(
			"deployment mode %q is not declared; it must be %s or %s",
			mode, ModeDevelopment, ModeProduction))
	}
	if router == nil {
		problems = append(problems, "a registry needs the speech provider router: "+
			"it describes providers and delegates their selection, and it does not "+
			"implement selection itself")
	}
	if len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	return &ProviderRegistry{
		mode:   mode,
		router: router,
		byID:   make(map[ProviderID]ProviderDescriptor),
		models: make(map[ProviderID]rt.Provider),
	}, nil
}

// Mode reports what this registry permits.
func (r *ProviderRegistry) Mode() DeploymentMode { return r.mode }

// RegisterSTT describes a recognition provider and registers it for routing.
func (r *ProviderRegistry) RegisterSTT(p speech.STTProvider, spec ProviderSpec) error {
	if p == nil {
		return &ConfigError{Problems: []string{"nil STT provider"}}
	}

	caps := p.Capabilities()
	d, err := r.describe(ProviderID(p.ID()), KindSTT, spec, ProviderCapabilities{
		Streaming:      caps.Streaming,
		PartialResults: caps.PartialResults,
		SampleRates:    caps.SampleRates,
	}, caps.Languages)
	if err != nil {
		return err
	}

	// The router first. It owns duplicate detection and identifier validity,
	// and a descriptor stored before a rejected registration would describe a
	// provider that is not there.
	if err := r.router.RegisterSTT(p, spec.Tier); err != nil {
		return err
	}

	d.routed, d.tier = true, spec.Tier
	return r.store(d, nil)
}

// RegisterTTS describes a synthesis provider and registers it for routing.
func (r *ProviderRegistry) RegisterTTS(p speech.TTSProvider, spec ProviderSpec) error {
	if p == nil {
		return &ConfigError{Problems: []string{"nil TTS provider"}}
	}

	caps := p.Capabilities()
	d, err := r.describe(ProviderID(p.ID()), KindTTS, spec, ProviderCapabilities{
		Streaming:   caps.Streaming,
		SampleRates: caps.SampleRates,
	}, caps.Languages)
	if err != nil {
		return err
	}

	if err := r.router.RegisterTTS(p, spec.Tier); err != nil {
		return err
	}

	d.routed, d.tier = true, spec.Tier
	return r.store(d, nil)
}

// RegisterModel describes a generation provider.
//
// # It is described, and deliberately not routed
//
// speech.ProviderRouter routes recognition and synthesis; it has no concept of
// a language model, and teaching it one would be building the second routing
// engine this phase is forbidden to build. Model selection is ADR-0006's ladder
// inside runtime.Kernel, untouched.
//
// So this records what is loaded — for a readiness endpoint and for an operator
// asking which model answered — and hands back nothing that could select one.
// [ProviderDescriptor.RoutingTier] reports "not routed" for every provider
// registered here.
func (r *ProviderRegistry) RegisterModel(p rt.Provider, spec ProviderSpec) error {
	if p == nil {
		return &ConfigError{Problems: []string{"nil model provider"}}
	}

	caps := p.Capabilities()
	d, err := r.describe(ProviderID(p.ID()), KindModel, spec, ProviderCapabilities{
		Streaming:        caps.Streaming,
		Thinking:         caps.Thinking,
		ToolCalling:      caps.ToolCalling,
		MaxContextTokens: caps.MaxContextTokens,
		MaxOutputTokens:  caps.MaxOutputTokens,
	}, nil)
	if err != nil {
		return err
	}

	return r.store(d, p)
}

// describe validates a registration and builds its descriptor.
func (r *ProviderRegistry) describe(
	id ProviderID, kind ProviderKind, spec ProviderSpec,
	caps ProviderCapabilities, languages []speech.Language,
) (ProviderDescriptor, error) {
	var problems []string

	if !id.Valid() {
		problems = append(problems, fmt.Sprintf(
			"the provider reports the identifier %q, which is not a valid label", id))
	}
	if !spec.Class.Valid() {
		problems = append(problems, fmt.Sprintf(
			"Class is %q: it must be stated as %s or %s. There is no default, "+
				"because defaulting to production is how a development provider "+
				"reaches a caller", spec.Class, ClassProduction, ClassDevelopment))
	}
	if !spec.Locality.Valid() {
		problems = append(problems, fmt.Sprintf("Locality %q is not declared", spec.Locality))
	}
	if strings.TrimSpace(spec.Engine) == "" {
		problems = append(problems, "Engine must name the implementation behind the port")
	}
	if strings.TrimSpace(spec.Version) == "" {
		problems = append(problems, "Version must be set; no engine here reports "+
			"one over its port, so it is authored rather than invented")
	}
	if !spec.Model.Model.Valid() {
		problems = append(problems, fmt.Sprintf(
			"Model %q is not a valid identifier", spec.Model.Model))
	}
	if kind == KindModel && spec.Model.Model == "" {
		problems = append(problems, "Model is required for a generation provider: "+
			"this phase hardcodes no model name, so there is nothing to fall back to")
	}

	// The class rule, and the only one that refuses a working provider.
	if r.mode == ModeProduction && spec.Class == ClassDevelopment {
		problems = append(problems, fmt.Sprintf(
			"provider %q is classified %s and this process runs in %s mode. "+
				"ADR-0006 freezes the production ladder and rejected self-hosted "+
				"open-weight models; a development provider does not become a tier "+
				"by being registered", id, ClassDevelopment, ModeProduction))
	}

	// A descriptor is read by operators and served from status endpoints. A
	// credential reaching one would be published, so the authored strings are
	// checked against the same heuristic the process environment uses.
	for field, value := range map[string]string{
		"Engine":             spec.Engine,
		"Version":            spec.Version,
		"Model.Revision":     spec.Model.Revision,
		"Model.Quantization": spec.Model.Quantization,
		"Model":              string(spec.Model.Model),
	} {
		if value != "" && looksLikeCredential(value) {
			problems = append(problems, fmt.Sprintf(
				"%s looks like a credential; a descriptor is published and carries "+
					"no secrets", field))
		}
	}

	if len(problems) > 0 {
		prefixed := make([]string, 0, len(problems))
		for _, p := range problems {
			prefixed = append(prefixed, fmt.Sprintf("provider %q: %s", id, p))
		}
		return ProviderDescriptor{}, &ConfigError{Problems: prefixed}
	}

	return ProviderDescriptor{
		ID:        id,
		Kind:      kind,
		Class:     spec.Class,
		Engine:    spec.Engine,
		Version:   spec.Version,
		Model:     spec.Model,
		Locality:  spec.Locality,
		Languages: append([]speech.Language(nil), languages...),
		Capabilities: ProviderCapabilities{
			Streaming:        caps.Streaming,
			PartialResults:   caps.PartialResults,
			SampleRates:      append([]media.SampleRate(nil), caps.SampleRates...),
			Thinking:         caps.Thinking,
			ToolCalling:      caps.ToolCalling,
			MaxContextTokens: caps.MaxContextTokens,
			MaxOutputTokens:  caps.MaxOutputTokens,
		},
	}, nil
}

// store records a descriptor, rejecting a duplicate identifier.
func (r *ProviderRegistry) store(d ProviderDescriptor, model rt.Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.byID[d.ID]; dup {
		return &ConfigError{Problems: []string{fmt.Sprintf(
			"provider %q is already registered", d.ID)}}
	}

	r.byID[d.ID] = d
	r.order = append(r.order, d.ID)
	if model != nil {
		r.models[d.ID] = model
	}
	return nil
}

// ---------------------------------------------------------------------------
// Description
// ---------------------------------------------------------------------------

// Describe returns one provider's descriptor.
func (r *ProviderRegistry) Describe(id ProviderID) (ProviderDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.byID[id]
	if !ok {
		return ProviderDescriptor{}, false
	}
	return d.clone(), true
}

// Descriptors returns every descriptor, in registration order.
func (r *ProviderRegistry) Descriptors() []ProviderDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ProviderDescriptor, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id].clone())
	}
	return out
}

// DescriptorsOfKind returns the descriptors for one port, in registration
// order.
func (r *ProviderRegistry) DescriptorsOfKind(kind ProviderKind) []ProviderDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []ProviderDescriptor
	for _, id := range r.order {
		if d := r.byID[id]; d.Kind == kind {
			out = append(out, d.clone())
		}
	}
	return out
}

// Languages returns every language declared by providers of one kind, sorted
// and deduplicated.
//
// What a caller needs before offering a language, and the honest answer to it:
// a language is offerable if some registered provider declares it. Whether one
// is HEALTHY right now is a routing question, and [ProviderRegistry.PickSTT]
// answers that — with the two distinct errors the router already distinguishes.
func (r *ProviderRegistry) Languages(kind ProviderKind) []speech.Language {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[speech.Language]struct{})
	for _, id := range r.order {
		d := r.byID[id]
		if d.Kind != kind {
			continue
		}
		for _, l := range d.Languages {
			seen[l] = struct{}{}
		}
	}

	out := make([]speech.Language, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Count returns how many providers are registered.
func (r *ProviderRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}

// clone returns a descriptor whose slices the caller may keep.
func (d ProviderDescriptor) clone() ProviderDescriptor {
	d.Languages = append([]speech.Language(nil), d.Languages...)
	d.Capabilities.SampleRates = append([]media.SampleRate(nil), d.Capabilities.SampleRates...)
	return d
}

// ---------------------------------------------------------------------------
// Routing: delegated, in full
// ---------------------------------------------------------------------------
//
// Every function below forwards to the router and returns what it returns,
// including its errors unwrapped and unclassified. ErrUnsupportedLanguage and
// ErrProviderUnavailable mean different things — nobody has registered a
// provider for this language, versus some have and none is healthy — and they
// send an operator to different runbooks. Rewrapping them here would collapse
// that distinction.

// PickSTT selects a recognition provider for a language.
func (r *ProviderRegistry) PickSTT(l speech.Language) (speech.STTProvider, error) {
	return r.router.PickSTT(l)
}

// PickTTS selects a synthesis provider for a language.
func (r *ProviderRegistry) PickTTS(l speech.Language) (speech.TTSProvider, error) {
	return r.router.PickTTS(l)
}

// Report records the outcome of a provider call, feeding the breaker.
func (r *ProviderRegistry) Report(id ProviderID, outcome speech.Outcome) {
	r.router.Report(speech.ProviderID(id), outcome)
}

// Health returns one provider's health as the router sees it.
func (r *ProviderRegistry) Health(id ProviderID) speech.ProviderHealth {
	return r.router.Health(speech.ProviderID(id))
}

// AllHealth returns every routed provider's health.
func (r *ProviderRegistry) AllHealth() []speech.ProviderHealth {
	return r.router.AllHealth()
}

// Router returns the router this registry delegates to.
//
// Exposed because the registry adds nothing to routing and hiding it would push
// callers into asking this type to grow routing methods it should not have.
func (r *ProviderRegistry) Router() *speech.ProviderRouter { return r.router }

// Model returns a registered generation provider by identifier.
//
// A LOOKUP BY NAME, NOT A SELECTION. There is no Pick for models here on
// purpose: choosing one is ADR-0006's ladder in runtime.Kernel, and a chooser
// on this type would be a second one.
func (r *ProviderRegistry) Model(id ProviderID) (rt.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.models[id]
	return p, ok
}
