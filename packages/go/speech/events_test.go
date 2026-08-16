package speech

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSpeechEvent_TopicShape(t *testing.T) {
	t.Parallel()
	for _, ty := range AllSpeechEventTypes() {
		topic := ty.Topic()
		if !strings.HasPrefix(topic, "speech.session.") {
			t.Errorf("%s: topic %q lacks the speech.session prefix", ty, topic)
		}
		if !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s: topic %q lacks the mandatory version suffix", ty, topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("%s: topic %q contains a hyphen", ty, topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("%s: topic %q is not lowercase", ty, topic)
		}
	}
}

func TestSpeechEvent_FifteenTypesDeclared(t *testing.T) {
	t.Parallel()
	if got := len(AllSpeechEventTypes()); got != 15 {
		t.Errorf("%d event types declared, want 15", got)
	}
	seen := make(map[SpeechEventType]bool)
	for _, ty := range AllSpeechEventTypes() {
		if seen[ty] {
			t.Errorf("%s is declared twice", ty)
		}
		seen[ty] = true
	}
}

// The privacy contract, enforced structurally so a later field addition cannot
// quietly break it.
func TestSpeechEvent_CarriesNoContent(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"payload", "audio", "pcm", "frame", "sample", "buffer",
		"transcript", "text", "utterance", "content", "words",
		"credential", "apikey", "token", "secret", "password",
	}
	ty := reflect.TypeOf(SpeechEvent{})
	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		name := strings.ToLower(field.Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("SpeechEvent.%s may carry content or credentials", field.Name)
			}
		}
		// A byte slice is the shape audio and raw payloads arrive in.
		if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
			t.Errorf("SpeechEvent.%s is a byte slice and could carry audio", field.Name)
		}
		// A map would let anything be smuggled through as metadata.
		if field.Type.Kind() == reflect.Map {
			t.Errorf("SpeechEvent.%s is a map; arbitrary metadata defeats the bound", field.Name)
		}
	}
}

// The rendered forms are what reach logs, so they must be content-free too.
func TestSpeechEvent_SummaryOmitsContent(t *testing.T) {
	t.Parallel()
	e := SpeechEvent{
		Type: EventSpeechFinal, Session: NewSessionID(), Turn: NewTurnID(),
		CharCount: 26, Reason: "final_transcript",
	}
	for _, form := range []string{e.Summary(), e.String()} {
		if strings.Contains(form, "account") || strings.Contains(form, "12345") {
			t.Errorf("rendered event leaked content: %q", form)
		}
	}
}

func TestRecordingEventPublisher_IsBounded(t *testing.T) {
	t.Parallel()
	p := NewBoundedRecordingEventPublisher(3)
	for i := 0; i < 10; i++ {
		if err := p.Publish(context.Background(), SpeechEvent{Type: EventSpeechStarted}); err != nil {
			t.Fatal(err)
		}
	}
	if p.Len() != 3 {
		t.Errorf("held %d events, want the bound of 3", p.Len())
	}
	if p.Dropped() != 7 {
		t.Errorf("dropped = %d, want 7", p.Dropped())
	}
}

func TestRecordingEventPublisher_FiltersByTypeAndSession(t *testing.T) {
	t.Parallel()
	p := NewRecordingEventPublisher()
	a, b := NewSessionID(), NewSessionID()

	for _, e := range []SpeechEvent{
		{Type: EventSpeechStarted, Session: a},
		{Type: EventSpeechFinal, Session: a},
		{Type: EventSpeechStarted, Session: b},
	} {
		if err := p.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(p.OfType(EventSpeechStarted)); got != 2 {
		t.Errorf("OfType returned %d, want 2", got)
	}
	if got := len(p.ForSession(a)); got != 2 {
		t.Errorf("ForSession returned %d, want 2", got)
	}
}

func TestSpeechEvent_PartitionKeyIsTheSession(t *testing.T) {
	t.Parallel()
	// Every event for one session must land on one partition, or a consumer
	// can see final before started.
	id := NewSessionID()
	e := SpeechEvent{Type: EventSpeechFinal, Session: id, Turn: NewTurnID()}
	if e.PartitionKey() != string(id) {
		t.Errorf("PartitionKey() = %q, want the session id", e.PartitionKey())
	}
}

func TestBoundedReason_IsSafeAsALabel(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                       "unspecified",
		"caller_spoke":           "caller_spoke",
		"Caller Spoke":           "caller_spoke",
		"barge-in":               "barge_in",
		"!!!":                    "unspecified",
		strings.Repeat("a", 200): strings.Repeat("a", maxReasonLen),
	}
	for in, want := range cases {
		if got := boundedReason(in); got != want {
			t.Errorf("boundedReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNopEventPublisher_Discards(t *testing.T) {
	t.Parallel()
	if err := (NopEventPublisher{}).Publish(context.Background(), SpeechEvent{}); err != nil {
		t.Errorf("NopEventPublisher returned %v", err)
	}
}

// Metric labels must be bounded enums or authored identifiers, never content.
func TestSpeechMetrics_NoContentBearingLabels(t *testing.T) {
	t.Parallel()
	m := NewSpeechMetrics()
	if m == nil {
		t.Fatal("nil metrics")
	}
	// Language labels are clamped, so a malformed provider tag cannot inject
	// unbounded cardinality.
	for _, in := range []Language{
		Language("this-is-a-very-long-and-malformed-language-tag-from-a-provider"),
		Language("hi; DROP TABLE"),
		LangHinglish,
	} {
		label := in.Label()
		if len(label) > 16 {
			t.Errorf("Label() for %q is %d characters", in, len(label))
		}
		for _, r := range label {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-'
			if !ok {
				t.Errorf("Label() for %q contains %q", in, r)
			}
		}
	}
}
