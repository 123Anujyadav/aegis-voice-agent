package voice

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestVoiceEvent_CarriesNoContent is the structural form of §17's ban on
// transcript and audio labels, applied to the event stream.
//
// # This module is where content is most likely to leak
//
// The layers below deal in frames and features. This one holds a caller's
// transcript and a model's response in memory at the same moment, so it is the
// layer where a well-meaning field addition would put words on a topic that can
// never be erased.
//
// Review catches that on the day the field is added and never again. Reflection
// catches it on every run.
func TestVoiceEvent_CarriesNoContent(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"transcript", "text", "content", "words", "utterance", "response",
		"prompt", "message", "audio", "payload", "pcm", "sample", "frame",
		"phone", "number", "msisdn", "caller", "subscriber",
		"credential", "token", "secret", "key", "password",
	}

	// CharCount is a LENGTH, not the text. Named exceptions are listed so the
	// substring rule can stay blunt everywhere else.
	allowed := map[string]bool{"CharCount": true}

	const ownPackage = "github.com/callscreen/callscreen-platform/packages/go/voice"

	var walk func(ty reflect.Type, path string)
	walk = func(ty reflect.Type, path string) {
		for i := 0; i < ty.NumField(); i++ {
			field := ty.Field(i)
			if field.PkgPath != "" {
				continue
			}
			full := path + "." + field.Name

			if !allowed[field.Name] {
				lower := strings.ToLower(field.Name)
				for _, bad := range forbidden {
					if strings.Contains(lower, bad) {
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
				t.Errorf("%s is a map; bounded metadata must be a fixed struct so "+
					"this test can prove what it holds", full)
			case reflect.Interface:
				t.Errorf("%s is an interface and could hold anything at all", full)
			case reflect.Chan, reflect.Func, reflect.UnsafePointer:
				t.Errorf("%s is a %s; an event must be a value a consumer can copy",
					full, field.Type.Kind())
			case reflect.Struct:
				if field.Type.PkgPath() == ownPackage {
					walk(field.Type, full)
				}
			}
		}
	}

	walk(reflect.TypeOf(VoiceEvent{}), "VoiceEvent")
}

func TestVoiceEventTypes_AreWellFormed(t *testing.T) {
	t.Parallel()

	all := AllVoiceEventTypes()
	if len(all) == 0 {
		t.Fatal("no event types declared")
	}

	seen := map[string]VoiceEventType{}
	for _, tp := range all {
		if !tp.Valid() {
			t.Errorf("%q is declared but Valid() reports false", tp)
		}

		topic := tp.Topic()
		if prev, dup := seen[topic]; dup {
			t.Errorf("%s and %s produce the same topic %q", prev, tp, topic)
		}
		seen[topic] = tp

		if !strings.HasPrefix(topic, "voice.session.") {
			t.Errorf("%s topic %q lacks the domain prefix", tp, topic)
		}
		if !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s topic %q is unversioned", tp, topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("%s topic %q contains a hyphen; eventbus prohibits them in "+
				"segments and Prometheus normalises them away", tp, topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("%s topic %q is not lowercase", tp, topic)
		}
	}

	for _, bad := range []VoiceEventType{"", "made_up", "VOICE_TURN_STARTED"} {
		if bad.Valid() {
			t.Errorf("%q reported valid", bad)
		}
	}
}

func TestRecordingEventPublisher_IsBounded(t *testing.T) {
	t.Parallel()

	p := NewBoundedRecordingEventPublisher(3)
	for i := 0; i < 10; i++ {
		if err := p.Publish(context.Background(), VoiceEvent{
			Type: EventTurnStarted, Sequence: i,
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

	// The NEWEST are kept: a recorder that discarded recent events would show
	// an operator the start of an incident and none of it.
	got := p.Events()
	if got[len(got)-1].Sequence != 9 {
		t.Errorf("newest retained sequence = %d, want 9", got[len(got)-1].Sequence)
	}

	p.Reset()
	if p.Len() != 0 || p.Dropped() != 0 {
		t.Errorf("after Reset: len=%d dropped=%d", p.Len(), p.Dropped())
	}
}

func TestVoiceEvent_SummaryIsContentFree(t *testing.T) {
	t.Parallel()

	e := VoiceEvent{
		Type:      EventTranscriptFinal,
		Session:   "vs-abc",
		Turn:      "vt-def",
		Provider:  "whisper-local",
		CharCount: 42,
		Reason:    ReasonOK,
	}

	s := e.Summary()
	for _, want := range []string{"voice_transcript_final", "vs-abc", "vt-def",
		"whisper-local", "chars=42"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary() omits %q: %s", want, s)
		}
	}
	if e.String() != s {
		t.Error("String() and Summary() disagree")
	}
}

func TestNopEventPublisher_Discards(t *testing.T) {
	t.Parallel()

	if err := (NopEventPublisher{}).Publish(context.Background(), VoiceEvent{}); err != nil {
		t.Fatalf("NopEventPublisher returned %v", err)
	}
}
