package audiointel

import (
	"testing"
	"time"
)

func mustSilenceClassifier(t *testing.T, cfg Config) *SilenceClassifier {
	t.Helper()
	s, err := NewSilenceClassifier(cfg)
	if err != nil {
		t.Fatalf("NewSilenceClassifier: %v", err)
	}
	return s
}

// TestSilence_ClassifiesByDurationAndPosition walks every class §10 requires.
func TestSilence_ClassifiesByDurationAndPosition(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())

	cases := []struct {
		name  string
		setup func(s *SilenceClassifier)
		d     time.Duration
		want  SilenceClass
	}{
		{
			name:  "before anybody has spoken",
			setup: func(*SilenceClassifier) {},
			d:     50 * time.Millisecond,
			want:  SilenceInitial,
		},
		{
			name:  "before anybody has spoken, however long",
			setup: func(*SilenceClassifier) {},
			d:     10 * time.Second,
			want:  SilenceInitial,
		},
		{
			name:  "a gap inside a phrase",
			setup: (*SilenceClassifier).NoteSpeech,
			d:     cfg.Silence.InterWordMax - time.Millisecond,
			want:  SilenceInterWord,
		},
		{
			name:  "exactly at the inter-word boundary",
			setup: (*SilenceClassifier).NoteSpeech,
			d:     cfg.Silence.InterWordMax,
			want:  SilenceInterWord,
		},
		{
			name:  "just past the inter-word boundary",
			setup: (*SilenceClassifier).NoteSpeech,
			d:     cfg.Silence.InterWordMax + time.Millisecond,
			want:  SilenceInterSentence,
		},
		{
			name:  "at the endpoint window",
			setup: (*SilenceClassifier).NoteSpeech,
			d:     cfg.Endpoint.SilenceWindow + time.Millisecond,
			want:  SilenceEndpoint,
		},
		{
			name:  "past anything conversational",
			setup: (*SilenceClassifier).NoteSpeech,
			d:     cfg.Silence.LongSilenceMin,
			want:  SilenceLong,
		},
		{
			name: "after the agent finished, with no reply yet",
			setup: func(s *SilenceClassifier) {
				s.NoteSpeech()
				s.NoteAgentFinished()
			},
			d:    cfg.Silence.ThinkingMin,
			want: SilenceThinking,
		},
		{
			name: "after the agent finished, but only briefly",
			setup: func(s *SilenceClassifier) {
				s.NoteSpeech()
				s.NoteAgentFinished()
			},
			d:    cfg.Silence.ThinkingMin - time.Millisecond,
			want: SilenceEndpoint,
		},
		{
			name: "after the agent finished, but the line has gone dead",
			setup: func(s *SilenceClassifier) {
				s.NoteSpeech()
				s.NoteAgentFinished()
			},
			d: cfg.Silence.LongSilenceMin,
			// A long silence outranks the positional classification: past
			// LongSilenceMin the interesting fact is that the line is dead, not
			// who spoke last.
			want: SilenceLong,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustSilenceClassifier(t, cfg)
			tc.setup(s)
			if got := s.Observe(tc.d).Class; got != tc.want {
				t.Errorf("a %s silence classified %s, want %s", tc.d, got, tc.want)
			}
		})
	}
}

// TestSilence_ReportsStartAndExtensionExactlyOnce is what stops a consumer
// emitting silence_started on every frame of a pause.
func TestSilence_ReportsStartAndExtensionExactlyOnce(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	s := mustSilenceClassifier(t, cfg)
	s.NoteSpeech()

	var starts, extensions int
	classes := []SilenceClass{}

	// Walk a silence from nothing out past the long threshold.
	for d := testInterval; d <= cfg.Silence.LongSilenceMin+testInterval; d += testInterval {
		r := s.Observe(d)
		if r.Started {
			starts++
		}
		if r.Extended {
			extensions++
			classes = append(classes, r.Class)
		}
	}

	if starts != 1 {
		t.Errorf("%d silence starts across one continuous pause, want 1", starts)
	}
	if extensions == 0 {
		t.Error("a pause that grew from inter-word to long reported no extensions")
	}

	// Each extension must reach a class it had not already reported.
	seen := map[SilenceClass]bool{}
	for _, c := range classes {
		if seen[c] {
			t.Errorf("class %s was reported as an extension twice", c)
		}
		seen[c] = true
	}
	t.Logf("the pause passed through %v", classes)
}

