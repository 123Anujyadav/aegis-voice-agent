# CI/CD Readiness Report — Phase 10.5

**Verdict:** the pipeline is now correct. It has still never run.

---

## 1. The finding that reframed this section

The repository is **not under version control**.

```console
$ git rev-parse --is-inside-work-tree
fatal: not a git repository (or any of the parent directories): .git
```

No `.git`, no remote, no commits. Five GitHub Actions workflows exist and **not
one has ever executed**.

That matters more than any individual defect below, and it had been hiding
behind a narrower description of itself. Four consecutive phase audits recorded
"`-race` has never been run" and recommended "create the CI configuration". The
configuration was there all along. What is missing is a git repository.

---

## 2. Workflow inventory

| Workflow | Purpose | Status |
|---|---|---|
| `ci.yml` | Whole-repo build and test on push | **was broken**, fixed |
| `pr-go.yml` | Per-module matrix: build, race, lint, format, vulnerabilities | sound, three fixes |
| `pr-python.yml` | Python quality and lockfile | not reviewed (out of scope) |
| `pr-contracts.yml` | Buf lint, breaking-change and drift checks | not reviewed (out of scope) |
| `hardening.yml` | **New.** Race artifacts, coverage floor, benchmarks, boundary checks, release gate | added this phase |

All five parse as valid YAML (checked).

---

## 3. Defects found and fixed

A workflow that has never run accumulates errors silently. Three were blocking.

### C1 — Go version too old to build the platform *(fixed)*

```yaml
GO_VERSION: "1.23.5"      # pr-go.yml
go-version: '1.23.0'      # ci.yml
```

Every AI-plane module declares `go 1.25.0`.

Under `GOTOOLCHAIN=auto` — the default — Go 1.23 silently downloads a 1.25
toolchain that `setup-go` has not cached: slow on every run, and an undeclared
network fetch in a build that sets `-mod=readonly` precisely to avoid surprises.
Under a hardened `GOTOOLCHAIN=local` it fails outright.

**Fixed** in both: `GO_VERSION: "1.25.x"` and `GOTOOLCHAIN: "local"`, so the
version the file declares is the version the build uses, or the build says so.

### C2 — `ci.yml` could not build anything *(fixed)*

```yaml
- name: Build All Go Modules
  run: go build ./...
```

From the repository root, in a workspace, this fails — verified locally:

```console
$ go build ./...
pattern ./...: directory prefix . does not contain modules listed in go.work
```

The root has no `go.mod`, so it is not inside any module. `pr-go.yml`'s header
documents this exact hazard at length; `ci.yml` had it anyway, in both its build
and test steps. Two workflows, one that understood the workspace and one that
did not, and nothing to reconcile them because neither ever ran.

**Fixed** by iterating the module list derived from `go.work`, with the test
step collecting failures rather than stopping at the first — seeing every
failing module in one run is worth more than failing fast.

### C3 — No gate on the default branch *(fixed)*

`pr-go.yml` triggered only on `pull_request` and `merge_group`. A direct push to
`main` ran nothing at all.

**Fixed** by adding a `push` trigger on `main`.

### C4 — Cache key points at a file that does not exist *(open, low)*

```yaml
cache-dependency-path: ${{ matrix.module }}/go.sum
```

No AI-plane module has a `go.sum`, because none has a third-party dependency.
`setup-go` warns and continues, so caching degrades rather than the build
failing. Left as-is: special-casing dependency-free modules costs more machinery
than the warning does. See [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md) D1.

### C5 — Actions pinned by mutable tag *(open, medium)*

`actions/checkout@v4`, `setup-go@v5`, `upload-artifact@v4`. A tag is mutable, so
an upstream compromise executes inside the build. This is the platform's largest
remaining supply-chain exposure, which is mildly ironic given the Go code has
none. See [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md) D3.

---

## 4. What `hardening.yml` adds

Whole-platform properties that a per-module matrix cannot express.

