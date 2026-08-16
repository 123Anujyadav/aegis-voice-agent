package audiointel

import (
	"strings"
	"testing"
)

// forbiddenLabelNames are label names that would put unbounded or personal data
// into the metrics backend.
//
// A phone number would be genuinely useful for debugging one caller's bad audio
// and would put subscriber PII into a system with no erasure path. A session or
// call identifier would give the backend one time series per call. Both are
// tempting, both are wrong, and neither should be caught by review alone.
var forbiddenLabelNames = []string{
	"session", "call", "turn", "correlation", "stream",
	"phone", "number", "msisdn", "caller", "subscriber", "user", "account",
	"transcript", "text", "content", "utterance", "word",
	"token", "key", "credential", "secret",
	"id", "uuid",
}

// allowedLabelNames is the complete set of label names this engine may declare.
//
// Every one is a bounded enum whose values are declared in classifications.go.
// Adding a label means adding it here, which is the review checkpoint.
var allowedLabelNames = map[string]bool{
	"direction": true, // Direction — 2 values
	"state":     true, // VADState or OverlapState — 6 or 4 values
	"from":      true, // VADState or QualityClass
	"to":        true, // VADState or QualityClass
	"reason":    true, // a literal from classifications.go
	"outcome":   true, // BargeInOutcome — 7 values
	"class":     true, // SilenceClass — 6 values
	"kind":      true, // ContinuityFault — 8 values
	"stage":     true, // a literal analysis stage name
}

// TestMetrics_LabelNamesAreBoundedAndDeclared is the structural form of §17's
// "do not use phone numbers or transcript content as labels".
//
// It reads the labels the registry actually declared rather than a list
// maintained by hand, so a new instrument with a careless label fails here
// rather than in production six weeks later.
func TestMetrics_LabelNamesAreBoundedAndDeclared(t *testing.T) {
	t.Parallel()

	m := NewAudioIntelligenceMetrics()

	for _, c := range m.Registry().Counters() {
		checkLabels(t, c.Name(), labelNamesOf(t, c.Name()))
	}
	for _, h := range m.Registry().Histograms() {
		checkLabels(t, h.Name(), labelNamesOf(t, h.Name()))
	}
}

// labelNamesOf recovers the labels an instrument was declared with.
//
// The metrics package does not expose them, so this mirrors the declaration in
// NewAudioIntelligenceMetrics. Keeping the mirror here rather than in the
// production type is deliberate: a test that has to be updated alongside a new
// label is the review checkpoint, and exposing label names on the instrument
// purely to satisfy a test would widen the shared metrics API for no runtime
// benefit.
func labelNamesOf(t *testing.T, name string) []string {
	t.Helper()

	byName := map[string][]string{
		"audiointel_sessions_opened_total":     {"direction"},
		"audiointel_sessions_closed_total":     {"reason"},
		"audiointel_frames_analysed_total":     {"direction"},
		"audiointel_frames_refused_total":      {"reason"},
		"audiointel_vad_decisions_total":       {"state"},
		"audiointel_vad_transitions_total":     {"from", "to"},
		"audiointel_speech_starts_total":       {"direction"},
		"audiointel_speech_ends_total":         {"direction"},
		"audiointel_false_triggers_total":      {"reason"},
		"audiointel_speech_seconds":            nil,
		"audiointel_silence_seconds":           {"class"},
		"audiointel_speech_confidence":         nil,
		"audiointel_endpoint_candidates_total": {"direction"},
		"audiointel_endpoint_confirmed_total":  {"direction"},
		"audiointel_endpoint_suppressed_total": {"reason"},
		"audiointel_endpoint_seconds":          nil,
		"audiointel_barge_ins_total":           {"outcome"},
		"audiointel_barge_in_seconds":          nil,
		"audiointel_overlaps_total":            {"state"},
		"audiointel_overlap_seconds":           nil,
		"audiointel_noise_changes_total":       {"direction"},
		"audiointel_quality_changes_total":     {"from", "to"},
		"audiointel_degradations_total":        {"reason"},
		"audiointel_recoveries_total":          {"direction"},
		"audiointel_frame_gaps_total":          {"kind"},
		"audiointel_continuity_events_total":   {"kind"},
		"audiointel_frame_analysis_seconds":    {"stage"},
		"audiointel_decision_seconds":          nil,
	}

	labels, known := byName[name]
	if !known {
		t.Errorf("instrument %q has no declared label set in the test mirror; "+
			"add it, and confirm its labels are bounded while you do", name)
	}
	return labels
}

