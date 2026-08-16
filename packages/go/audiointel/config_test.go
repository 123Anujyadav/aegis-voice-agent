package audiointel

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

func TestConfig_DefaultIsValid(t *testing.T) {
	t.Parallel()

	for _, format := range []media.AudioFormat{
		media.PCM16Mono8k(),
		media.PCM16Mono16k(),
		{Format: media.FormatPCM32, Layout: media.LayoutMono,
			Rate: media.Rate24kHz, Codec: media.CodecPCM},
	} {
		if err := DefaultConfig(format).Validate(); err != nil {
			t.Fatalf("DefaultConfig(%s) is invalid: %v", format, err)
		}
	}
}

// TestConfig_RefusesUnanalysableFormats is the fail-closed guarantee for §15.
//
// Both refusals matter for a different reason and both are asserted, because a
// silent default for either would produce a detector that reports permanent
// silence (opaque) or analyses half the audio (stereo) while looking healthy.
func TestConfig_RefusesUnanalysableFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		format media.AudioFormat
		want   string
	}{
		{
			name: "stereo",
			format: media.AudioFormat{Format: media.FormatPCM16,
				Layout: media.LayoutStereo, Rate: media.Rate8kHz, Codec: media.CodecPCM},
			want: "mixing policy",
		},
		{
			name: "opaque codec",
			format: media.AudioFormat{Format: media.FormatPCM16,
				Layout: media.LayoutMono, Rate: media.Rate8kHz, Codec: media.CodecOpaque},
			want: "cannot be inspected",
		},
		{
			name: "unset format",
			format: media.AudioFormat{Layout: media.LayoutMono,
				Rate: media.Rate8kHz, Codec: media.CodecPCM},
			want: "sample format",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := DefaultConfig(tc.format).Validate()
			if err == nil {
				t.Fatalf("format %s was accepted; it must fail closed", tc.format)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error does not explain the refusal\n got: %v\nwant substring: %q",
					err, tc.want)
			}
		})
	}
}

func TestConfig_AcceptsBothPCMWidths(t *testing.T) {
	t.Parallel()

	for _, sf := range []media.SampleFormat{media.FormatPCM16, media.FormatPCM32} {
		f := media.AudioFormat{Format: sf, Layout: media.LayoutMono,
			Rate: media.Rate8kHz, Codec: media.CodecPCM}
		if err := validateAnalysisFormat(f); err != nil {
			t.Fatalf("%s was refused: %v", sf, err)
		}
	}
}

