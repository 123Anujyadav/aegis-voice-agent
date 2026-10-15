package platform

import (
	"bytes"
	"log/slog"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	sha1Hex   = "06e276d8a78467360b9ed6d5554b445a88b94adc" // a test value, not a release
	sha256Hex = "8888c2ffdc1399e906a1b6a4f1d2f0b1c0e7a3d4b5c6d7e8f9a0b1c2d3e4f5a6"
)

func vcs(rev, modified string) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: modified},
	}}
}

func TestBuildInfo_DefaultsAreHonestWhenNothingIsSupplied(t *testing.T) {
	for _, in := range [][3]string{{"", "", ""}, {"dev", "unknown", ""}, {"  ", " unknown ", "unknown"}} {
		b := NewBuildInfo(in[0], in[1], in[2], nil)
		if b.Version != "dev" || b.Commit != "unknown" || b.BuildTime != "unknown" || len(b.Rejected) != 0 {
			t.Errorf("%q -> %+v", in, b)
		}
	}
}

func TestBuildInfo_SuppliedValuesAreReported(t *testing.T) {
	b := NewBuildInfo("1.4.2-rc.1+build.7", sha1Hex, "2026-10-12T07:30:00Z", vcs(sha256Hex, "false"))
	if b.Version != "1.4.2-rc.1+build.7" || b.Commit != sha1Hex || b.BuildTime != "2026-10-12T07:30:00Z" || b.Rejected != nil {
		t.Fatalf("%+v", b)
	}
	// A commit from the linker wins over the toolchain's stamp; SHA-256 object
	// ids and short ids are commits too; an offset time is normalised to UTC.
	for _, c := range []string{sha256Hex, sha1Hex[:7], sha1Hex + "-dirty"} {
		if got := NewBuildInfo("", c, "", vcs(sha256Hex, "true")).Commit; got != c {
			t.Errorf("commit %q reported as %q", c, got)
		}
	}
	if got := NewBuildInfo("", "", "2026-10-12T13:00:00+05:30", nil).BuildTime; got != "2026-10-12T07:30:00Z" {
		t.Errorf("buildTime = %q", got)
	}
}

func TestBuildInfo_ToolchainStampFillsAMissingCommitOnly(t *testing.T) {
	for _, c := range []struct {
		bi   *debug.BuildInfo
		want string
	}{
		{vcs(sha1Hex, "false"), sha1Hex},
		{vcs(sha1Hex, "true"), sha1Hex + "-dirty"},
		{vcs("", "true"), "unknown"},
		{vcs("not-a-revision", "false"), "unknown"},
		{&debug.BuildInfo{}, "unknown"},
		{nil, "unknown"},
	} {
		b := NewBuildInfo("", "", "", c.bi)
		if b.Commit != c.want || b.Version != "dev" || b.BuildTime != "unknown" {
			t.Errorf("%+v -> %+v, want commit %q", c.bi, b, c.want)
		}
	}
}

// Malformed values never crash, are never reported, and are named (without
// their content) in Rejected.
func TestBuildInfo_MalformedValuesAreReplacedAndNamed(t *testing.T) {
	cases := []struct {
		version, commit, buildTime string
		rejected                   string
	}{
		{"v 1", "", "", "version"},
		{"-1.0", "", "", "version"},
		{strings.Repeat("a", 129), "", "", "version"},
		{"1.0\nFAKE=1", "", "", "version"},
		{"", "06E276D", "", "commit"},
		{"", "abc123", "", "commit"},
		{"", sha1Hex + "x", "", "commit"},
		{"", sha1Hex + "-modified", "", "commit"},
		{"", "", "yesterday", "buildTime"},
		{"", "", "2026-10-12 07:30:00", "buildTime"},
		{"bad version", "bad commit", "bad time", "version,commit,buildTime"},
	}
	for _, c := range cases {
		b := NewBuildInfo(c.version, c.commit, c.buildTime, vcs(sha1Hex, "false"))
		if strings.Join(b.Rejected, ",") != c.rejected {
			t.Errorf("%q/%q/%q rejected %v, want %s", c.version, c.commit, c.buildTime, b.Rejected, c.rejected)
		}
		if strings.Contains(c.rejected, "version") && b.Version != "dev" ||
			strings.Contains(c.rejected, "commit") && b.Commit != "unknown" ||
			strings.Contains(c.rejected, "buildTime") && b.BuildTime != "unknown" {
			t.Errorf("a malformed value was reported: %+v", b)
		}
		var out bytes.Buffer
		if err := b.WriteVersion(&out, "svc"); err != nil || strings.Contains(out.String(), "bad") || strings.Contains(out.String(), "FAKE") {
			t.Errorf("output %q (%v)", out.String(), err)
		}
	}
}

func TestBuildInfo_WriteVersionIsExactAndDeterministic(t *testing.T) {
	b := NewBuildInfo("1.0.0", sha1Hex, "2026-10-12T07:30:00Z", nil)
	want := `{"service":"identity","version":"1.0.0","commit":"` + sha1Hex + `","buildTime":"2026-10-12T07:30:00Z"}` + "\n"
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		if err := b.WriteVersion(&out, "identity"); err != nil || out.String() != want {
			t.Fatalf("got %q, want %q", out.String(), want)
		}
	}
	var out bytes.Buffer
	_ = NewBuildInfo("", "", "nope", nil).WriteVersion(&out, "edge-api")
	if out.String() != `{"service":"edge-api","version":"dev","commit":"unknown","buildTime":"unknown","rejected":["buildTime"]}`+"\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestLogBuildInfo_RecordsTheBuildAndNamesWhatItIgnores(t *testing.T) {
	logs := func(b BuildInfo) string {
		var out bytes.Buffer
		LogBuildInfo(slog.New(slog.NewJSONHandler(&out, nil)), b, "CS_TESTSVC")
		return out.String()
	}
	b := NewBuildInfo("1.0.0", sha1Hex, "2026-10-12T07:30:00Z", nil)

	t.Setenv("CS_TESTSVC_VERSION", "1.0.0") // set, but no discrepancy
	got := logs(b)
	if !strings.Contains(got, `"msg":"build","commit":"`+sha1Hex+`","build_time":"2026-10-12T07:30:00Z"`) || strings.Count(got, "\n") != 1 {
		t.Errorf("%s", got)
	}

	t.Setenv("CS_TESTSVC_VERSION", "secret-looking-value")
	got = logs(NewBuildInfo("bad version", "", "", nil))
	if !strings.Contains(got, `"fields":["version"]`) || !strings.Contains(got, `"variable":"CS_TESTSVC_VERSION"`) ||
		strings.Contains(got, "secret-looking-value") || strings.Contains(got, "bad version") {
		t.Errorf("%s", got)
	}
}