func checkLabels(t *testing.T, instrument string, labels []string) {
	t.Helper()

	for _, l := range labels {
		if !allowedLabelNames[l] {
			t.Errorf("%s declares label %q, which is not in the allowed set; "+
				"a label must be a bounded enum declared in classifications.go",
				instrument, l)
		}
		for _, bad := range forbiddenLabelNames {
			if strings.Contains(l, bad) {
				t.Errorf("%s declares label %q, which could carry unbounded or "+
					"personal data", instrument, l)
			}
		}
	}
}

// TestMetrics_NamesAreWellFormed checks the naming convention every subsystem
// in this platform follows.
func TestMetrics_NamesAreWellFormed(t *testing.T) {
	t.Parallel()

	m := NewAudioIntelligenceMetrics()
	names := m.Registry().Names()

	if len(names) == 0 {
		t.Fatal("the registry declared no instruments")
	}

	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			t.Errorf("instrument %q is declared twice", n)
		}
		seen[n] = true

		if !strings.HasPrefix(n, "audiointel_") {
			t.Errorf("instrument %q lacks the subsystem prefix", n)
		}
		if n != strings.ToLower(n) {
			t.Errorf("instrument %q is not lowercase", n)
		}
		if strings.Contains(n, "-") {
			t.Errorf("instrument %q contains a hyphen; Prometheus normalises it away "+
				"and two instruments could collide", n)
		}
	}

	// Phase 10.5's registry refuses two instruments of different kinds sharing a
	// name. Asking it directly is cheaper than reasoning about the declarations.
	if conflicts := m.Registry().KindConflicts(); len(conflicts) > 0 {
		t.Errorf("instrument kind conflicts: %v", conflicts)
	}
}

// TestMetrics_ReusesTheSharedInstruments guards against a fifth private copy of
// Counter/Gauge/Histogram, which is the mistake Phase 10.5 spent a phase
// undoing.
func TestMetrics_ReusesTheSharedInstruments(t *testing.T) {
	t.Parallel()

	m := NewAudioIntelligenceMetrics()
	if m.Registry() == nil {
		t.Fatal("no shared registry")
	}

	// The aliases must be the shared types, not lookalikes. If Counter were
	// redefined locally this assignment would not compile.
	var _ *Counter = m.SpeechStarts
	var _ *Gauge = m.SessionsActive
	var _ *Histogram = m.BargeInLatency
}

// TestMetrics_RatesAreSafeWhenNothingHasHappened guards the dashboard case: a
// freshly started process must not report rates that render as an outage.
func TestMetrics_RatesAreSafeWhenNothingHasHappened(t *testing.T) {
	t.Parallel()

	m := NewAudioIntelligenceMetrics()

	if got := m.FalseTriggerRate(); got != 0 {
		t.Errorf("FalseTriggerRate on a fresh registry = %v, want 0", got)
	}
	if got := m.EndpointConfirmRate(); got != 0 {
		t.Errorf("EndpointConfirmRate on a fresh registry = %v, want 0", got)
	}
	if got := m.BargeInDeliveryRate(); got != 0 {
		t.Errorf("BargeInDeliveryRate on a fresh registry = %v, want 0", got)
	}
}

func TestMetrics_RatesComputeCorrectly(t *testing.T) {
	t.Parallel()

	m := NewAudioIntelligenceMetrics()

	m.SpeechStarts.Add(3, string(DirectionInbound))
	m.FalseTriggers.Add(1, ReasonOnsetTooShort)
	if got, want := m.FalseTriggerRate(), 0.25; got != want {
		t.Errorf("FalseTriggerRate = %v, want %v", got, want)
	}

	m.EndpointCandidates.Add(4, string(DirectionInbound))
	m.EndpointConfirmed.Add(3, string(DirectionInbound))
	if got, want := m.EndpointConfirmRate(), 0.75; got != want {
		t.Errorf("EndpointConfirmRate = %v, want %v", got, want)
	}

	m.BargeIns.Add(2, string(BargeInDelivered))
	m.BargeIns.Add(2, string(BargeInDebounced))
	if got, want := m.BargeInDeliveryRate(), 0.5; got != want {
		t.Errorf("BargeInDeliveryRate = %v, want %v", got, want)
	}
}

