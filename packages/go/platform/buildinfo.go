package platform

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// Build metadata: which code a process is running.
//
// THE CONTRACT. A service's cmd/server/main.go declares three package-level
// strings that the linker sets (docker/go/Dockerfile passes
// -X main.version=… -X main.commit=… -X main.buildTime=…) and hands them to
// ReadBuildInfo. Values the build did not supply — or supplied malformed — are
// reported as the defaults below, never guessed:
//
//	version    a release label: [0-9A-Za-z][0-9A-Za-z._+-]{0,127}.
//	           Default "dev": not a release.
//	commit     the source commit: 7-64 lowercase hex, optionally "-dirty" when
//	           the tree had uncommitted changes. Default "unknown". When the
//	           linker supplies none, the Go toolchain's own VCS stamp is used
//	           (`go build` inside a git checkout records it; a Docker build does
//	           not, because .dockerignore excludes .git).
//	buildTime  RFC 3339, normalised to UTC. Default "unknown". Never the
//	           process's start time: an unsupplied build time stays unknown.
//
// The Dockerfile's own defaults ("dev", "unknown", empty) mean "not supplied"
// and are not reported as malformed.
//
// This is build metadata, not an image identity: it says what was built from,
// and a registry digest (infra/README.md, "Image references") says which
// bytes are deployed. Neither substitutes for the other.

// Defaults for metadata the build did not supply.
const (
	DevVersion     = "dev"
	UnknownCommit  = "unknown"
	UnknownBuildAt = "unknown"
)

var (
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{7,64}(-dirty)?$`)
	vcsRevRE  = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// BuildInfo is a binary's build metadata. Rejected names the fields whose
// supplied value was malformed and replaced by its default; it never carries
// the value itself.
type BuildInfo struct {
	Version   string   `json:"version"`
	Commit    string   `json:"commit"`
	BuildTime string   `json:"buildTime"`
	Rejected  []string `json:"rejected,omitempty"`
}

// ReadBuildInfo is NewBuildInfo with the running binary's Go build
// information.
func ReadBuildInfo(version, commit, buildTime string) BuildInfo {
	bi, _ := debug.ReadBuildInfo()
	return NewBuildInfo(version, commit, buildTime, bi)
}

// NewBuildInfo validates the linker-supplied values and applies the defaults.
// A nil bi is accepted; its VCS settings are consulted only for a missing commit.
func NewBuildInfo(version, commit, buildTime string, bi *debug.BuildInfo) BuildInfo {
	b := BuildInfo{Version: DevVersion, Commit: UnknownCommit, BuildTime: UnknownBuildAt}

	switch v := strings.TrimSpace(version); {
	case v == "" || v == DevVersion:
	case versionRE.MatchString(v):
		b.Version = v
	default:
		b.Rejected = append(b.Rejected, "version")
	}

	switch c := strings.TrimSpace(commit); {
	case c == "" || c == UnknownCommit:
		b.Commit = vcsCommit(bi)
	case commitRE.MatchString(c):
		b.Commit = c
	default:
		b.Rejected = append(b.Rejected, "commit")
	}

	switch t := strings.TrimSpace(buildTime); {
	case t == "" || t == UnknownBuildAt:
	default:
		if at, err := time.Parse(time.RFC3339, t); err == nil {
			b.BuildTime = at.UTC().Format(time.RFC3339)
		} else {
			b.Rejected = append(b.Rejected, "buildTime")
		}
	}
	return b
}

// vcsCommit is the Go toolchain's VCS stamp, or UnknownCommit.
func vcsCommit(bi *debug.BuildInfo) string {
	if bi == nil {
		return UnknownCommit
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if !vcsRevRE.MatchString(rev) {
		return UnknownCommit
	}
	if modified == "true" {
		return rev + "-dirty"
	}
	return rev
}

// WriteVersion writes one line of JSON naming the service and its build:
// {"service":…,"version":…,"commit":…,"buildTime":…} (plus "rejected" when
// any supplied value was malformed). The output depends only on its inputs.
func (b BuildInfo) WriteVersion(w io.Writer, service string) error {
	return json.NewEncoder(w).Encode(struct {
		Service string `json:"service"`
		BuildInfo
	}{service, b})
}

// LogBuildInfo records, once at startup, what the process runs. The version is
// already on every log line (AttrVersion); this adds the commit and build time.
// A malformed linker value is named, never echoed.
//
// A service with build metadata reports the binary's version, not
// <envPrefix>_VERSION (cmd/server: cfg.Version = build.Version). So that the
// change is never silent, a <envPrefix>_VERSION that is set and differs from
// the build is reported as ignored - by name only.
func LogBuildInfo(log *slog.Logger, b BuildInfo, envPrefix string) {
	log.Info("build", slog.String("commit", b.Commit), slog.String("build_time", b.BuildTime))
	if len(b.Rejected) > 0 {
		log.Warn("malformed build metadata replaced by defaults", slog.Any("fields", b.Rejected))
	}
	name := envPrefix + "_VERSION"
	if v, ok := os.LookupEnv(name); ok && v != b.Version {
		log.Warn("environment version ignored; the reported version is the binary's build metadata", slog.String("variable", name))
	}
}