// TestConfig_EveryFieldHasARejectingCase walks every tunable and proves an
// invalid value is refused.
//
// The point is not that each individual check works — it is that no field can
// be added without a rejecting case, because a field with no validation is a
// field that fails open, and a detector built from nonsense produces nonsense
// confidently.
func TestConfig_EveryFieldHasARejectingCase(t *testing.T) {
	t.Parallel()

	base := DefaultConfig(media.PCM16Mono8k())

	cases := []struct {
		field  string
		mutate func(*Config)
		want   string
	}{
		// Top level.
		{"FrameInterval", func(c *Config) { c.FrameInterval = 0 }, "FrameInterval"},
		{"MaxSessions", func(c *Config) { c.MaxSessions = 0 }, "MaxSessions"},
		{"SignalWindowFrames", func(c *Config) { c.SignalWindowFrames = 0 }, "SignalWindowFrames"},

		// Noise.
		{"Noise.WarmupFrames", func(c *Config) { c.Noise.WarmupFrames = 0 }, "WarmupFrames"},
		{"Noise.WindowFrames", func(c *Config) { c.Noise.WindowFrames = 0 }, "WindowFrames"},
		{"Noise.WindowFrames<Warmup", func(c *Config) {
			c.Noise.WindowFrames = 2
			c.Noise.WarmupFrames = 10
		}, "at least WarmupFrames"},
		{"Noise.RiseAlpha", func(c *Config) { c.Noise.RiseAlpha = 0 }, "RiseAlpha"},
		{"Noise.RiseAlpha>=1", func(c *Config) { c.Noise.RiseAlpha = 1 }, "RiseAlpha"},
		{"Noise.FallAlpha", func(c *Config) { c.Noise.FallAlpha = 0 }, "FallAlpha"},
		{"Noise.Rise>=Fall", func(c *Config) {
			c.Noise.RiseAlpha = 0.5
			c.Noise.FallAlpha = 0.1
		}, "smaller than FallAlpha"},
		{"Noise.MaxRiseDBPerSecond", func(c *Config) { c.Noise.MaxRiseDBPerSecond = 0 },
			"MaxRiseDBPerSecond"},
		{"Noise.MinFloor", func(c *Config) { c.Noise.MinFloor = 0 }, "MinFloor"},
		{"Noise.MaxFloor", func(c *Config) { c.Noise.MaxFloor = 1e-9 }, "MaxFloor"},
		{"Noise.ConfidenceFrames", func(c *Config) { c.Noise.ConfidenceFrames = 0 },
			"ConfidenceFrames"},
		{"Noise.QuietFloor", func(c *Config) { c.Noise.QuietFloor = 0 }, "QuietFloor"},
		{"Noise.QuietFloorBelowMinFloor", func(c *Config) {
			c.Noise.MinFloor = 1e-3
			c.Noise.QuietFloor = 1e-6
		}, "could ever be classified as quiet"},
		{"Noise.TransientModulation", func(c *Config) { c.Noise.TransientModulation = 0 },
			"TransientModulation"},

		// VAD.
		{"VAD.OnsetThresholdDB", func(c *Config) { c.VAD.OnsetThresholdDB = 0 },
			"OnsetThresholdDB"},
		{"VAD.ReleaseThresholdDB", func(c *Config) { c.VAD.ReleaseThresholdDB = 0 },
			"ReleaseThresholdDB"},
		{"VAD.NoHysteresis", func(c *Config) {
			c.VAD.ReleaseThresholdDB = c.VAD.OnsetThresholdDB
		}, "hysteresis"},
		{"VAD.MinOnsetFrames", func(c *Config) { c.VAD.MinOnsetFrames = 0 }, "MinOnsetFrames"},
		{"VAD.MinSilence", func(c *Config) { c.VAD.MinSilence = 0 }, "MinSilence"},
		{"VAD.MinSilenceSubFrame", func(c *Config) { c.VAD.MinSilence = time.Nanosecond },
			"shorter than one frame"},
		{"VAD.MinSpeech", func(c *Config) { c.VAD.MinSpeech = -1 }, "MinSpeech"},
		{"VAD.ZCRMin", func(c *Config) { c.VAD.ZCRMin = -0.1 }, "ZCRMin"},
		{"VAD.ZCRMax", func(c *Config) { c.VAD.ZCRMax = 1.5 }, "ZCRMax"},
		{"VAD.ZCRBandInverted", func(c *Config) {
			c.VAD.ZCRMin = 0.8
			c.VAD.ZCRMax = 0.2
		}, "below ZCRMax"},
		{"VAD.MinEnergyModulation", func(c *Config) { c.VAD.MinEnergyModulation = -1 },
			"MinEnergyModulation"},
		{"VAD.AbsoluteSilenceRMS", func(c *Config) { c.VAD.AbsoluteSilenceRMS = 0 },
			"AbsoluteSilenceRMS"},
		{"VAD.NoiseHoldFrames", func(c *Config) { c.VAD.NoiseHoldFrames = 0 },
			"NoiseHoldFrames"},

		// Silence.
		{"Silence.InterWordMax", func(c *Config) { c.Silence.InterWordMax = 0 },
			"InterWordMax"},
		{"Silence.InterSentenceMax", func(c *Config) {
			c.Silence.InterSentenceMax = c.Silence.InterWordMax
		}, "must exceed"},
		{"Silence.LongSilenceMin", func(c *Config) {
			c.Silence.LongSilenceMin = c.Silence.InterSentenceMax
		}, "LongSilenceMin"},
		{"Silence.ThinkingMin", func(c *Config) { c.Silence.ThinkingMin = 0 }, "ThinkingMin"},

		// Endpoint.
		{"Endpoint.SilenceWindow", func(c *Config) { c.Endpoint.SilenceWindow = 0 },
			"SilenceWindow"},
		{"Endpoint.MinSpeechDuration", func(c *Config) { c.Endpoint.MinSpeechDuration = -1 },
			"MinSpeechDuration"},
		{"Endpoint.MaxTurnDuration", func(c *Config) { c.Endpoint.MaxTurnDuration = 0 },
			"MaxTurnDuration"},
		{"Endpoint.MaxTurn<=MinSpeech", func(c *Config) {
			c.Endpoint.MinSpeechDuration = 2 * time.Second
			c.Endpoint.MaxTurnDuration = time.Second
		}, "must exceed MinSpeechDuration"},
		{"Endpoint.EnergyTrendTolerance", func(c *Config) {
			c.Endpoint.EnergyTrendTolerance = -1
		}, "EnergyTrendTolerance"},

		// Barge-in.
		{"BargeIn.MinInterval", func(c *Config) { c.BargeIn.MinInterval = 0 }, "MinInterval"},
		{"BargeIn.MaxAge", func(c *Config) { c.BargeIn.MaxAge = 0 }, "MaxAge"},
		{"BargeIn.ConfirmFrames", func(c *Config) { c.BargeIn.ConfirmFrames = -1 },
			"ConfirmFrames"},

		// Overlap.
		{"Overlap.MinDuration", func(c *Config) { c.Overlap.MinDuration = 0 }, "MinDuration"},
		{"Overlap.ResolveAfter", func(c *Config) { c.Overlap.ResolveAfter = 0 },
			"ResolveAfter"},
		{"Overlap.EchoCorrelationPenalty", func(c *Config) {
			c.Overlap.EchoCorrelationPenalty = 2
		}, "EchoCorrelationPenalty"},
		{"Overlap.MinConfidence", func(c *Config) { c.Overlap.MinConfidence = -1 },
			"MinConfidence"},

		// Quality.
		{"Quality.MinSignalRMS", func(c *Config) { c.Quality.MinSignalRMS = 0 },
			"MinSignalRMS"},
		{"Quality.DegradedClipRatio", func(c *Config) { c.Quality.DegradedClipRatio = 0 },
			"DegradedClipRatio"},
		{"Quality.MaxClipRatio", func(c *Config) {
			c.Quality.MaxClipRatio = c.Quality.DegradedClipRatio
		}, "MaxClipRatio"},
		{"Quality.SNRBandInverted", func(c *Config) {
			c.Quality.DegradedSNRDB = 1
			c.Quality.MinSNRDB = 20
		}, "DegradedSNRDB"},
		{"Quality.DegradedGapRatio", func(c *Config) { c.Quality.DegradedGapRatio = 0 },
			"DegradedGapRatio"},
		{"Quality.MaxGapRatio", func(c *Config) { c.Quality.MaxGapRatio = 1.5 },
			"MaxGapRatio"},
		{"Quality.MinDynamicRange", func(c *Config) { c.Quality.MinDynamicRange = -1 },
			"MinDynamicRange"},
		{"Quality.WindowFrames", func(c *Config) { c.Quality.WindowFrames = 0 },
			"WindowFrames"},
		{"Quality.HysteresisFrames", func(c *Config) { c.Quality.HysteresisFrames = 0 },
			"HysteresisFrames"},

		// Continuity.
		{"Continuity.MaxTimestampJump", func(c *Config) {
			c.Continuity.MaxTimestampJump = 0
		}, "MaxTimestampJump"},
		{"Continuity.WindowFrames", func(c *Config) { c.Continuity.WindowFrames = 0 },
			"WindowFrames"},
		{"Continuity.RestoreFrames", func(c *Config) { c.Continuity.RestoreFrames = 0 },
			"RestoreFrames"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%s accepted an invalid value; it must fail closed", tc.field)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error does not name the problem\n got: %v\nwant substring: %q",
					tc.field, err, tc.want)
			}

			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("%s: error is %T, want *ConfigError", tc.field, err)
			}
		})
	}
}

