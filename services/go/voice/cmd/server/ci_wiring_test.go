package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Phase 14 T12 — CI WIRING VERIFICATION.
//
// These tests assert how the repository's EXISTING CI discovers and gates this
// module. They change no policy and no workflow; they make the claims in the
// T12 report checkable, and they fail if the wiring silently drifts.
//
// Nothing here triggers CI. Local evidence and CI evidence stay separate.

const repoRoot = "../../../../../"

// ciModuleRE is the EXACT expression pr-go.yml and ci.yml use to enumerate the
// workspace. Copied deliberately rather than approximated: a test that uses a
// different pattern proves something other than what CI does.
//
//	grep -oE '\./[a-z0-9/_-]+' go.work | sed 's|^\./||'
var ciModuleRE = regexp.MustCompile(`\./[a-z0-9/_-]+`)

// discoverModules reproduces the CI discovery step against the real go.work.
func discoverModules(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(repoRoot + "go.work")
	if err != nil {
		t.Fatalf("reading go.work: %v", err)
	}
	var out []string
	for _, m := range ciModuleRE.FindAllString(string(b), -1) {
		out = append(out, strings.TrimPrefix(m, "./"))
	}
	return out
}

// TestT12_CIAutoDiscoversPhase14Modules proves the three Phase 14 modules are
// picked up by the workspace-derived matrix, so pr-go.yml and ci.yml gate them
// without any workflow edit.
func TestT12_CIAutoDiscoversPhase14Modules(t *testing.T) {
	t.Parallel()
	found := map[string]bool{}
	for _, m := range discoverModules(t) {
		found[m] = true
	}
	for _, m := range []string{
		"packages/go/intent",
		"packages/go/voiceintel",
		"services/go/voice",
	} {
		if !found[m] {
			t.Errorf("%s is NOT discovered by the CI module enumeration — it would "+
				"be silently excluded from build, vet, race, coverage, lint and "+
				"govulncheck in pr-go.yml", m)
		}
	}
	if len(found) < 40 {
		t.Errorf("only %d modules discovered; the enumeration looks broken rather "+
			"than the module list having shrunk", len(found))
	}
}

// TestT12_PRGoUsesWorkspaceDiscoveryWithNoAllowlist pins the property that makes
// the previous test meaningful: pr-go derives its matrix from go.work and
// carries no allowlist or blocklist that could exclude a discovered module.
func TestT12_PRGoUsesWorkspaceDiscoveryWithNoAllowlist(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(repoRoot + ".github/workflows/pr-go.yml")
	if err != nil {
		t.Fatalf("reading pr-go.yml: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, `grep -oE '\./[a-z0-9/_-]+' go.work`) {
		t.Error("pr-go.yml no longer derives its module matrix from go.work; the " +
			"auto-discovery claim in the T12 report would no longer hold")
	}
	// Every gating job must fan out over the discovered matrix.
	for _, job := range []string{"build-test", "lint", "vulnerabilities"} {
		if !strings.Contains(src, job+":") {
			t.Errorf("pr-go.yml has no %s job", job)
		}
	}
	if n := strings.Count(src, "fromJSON(needs.discover.outputs.modules)"); n < 3 {
		t.Errorf("only %d jobs consume the discovered module list, want at least 3 "+
			"(build-test, lint, vulnerabilities)", n)
	}
	// A module-name exclusion would defeat discovery. There is none today.
	for _, bad := range []string{"exclude:", "skip-modules", "SKIP_MODULES"} {
		if strings.Contains(src, bad) {
			t.Errorf("pr-go.yml contains %q — a module exclusion mechanism now "+
				"exists and the no-allowlist claim must be re-checked", bad)
		}
	}
	// The race flag is what makes pr-go the authoritative race evidence.
	if !strings.Contains(src, "go test -race -count=1 -coverprofile=coverage.out") {
		t.Error("pr-go.yml no longer runs the per-module race+coverage command")
	}
}

// aiModules parses the hard-coded AI_MODULES allowlist from hardening.yml.
func aiModules(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(repoRoot + ".github/workflows/hardening.yml")
	if err != nil {
		t.Fatalf("reading hardening.yml: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "AI_MODULES: >-")
	if i < 0 {
		t.Fatal("hardening.yml has no AI_MODULES block")
	}
	var out []string
	for _, line := range strings.Split(src[i:], "\n")[1:] {
		f := strings.TrimSpace(line)
		if f == "" || strings.HasPrefix(f, "#") {
			break
		}
		if !strings.HasPrefix(f, "packages/go/") && !strings.HasPrefix(f, "services/go/") {
			break
		}
		out = append(out, f)
	}
	return out
}

// TestT12_HardeningCoversPhase13IntelligenceModules asserts the two modules
// Phase 13 T14 registered are still in the hardening allowlist.
func TestT12_HardeningCoversPhase13IntelligenceModules(t *testing.T) {
	t.Parallel()
	in := map[string]bool{}
	for _, m := range aiModules(t) {
		in[m] = true
	}
	for _, m := range []string{"packages/go/intent", "packages/go/voiceintel"} {
		if !in[m] {
			t.Errorf("%s dropped out of hardening AI_MODULES; it would lose repeated "+
				"shuffled race, the coverage floor and benchmark execution", m)
		}
	}
}

// TestT12_ServiceModuleIsAbsentFromHardeningAllowlist PINS A KNOWN GAP.
//
// services/go/voice — the module holding every Phase 14 T2–T11 test — is NOT in
// hardening.yml's hard-coded AI_MODULES. It is fully gated by pr-go.yml
// (build, vet, single-pass -race, coverage artifact, golangci-lint,
// govulncheck) but does NOT receive hardening's three extra gates:
//
//	repeated shuffled race   -race -count=5 -shuffle=on
//	coverage floor           60% enforced
//	benchmark execution      the T10 benchmarks never run in CI
//
// Adding it is a CI POLICY CHANGE to a hard-coded allowlist, which T12 requires
// be reported for approval rather than made silently. This test asserts the gap
// as it stands so that:
//
//   - the T12 report cannot go stale, and
//   - if somebody adds the entry, this test fails and forces the report to be
//     updated rather than the change passing unnoticed.
//
// It is NOT asserting that the gap is desirable.
func TestT12_ServiceModuleIsAbsentFromHardeningAllowlist(t *testing.T) {
	t.Parallel()
	for _, m := range aiModules(t) {
		if m == "services/go/voice" {
			t.Fatal("services/go/voice is now in hardening AI_MODULES. That is the " +
				"change T12 reported for approval. Update the T12 report and this " +
				"test together — do not simply delete the assertion.")
		}
	}
	t.Log("CONFIRMED GAP: services/go/voice is gated by pr-go.yml but is absent " +
		"from hardening AI_MODULES (no repeated shuffled race, no coverage floor, " +
		"no benchmark execution). Reported for approval; not changed.")
}

// TestT12_HardeningAllowlistIsIntact guards the parse itself, so the two tests
// above cannot pass vacuously on an empty or truncated list.
func TestT12_HardeningAllowlistIsIntact(t *testing.T) {
	t.Parallel()
	got := aiModules(t)
	if len(got) != 16 {
		t.Errorf("AI_MODULES has %d entries (%v), want the 16 recorded at the end "+
			"of Phase 13 T14 — the allowlist changed and T12's analysis needs "+
			"re-checking", len(got), got)
	}
	for _, m := range got {
		if !strings.HasPrefix(m, "packages/go/") && !strings.HasPrefix(m, "services/go/") {
			t.Errorf("AI_MODULES entry %q is not a module path; the parse is wrong", m)
		}
	}
}
