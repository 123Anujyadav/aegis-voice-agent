# Dependency Audit — Phase 10.5

---

## 1. Headline

**The Go plane of this repository has zero third-party dependencies.**

Not "few". Not "well-chosen". Zero. Every `require` line across
`packages/go/**` and `services/go/**` resolves to a first-party module:

```console
$ cat packages/go/*/go.mod services/go/*/go.mod \
    | grep -E '^\s+[a-z0-9-]+\.[a-z]+/' | awk '{print $1}' | sort -u
github.com/callscreen/callscreen-platform/packages/go/conversation
github.com/callscreen/callscreen-platform/packages/go/eventbus
github.com/callscreen/callscreen-platform/packages/go/evaluation
github.com/callscreen/callscreen-platform/packages/go/governance
github.com/callscreen/callscreen-platform/packages/go/memory
github.com/callscreen/callscreen-platform/packages/go/metrics
github.com/callscreen/callscreen-platform/packages/go/outbox
github.com/callscreen/callscreen-platform/packages/go/platform
github.com/callscreen/callscreen-platform/packages/go/redis
github.com/callscreen/callscreen-platform/packages/go/repository
github.com/callscreen/callscreen-platform/packages/go/runtime
github.com/callscreen/callscreen-platform/packages/go/toolruntime
```

Confirmed transitively for the AI plane:

```console
$ for m in metrics runtime conversation memory toolruntime governance \
           evaluation evalsubjects; do
    (cd packages/go/$m && go list -deps ./... | grep -E '^[a-z0-9-]+\.[a-z]+/' \
      | grep -v callscreen | wc -l)
  done
0 0 0 0 0 0 0 0
```

The transitive closure of every AI-plane module is **the Go standard library
plus first-party code**.

---

## 2. What that buys

This is the platform's single strongest security property and it should be
stated in those terms.

- **No supply-chain surface.** No dependency confusion, no typosquat, no
  compromised maintainer, no transitive package with a postinstall equivalent.
  The most security-sensitive component in the platform — the runtime that sits
  between a hostile caller's speech and a language model — has an attack surface
  of exactly the Go standard library.
- **No CVE exposure from third parties.** `govulncheck` (wired in `pr-go.yml`)
  scans the standard library too, which is where any finding will come from.
- **No license risk.** See §4.
- **No version-skew failures.** Nothing to pin, nothing to resolve, no diamond
  dependency.
- **Offline builds and tests.** Every module compiles and its full suite runs
  with no network. Phase 10A made this a design constraint; it still holds after
  six phases and one new module.

---

## 3. The new module preserves it

`packages/go/metrics` was added this phase and carries the same constraint:

```
module github.com/callscreen/callscreen-platform/packages/go/metrics
go 1.25.0
# no require block
```

`packages/go/runtime` previously had **no `require` at all** and now requires
`metrics`. Its `go.mod` header claims "THIS MODULE HAS NO EXTERNAL
DEPENDENCIES". That claim is still true — `metrics` is first-party and itself
dependency-free, so the transitive closure is unchanged — but the module is no
longer require-free, and that is a real change to a frozen phase, made under the
Section 2 mandate. Recorded rather than glossed.

---

## 4. Licenses

**No third-party code is vendored, imported or built into any Go artifact**, so
there is no third-party license obligation on the Go plane. The only license in
play is the Go standard library's BSD-3-Clause, via the toolchain.

Automated license scanning is therefore **not currently useful for Go** and would
report an empty set. It becomes necessary the moment the first external
dependency lands — most likely OpenTelemetry, per
[OBSERVABILITY_AUDIT.md](OBSERVABILITY_AUDIT.md) O1/O4.

**Recommendation:** add license scanning to CI **at the same time** as the first
third-party dependency, not before. A scanner that always reports "clean" trains
people to ignore it, and it will be ignored on the day it first has something to
say.