// TestConfig_CrossSectionInvariants covers the mistakes per-section validation
// cannot catch: two individually valid settings that contradict each other.
func TestConfig_CrossSectionInvariants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "inter-sentence pause longer than the endpoint window",
			mutate: func(c *Config) {
				c.Silence.InterSentenceMax = 400 * time.Millisecond
				c.Endpoint.SilenceWindow = 250 * time.Millisecond
			},
			want: "two names for one event",
		},
		{
			name: "endpoint window at or beyond long silence",
			mutate: func(c *Config) {
				c.Endpoint.SilenceWindow = 4 * time.Second
				c.Silence.LongSilenceMin = 3 * time.Second
			},
			want: "every endpoint is also a long silence",
		},
		{
			name: "hangover outlasts the endpoint window",
			mutate: func(c *Config) {
				c.VAD.MinSilence = 400 * time.Millisecond
				c.Endpoint.SilenceWindow = 250 * time.Millisecond
			},
			want: "before the VAD has decided speech ended",
		},
		{
			name: "noise floor sits above the absolute speech floor",
			mutate: func(c *Config) {
				c.Noise.MinFloor = 1e-2
				c.VAD.AbsoluteSilenceRMS = 1e-4
			},
			want: "nothing would ever be detected",
		},
		{
			name: "quality window wider than the signal window",
			mutate: func(c *Config) {
				c.SignalWindowFrames = 10
				c.Quality.WindowFrames = 50
			},
			want: "cannot see more history",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig(media.PCM16Mono8k())
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("contradictory configuration was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error does not explain the contradiction\n got: %v\nwant: %q",
					err, tc.want)
			}
		})
	}
}

