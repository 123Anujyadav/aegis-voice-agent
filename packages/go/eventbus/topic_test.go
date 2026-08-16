package eventbus

import (
	"errors"
	"strings"
	"testing"
)

// TestParseTopic_Valid confirms that names following the convention parse and
// decompose into their segments.
func TestParseTopic_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantDomain  string
		wantEntity  string
		wantEvent   string
		wantVersion int
	}{
		{
			name:        "canonical example",
			input:       "telephony.call.answered.v1",
			wantDomain:  "telephony", wantEntity: "call",
			wantEvent: "answered", wantVersion: 1,
		},
		{
			name:        "underscores inside segments",
			input:       "billing.subscription_plan.price_changed.v2",
			wantDomain:  "billing", wantEntity: "subscription_plan",
			wantEvent: "price_changed", wantVersion: 2,
		},
		{
			name:        "multi digit version",
			input:       "ai.transcript.finalised.v12",
			wantDomain:  "ai", wantEntity: "transcript",
			wantEvent: "finalised", wantVersion: 12,
		},
		{
			name:        "single character segments",
			input:       "a.b.c.v1",
			wantDomain:  "a", wantEntity: "b", wantEvent: "c", wantVersion: 1,
		},
		{
			name:        "digits inside segments",
			input:       "telephony.sip2.invite_received.v1",
			wantDomain:  "telephony", wantEntity: "sip2",
			wantEvent: "invite_received", wantVersion: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topic, err := ParseTopic(tt.input)
			if err != nil {
				t.Fatalf("ParseTopic(%q) returned error: %v", tt.input, err)
			}
			if topic.Domain() != tt.wantDomain {
				t.Errorf("Domain() = %q, want %q", topic.Domain(), tt.wantDomain)
			}
			if topic.Entity() != tt.wantEntity {
				t.Errorf("Entity() = %q, want %q", topic.Entity(), tt.wantEntity)
			}
			if topic.Event() != tt.wantEvent {
				t.Errorf("Event() = %q, want %q", topic.Event(), tt.wantEvent)
			}
			if topic.Version() != tt.wantVersion {
				t.Errorf("Version() = %d, want %d", topic.Version(), tt.wantVersion)
			}
			if topic.String() != tt.input {
				t.Errorf("String() = %q, want %q", topic.String(), tt.input)
			}
			if topic.IsZero() {
				t.Error("IsZero() = true for a parsed topic")
			}
		})
	}
}

// TestParseTopic_Invalid covers the malformed names the convention must reject.
// Each case is one a real engineer would plausibly write.
func TestParseTopic_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "missing version suffix", input: "telephony.call.answered"},
		{name: "version without v prefix", input: "telephony.call.answered.1"},
		{name: "version zero", input: "telephony.call.answered.v0"},
		{name: "leading zero in version", input: "telephony.call.answered.v01"},
		{name: "too few segments", input: "telephony.call.v1"},
		{name: "too many segments", input: "telephony.call.sub.answered.v1"},
		{name: "uppercase", input: "Telephony.Call.Answered.v1"},
		// Hyphens are excluded because Prometheus exporters rewrite them to
		// underscores, silently merging two distinct topics on a dashboard.
		{name: "hyphen in segment", input: "telephony.call-session.answered.v1"},
		{name: "leading underscore", input: "telephony._call.answered.v1"},
		{name: "trailing underscore", input: "telephony.call_.answered.v1"},
		{name: "leading digit", input: "1telephony.call.answered.v1"},
		{name: "empty segment", input: "telephony..answered.v1"},
		{name: "trailing dot", input: "telephony.call.answered.v1."},
		{name: "whitespace", input: "telephony.call. answered.v1"},
		{name: "over length limit", input: strings.Repeat("a", 250) + ".b.c.v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseTopic(tt.input)
			if err == nil {
				t.Fatalf("ParseTopic(%q) succeeded, want an error", tt.input)
			}
			if !errors.Is(err, ErrInvalidTopic) {
				t.Errorf("error %v does not wrap ErrInvalidTopic", err)
			}
		})
	}
}