**Out of scope for this audit:** the Python, Node/pnpm and Gradle planes have
real third-party dependencies. They are governed by `uv.lock`, `package.json`
and the Gradle version catalog, and by `pr-python.yml`. This audit covers Go.

---

## 5. Version pinning

| Mechanism | Status |
|---|---|
| `go.work` | Pins the workspace at Go 1.25.0 |
| Module `go` directives | All eight AI modules at 1.25.0 |
| `replace` directives | Relative paths for every first-party module |
| `go.sum` | **Absent from all eight AI modules** — see D1 |
| `GOFLAGS: -mod=readonly` | Set in CI; a build cannot rewrite its own manifest |
| `GOTOOLCHAIN: local` | Set in CI this phase; no implicit toolchain downloads |
| Renovate | Configured, grouped updates, auto-merge on green |
| Dependabot | Configured for security advisories only |

The Renovate/Dependabot split is well reasoned in `renovate.json5`: Renovate
keeps things current, Dependabot says when something is on fire. Neither has
anything to do on the Go plane today.

### D1 — No `go.sum`, and CI's cache key expects one *(low)*

No AI module has a `go.sum`, because none has a dependency to checksum. But
`pr-go.yml` sets:

```yaml
cache-dependency-path: ${{ matrix.module }}/go.sum
```

`setup-go` warns and continues, so caching degrades rather than the build
failing. Recorded and left: special-casing dependency-free modules is more
machinery than the warning costs.

**This is a good problem.** It is the platform's strongest supply-chain property
manifesting as a CI wart.

### D2 — `replace` directives are load-bearing *(informational)*

Every first-party require is paired with a relative `replace`, because the
repository is private and unpublished so the module paths are not fetchable.
`go.work` documents this at length and correctly calls it "load-bearing, not
legacy". It also keeps each module buildable standalone under `GOWORK=off`,
which is how CI proves a `go.mod` is genuinely self-sufficient rather than
leaning on the workspace.

Removed when the repository is published. No action.

---

## 6. Supply-chain risk assessment

| Vector | Exposure |
|---|---|
| Malicious third-party package | **none** — there are none |
| Dependency confusion | **none** — no external module paths resolved |
| Typosquatting | **none** |
| Compromised transitive dependency | **none** |
| Compromised Go toolchain | present, and irreducible. Mitigated by pinning `GO_VERSION` and `GOTOOLCHAIN: local` so CI cannot silently acquire an unpinned toolchain — a gap that existed until this phase. |
| Standard library CVE | present. `govulncheck` covers it, and it has never run — see [RACE_VERIFICATION_REPORT.md](RACE_VERIFICATION_REPORT.md) §2. |
| Compromised CI action | present. `actions/checkout@v4`, `setup-go@v5`, `upload-artifact@v4` are referenced by tag, not by commit SHA. |

### D3 — GitHub Actions are pinned by tag, not SHA *(medium, open)*

A tag is mutable. `actions/checkout@v4` resolves to whatever the `v4` tag points
at today, and an upstream compromise would execute in a workflow that has
`contents: read` and access to the build.

This is the platform's **largest remaining supply-chain exposure**, and it is
mildly ironic: the Go code has none, and the machinery that builds it has the
usual amount.

**Recommendation:** pin every action to a commit SHA with the version in a
trailing comment, and let Renovate manage the updates — it understands SHA pins.

---

## 7. Findings

| ID | Finding | Severity | Status |
|---|---|---|---|
| D1 | No `go.sum`; CI cache key expects one | low | open, benign |
| D2 | `replace` directives load-bearing until publication | info | no action |
| D3 | GitHub Actions pinned by mutable tag | medium | open |
| — | Third-party dependencies | — | **zero** |
| — | License obligations | — | **none on the Go plane** |

---

## 8. Related

- [SECURITY_HARDENING_REPORT.md](SECURITY_HARDENING_REPORT.md)
- [CICD_READINESS_REPORT.md](CICD_READINESS_REPORT.md)
