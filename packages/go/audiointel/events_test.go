package audiointel

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestAudioEvent_CarriesNoAudio is the structural form of §24.
//
// # Why this is a reflection test rather than a code review item
//
// An audio intelligence topic is a record of every call the platform listened
// to. Kafka cannot delete an individual record, so a field that can hold a
// sample is a recording nobody consented to and nobody can erase. Review
// catches that on the day the field is added and never again; reflection
// catches it on every run, including the run six months later when somebody
// adds "just a small buffer for debugging".
//
// It walks NESTED structs too, which is the difference between this and the
// Phase 11B version it is modelled on — EventDetail would otherwise be an
// unexamined hole.
func TestAudioEvent_CarriesNoAudio(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"payload", "audio", "sample", "pcm", "frame", "buffer", "recording",
		"transcript", "text", "content", "words", "utterance",
		"phone", "number", "msisdn", "caller", "credential", "token", "key",
	}

	// FrameCount is a COUNT of frames, not a frame. Named exceptions are listed
	// here so the substring rule can stay blunt everywhere else — a blunt rule
	// with three exceptions is safer than a clever rule with none.
	allowed := map[string]bool{
		"FrameCount": true,
	}

	// Only structs declared in THIS package are walked. time.Time carries an
	// unexported *Location that no consumer can reach and that this rule has
	// nothing useful to say about; recursing into the standard library would
	// make the test fail on an implementation detail rather than on a policy
	// breach.
	const ownPackage = "github.com/callscreen/callscreen-platform/packages/go/audiointel"

	var walk func(ty reflect.Type, path string)
	walk = func(ty reflect.Type, path string) {
		for i := 0; i < ty.NumField(); i++ {
			field := ty.Field(i)
			full := path + "." + field.Name

			// Unexported fields cannot be populated by a producer outside this
			// package and cannot be read by a consumer at all.
			if field.PkgPath != "" {
				continue
			}

			if !allowed[field.Name] {
				name := strings.ToLower(field.Name)
				for _, bad := range forbidden {
					if strings.Contains(name, bad) {
						t.Errorf("%s may carry content or identify a person", full)
					}
				}
			}

			switch field.Type.Kind() {
			case reflect.Slice, reflect.Array:
				if field.Type.Elem().Kind() == reflect.Uint8 {
					t.Errorf("%s is a byte sequence and could carry audio", full)
				}
			case reflect.Map:
				t.Errorf("%s is a map; bounded metadata must be a fixed struct so this "+
					"test can prove what it holds", full)
			case reflect.Interface:
				t.Errorf("%s is an interface and could hold anything at all", full)
			case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Func:
				t.Errorf("%s is a %s; an event must be a value a consumer can copy",
					full, field.Type.Kind())
			case reflect.Struct:
				if field.Type.PkgPath() == ownPackage {
					walk(field.Type, full)
				}
			}
		}
	}

	walk(reflect.TypeOf(AudioEvent{}), "AudioEvent")
}

// TestAudioEventTypes_AreCompleteAndWellFormed pins the sixteen types §16
// requires and proves each produces a topic packages/go/eventbus will accept.
func TestAudioEventTypes_AreCompleteAndWellFormed(t *testing.T) {
	t.Parallel()

	required := []AudioEventType{
		"speech_started", "speech_detected", "speech_stopped",
		"endpoint_candidate", "endpoint_confirmed",
		"silence_started", "silence_extended",
		"barge_in_detected", "overlap_detected", "overlap_resolved",
		"noise_changed", "quality_changed", "audio_degraded", "audio_recovered",
		"frame_gap_detected", "frame_continuity_restored",
	}

	all := AllAudioEventTypes()
	if len(all) != len(required) {
		t.Fatalf("declared %d event types, §16 requires %d", len(all), len(required))
	}

	declared := make(map[AudioEventType]bool, len(all))
	for _, tp := range all {
		declared[tp] = true
	}
	for _, want := range required {
		if !declared[want] {
			t.Errorf("§16 requires event type %q and it is not declared", want)
		}
		if !want.Valid() {
			t.Errorf("%q is required but Valid() reports false", want)
		}
	}

	seen := make(map[string]AudioEventType, len(all))
	for _, tp := range all {
		topic := tp.Topic()

		if prev, dup := seen[topic]; dup {
			t.Errorf("%s and %s produce the same topic %q", prev, tp, topic)
		}
		seen[topic] = tp

		if !strings.HasPrefix(topic, "audio.intel.") {
			t.Errorf("%s topic %q lacks the domain prefix", tp, topic)
		}
		if !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s topic %q is unversioned; retrofitting a version onto a live "+
				"topic needs a dual-write migration across every consumer", tp, topic)
		}
		// Hyphens collide with Prometheus metric-name normalisation and are
		// prohibited in topic segments by packages/go/eventbus.
		if strings.Contains(topic, "-") {
			t.Errorf("%s topic %q contains a hyphen", tp, topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("%s topic %q is not lowercase", tp, topic)
		}
	}
}

