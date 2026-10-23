package main

// THIS FILE IS IDENTICAL IN EVERY Go SERVICE (services/go/*/cmd/server);
// packages/go/platform's TestBuildInfo_EveryServiceUsesTheSharedContract fails
// if one drifts. The contract itself is packages/go/platform/buildinfo.go.

import (
	"io"
	"log/slog"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
)

// Build metadata, set by the linker: docker/go/Dockerfile builds with
// -X main.version=… -X main.commit=… -X main.buildTime=…. Unset (or
// malformed) values are reported as platform.BuildInfo's honest defaults —
// "dev", "unknown", "unknown" — never invented.
var (
	version   string
	commit    string
	buildTime string
)

// buildInfo is this binary's validated build metadata.
func buildInfo() platform.BuildInfo { return platform.ReadBuildInfo(version, commit, buildTime) }

// writeVersion is the `version` command: one line of JSON, no configuration
// read, nothing from the environment.
func writeVersion(w io.Writer) error { return buildInfo().WriteVersion(w, serviceName) }

// logBuild records what this process runs (platform.LogBuildInfo).
func logBuild(log *slog.Logger, b platform.BuildInfo) { platform.LogBuildInfo(log, b, envPrefix) }
