package voice

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// Stub providers
// ---------------------------------------------------------------------------
//
// These implement the frozen ports and nothing else. The registry never opens a
// stream, spawns a process or generates a token — it reads Capabilities() and
// hands the provider to the router — so a real adapter here would add a
// subprocess and a model download to prove nothing extra.
//
// What IS real in this file is the router: every routing assertion below runs
// against an actual speech.ProviderRouter, because the claim being tested is
// that this registry delegates to it rather than reimplementing it.

type stubSTT struct {
	id   speech.ProviderID
	caps speech.Capabilities
}

func (s *stubSTT) ID() speech.ProviderID             { return s.id }
func (s *stubSTT) Capabilities() speech.Capabilities { return s.caps }
func (s *stubSTT) OpenSTT(context.Context, speech.STTConfig) (speech.STTStream, error) {
	return nil, errors.New("stub: recognition is not exercised by the registry")
}

type stubTTS struct {
	id   speech.ProviderID
	caps speech.Capabilities
}

func (s *stubTTS) ID() speech.ProviderID             { return s.id }
func (s *stubTTS) Capabilities() speech.Capabilities { return s.caps }
func (s *stubTTS) OpenTTS(context.Context, speech.TTSConfig) (speech.TTSStream, error) {
	return nil, errors.New("stub: synthesis is not exercised by the registry")
}

type stubModel struct {
	id   rt.ProviderID
	caps rt.Capabilities
}

func (s *stubModel) ID() rt.ProviderID             { return s.id }
func (s *stubModel) Capabilities() rt.Capabilities { return s.caps }
func (s *stubModel) Generate(context.Context, rt.GenerateRequest) (rt.TokenStream, error) {
	return nil, errors.New("stub: generation is not exercised by the registry")
}
func (s *stubModel) Probe(context.Context) error { return nil }
func (s *stubModel) Close() error                { return nil }

const (
	langEN = speech.Language("en-GB")
	langHI = speech.Language("hi-IN")
)

func sttStub(id string, languages ...speech.Language) *stubSTT {
	return &stubSTT{
		id: speech.ProviderID(id),
		caps: speech.Capabilities{
			Streaming:      true,
			PartialResults: true,
			Languages:      languages,
			SampleRates:    []media.SampleRate{media.Rate16kHz},
		},
	}
}

func ttsStub(id string, languages ...speech.Language) *stubTTS {
	return &stubTTS{
		id: speech.ProviderID(id),
		caps: speech.Capabilities{
			Streaming:   true,
			Languages:   languages,
			SampleRates: []media.SampleRate{media.Rate16kHz},
		},
	}
}

func modelStub(id string) *stubModel {
	return &stubModel{
		id: rt.ProviderID(id),
		caps: rt.Capabilities{
			Streaming:        true,
			Thinking:         false,
			ToolCalling:      false,
			MaxContextTokens: 8192,
			MaxOutputTokens:  2048,
		},
	}
}

func newRouter(t *testing.T) *speech.ProviderRouter {
	t.Helper()

	r, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), nil, nil)
	if err != nil {
		t.Fatalf("building the router: %v", err)
	}
	return r
}

// newRouterWithClock builds a router whose breaker cooldown a test can advance.
//
// The cooldown is thirty seconds. Waiting for it would make provider-recovery
// tests take half a minute each and turn them into timing bets; advancing an
// injected clock makes the same behaviour deterministic and instant.
func newRouterWithClock(t *testing.T, clock rt.Clock) *speech.ProviderRouter {
	t.Helper()

	r, err := speech.NewProviderRouter(speech.DefaultRouterConfig(), clock, nil)
	if err != nil {
		t.Fatalf("building the router: %v", err)
	}
	return r
}

// newRegistryWithRouter wraps a caller-supplied router.
func newRegistryWithRouter(t *testing.T, router *speech.ProviderRouter) *ProviderRegistry {
	t.Helper()

	reg, err := NewProviderRegistry(ModeDevelopment, router)
	if err != nil {
		t.Fatalf("building the registry: %v", err)
	}
	return reg
}

