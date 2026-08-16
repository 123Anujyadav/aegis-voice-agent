# ADR-0001: Monorepo with native per-language tooling

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering

---

## Context

The platform spans four runtimes that must evolve together: a Kotlin Android
client, Go services on the realtime telephony path, Python services in the AI
tier, and a TypeScript admin console arriving later. They are joined by one hard
coupling that dominates every other consideration:

> A call session's state machine spans the Android client, the telephony gateway
> and the AI orchestrator **simultaneously, in real time, inside a sub-second
> latency budget.**

A schema change to a turn-taking event therefore ripples across Kotlin, Go and
Python within a single logical change.

At the time of this decision the team is under 25 engineers, the repository is
private and unpublished, and no code exists beyond this foundation.

## Decision Drivers

1. **Atomic cross-language change.** A contract change must be one commit, one
   review, one CI run — not a three-repository dance with a deployment-ordering
   constraint and a window where production is inconsistent.
2. **Build correctness over build sophistication.** At this team size, engineer
   time spent on build infrastructure is time not spent on the product.
3. **Onboarding cost.** A new engineer must reach a green build on day one.
4. **CI cost proportional to the change**, not to the repository.
5. **Reversibility.** Whatever is chosen now must not foreclose a later move to
   a stricter build system.

## Considered Options

1. Monorepo with native per-language tooling and a thin task-runner facade
2. Monorepo with Bazel or Buck2
3. Monorepo with Nx or Turborepo
4. Polyrepo, one repository per service

## Decision Outcome

**Chosen: Option 1 — monorepo with native per-language tooling.**

Gradle owns Android, `go.work` owns Go, `uv` owns Python, `buf` owns contracts,
`pnpm` owns TypeScript, Terraform owns infrastructure. A root `Taskfile.yml`
provides one uniform verb surface (`task build`, `task test`, `task lint`) so no
engineer needs to learn six CLIs to make a change. CI detects affected targets
with path filters rather than a build graph.

This directly serves driver 1 — the coupling that motivated the whole decision —
while deferring the cost of a build system that the team is currently too small
to maintain.

### Consequences

**Positive**

- A contract change and all four of its consumers land in one reviewable commit.
- One dependency-upgrade surface, one lockfile policy, one CI configuration.
- Each toolchain is used idiomatically, so ecosystem documentation applies
  directly and Stack Overflow answers are usable as written.
- `git clone && task bootstrap` works, which is guideline #9 in the repository
  conventions.

**Negative**

- **Path-filter affected-detection is approximate.** It over-builds: a change to
  `packages/go/platform` rebuilds all eight Go services. It can also
  *under*-build if a dependency is implicit and unmatched by any filter.
  Mitigated by a nightly full build, which bounds the blast radius of an
  under-build to 24 hours. Over-building is accepted as cheaper than the
  engineering cost of a true build graph today.
- No remote execution and no cross-language incremental correctness. Full-repo
  CI will get slower as the repository grows.
- Coarse access control. The repository cannot grant per-directory permissions;
  CODEOWNERS provides review enforcement but not access restriction.
- `git clone` grows monotonically and cannot be pruned.

**Neutral**

- Six lockfiles rather than one. Each is idiomatic for its ecosystem and
  Renovate handles all of them.

### Confidence

**High** for the monorepo decision, **medium** for deferring Bazel. The monorepo
choice follows almost mechanically from the real-time cross-language coupling.
Deferring Bazel is a judgement about team size that could prove wrong sooner
than expected if headcount grows faster than planned.

### Revisit Trigger

Re-evaluate Option 2 when **either** holds:

- Full-repository CI wall-clock exceeds **25 minutes** after caching, or
- Engineering headcount crosses **60**.

Re-evaluate Pants specifically if the Python service count exceeds **15**.

## Options in Detail

### Option 2: Bazel or Buck2

Hermetic, correct, and genuinely scalable. Provides exact affected-target
detection and remote caching and execution — precisely the properties Option 1
approximates.

- **Good:** Correct incremental builds across language boundaries; remote
  execution; a single build graph for the whole repository.
- **Bad:** Android Gradle Plugin interop is a permanent, recurring tax. Python
  ML dependency graphs (torch and its CUDA wheels) fight hermeticity in ways
  that consume real engineering time. Ramp cost is one to two engineer-quarters
  before the first product feature benefits. At under 25 engineers that is a
  large fraction of total capacity spent on infrastructure.

### Option 3: Nx or Turborepo

- **Good:** Excellent affected-detection and caching for TypeScript; low ramp.
- **Bad:** Kotlin/Gradle, Go and Python are second-class or unsupported. This
  would mean adopting a JavaScript-ecosystem tool to orchestrate a repository
  that is under 10% JavaScript, and the parts it cannot manage are exactly the
  parts that matter most.

### Option 4: Polyrepo

- **Good:** Clean ownership boundaries; independent CI; fine-grained access
  control; each repository stays small.
- **Bad:** Fails driver 1 outright. A contract change becomes three coordinated
  pull requests with a deployment ordering constraint and an interval during
  which production runs mismatched schemas. For a system where the failure mode
  of a mismatched schema is a dropped call, this is the decisive objection.

## References

- Phase 1 Repository Foundation, §1 (monorepo rationale) and §12 (tooling)
- `go.work` — records why `replace` directives are currently load-bearing
- `Taskfile.yml` — records why `go build ./...` cannot run from the repository
  root in a Go workspace
- Superseded by: —