func TestAudioEventType_ValidRejectsUnknown(t *testing.T) {
	t.Parallel()

	for _, bad := range []AudioEventType{"", "speech", "SPEECH_STARTED", "made_up"} {
		if bad.Valid() {
			t.Errorf("%q reported valid", bad)
		}
	}
}

func TestRecordingEventPublisher_IsBounded(t *testing.T) {
	t.Parallel()

	p := NewBoundedRecordingEventPublisher(3)
	for i := 0; i < 10; i++ {
		if err := p.Publish(context.Background(), AudioEvent{
			Type: EventSpeechStarted, Sequence: i,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if p.Len() != 3 {
		t.Errorf("held %d events, want the bound of 3", p.Len())
	}
	if p.Dropped() != 7 {
		t.Errorf("dropped = %d, want 7", p.Dropped())
	}

	// The NEWEST are kept: a recorder that discarded recent events would show an
	// operator the start of an incident and none of it.
	got := p.Events()
	if got[len(got)-1].Sequence != 9 {
		t.Errorf("newest retained event has sequence %d, want 9", got[len(got)-1].Sequence)
	}
}

func TestRecordingEventPublisher_Filters(t *testing.T) {
	t.Parallel()

	p := NewRecordingEventPublisher()
	ctx := context.Background()

	_ = p.Publish(ctx, AudioEvent{Type: EventSpeechStarted, Session: "ai-a"})
	_ = p.Publish(ctx, AudioEvent{Type: EventSpeechStopped, Session: "ai-a"})
	_ = p.Publish(ctx, AudioEvent{Type: EventSpeechStarted, Session: "ai-b"})

	if n := len(p.OfType(EventSpeechStarted)); n != 2 {
		t.Errorf("OfType(speech_started) returned %d, want 2", n)
	}
	if n := p.Count(EventSpeechStopped); n != 1 {
		t.Errorf("Count(speech_stopped) = %d, want 1", n)
	}
	if n := len(p.ForSession("ai-a")); n != 2 {
		t.Errorf("ForSession(ai-a) returned %d, want 2", n)
	}

	p.Reset()
	if p.Len() != 0 || p.Dropped() != 0 {
		t.Errorf("after Reset: len=%d dropped=%d, want 0/0", p.Len(), p.Dropped())
	}
}

func TestAudioEvent_SummaryIsContentFree(t *testing.T) {
	t.Parallel()

	e := AudioEvent{
		Type:           EventSpeechStarted,
		Session:        "ai-abc",
		Classification: "speech",
		Confidence:     0.87,
		Reason:         "onset_confirmed",
		Detail:         EventDetail{DurationMillis: 240, LevelDBFS: -21.5},
	}

	s := e.Summary()
	for _, want := range []string{"speech_started", "ai-abc", "speech", "0.87", "240ms"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary() omits %q: %s", want, s)
		}
	}
	if e.String() != s {
		t.Error("String() and Summary() disagree")
	}
}

func TestAudioEvent_PartitionKeyIsTheSession(t *testing.T) {
	t.Parallel()

	e := AudioEvent{Session: "ai-xyz"}
	if e.PartitionKey() != "ai-xyz" {
		t.Errorf("PartitionKey() = %q, want the session identifier", e.PartitionKey())
	}
}

func TestNopEventPublisher_Discards(t *testing.T) {
	t.Parallel()

	if err := (NopEventPublisher{}).Publish(context.Background(), AudioEvent{}); err != nil {
		t.Fatalf("NopEventPublisher returned %v", err)
	}
}

// assertNoByteSlices walks a value and fails if any exported field at any depth
// is a byte sequence.
//
// Used by TestScenarios_NoPCMEscapesTheEngine to cover the RETURN path, which
// the AudioEvent reflection test does not reach. Between them they cover both
// ways audio could leave this engine: published, or handed back.
func assertNoByteSlices(t *testing.T, path string, v any) {
	t.Helper()
	assertNoByteSlicesType(t, path, reflect.TypeOf(v))
}

func assertNoByteSlicesType(t *testing.T, path string, ty reflect.Type) {
	t.Helper()

	const ownPackage = "github.com/callscreen/callscreen-platform/packages/go/audiointel"

	if ty.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		if field.PkgPath != "" {
			continue
		}
		full := path + "." + field.Name

		switch field.Type.Kind() {
		case reflect.Slice, reflect.Array:
			if field.Type.Elem().Kind() == reflect.Uint8 {
				t.Errorf("%s is a byte sequence; audio must not leave the engine", full)
			}
		case reflect.Struct:
			// Walked for this package's own types AND for media's, because
			// media.Frame is exactly the type that would carry a payload back
			// out if one were ever embedded in a result.
			pkg := field.Type.PkgPath()
			if pkg == ownPackage || strings.Contains(pkg, "packages/go/media") {
				assertNoByteSlicesType(t, full, field.Type)
			}
		}
	}
}