// TestConfig_ReportsEveryProblemNotTheFirst is the property that makes a
// misconfiguration one fix-and-restart cycle instead of four.
func TestConfig_ReportsEveryProblemNotTheFirst(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	cfg.FrameInterval = 0
	cfg.MaxSessions = 0
	cfg.Noise.WarmupFrames = 0
	cfg.VAD.MinOnsetFrames = 0
	cfg.Quality.WindowFrames = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("five invalid fields were accepted")
	}

	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
	if len(ce.Problems) < 5 {
		t.Fatalf("reported %d problems, want at least 5:\n%v", len(ce.Problems), ce.Problems)
	}
}

// TestConfig_FramesRoundsUp guards a silent behaviour change on any cadence
// that does not divide a threshold evenly.
//
// Every threshold in this engine is a MINIMUM. Rounding down makes the
// effective hangover shorter than the configured one — which is not a rounding
// error, it is the detector doing something other than what the configuration
// says.
func TestConfig_FramesRoundsUp(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	cfg.FrameInterval = 30 * time.Millisecond

	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 0},
		{-1, 0},
		{time.Nanosecond, 1},
		{30 * time.Millisecond, 1},
		{31 * time.Millisecond, 2},
		{60 * time.Millisecond, 2},
		{200 * time.Millisecond, 7}, // 6.67 rounds UP, never down
		{250 * time.Millisecond, 9}, // 8.33 rounds UP
	}

	for _, tc := range cases {
		if got := cfg.frames(tc.d); got != tc.want {
			t.Errorf("frames(%s) at %s cadence = %d, want %d",
				tc.d, cfg.FrameInterval, got, tc.want)
		}
	}
}

// TestConfig_FrozenBudgetsMatchTheADRs pins the two numbers this phase did not
// choose.
//
// If ADR-0011 or ADR-0004 is ever revised, this test fails and forces the
// change to be deliberate rather than absorbed silently into a default.
func TestConfig_FrozenBudgetsMatchTheADRs(t *testing.T) {
	t.Parallel()

	// ADR-0011 §5.2 hop 1: endpoint detection, 250 ms p50.
	if DefaultEndpointSilenceWindow != 250*time.Millisecond {
		t.Errorf("DefaultEndpointSilenceWindow = %s, ADR-0011 §5.2 hop 1 says 250ms",
			DefaultEndpointSilenceWindow)
	}
	// ADR-0004 §12 and ADR-0011 §5.1: barge-in, one frame interval.
	if BargeInBudget != 20*time.Millisecond {
		t.Errorf("BargeInBudget = %s, ADR-0004 §12 says 20ms", BargeInBudget)
	}
	// The default configuration must actually use the frozen window rather than
	// a number that merely looks like it.
	if got := DefaultConfig(media.PCM16Mono8k()).Endpoint.SilenceWindow; got != DefaultEndpointSilenceWindow {
		t.Errorf("default endpoint window = %s, want the frozen %s",
			got, DefaultEndpointSilenceWindow)
	}
}

// detectorFiles are the implementation files that must contain no tunable
// numbers of their own.
//
// config.go and defaults.go are where numbers live. harness.go and fixtures.go
// build test signals, which are data rather than policy. Everything that makes
// a DECISION is listed here.
var detectorFiles = []string{
	"frame.go", "window.go", "noise.go", "signal.go", "vad.go",
	"speechdetect.go", "silence.go", "endpoint.go", "bargein.go",
	"overlap.go", "quality.go", "continuity.go", "analyzer.go",
	"session.go", "registry.go", "runtime.go",
}

// durationLiteral matches a duration constant such as `200 * time.Millisecond`.
//
// Deliberately narrow: it matches a NUMBER multiplied by a time unit, which is
// what a hardcoded threshold looks like. It does not match `float64(d) /
// float64(time.Second)`, which is a unit conversion and carries no policy.
var durationLiteral = regexp.MustCompile(`[0-9]\s*\*\s*time\.(Nanosecond|Microsecond|Millisecond|Second|Minute|Hour)`)

// TestConfig_NoDurationLiteralsInDetectors enforces §15: no scattered numeric
// constants.
//
// A threshold typed into a detector is a threshold nobody can find, nobody can
// tune, and nobody can tell was ever reasoned about. This test makes that
// mistake fail the build rather than survive review.
func TestConfig_NoDurationLiteralsInDetectors(t *testing.T) {
	t.Parallel()

	for _, name := range detectorFiles {
		path := filepath.Join(".", name)
		src, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// The file is added later in the phase. Absent is not a failure;
				// present-and-polluted is.
				continue
			}
			t.Fatalf("reading %s: %v", name, err)
		}

		for i, line := range strings.Split(string(src), "\n") {
			// A comment may legitimately discuss "200 * time.Millisecond" while
			// explaining why the value lives in defaults.go.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
				continue
			}
			if durationLiteral.MatchString(line) {
				t.Errorf("%s:%d contains a duration literal; every threshold belongs "+
					"in config.go or defaults.go\n\t%s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
