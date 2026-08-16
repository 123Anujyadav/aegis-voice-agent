// =============================================================================
// packages/go/platform — cross-cutting service infrastructure.
//
// DELIBERATE CONSTRAINT: THIS MODULE HAS NO EXTERNAL DEPENDENCIES.
//
// Every Go service in the platform imports this module. That makes its
// dependency graph the union of every service's mandatory supply-chain
// exposure, so a single compromised transitive dependency here would reach
// every binary we ship. Keeping it stdlib-only means:
//
//   1. Supply-chain surface is zero. There is nothing here for a dependency
//      confusion or typosquat attack to reach.
//   2. It always builds, offline, on any machine with a Go toolchain — which
//      matters directly for CI reliability on a constrained network.
//   3. No transitive version conflicts are forced on consumers. A service is
//      free to choose its own HTTP router, metrics client or database driver.
//
// Go 1.21+ made this practical: log/slog covers structured logging, and
// net/http covers servers with graceful shutdown. Before slog this module would
// have needed zap or zerolog; it no longer does.
//
// If a future requirement genuinely cannot be met by the standard library, the
// dependency belongs in a NEW sibling module that services opt into, not here.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/platform

go 1.23.0
