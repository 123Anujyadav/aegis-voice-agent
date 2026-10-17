//go:build !unix

package main

import "testing"

// TestT13_RunServesHealthAndStopsCleanlyOnSIGTERM needs to signal its own
// process, which this platform cannot do (see run_unix_test.go). It is
// declared here so the gap is visible: coverage measured on this platform
// leaves run()'s success path uncovered and is lower than CI's, which runs on
// Linux and is the figure the hardening floor applies to.
func TestT13_RunServesHealthAndStopsCleanlyOnSIGTERM(t *testing.T) {
	t.Skip("unix-only: sends SIGTERM to the test process; run()'s serve-and-" +
		"shutdown path is covered on Linux CI, not on this platform")
}