**Race verification, two modes.** `single` (`-race -count=1`) and `repeated`
(`-race -count=5 -shuffle=on`). They find different things: one pass checks the
interleavings it happens to take; repeated shuffled runs re-order test execution
and re-time goroutines, which is how interleaving-dependent races surface.
Logs are captured as 30-day artifacts and grepped for `WARNING: DATA RACE`,
because `go test` can exit zero on a race in some configurations and because a
race report is something an engineer needs to *read*, not scroll past.

A cgo precondition check fails early with a clear message rather than letting
the run reach the confusing `-race requires cgo` error — the exact error that
blocked local verification for six phases.

**Nightly schedule.** The detector finds interleavings by observing them, so
more executions mean more coverage. A gate that only runs on changed code never
re-examines code that has stopped changing, which for six frozen phases is all
of it.

**Coverage with a floor of 60%.** Measured actuals:

| Module | Coverage |
|---|---:|
| `metrics` | 84.9% |
| `memory` | 80.7% |
| `governance` | 77.1% |
| `toolruntime` | 76.2% |
| `conversation` | 76.1% |
| `evaluation` | 75.9% |
| `runtime` | 72.8% |
| `evalsubjects` | 71.2% |

A floor, not a ratchet. Ratcheting coverage upward turns it into a number people
optimise; a floor catches a module whose tests were deleted or never written. At
60% against an actual range of 71–85% it has genuine headroom and is not theatre.

**Benchmarks recorded, not gated.** 90-day artifacts. A benchmark threshold on a
shared runner fails for reasons unrelated to the code, and a gate that fails for
unrelated reasons is one people learn to re-run until it passes. §4 of
[PERFORMANCE_VERIFICATION_REPORT.md](PERFORMANCE_VERIFICATION_REPORT.md) shows
40% between-session swings on an idle laptop; a CI runner is worse.

**Dependency boundary check.** The evaluation core must import nothing it
evaluates — the architecture's central claim, and one command to verify. Now
checked on every run instead of asserted in a document.

**Release gate.** Depends on all four jobs, runs with `if: always()` so it
reports *which* gate failed rather than being skipped and leaving a green-looking
summary, and **fails closed**: an inconclusive result is a failure, matching the
default-deny stance the governance engine takes.

---

## 5. What is NOT prepared

Deliberately, per the brief's "do not deploy anything":

- No deployment workflow, no environment, no cluster credentials.
- No release/tag automation, no changelog generation, no artifact publication.
- No container build or registry push.
- No migration runner. `Repository.Migrate` exists and is tested; nothing
  invokes it from a pipeline.

---

## 6. Readiness

| Capability | Status |
|---|---|
| Build | ✅ correct after C2 |
| Vet | ✅ `pr-go.yml` |
| Format | ✅ `gofumpt` in `pr-go.yml` |
| Lint | ✅ `golangci-lint` in `pr-go.yml` |
| Unit tests | ✅ per-module matrix |
| Race detection | ✅ configured, two modes, artifacts |
| Coverage | ✅ measured, floor enforced |
| Benchmarks | ✅ executed and archived |
| Vulnerability scan | ✅ `govulncheck` |
| Dependency boundary | ✅ new |
| Cross-subsystem observability | ✅ new |
| Release gate | ✅ new, fails closed |
| **Has ever executed** | ❌ **no** |

---

## 7. The remaining step

Everything above is preparation. The pipeline is correct as far as static review
can establish, and "parses correctly and reads correctly" is not "runs
correctly" — expect a first run to surface something, most likely in the shell
logic of the new jobs.

Two commands stand between here and a verified pipeline:

```console
$ git init && git add -A && git commit -m "Phases 10A–10.5"
$ git remote add origin <url> && git push -u origin main
```

Then read the `race-logs-repeated` artifact. Until that artifact exists, every
concurrency claim across 41,920 lines of Go rests on the absence of observed
symptoms rather than on evidence.

---

## 8. Related

- [RACE_VERIFICATION_REPORT.md](RACE_VERIFICATION_REPORT.md)
- [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md)
- [FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md)
