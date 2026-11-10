package whispercpp

import (
	"slices"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

// The child is given an allowlisted environment, as piper and whispercli
// already are: the operator's whole shell, credentials included, is not the
// recogniser's business.
func TestAdapter_ChildEnvironmentIsAllowlisted(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	got := p.processConfig().InheritEnv
	if len(got) == 0 {
		t.Fatal("no InheritEnv: the child would inherit the parent's entire environment")
	}
	if !slices.Equal(got, process.DefaultInheritEnv()) {
		t.Errorf("InheritEnv = %v, want process.DefaultInheritEnv()", got)
	}
}

// A relative path resolves against the working directory, where anything could
// be planted; the comments always asked for absolute paths, and now New does.
func TestAdapter_RelativePathsAreRefused(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"executable": func(c *Config) { c.Executable = "whisper-stream" },
		"model":      func(c *Config) { c.ModelPath = "models/ggml-base.bin" },
	} {
		cfg := testConfig(t)
		mutate(&cfg)
		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Errorf("%s: want an absolute-path error, got %v", name, err)
		}
	}
}