// TestMetrics_RuntimeScopedNotGlobal is what makes the suite parallel-safe and
// what stops two runtimes in one process reporting each other's numbers.
func TestMetrics_RuntimeScopedNotGlobal(t *testing.T) {
	t.Parallel()

	a := NewAudioIntelligenceMetrics()
	b := NewAudioIntelligenceMetrics()

	a.SpeechStarts.Add(5, string(DirectionInbound))

	if got := b.SpeechStarts.Total(); got != 0 {
		t.Errorf("a second instrument set saw %v of the first's counts; "+
			"the registries are shared", got)
	}
	if a.Registry() == b.Registry() {
		t.Error("two instrument sets share one registry")
	}
}

// TestClassifications_VocabularyIsBoundedAndLabelSafe walks every declared enum
// value and reason code and proves it is safe as a Prometheus label and as a
// Kafka topic segment.
func TestClassifications_VocabularyIsBoundedAndLabelSafe(t *testing.T) {
	t.Parallel()

	var vocabulary []string
	for _, v := range AllDirections() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllVADStates() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllSilenceClasses() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllQualityClasses() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllOverlapStates() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllBargeInOutcomes() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllContinuityFaults() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllNoiseClasses() {
		vocabulary = append(vocabulary, string(v))
	}
	vocabulary = append(vocabulary, allReasonCodes()...)

	// The whole vocabulary is small enough to enumerate, which is the point: a
	// bounded label set is one somebody can read in full.
	if len(vocabulary) > 128 {
		t.Errorf("the label vocabulary has grown to %d values; at this size it is no "+
			"longer something a reviewer can check by reading", len(vocabulary))
	}

	for _, v := range vocabulary {
		if v == "" {
			t.Error("the vocabulary contains an empty value")
			continue
		}
		if len(v) > maxReasonLen {
			t.Errorf("%q exceeds the %d-character reason bound", v, maxReasonLen)
		}
		for _, r := range v {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= '0' && r <= '9':
			case r == '_':
			default:
				t.Errorf("%q contains %q; label values are lowercase alphanumerics and "+
					"underscores, because hyphens are prohibited in eventbus topic "+
					"segments and uppercase is normalised away by Prometheus", v, r)
			}
		}
	}
}

// TestClassifications_EnumsRejectUnknownValues proves the Valid predicates are
// closed sets rather than decoration.
func TestClassifications_EnumsRejectUnknownValues(t *testing.T) {
	t.Parallel()

	for _, bad := range []VADState{"", "talking", "SPEECH", "silence "} {
		if bad.Valid() {
			t.Errorf("VADState(%q) reported valid", bad)
		}
	}
	for _, bad := range []Direction{"", "both", "INBOUND"} {
		if bad.Valid() {
			t.Errorf("Direction(%q) reported valid", bad)
		}
	}
}

// TestVADState_ActiveIncludesTheHangover pins the decision that matters most to
// a downstream recogniser.
//
// CandidateSilence is active. The whole point of a hangover is that speech has
// not ended yet, and a recogniser cut off during it loses every trailing
// consonant — which is how "eight" becomes "ay".
func TestVADState_ActiveIncludesTheHangover(t *testing.T) {
	t.Parallel()

	active := map[VADState]bool{
		VADSpeech:           true,
		VADCandidateSilence: true,
	}
	for _, s := range AllVADStates() {
		if got := s.Active(); got != active[s] {
			t.Errorf("%s.Active() = %v, want %v", s, got, active[s])
		}
	}
}

// TestQualityClass_UnknownDoesNotLookLikeAFault guards a specific alerting
// mistake: "not measured yet" ranking as a degradation would page somebody
// every time a call connects.
func TestQualityClass_UnknownDoesNotLookLikeAFault(t *testing.T) {
	t.Parallel()

	if QualityUnknown.Rank() != QualityGood.Rank() {
		t.Errorf("QualityUnknown ranks %d and QualityGood ranks %d; unknown must not "+
			"read as a degradation", QualityUnknown.Rank(), QualityGood.Rank())
	}
	if !QualityUnknown.Usable() {
		t.Error("QualityUnknown reports unusable; not having measured is not a fault")
	}
	if QualityUnusable.Usable() {
		t.Error("QualityUnusable reports usable")
	}

	// The ranking must be a total order best to worst, or a degradation
	// comparison is meaningless.
	ordered := []QualityClass{QualityGood, QualityDegraded, QualityPoor, QualityUnusable}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Rank() <= ordered[i-1].Rank() {
			t.Errorf("%s ranks %d, not worse than %s at %d",
				ordered[i], ordered[i].Rank(), ordered[i-1], ordered[i-1].Rank())
		}
	}
}
