package piper

import (
	"strings"
	"testing"
)

// A relative path resolves against the working directory, where anything could
// be planted; the comments always asked for absolute paths, and now New does.
func TestAdapter_RelativePathsAreRefused(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"executable": func(c *Config) { c.Executable = "piper" },
		"model":      func(c *Config) { c.ModelPath = "voices/en_GB.onnx" },
	} {
		cfg := testConfig(t)
		mutate(&cfg)
		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Errorf("%s: want an absolute-path error, got %v", name, err)
		}
	}
}