func newRegistry(t *testing.T, mode DeploymentMode) *ProviderRegistry {
	t.Helper()

	reg, err := NewProviderRegistry(mode, newRouter(t))
	if err != nil {
		t.Fatalf("building the registry: %v", err)
	}
	return reg
}

// sttSpec returns a valid recognition spec.
func sttSpec() ProviderSpec {
	return ProviderSpec{
		Class:    ClassProduction,
		Tier:     speech.TierPrimary,
		Engine:   "whisper.cpp",
		Version:  "1.7.4",
		Model:    ModelIdentity{Model: ModelID("ggml-base-en"), Quantization: "q5_1"},
		Locality: LocalityProcess,
	}
}

// ---------------------------------------------------------------------------
// Descriptors
// ---------------------------------------------------------------------------

func TestRegistry_DescribesWhatTheRouterRefusesToKnow(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	spec := sttSpec()

	if err := reg.RegisterSTT(sttStub("whisper-local", langEN), spec); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	d, ok := reg.Describe(ProviderID("whisper-local"))
	if !ok {
		t.Fatal("the registered provider has no descriptor")
	}

	// The identity vocabulary the router deliberately does not have.
	if d.Engine != "whisper.cpp" {
		t.Errorf("Engine is %q, want whisper.cpp", d.Engine)
	}
	if d.Version != "1.7.4" {
		t.Errorf("Version is %q, want 1.7.4", d.Version)
	}
	if d.Model.Model != ModelID("ggml-base-en") || d.Model.Quantization != "q5_1" {
		t.Errorf("model identity is %v, want ggml-base-en (q5_1)", d.Model)
	}
	if d.Class != ClassProduction {
		t.Errorf("Class is %q, want production", d.Class)
	}
	if d.Locality != LocalityProcess {
		t.Errorf("Locality is %q, want local_process", d.Locality)
	}
	if d.Kind != KindSTT {
		t.Errorf("Kind is %q, want stt", d.Kind)
	}
}

