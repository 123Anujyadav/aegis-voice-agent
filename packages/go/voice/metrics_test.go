package voice

import (
	"strings"
	"testing"
)

// forbiddenLabelNames would put unbounded or personal data into the metrics
// backend.
//
// In this module the tempting labels are the dangerous ones: a transcript label
// would make one caller's bad recognition trivially debuggable and would put
// their words into a system with no erasure path.
var forbiddenLabelNames = []string{
	"session", "call", "turn", "correlation",
	"transcript", "text", "content", "response", "prompt", "utterance", "word",
	"phone", "number", "msisdn", "caller", "subscriber", "user", "account",
	"token", "key", "credential", "secret", "model",
	"id", "uuid", "path", "endpoint",
}

// allowedLabelNames is the complete set this module may declare.
//
// "provider" is an AUTHORED identifier, not a vendor's string: it comes from
// configuration and is validated as a label, so its cardinality is the number
// of providers an operator configured.
var allowedLabelNames = map[string]bool{
	"provider": true,
	"kind":     true,
	"outcome":  true,
	"reason":   true,
	"from":     true,
	"to":       true,
	"stage":    true,
}

// labelNamesOf mirrors the declarations in NewVoiceMetrics.
//
// Kept in the test rather than exposed on the instrument, so adding a label
// requires updating this table — which is the review checkpoint.
func labelNamesOf(t *testing.T, name string) []string {
	t.Helper()

	byName := map[string][]string{
		"voice_sessions_opened_total":      {"reason"},
		"voice_sessions_closed_total":      {"reason"},
		"voice_turns_started_total":        nil,
		"voice_turns_completed_total":      {"outcome"},
		"voice_state_transitions_total":    {"from", "to"},
		"voice_invalid_transitions_total":  {"from", "to"},
		"voice_stt_first_partial_seconds":  {"provider"},
		"voice_stt_final_seconds":          {"provider"},
		"voice_model_first_token_seconds":  {"provider"},
		"voice_tts_first_audio_seconds":    {"provider"},
		"voice_turn_seconds":               nil,
		"voice_barge_in_cancel_seconds":    nil,
		"voice_provider_calls_total":       {"provider", "kind", "outcome"},
		"voice_provider_failures_total":    {"provider", "kind", "reason"},
		"voice_provider_switches_total":    {"kind"},
		"voice_provider_restarts_total":    {"provider"},
		"voice_process_failures_total":     {"provider", "reason"},
		"voice_backpressure_total":         {"stage"},
		"voice_dropped_chunks_total":       {"stage", "reason"},
		"voice_stale_chunks_blocked_total": nil,
		"voice_governance_decisions_total": {"outcome"},
	}

	labels, known := byName[name]
	if !known {
		t.Errorf("instrument %q has no declared label set in the test mirror; add "+
			"it, and confirm its labels are bounded while you do", name)
	}
	return labels
}

// TestMetrics_LabelNamesAreBoundedAndDeclared is §17's "no phone number, no
// transcript" checked against what the registry actually declared.
func TestMetrics_LabelNamesAreBoundedAndDeclared(t *testing.T) {
	t.Parallel()

	m := NewVoiceMetrics()

	check := func(instrument string, labels []string) {
		for _, l := range labels {
			if !allowedLabelNames[l] {
				t.Errorf("%s declares label %q, which is not in the allowed set",
					instrument, l)
			}
			for _, bad := range forbiddenLabelNames {
				if l == bad {
					t.Errorf("%s declares label %q, which could carry unbounded or "+
						"personal data", instrument, l)
				}
			}
		}
	}

	for _, c := range m.Registry().Counters() {
		check(c.Name(), labelNamesOf(t, c.Name()))
	}
	for _, h := range m.Registry().Histograms() {
		check(h.Name(), labelNamesOf(t, h.Name()))
	}
}