// TestMustParseTopic_PanicsOnInvalid confirms the fail-fast contract for
// package-level topic constants.
func TestMustParseTopic_PanicsOnInvalid(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("MustParseTopic did not panic on an invalid name")
		}
	}()

	MustParseTopic("not a topic")
}

// TestMustParseTopic_ReturnsOnValid confirms the happy path does not panic.
func TestMustParseTopic_ReturnsOnValid(t *testing.T) {
	t.Parallel()

	topic := MustParseTopic("telephony.call.answered.v1")
	if topic.String() != "telephony.call.answered.v1" {
		t.Errorf("String() = %q", topic.String())
	}
}

// TestDeadLetterTopic_IsNotItselfParseable is a structural guarantee, not a
// cosmetic one: a dead-letter topic must never be consumed by the normal
// pipeline, and making its name unparseable enforces that in the type system
// rather than by convention.
func TestDeadLetterTopic_IsNotItselfParseable(t *testing.T) {
	t.Parallel()

	topic := MustParseTopic("telephony.call.answered.v1")
	dlq := topic.DeadLetterTopic()

	if dlq != "telephony.call.answered.v1.dlq" {
		t.Errorf("DeadLetterTopic() = %q", dlq)
	}
	if _, err := ParseTopic(dlq); err == nil {
		t.Error("the dead-letter topic parses as a normal topic, which would allow the main pipeline to consume it")
	}
}

// TestRetryTopic covers the staged-retry naming and its guard against a
// non-positive attempt, which would otherwise produce a name colliding with the
// source topic.
func TestRetryTopic(t *testing.T) {
	t.Parallel()

	topic := MustParseTopic("ai.transcript.finalised.v1")

	tests := []struct {
		attempt int
		want    string
	}{
		{attempt: 1, want: "ai.transcript.finalised.v1.retry-1"},
		{attempt: 3, want: "ai.transcript.finalised.v1.retry-3"},
		{attempt: 0, want: ""},
		{attempt: -1, want: ""},
	}

	for _, tt := range tests {
		if got := topic.RetryTopic(tt.attempt); got != tt.want {
			t.Errorf("RetryTopic(%d) = %q, want %q", tt.attempt, got, tt.want)
		}
	}
}

// TestNormaliseForMetrics pins the label-safe rendering.
func TestNormaliseForMetrics(t *testing.T) {
	t.Parallel()

	topic := MustParseTopic("telephony.call.answered.v1")
	if got := topic.NormaliseForMetrics(); got != "telephony_call_answered_v1" {
		t.Errorf("NormaliseForMetrics() = %q", got)
	}
}

// TestValidateConsumerGroup covers the <service>.<purpose> convention.
func TestValidateConsumerGroup(t *testing.T) {
	t.Parallel()

	valid := []string{
		"fraud-engine.scoring",
		"session-orchestrator.turn_taking",
		"billing.webhooks",
	}
	for _, group := range valid {
		if err := ValidateConsumerGroup(group); err != nil {
			t.Errorf("ValidateConsumerGroup(%q) returned %v, want nil", group, err)
		}
	}

	invalid := []string{
		"",
		"fraud-engine",           // no purpose segment
		"Fraud-Engine.scoring",   // uppercase
		"fraud-engine.scoring.x", // too many segments
		".scoring",               // empty service
		"fraud-engine.",          // empty purpose
	}
	for _, group := range invalid {
		err := ValidateConsumerGroup(group)
		if err == nil {
			t.Errorf("ValidateConsumerGroup(%q) succeeded, want an error", group)
			continue
		}
		if !errors.Is(err, ErrInvalidConsumerGroup) {
			t.Errorf("error for %q does not wrap ErrInvalidConsumerGroup: %v", group, err)
		}
	}
}

// TestZeroTopic confirms the zero value is detectable, so an uninitialised
// struct field is caught rather than publishing to the empty topic.
func TestZeroTopic(t *testing.T) {
	t.Parallel()

	var topic Topic
	if !topic.IsZero() {
		t.Error("IsZero() = false for the zero value")
	}
	if topic.String() != "" {
		t.Errorf("String() = %q for the zero value", topic.String())
	}
}