func TestRegistry_CapabilitiesAreReadFromTheProviderNotAuthored(t *testing.T) {
	t.Parallel()

	// A hand-written duplicate drifts from the provider it describes, and the
	// drift surfaces as a router declining work the provider could do. There is
	// deliberately no way to type these in: the spec has no capability field.
	specType := reflect.TypeOf(ProviderSpec{})
	for i := 0; i < specType.NumField(); i++ {
		switch name := specType.Field(i).Name; name {
		case "Capabilities", "Languages", "Streaming", "PartialResults", "SampleRates":
			t.Errorf("ProviderSpec has an authored %q field; capabilities must be "+
				"read from the provider", name)
		}
	}

	reg := newRegistry(t, ModeDevelopment)
	stub := sttStub("whisper-local", langEN, langHI)
	if err := reg.RegisterSTT(stub, sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	d, _ := reg.Describe(ProviderID("whisper-local"))

	if !d.Capabilities.Streaming || !d.Capabilities.PartialResults {
		t.Errorf("capabilities %+v do not match the provider's", d.Capabilities)
	}
	if len(d.Languages) != 2 || d.Languages[0] != langEN || d.Languages[1] != langHI {
		t.Errorf("languages are %v, want the provider's two", d.Languages)
	}
	if len(d.Capabilities.SampleRates) != 1 || d.Capabilities.SampleRates[0] != media.Rate16kHz {
		t.Errorf("sample rates are %v, want the provider's", d.Capabilities.SampleRates)
	}
}

func TestRegistry_DescriptorsCannotBeMutatedByACaller(t *testing.T) {
	t.Parallel()

	// Descriptors are handed to status endpoints and operators. A caller that
	// could edit the slice inside one would change what the next caller reads.
	reg := newRegistry(t, ModeDevelopment)
	if err := reg.RegisterSTT(sttStub("whisper-local", langEN, langHI), sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	first, _ := reg.Describe(ProviderID("whisper-local"))
	first.Languages[0] = speech.Language("tampered")
	first.Capabilities.SampleRates[0] = media.Rate8kHz

	second, _ := reg.Describe(ProviderID("whisper-local"))
	if second.Languages[0] != langEN {
		t.Errorf("a caller mutated the registry's languages: now %v", second.Languages)
	}
	if second.Capabilities.SampleRates[0] != media.Rate16kHz {
		t.Errorf("a caller mutated the registry's sample rates: now %v",
			second.Capabilities.SampleRates)
	}
}

func TestRegistry_ListsDescriptorsInRegistrationOrder(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)

	if err := reg.RegisterSTT(sttStub("stt-primary", langEN), sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	secondary := sttSpec()
	secondary.Tier = speech.TierSecondary
	if err := reg.RegisterSTT(sttStub("stt-secondary", langEN), secondary); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	ttsSpec := sttSpec()
	ttsSpec.Engine, ttsSpec.Model = "piper", ModelIdentity{Model: ModelID("en-gb-medium")}
	if err := reg.RegisterTTS(ttsStub("piper-local", langEN), ttsSpec); err != nil {
		t.Fatalf("RegisterTTS: %v", err)
	}

	all := reg.Descriptors()
	if len(all) != 3 {
		t.Fatalf("got %d descriptors, want 3", len(all))
	}
	want := []ProviderID{"stt-primary", "stt-secondary", "piper-local"}
	for i, id := range want {
		if all[i].ID != id {
			t.Errorf("descriptor %d is %q, want %q", i, all[i].ID, id)
		}
	}

	stt := reg.DescriptorsOfKind(KindSTT)
	if len(stt) != 2 {
		t.Errorf("got %d STT descriptors, want 2", len(stt))
	}
	if got := reg.Count(); got != 3 {
		t.Errorf("Count is %d, want 3", got)
	}
}

func TestRegistry_ReportsTheUnionOfDeclaredLanguages(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)

	if err := reg.RegisterSTT(sttStub("stt-a", langEN), sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}
	secondary := sttSpec()
	secondary.Tier = speech.TierSecondary
	if err := reg.RegisterSTT(sttStub("stt-b", langHI, langEN), secondary); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	got := reg.Languages(KindSTT)
	if len(got) != 2 {
		t.Fatalf("got %v, want two distinct languages", got)
	}
	// Sorted and deduplicated: en-GB before hi-IN, and en-GB only once.
	if got[0] != langEN || got[1] != langHI {
		t.Errorf("got %v, want [%s %s] sorted and deduplicated", got, langEN, langHI)
	}

	if tts := reg.Languages(KindTTS); len(tts) != 0 {
		t.Errorf("no synthesis provider is registered, but Languages(tts) is %v", tts)
	}
}

// ---------------------------------------------------------------------------
// Routing is delegated, not reimplemented
// ---------------------------------------------------------------------------

// TestRegistry_RoutesIdenticallyToTheBareRouter is the anti-second-engine test.
//
// The same providers, the same failures, the same questions — once through a
// bare speech.ProviderRouter and once through the registry — must produce the
// same answers at every step. Any selection logic the registry had acquired of
// its own would have to show up here as a divergence.
func TestRegistry_RoutesIdenticallyToTheBareRouter(t *testing.T) {
	t.Parallel()

	// The bare router, driven directly.
	bare := newRouter(t)
	if err := bare.RegisterSTT(sttStub("primary", langEN), speech.TierPrimary); err != nil {
		t.Fatalf("bare RegisterSTT: %v", err)
	}
	if err := bare.RegisterSTT(sttStub("secondary", langEN), speech.TierSecondary); err != nil {
		t.Fatalf("bare RegisterSTT: %v", err)
	}

	// The same thing through the registry.
	reg := newRegistry(t, ModeDevelopment)
	primarySpec, secondarySpec := sttSpec(), sttSpec()
	secondarySpec.Tier = speech.TierSecondary
	if err := reg.RegisterSTT(sttStub("primary", langEN), primarySpec); err != nil {
		t.Fatalf("registry RegisterSTT: %v", err)
	}
	if err := reg.RegisterSTT(sttStub("secondary", langEN), secondarySpec); err != nil {
		t.Fatalf("registry RegisterSTT: %v", err)
	}

	compare := func(step string) {
		t.Helper()

		bareProvider, bareErr := bare.PickSTT(langEN)
		regProvider, regErr := reg.PickSTT(langEN)

		switch {
		case (bareErr == nil) != (regErr == nil):
			t.Fatalf("%s: bare router error %v, registry error %v", step, bareErr, regErr)
		case bareErr != nil:
			if bareErr.Error() != regErr.Error() {
				t.Errorf("%s: errors differ:\n  bare:     %v\n  registry: %v",
					step, bareErr, regErr)
			}
		default:
			if bareProvider.ID() != regProvider.ID() {
				t.Errorf("%s: bare router picked %s, registry picked %s",
					step, bareProvider.ID(), regProvider.ID())
			}
		}
	}

	compare("both healthy")

	// Open the primary's breaker on both, one failure at a time, comparing
	// after each — a registry that counted differently would diverge before the
	// threshold rather than at it.
	for i := 0; i < speech.DefaultRouterConfig().FailureThreshold; i++ {
		bare.Report(speech.ProviderID("primary"), speech.OutcomeFailure)
		reg.Report(ProviderID("primary"), speech.OutcomeFailure)
		compare(fmt.Sprintf("after %d failures", i+1))
	}

	// And with every provider unhealthy, the second of the two distinct errors.
	for i := 0; i < speech.DefaultRouterConfig().FailureThreshold; i++ {
		bare.Report(speech.ProviderID("secondary"), speech.OutcomeFailure)
		reg.Report(ProviderID("secondary"), speech.OutcomeFailure)
	}
	compare("all unhealthy")
}

func TestRegistry_PreservesTheTwoDistinctRoutingErrors(t *testing.T) {
	t.Parallel()

	// ErrUnsupportedLanguage means nobody declares it: add a provider.
	// ErrProviderUnavailable means some do and none is healthy: fix one.
	// Collapsing them sends an operator to the wrong runbook, so the registry
	// must forward them unwrapped.
	reg := newRegistry(t, ModeDevelopment)
	if err := reg.RegisterSTT(sttStub("stt-en", langEN), sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	_, err := reg.PickSTT(langHI)
	if !errors.Is(err, speech.ErrUnsupportedLanguage) {
		t.Errorf("a language nobody declares must give ErrUnsupportedLanguage, got %v", err)
	}

	for i := 0; i < speech.DefaultRouterConfig().FailureThreshold; i++ {
		reg.Report(ProviderID("stt-en"), speech.OutcomeFailure)
	}
	if _, err := reg.PickSTT(langEN); !errors.Is(err, speech.ErrProviderUnavailable) {
		t.Errorf("a declared but unhealthy language must give ErrProviderUnavailable, "+
			"got %v", err)
	}
}

func TestRegistry_HealthComesFromTheRouter(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	if err := reg.RegisterSTT(sttStub("stt-en", langEN), sttSpec()); err != nil {
		t.Fatalf("RegisterSTT: %v", err)
	}

	if h := reg.Health(ProviderID("stt-en")); h.State != speech.CircuitClosed {
		t.Errorf("a fresh provider is %s, want closed", h.State)
	}

	reg.Report(ProviderID("stt-en"), speech.OutcomeSuccess)
	if h := reg.Health(ProviderID("stt-en")); h.Successes != 1 {
		t.Errorf("the registry's Report did not reach the router: successes = %d",
			h.Successes)
	}

	if all := reg.AllHealth(); len(all) != 1 {
		t.Errorf("AllHealth returned %d entries, want the router's 1", len(all))
	}

	// The router is reachable directly, because the registry adds nothing to
	// routing and hiding it would grow routing methods here.
	if reg.Router() == nil {
		t.Error("Router must expose the router this registry delegates to")
	}
}

func TestRegistry_RejectsWhatTheRouterRejects(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	if err := reg.RegisterSTT(sttStub("duplicate", langEN), sttSpec()); err != nil {
		t.Fatalf("first RegisterSTT: %v", err)
	}

	// Duplicate identifiers are the router's rule, not a second one here.
	if err := reg.RegisterSTT(sttStub("duplicate", langEN), sttSpec()); err == nil {
		t.Fatal("a duplicate provider identifier was accepted")
	}

	// And the descriptor of the rejected registration must not have been kept:
	// a descriptor for a provider the router does not have would be a lie.
	if got := reg.Count(); got != 1 {
		t.Errorf("after a rejected registration the registry holds %d descriptors, "+
			"want 1", got)
	}

	// An identifier the router considers invalid is refused before anything is
	// stored.
	if err := reg.RegisterSTT(sttStub("Not A Valid Label!", langEN), sttSpec()); err == nil {
		t.Error("an invalid provider identifier was accepted")
	}
	if got := reg.Count(); got != 1 {
		t.Errorf("an invalid registration left %d descriptors, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// The ADR-0006 boundary
// ---------------------------------------------------------------------------

func TestRegistry_ProductionModeRefusesADevelopmentProvider(t *testing.T) {
	t.Parallel()

	// ADR-0006 freezes a four-tier ladder, all on Claude, and rejected
	// self-hosted open-weight models as Option 5. A development provider does
	// not become a tier by being registered — and in a production process it is
	// not registered at all.
	reg := newRegistry(t, ModeProduction)

	spec := sttSpec()
	spec.Class = ClassDevelopment

	err := reg.RegisterSTT(sttStub("whisper-local", langEN), spec)
	if err == nil {
		t.Fatal("a development-class provider was registered in production mode")
	}
	if !strings.Contains(err.Error(), "ADR-0006") {
		t.Errorf("the refusal must name the decision it enforces, got %v", err)
	}
	if reg.Count() != 0 {
		t.Error("a refused provider was still described")
	}

	// The router must not have been given it either.
	if _, pickErr := reg.PickSTT(langEN); !errors.Is(pickErr, speech.ErrUnsupportedLanguage) {
		t.Errorf("the refused provider reached the router: PickSTT gave %v", pickErr)
	}
}

func TestRegistry_DevelopmentModeAcceptsBothClasses(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)

	dev := sttSpec()
	dev.Class = ClassDevelopment
	if err := reg.RegisterSTT(sttStub("whisper-local", langEN), dev); err != nil {
		t.Fatalf("development mode must accept a development provider: %v", err)
	}

	prod := sttSpec()
	prod.Tier = speech.TierSecondary
	if err := reg.RegisterSTT(sttStub("whisper-other", langEN), prod); err != nil {
		t.Fatalf("development mode must accept a production provider: %v", err)
	}
}

func TestRegistry_AnUnclassifiedProviderIsRefused(t *testing.T) {
	t.Parallel()

	// The zero value must not mean production: a forgotten field would then
	// silently promote a laptop model into the production path.
	if ClassUnspecified.Valid() {
		t.Error("the zero-value class must not be valid")
	}

	reg := newRegistry(t, ModeProduction)
	spec := sttSpec()
	spec.Class = ClassUnspecified

	err := reg.RegisterSTT(sttStub("whisper-local", langEN), spec)
	if err == nil {
		t.Fatal("a provider with no classification was registered")
	}
	if !strings.Contains(err.Error(), "no default") {
		t.Errorf("the error should explain why there is no default, got %v", err)
	}
}

func TestRegistry_AModelProviderIsDescribedButNeverRouted(t *testing.T) {
	t.Parallel()

	// speech.ProviderRouter routes recognition and synthesis. Teaching it about
	// language models would be the second routing engine this phase forbids, so
	// a model provider is recorded and never selected here.
	reg := newRegistry(t, ModeDevelopment)

	spec := ProviderSpec{
		Class:    ClassDevelopment,
		Engine:   "ollama",
		Version:  "0.12.11",
		Model:    ModelIdentity{Model: ModelID("operator-chosen-model")},
		Locality: LocalityDaemon,
	}
	if err := reg.RegisterModel(modelStub("ollama-dev"), spec); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}

	d, ok := reg.Describe(ProviderID("ollama-dev"))
	if !ok {
		t.Fatal("the model provider has no descriptor")
	}
	if d.Kind != KindModel {
		t.Errorf("Kind is %q, want model", d.Kind)
	}
	if d.Class != ClassDevelopment {
		t.Errorf("Class is %q, want development", d.Class)
	}

	// The critical assertion: it reports no tier at all. Returning "primary"
	// here — the zero value of speech.Tier — is exactly how a development model
	// would come to read as an ADR-0006 primary tier.
	if tier, routed := d.RoutingTier(); routed {
		t.Errorf("a model provider reported tier %s; it must report no tier, "+
			"because ADR-0006 owns the model ladder and this registry does not "+
			"select models", tier)
	}
	if d.Routable() {
		t.Error("a model provider must not be routable by this registry")
	}
	if !strings.Contains(d.String(), "unrouted") {
		t.Errorf("the rendering must say it is unrouted, got %q", d.String())
	}

	// It is retrievable by name — a lookup, not a selection.
	if _, found := reg.Model(ProviderID("ollama-dev")); !found {
		t.Error("the model provider is not retrievable by identifier")
	}
	if _, found := reg.Model(ProviderID("never-registered")); found {
		t.Error("an unregistered identifier returned a model provider")
	}

	// There must be no way to ASK this registry to choose a model.
	regType := reflect.TypeOf(reg)
	for i := 0; i < regType.NumMethod(); i++ {
		switch name := regType.Method(i).Name; {
		case name == "PickModel", name == "PickLLM", name == "SelectModel", name == "RouteModel":
			t.Errorf("ProviderRegistry has a %q method; model selection is "+
				"ADR-0006's ladder in runtime.Kernel, and a chooser here would be "+
				"a second one", name)
		}
	}
}

func TestRegistry_AModelProviderRequiresAnExplicitModel(t *testing.T) {
	t.Parallel()

	// No model name is hardcoded anywhere in this phase, so there is nothing to
	// fall back to when the operator does not name one.
	reg := newRegistry(t, ModeDevelopment)

	spec := ProviderSpec{
		Class:    ClassDevelopment,
		Engine:   "ollama",
		Version:  "0.12.11",
		Locality: LocalityDaemon,
	}
	err := reg.RegisterModel(modelStub("ollama-dev"), spec)
	if err == nil {
		t.Fatal("a generation provider was registered with no model named")
	}
	if !strings.Contains(err.Error(), "Model is required") {
		t.Errorf("the error must say the model is required, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

func TestDescriptor_HasNowhereToPutACredential(t *testing.T) {
	t.Parallel()

	// A descriptor is read by operators and served from status endpoints, so a
	// credential reaching one would be published. Checking the STRUCT rather
	// than an instance means a future field called APIKey fails here rather
	// than in review.
	forbidden := []string{
		"key", "token", "secret", "password", "passwd", "credential",
		"auth", "bearer",
	}

	// "Token" collides: in a model's capabilities it is a unit of text, not a
	// credential. These two are named explicitly rather than by dropping
	// "token" from the list, so a field called AuthToken still fails.
	allowed := map[string]bool{
		"MaxContextTokens": true,
		"MaxOutputTokens":  true,
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(ProviderDescriptor{}),
		reflect.TypeOf(ProviderSpec{}),
		reflect.TypeOf(ModelIdentity{}),
		reflect.TypeOf(ProviderCapabilities{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			if allowed[typ.Field(i).Name] {
				continue
			}
			name := strings.ToLower(typ.Field(i).Name)
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("%s has a field %q, which looks like somewhere a "+
						"credential would go", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}

func TestRegistry_RefusesASpecThatLooksLikeItCarriesASecret(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	spec := sttSpec()
	spec.Version = "api_key=sk-live-abcdef"

	err := reg.RegisterSTT(sttStub("whisper-local", langEN), spec)
	if err == nil {
		t.Fatal("a spec whose version looks like a credential was accepted")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Errorf("the error should say what it objected to, got %v", err)
	}
}

func TestRegistry_DescriptorsCarryNoPathToTheModel(t *testing.T) {
	t.Parallel()

	// Identity, not location. A descriptor is published; a filesystem path
	// describes this machine's layout rather than the model, and the config
	// that loads the model keeps it.
	typ := reflect.TypeOf(ModelIdentity{})
	for i := 0; i < typ.NumField(); i++ {
		if name := strings.ToLower(typ.Field(i).Name); strings.Contains(name, "path") ||
			strings.Contains(name, "dir") || strings.Contains(name, "file") {
			t.Errorf("ModelIdentity has a %q field; a descriptor states what was "+
				"loaded, not where it lives", typ.Field(i).Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration of the registry itself
// ---------------------------------------------------------------------------

func TestRegistry_RefusesToBeBuiltWithoutARouter(t *testing.T) {
	t.Parallel()

	// Building one internally would make this type the owner of routing
	// configuration and the second place thresholds and tiers are decided.
	if _, err := NewProviderRegistry(ModeDevelopment, nil); err == nil {
		t.Fatal("a registry was built with no router")
	}

	if _, err := NewProviderRegistry(DeploymentMode("staging"), newRouter(t)); err == nil {
		t.Fatal("an undeclared deployment mode was accepted")
	}

	reg, err := NewProviderRegistry(ModeProduction, newRouter(t))
	if err != nil {
		t.Fatalf("NewProviderRegistry: %v", err)
	}
	if reg.Mode() != ModeProduction {
		t.Errorf("Mode is %q, want production", reg.Mode())
	}
}

func TestRegistry_ReportsEveryRegistrationProblemAtOnce(t *testing.T) {
	t.Parallel()

	// One problem per run turns a misconfiguration into a guessing game, and
	// this module has used the same multi-problem error since 10A.
	reg := newRegistry(t, ModeProduction)

	spec := ProviderSpec{} // nothing set at all
	err := reg.RegisterSTT(sttStub("whisper-local", langEN), spec)
	if err == nil {
		t.Fatal("an entirely empty spec was accepted")
	}

	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want a *ConfigError, got %T: %v", err, err)
	}
	if len(cfgErr.Problems) < 4 {
		t.Errorf("only %d problems reported for an empty spec: %v",
			len(cfgErr.Problems), cfgErr.Problems)
	}
	for _, want := range []string{"Class", "Locality", "Engine", "Version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is missing from the error: %v", want, err)
		}
	}
}

func TestRegistry_RefusesANilProvider(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)

	if err := reg.RegisterSTT(nil, sttSpec()); err == nil {
		t.Error("a nil STT provider was registered")
	}
	if err := reg.RegisterTTS(nil, sttSpec()); err == nil {
		t.Error("a nil TTS provider was registered")
	}
	if err := reg.RegisterModel(nil, sttSpec()); err == nil {
		t.Error("a nil model provider was registered")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestRegistry_IsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()

	// Registration happens at assembly; description and routing happen
	// throughout a call, from whichever goroutine is serving it.
	reg := newRegistry(t, ModeDevelopment)

	const providers = 8
	var wg sync.WaitGroup

	for i := 0; i < providers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			spec := sttSpec()
			if n%2 == 1 {
				spec.Tier = speech.TierSecondary
			}
			_ = reg.RegisterSTT(sttStub(fmt.Sprintf("stt-%d", n), langEN), spec)
		}(i)
	}

	for i := 0; i < providers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			reg.Descriptors()
			reg.Languages(KindSTT)
			reg.DescriptorsOfKind(KindSTT)
			reg.AllHealth()
			_, _ = reg.Describe(ProviderID(fmt.Sprintf("stt-%d", n)))
			_, _ = reg.PickSTT(langEN)
			reg.Report(ProviderID(fmt.Sprintf("stt-%d", n)), speech.OutcomeSuccess)
		}(i)
	}

	wg.Wait()

	if got := reg.Count(); got != providers {
		t.Errorf("registered %d providers concurrently, registry holds %d",
			providers, got)
	}
}