func TestMetrics_NamesAreWellFormed(t *testing.T) {
	t.Parallel()

	m := NewVoiceMetrics()
	names := m.Registry().Names()
	if len(names) == 0 {
		t.Fatal("the registry declared no instruments")
	}

	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("instrument %q is declared twice", n)
		}
		seen[n] = true

		if !strings.HasPrefix(n, "voice_") {
			t.Errorf("instrument %q lacks the subsystem prefix", n)
		}
		if n != strings.ToLower(n) || strings.Contains(n, "-") {
			t.Errorf("instrument %q is not a well-formed metric name", n)
		}
	}

	if conflicts := m.Registry().KindConflicts(); len(conflicts) > 0 {
		t.Errorf("instrument kind conflicts: %v", conflicts)
	}
}

// TestMetrics_LabelVocabularyIsBounded walks every declared enum value and
// reason code and proves it is safe as a label.
func TestMetrics_LabelVocabularyIsBounded(t *testing.T) {
	t.Parallel()

	var vocabulary []string
	for _, v := range AllProviderKinds() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllTurnOutcomes() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllPipelineStages() {
		vocabulary = append(vocabulary, string(v))
	}
	for _, v := range AllSessionStates() {
		vocabulary = append(vocabulary, string(v))
	}
	vocabulary = append(vocabulary, allReasonCodes()...)

	if len(vocabulary) > 96 {
		t.Errorf("the label vocabulary has grown to %d values; at this size it is "+
			"no longer something a reviewer can check by reading", len(vocabulary))
	}

	for _, v := range vocabulary {
		if v == "" {
			t.Error("the vocabulary contains an empty value")
			continue
		}
		if len(v) > maxReasonLen {
			t.Errorf("%q exceeds the %d-character bound", v, maxReasonLen)
		}
		for _, r := range v {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= '0' && r <= '9':
			case r == '_':
			default:
				t.Errorf("%q contains %q; label values are lowercase alphanumerics "+
					"and underscores", v, r)
			}
		}
	}
}

func TestMetrics_ReusesTheSharedInstruments(t *testing.T) {
	t.Parallel()

	m := NewVoiceMetrics()
	if m.Registry() == nil {
		t.Fatal("no shared registry")
	}
	// If Counter were redefined locally these assignments would not compile.
	var _ *Counter = m.TurnsStarted
	var _ *Gauge = m.SessionsActive
	var _ *Histogram = m.TurnLatency
}

func TestMetrics_RuntimeScopedNotGlobal(t *testing.T) {
	t.Parallel()

	a, b := NewVoiceMetrics(), NewVoiceMetrics()
	a.TurnsStarted.Add(5)

	if got := b.TurnsStarted.Total(); got != 0 {
		t.Errorf("a second instrument set saw %v of the first's counts", got)
	}
	if a.Registry() == b.Registry() {
		t.Error("two instrument sets share one registry")
	}
}

func TestMetrics_RatesAreSafeWhenNothingHasHappened(t *testing.T) {
	t.Parallel()

	m := NewVoiceMetrics()
	if got := m.TurnSuccessRate(); got != 0 {
		t.Errorf("TurnSuccessRate on a fresh registry = %v, want 0", got)
	}
	if got := m.ProviderFailureRate(); got != 0 {
		t.Errorf("ProviderFailureRate on a fresh registry = %v, want 0", got)
	}
}

// TestTurnOutcome_InterruptionIsNotAFailure pins a judgement that matters for
// every dashboard built on this.
//
// A barge-in is the system working — arguably at its best. Counting it as an
// error would make a responsive agent look broken, and would push somebody to
// "fix" it by making barge-in less sensitive.
func TestTurnOutcome_InterruptionIsNotAFailure(t *testing.T) {
	t.Parallel()

	successful := map[TurnOutcome]bool{
		OutcomeCompleted:   true,
		OutcomeInterrupted: true,
		OutcomeDenied:      true,
		OutcomeCancelled:   true,
		OutcomeFailed:      false,
		OutcomeTimeout:     false,
	}

	for _, o := range AllTurnOutcomes() {
		want, listed := successful[o]
		if !listed {
			t.Errorf("outcome %s is not classified by this test", o)
			continue
		}
		if got := o.Successful(); got != want {
			t.Errorf("%s.Successful() = %v, want %v", o, got, want)
		}
	}
}
