# Race Verification Report — Phase 10.5

**Addresses:** production blocker B1, the platform's top item for four
consecutive phases.
**Outcome:** the blocker is **narrowed and re-characterised, not closed.** Read
§2 before §4.

---

## 1. The finding as previously stated

> **B1 — `-race` has never been run.** The race detector requires cgo and this
> machine has no C toolchain. Seven modules, 40,976 lines, 454 tests, extensive
> concurrent code — none of it checked by the race detector.
>
> *Recommendation: run `go test -race ./...` on a Linux runner. Create the CI
> configuration required.*

The recommendation was wrong in a way worth correcting: **the CI configuration
already existed.**

---

## 2. What the audit found

`.github/workflows/pr-go.yml` has run race detection on every workspace module
since it was written:

```yaml
# -race is non-negotiable for this platform. The session orchestrator and
# media relay are concurrent by nature, and a data race that only appears
# under production load is the worst class of bug this repository can ship.
- name: Test with race detection
  run: go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
```

The module matrix is derived from `go.work` at run time, so every AI-plane
module — including `packages/go/metrics`, added this phase — is covered without
anybody editing the workflow.

So why has it never run?

```console
$ git rev-parse --is-inside-work-tree
fatal: not a git repository (or any of the parent directories): .git
```

**The repository is not under version control.** There is no `.git` directory,
no remote, no commits. Every workflow in `.github/workflows/` is a file that has
never executed — not because it is misconfigured, but because nothing has ever
triggered it.

That reframes the blocker entirely. It was never "we need to write CI". It is
**"the repository has never been committed, so no gate of any kind has ever
run"** — which is a larger problem than the race detector and had been hiding
behind a narrower description of itself.

---

## 3. Defects found in the never-executed CI

A workflow that has never run accumulates errors silently. Three were found and
fixed this phase:

### C1 — Go version too old to build the platform *(fixed)*

```yaml
GO_VERSION: "1.23.5"      # pr-go.yml
go-version: '1.23.0'      # ci.yml
```

Every AI-plane module declares `go 1.25.0`. Under `GOTOOLCHAIN=auto` Go 1.23
would silently download a 1.25 toolchain that `setup-go` has not cached — slow
on every run and an undeclared network dependency; under the hardened
`GOTOOLCHAIN=local` it fails outright.

Fixed: `GO_VERSION: "1.25.x"` and `GOTOOLCHAIN: "local"` in both, so the version
the file declares is the version the build uses.

### C2 — `ci.yml` could not build anything *(fixed)*

```yaml
- name: Build All Go Modules
  run: go build ./...
```

From the repository root, in a workspace, this fails:

```console
$ go build ./...
pattern ./...: directory prefix . does not contain modules listed in go.work
```

`pr-go.yml`'s own header documents this exact hazard. `ci.yml` had it anyway.
Fixed by iterating the module list, as `pr-go.yml` does.

### C3 — No gate on the default branch *(fixed)*

`pr-go.yml` triggered only on `pull_request` and `merge_group`. A direct push to
`main` ran nothing. Added a `push` trigger on `main`.

### C4 — Cache key points at a file that does not exist *(open, low)*

```yaml
cache-dependency-path: ${{ matrix.module }}/go.sum
```

No AI-plane module has a `go.sum`, because none has a third-party dependency —
which is the platform's strongest supply-chain property, expressing itself as a
CI wart. `setup-go` warns and continues, so this degrades caching rather than
breaking the build. Left as-is and recorded; fixing it means special-casing
dependency-free modules, which is more machinery than the warning costs.

---

## 4. What was executed, and what was not

**Locally: nothing.** The constraint is unchanged and was re-confirmed:

```console
$ go env CGO_ENABLED
0
$ which gcc clang cc
# none found
$ go test -race -run TestNothing .
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

Docker Desktop is installed (v29.6.2) and would have provided a Linux container
with a toolchain, which would have closed this properly. Its daemon was not
running for the duration of this phase and was checked three times:

```console
$ docker info
failed to connect to the docker API at npipe:////./pipe/dockerDesktopLinuxEngine
```

**No race detector output exists for this platform.** No run, no log, no
artifact. Everything below is preparation for a run that has not happened.

---

## 5. What was prepared

`.github/workflows/hardening.yml`, a dedicated gate:

- **Two race modes.** `single` (`-race -count=1`) and `repeated`
  (`-race -count=5 -shuffle=on`). They find different things: one pass checks
  the interleavings it happens to take; repeated shuffled runs re-order test
  execution and re-time goroutines, which is how interleaving-dependent races
  surface.
- **A cgo precondition check** that fails with a clear message rather than
  letting the run reach the confusing `-race requires cgo` error.
- **Logs captured to artifacts**, 30-day retention. A `WARNING: DATA RACE` block
  is the thing an engineer needs to read, and scrolling a CI console is not
  reading it. The step greps the logs and fails the job on any occurrence,
  because `go test` can exit zero on a race in some configurations.
- **Nightly schedule.** The detector finds interleavings by observing them, so
  more executions mean more coverage. A gate that only runs on changed code
  never re-examines code that has stopped changing — which, for six frozen
  phases, is all of it.

Also prepared: coverage with a 60% floor (actual range 71.2–84.9%, so the floor
has real headroom and is not theatre), benchmark capture, the dependency-boundary
check, and a release gate that fails closed on an inconclusive result.

---

## 6. Honest limitations

1. **No race detector run exists.** Nothing in this phase weakens or strengthens
   any concurrency claim in Phases 10A–10F. The concurrency evidence remains
   what it was: extensive behavioural testing — 192 concurrent evaluations, 200
   evaluations against 40 concurrent registry publications, `-count=5
   -shuffle=on` across eight modules — which demonstrates that no race was
   severe enough to corrupt a result *in those runs*. That is evidence, not
   proof, and it is the same evidence as before.

2. **The new shared metrics package is concurrent and unchecked by the
   detector.** `packages/go/metrics` is now on the hot path of all six
   subsystems. Its `TestConcurrent_AllInstruments` drives 32 goroutines × 200
   iterations across a counter, a gauge, a histogram and concurrent snapshots,
   and the totals reconcile exactly. Under `-race` that test would be
   meaningful; without it, it is the same class of evidence as everything else.
   **Consolidating six implementations into one changes the risk profile in both
   directions** — one implementation to get right instead of six, but a defect in
   it now affects every subsystem rather than one.

3. **The workflows themselves are unexecuted.** They are YAML-valid (checked)
   and their shell logic is straightforward, but "parses correctly" is not "runs
   correctly". Expect a first run to surface something.

---

## 7. Status

**B1 remains open, and remains the platform's top blocker.** What changed:

| | Before | After |
|---|---|---|
| Characterisation | "no CI configuration for race" | "no CI has ever run, because the repo is not in git" |
| Race configuration | believed missing | **already existed**, plus a dedicated gate added |
| CI correctness | unknown | three blocking defects found and fixed |
| Race detector runs | zero | zero |

The remaining work is two steps, and neither is a code change:

1. `git init`, commit, push to a remote.
2. Let `hardening.yml` run, and read the artifact.

Until step 2 produces a log, every concurrency claim across 41,920 lines rests
on the absence of observed symptoms.

---

## 8. Related

- [CICD_READINESS_REPORT.md](CICD_READINESS_REPORT.md)
- [FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md)
