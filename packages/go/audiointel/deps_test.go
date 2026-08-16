package audiointel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDependencies_GoModDeclaresOnlyFirstPartyModules is §26 checked against
// the manifest.
//
// `go list -deps` is the authoritative check and the evaluation report runs it,
// but that is a command somebody has to remember. This fails the ordinary test
// run the moment a third-party require appears.
func TestDependencies_GoModDeclaresOnlyFirstPartyModules(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	const firstParty = "github.com/callscreen/callscreen-platform/packages/go/"

	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Only requirement lines carry a module path with a version.
		if !strings.Contains(trimmed, " v") {
			continue
		}
		if strings.HasPrefix(trimmed, "replace ") || strings.HasPrefix(trimmed, "go ") ||
			strings.HasPrefix(trimmed, "module ") {
			continue
		}
		if !strings.HasPrefix(trimmed, firstParty) {
			t.Errorf("go.mod:%d requires a module outside packages/go: %s\n"+
				"§26 requires this module's closure to be the standard library "+
				"plus first-party modules", i+1, trimmed)
		}
	}
}

// TestDependencies_NoForbiddenImports is §29 checked against the source.
//
// The dependency rule names specific modules that must never appear here, and
// each is forbidden for a different reason. speech and conversation would
// invert the layering. governance, memory and toolruntime are other planes
// entirely. telephony is two layers down. A provider SDK would make "provider
// agnostic" a claim rather than a property.
func TestDependencies_NoForbiddenImports(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		"packages/go/speech":       "would invert the layering; audiointel provides signals TO 11C through a port",
		"packages/go/conversation": "dialogue vocabulary does not belong in an audio pipeline",
		"packages/go/governance":   "a different plane",
		"packages/go/memory":       "a different plane",
		"packages/go/toolruntime":  "a different plane",
		"packages/go/telephony":    "two layers down; audiointel does not know what a call is",
		"packages/go/evaluation":   "test tooling must not reach production code",
		"packages/go/eventbus":     "the event port is an interface; a broker adapter is a service's job",

		// Provider and framework SDKs, by name, because §26 lists them.
		"pion":       "no WebRTC stack",
		"livekit":    "no voice-agent framework",
		"agora":      "no voice-agent framework",
		"deepgram":   "no provider SDK",
		"assemblyai": "no provider SDK",
		"elevenlabs": "no provider SDK",
		"silero":     "no third-party VAD",
		"webrtcvad":  "no third-party VAD",
		"langchain":  "no AI-agent framework",
	}

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range entries {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		inImports := false
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)

			switch {
			case trimmed == "import (":
				inImports = true
				continue
			case inImports && trimmed == ")":
				inImports = false
				continue
			case !inImports && !strings.HasPrefix(trimmed, "import "):
				continue
			}

			for needle, why := range forbidden {
				if strings.Contains(trimmed, needle) {
					t.Errorf("%s:%d imports %q — %s", path, i+1, needle, why)
				}
			}
		}
	}
}

// TestPorts_NoLearnedModelIsWiredIn is §14, enforced structurally.
//
// The brief forbids an opaque neural model in the hot path and asks that an
// adapter boundary be DEFINED without being implemented. [SpeechLikelihoodModel]
// is that boundary. This proves nothing implements it and nothing reaches it,
// so a later change that quietly puts a model on the frame path has to delete a
// test to do it.
func TestPorts_NoLearnedModelIsWiredIn(t *testing.T) {
	t.Parallel()

	modelType := reflect.TypeOf((*SpeechLikelihoodModel)(nil)).Elem()

	// No type this package hands to a caller may satisfy it or hold one.
	for name, sample := range map[string]any{
		"Config":         Config{},
		"Analysis":       Analysis{},
		"SignalView":     SignalView{},
		"VADDecision":    VADDecision{},
		"AudioEvent":     AudioEvent{},
		"SessionContext": SessionContext{},
	} {
		ty := reflect.TypeOf(sample)
		if ty.Implements(modelType) {
			t.Errorf("%s implements SpeechLikelihoodModel; the boundary is declared, "+
				"not implemented", name)
		}
		for i := 0; i < ty.NumField(); i++ {
			if ty.Field(i).Type == modelType {
				t.Errorf("%s.%s holds a SpeechLikelihoodModel; nothing may reach the "+
					"frame path", name, ty.Field(i).Name)
			}
		}
	}

	// And no detector file may so much as mention it.
	for _, path := range detectorFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("reading %s: %v", path, err)
		}
		if strings.Contains(string(src), "SpeechLikelihoodModel") {
			t.Errorf("%s references SpeechLikelihoodModel; §14 requires every "+
				"decision to be explainable through measured features and "+
				"configured thresholds", path)
		}
	}
}

// TestDependencies_NoVendorNamesInProductionCode guards the claim that this
// engine wraps nothing.
//
// Vendor names ARE allowed — in disclaiming comments, which is where every hit
// should be. A name appearing in code rather than in a comment is the thing
// this checks for.
func TestDependencies_NoVendorNamesInProductionCode(t *testing.T) {
	t.Parallel()

	vendors := []string{
		"webrtc", "silero", "pion", "livekit", "agora",
		"deepgram", "assemblyai", "elevenlabs",
	}

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	var commentHits int
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		for i, line := range strings.Split(string(src), "\n") {
			lower := strings.ToLower(line)
			trimmed := strings.TrimSpace(line)
			isComment := strings.HasPrefix(trimmed, "//")

			for _, vendor := range vendors {
				if !strings.Contains(lower, vendor) {
					continue
				}
				if isComment {
					commentHits++
					continue
				}
				t.Errorf("%s:%d mentions %q outside a comment; this engine wraps "+
					"nothing and every vendor mention should be a disclaimer",
					path, i+1, vendor)
			}
		}
	}

	if commentHits == 0 {
		t.Error("no disclaiming comment mentions any vendor at all; §26 asks this " +
			"module to be explicit about what it does not use")
	}
	t.Logf("%d vendor mentions, all in disclaiming comments", commentHits)
}