// TestSilence_ZeroDurationClosesTheStretch proves speech resuming resets the
// classifier so the next pause reports a fresh start.
func TestSilence_ZeroDurationClosesTheStretch(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	s := mustSilenceClassifier(t, cfg)
	s.NoteSpeech()

	if !s.Observe(50 * time.Millisecond).Started {
		t.Fatal("the first pause did not report a start")
	}
	if s.Observe(70 * time.Millisecond).Started {
		t.Fatal("the same pause reported a second start")
	}

	s.Observe(0)

	if !s.Observe(50 * time.Millisecond).Started {
		t.Error("a new pause after speech resumed did not report a start")
	}
}

// TestSilence_InitialEndsWhenSpeechIsHeard pins the positional latch.
func TestSilence_InitialEndsWhenSpeechIsHeard(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	s := mustSilenceClassifier(t, cfg)

	if s.SawSpeech() {
		t.Error("a fresh classifier claims speech was heard")
	}
	if got := s.Observe(time.Second).Class; got != SilenceInitial {
		t.Errorf("silence before any speech classified %s, want %s", got, SilenceInitial)
	}

	s.NoteSpeech()

	if !s.SawSpeech() {
		t.Error("NoteSpeech did not latch")
	}
	if got := s.Observe(time.Second).Class; got == SilenceInitial {
		t.Error("silence after speech is still classified as initial")
	}
}

// TestSilence_DocumentedLimitationHolds pins the honest consequence of a 250 ms
// endpoint window.
//
// At the default settings this engine CANNOT distinguish a clause boundary from
// a turn end: configuration validation refuses an InterSentenceMax above the
// endpoint window, so every pause long enough to be one is long enough to be
// the other. That is a property of the window, not a defect, and it is pinned
// here so a future change to either value has to confront it.
func TestSilence_DocumentedLimitationHolds(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())

	if cfg.Silence.InterSentenceMax > cfg.Endpoint.SilenceWindow {
		t.Fatalf("InterSentenceMax %s exceeds the endpoint window %s; validation "+
			"should have refused this configuration",
			cfg.Silence.InterSentenceMax, cfg.Endpoint.SilenceWindow)
	}

	s := mustSilenceClassifier(t, cfg)
	s.NoteSpeech()

	// The first duration past the inter-sentence boundary is already an
	// endpoint. There is no band in between at the defaults.
	justPast := cfg.Silence.InterSentenceMax + time.Millisecond
	if got := s.Observe(justPast).Class; got != SilenceEndpoint {
		t.Errorf("a %s pause classified %s; at the default settings the first "+
			"duration past InterSentenceMax is an endpoint", justPast, got)
	}
}

func TestSilence_Reset(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	s := mustSilenceClassifier(t, cfg)

	s.NoteSpeech()
	s.NoteAgentFinished()
	s.Observe(time.Second)

	s.Reset()

	if s.SawSpeech() {
		t.Error("Reset left the speech latch set")
	}
	if got := s.Observe(time.Second).Class; got != SilenceInitial {
		t.Errorf("after Reset a silence classified %s, want %s", got, SilenceInitial)
	}
}

func TestNewSilenceClassifier_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat())
	cfg.Silence.InterWordMax = 0
	if _, err := NewSilenceClassifier(cfg); err == nil {
		t.Error("an invalid silence configuration was accepted")
	}
}
