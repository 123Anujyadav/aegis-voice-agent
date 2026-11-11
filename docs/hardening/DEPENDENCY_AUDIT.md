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

### D3 — GitHub Actions are pinned by tag, not SHA *(medium; closed 2026-10-21, see §9)*

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
| D3 | GitHub Actions pinned by mutable tag | medium | **closed** 2026-10-21 (#51, §9) |
| — | Third-party dependencies | — | zero at Phase 10.5; **now present**, see §9 |
| — | License obligations | — | **none on the Go plane** |

---

## 8. Related

- [SECURITY_HARDENING_REPORT.md](SECURITY_HARDENING_REPORT.md)
- [CICD_READINESS_REPORT.md](CICD_READINESS_REPORT.md)

---

## 9. Status update (2026-10-21)

Sections 1 to 8 are the Phase 10.5 audit and are kept as written. Two of its
conclusions no longer hold.

**D3 is closed.** Every `uses:` in `.github/workflows/` is pinned to a full
commit SHA with its release as a trailing comment (#51). The commit for each was
read from the action's own repository, not derived from the tag.
`tools/ci/check_action_pins.py` rejects a tag, a branch, a short SHA or a SHA
without a version comment. Its tests run in ci-gate's unittest step on every PR,
including a check of this repository's own workflows.

**The Go plane now has third-party dependencies.** As of `main` `64717b8`, the
direct requirements outside this repository are:

| Module | Direct third-party requirements |
|---|---|
| `packages/go/contracts-go` | `google.golang.org/protobuf` |
| `packages/go/evalstore` | `github.com/jackc/pgx/v5` |
| `services/go/telephony-gateway` | `github.com/aws/aws-sdk-go-v2` (core, `credentials`, `feature/rds/auth`, `service/sts`), `github.com/jackc/pgx/v5` |

So the section 1 headline, the "none" rows for third-party and transitive
packages in section 6, and the "no license obligation on the Go plane" in
section 4 are out of date. `govulncheck` covers these modules on every Go PR
(45 modules, passing on Go 1.26.10 since #48), and `go.sum` records their
checksums. A license review of these dependencies has not been done: section
4's advice to add license scanning with the first third-party dependency
applies now and remains open.

Related controls added on 2026-10-21: CI secret scanning (`pr-security.yml`,
#50), Gradle wrapper verification (`pr-android-wrapper.yml`, #54), the Python
lock audit (`python-deps-audit.yml`, #55) and workflow linting
(`workflow-lint.yml`, #57).

---

## 10. Go licence inventory (2026-10-22)

Section 9 left a licence review of the Go plane's new third-party dependencies
open. This is that inventory. It records which licence text each shipped module
carries; it is not legal advice, and it draws no conclusion about obligations
beyond what the licence texts themselves say.

**Scope.** The modules whose packages are compiled into the non-test build of
the three modules that have third-party requirements (`main` `63cbed5`),
excluding the standard library and this repository's own modules:

```console
GOWORK=off GOFLAGS=-mod=readonly go list -deps \
  -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}' ./...
```

run in `packages/go/contracts-go`, `packages/go/evalstore` and
`services/go/telephony-gateway`. That is 18 module versions. Modules that appear
only in `go list -m all` (test-only or unused requirements) are not shipped and
are not listed.

**Method.** Each module's licence file, read from the Go module cache at the
exact version above, was identified by its standard text: "Apache License …
Version 2.0", the MIT permission grant, or the BSD redistribution conditions
(three-clause when it includes the "Neither the name…" clause). Nothing was
inferred from repository metadata. The sha256 prefix identifies the exact file.

| Module | Version | Licence file | Licence text | sha256 (prefix) |
|---|---|---|---|---|
| `github.com/aws/aws-sdk-go-v2` | v1.47.1 | `LICENSE.txt` (+ `NOTICE.txt`) | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/credentials` | v1.20.6 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/feature/rds/auth` | v1.7.4 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/internal/configsources` | v1.5.4 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | v2.8.4 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/internal/v4a` | v1.5.4 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | v1.13.19 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | v1.14.4 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/aws-sdk-go-v2/service/sts` | v1.51.1 | `LICENSE.txt` | Apache-2.0 | `cfc7749b96f63bd3` |
| `github.com/aws/smithy-go` | v1.28.1 | `LICENSE` (+ `NOTICE`) | Apache-2.0 | `09e8a9bcec806710` |
| `github.com/jackc/pgpassfile` | v1.0.0 | `LICENSE` | MIT | `adb1663fda031df8` |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | `LICENSE` | MIT | `fc505773403fe869` |
| `github.com/jackc/pgx/v5` | v5.10.0 | `LICENSE` | MIT | `467f95e074fe2307` |
| `github.com/jackc/puddle/v2` | v2.2.2 | `LICENSE` | MIT | `2d50e98a4900b4d6` |
| `golang.org/x/sync` | v0.21.0 | `LICENSE` | BSD-3-Clause | `911f8f5782931320` |
| `golang.org/x/text` | v0.39.0 (evalstore), v0.41.0 (gateway) | `LICENSE` | BSD-3-Clause | `911f8f5782931320` |
| `google.golang.org/protobuf` | v1.36.4 | `LICENSE` | BSD-3-Clause | `4835612df0098ca9` |

**What the texts show.** Every shipped module carries a permissive licence:
Apache-2.0 (10), MIT (4) or BSD-3-Clause (4 module versions). None carries a
copyleft licence.

**Open items, not resolved here:**
- `aws-sdk-go-v2` and `smithy-go` ship NOTICE files. Apache-2.0 section 4(d)
  asks that a redistributed work include the NOTICE text. Whether the service
  images, or any other distribution, carry it has not been checked, and it
  depends on the image strategy that is still undecided.
- MIT and BSD licences ask that the copyright and permission notice accompany
  copies; how this project reproduces third-party notices in its artefacts is
  not defined yet.
- This inventory is a point-in-time record. It is not enforced in CI; an
  automated licence check would have to run whenever `go.mod` changes.
- Python (`uv.lock`) and Android dependencies are not covered here.

---

## 11. Python licence inventory (2026-10-22)

The Python counterpart of section 10, with the same limits: it records the
licence text each locked package ships, it is not legal advice, and it draws no
conclusion beyond what those texts say.

**Scope.** Every third-party package `uv.lock` pins on `main` `9201a62` (25),
split by what reaches an image. `docker/python/Dockerfile` installs with
`uv sync --frozen --no-dev`, so only the runtime set is shipped:

```console
uv export --frozen --all-packages --no-emit-workspace [--no-dev]
```

**Method.** Each package was installed from the lock (`uv sync --frozen`,
uv 0.5.14) and its licence file read from the installed `.dist-info`, identified
by its standard text as in section 10. The `License-Expression` metadata, where
the package declares one, agrees with the file in every case.

**Runtime (shipped in images), 9 packages:**

| Package | Version | Licence file | Licence text | sha256 (prefix) |
|---|---|---|---|---|
| `annotated-types` | 0.8.0 | `licenses/LICENSE` | MIT | `fe1049884b1a0d93` |
| `protobuf` | 5.29.6 | `LICENSE` | BSD-3-Clause | `6e5e117324afd944` |
| `pydantic` | 2.13.4 | `licenses/LICENSE` | MIT | `a9e186f3ca16b5ee` |
| `pydantic-core` | 2.46.4 | `licenses/LICENSE` | MIT | `fbe7f615f18d135c` |
| `pydantic-settings` | 2.14.2 | `licenses/LICENSE` | MIT | `eb355a753e020346` |
| `python-dotenv` | 1.2.2 | `licenses/LICENSE` | BSD-3-Clause | `80619b7049f08c81` |
| `structlog` | 25.5.0 | `licenses/LICENSE-MIT`, `LICENSE-APACHE` (+ `NOTICE`) | MIT OR Apache-2.0 | `2bc12bb94eec2cce` (MIT file) |
| `typing-extensions` | 4.16.0 | `licenses/LICENSE` | PSF-2.0 | `3b2f81fe21d181c4` |
| `typing-inspection` | 0.4.2 | `licenses/LICENSE` | MIT | `804b59b25f2c31bd` |

**Development and test only (not installed in images), 16 packages:**

| Package | Version | Licence text |
|---|---|---|
| `attrs` | 26.1.0 | MIT |
| `colorama` | 0.4.6 | BSD-3-Clause |
| `coverage` | 7.15.4 | Apache-2.0 |
| `hypothesis` | 6.123.17 | **MPL-2.0** |
| `iniconfig` | 2.3.0 | MIT |
| `mypy` | 1.14.1 | MIT |
| `mypy-extensions` | 1.1.0 | MIT |
| `packaging` | 26.3 | Apache-2.0 OR BSD-2-Clause (its `LICENSE` points to `LICENSE.APACHE` and `LICENSE.BSD`) |
| `pluggy` | 1.6.0 | MIT |
| `pygments` | 2.21.0 | BSD-2-Clause |
| `pytest` | 9.0.3 | MIT |
| `pytest-asyncio` | 1.3.0 | Apache-2.0 |
| `pytest-cov` | 6.0.0 | MIT |
| `pytest-randomly` | 3.16.0 | MIT |
| `ruff` | 0.9.2 | Apache-2.0 |
| `sortedcontainers` | 2.4.0 | Apache-2.0 |

**What the texts show.** Every runtime package carries a permissive licence.
The one weak-copyleft licence, MPL-2.0 (`hypothesis`, a property-testing
library), is in the development set only and is not installed in images.

**Open items, not resolved here:**
- `structlog` ships a `NOTICE` file with its Apache-2.0 option. As in section 10,
  how third-party notices reach artefacts is not defined.
- This is a point-in-time record and is not enforced in CI.
- Android dependencies are not covered.

---

## 12. Go licence inventory, refreshed (2026-11-09)

Section 10 covered the three modules that had third-party requirements on
`63cbed5`. Since then identity, edge-api and `pgshared` ship third-party code
too, and identity added Google's OAuth2 library (the Play Integrity verifier's
token source). Section 9 says the licence review "applies now and remains
open"; this section brings the inventory up to date. Section 10 is kept as its
point-in-time record. As there, this records which licence text each shipped
module carries; it is not legal advice.

**Scope and method:** exactly section 10's. The same `go list -deps` command
(non-test build, `GOWORK=off`, `-mod=readonly`), run in **every** Go module
on `main` `4d88878`. Six modules ship third-party code:
- `packages/go/contracts-go`;
- `packages/go/evalstore`;
- `packages/go/pgshared`;
- `services/go/edge-api`;
- `services/go/identity`;
- `services/go/telephony-gateway`.

Every other module ships none. Each licence file was read from the module cache
at the exact version, and identified by its standard text. The sha256 prefixes
of the files section 10 also read are identical.

**New since section 10** (modules or versions not in its table):

| Module | Version | Shipped in | Licence file | Licence text | sha256 (prefix) |
|---|---|---|---|---|---|
| `golang.org/x/oauth2` | v0.36.0 | identity | `LICENSE` | BSD-3-Clause | `911f8f5782931320` |
| `cloud.google.com/go/compute/metadata` | v0.3.0 | identity (through `x/oauth2`) | `LICENSE` | Apache-2.0 | `cfc7749b96f63bd3` |
| `golang.org/x/sync` | v0.17.0 | edge-api, identity, `pgshared` | `LICENSE` | BSD-3-Clause | `911f8f5782931320` |
| `golang.org/x/text` | v0.29.0 | edge-api, identity, `pgshared` | `LICENSE` | BSD-3-Clause | `911f8f5782931320` |

**Unchanged modules, now shipped in more places:**

| Module | Version | Licence | Shipped in |
|---|---|---|---|
| `github.com/jackc/pgx/v5` | v5.10.0 | MIT | edge-api, evalstore, identity, `pgshared`, telephony-gateway |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | the same five |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT | the same five |
| `github.com/jackc/puddle/v2` | v2.2.2 | MIT | edge-api, evalstore, identity, `pgshared` |

The AWS SDK modules, `smithy-go`, `golang.org/x/sync` v0.21.0, `golang.org/x/text`
v0.39.0 and v0.41.0, and `google.golang.org/protobuf` v1.36.4 are unchanged
from section 10, in the same modules.

**What the texts show:** every shipped module still carries a permissive
licence (Apache-2.0, MIT or BSD-3-Clause), and none carries a copyleft licence.
The one new Apache-2.0 module, `compute/metadata`, ships **no** NOTICE file.

**The identity and edge-api images:** these are the two services with container
images today (`container-images.yml`). They ship:
- `pgx` and its three helpers (MIT);
- `x/sync` and `x/text` (BSD-3-Clause);
- in identity only, `x/oauth2` (BSD-3-Clause) and `compute/metadata`
  (Apache-2.0).

**Open items, unchanged and not resolved here:**
- **How third-party notices reach artefacts** is not defined. That covers the
  MIT and BSD copyright and permission notices, and the AWS NOTICE files. It
  depends on the image strategy, which is still undecided (phase16 README).
- **Point-in-time record:** this is not enforced in CI. A licence check would
  have to run whenever a `go.mod` changes.
- **Version skew:** `x/sync` and `x/text` are at different versions in
  different modules. Each is covered by `govulncheck` on every Go PR. Aligning
  them is a dependency update, not part of this inventory.

---

## 13. Container base-image maintenance (OD-16, 2026-11-10)

**Decision:** the owner, 2026-11-10 (OD-16; `docs/phase19/OWNER-DECISION-PACKET-2026-11-09.md` §2 and §5). Renovate is **not** installed. Dependabot, already installed, is the updater.

### Policy

| Image | Where | Updates | Why |
|---|---|---|---|
| **The Go builder** `golang:1.26.11-alpine3.23` | `docker/go/Dockerfile`, both build stages (`manifests`, `build`) | **Dependabot version updates**: `/docker/go`, daily (the existing cadence), at most one open PR, **patch releases only**. Security updates as published | Tested: `container-images.yml` builds identity and edge-api and smoke-tests them on every change to the Dockerfile |
| **Python** `python:3.12.8-slim-bookworm`, and `ghcr.io/astral-sh/uv:0.5.14` copied into it | `docker/python/Dockerfile` | **Security updates only** (`open-pull-requests-limit: 0`) | **No CI job builds or tests this image,** so a routine update could not be verified before merging. It ships OS packages, unlike the Go images |
| `scratch` (the identity and edge-api runtime) | `docker/go/Dockerfile`, `runtime` stage | None: it has no contents | Nothing to update |

**Out of scope, unchanged:** `docker/Dockerfile.base` (`golang:1.26.11-alpine3.23` and `alpine:3.20`; built by no workflow), and the development-only Compose images (`postgres`, `redis`).

### The pinned Go builder image

- **Reference:** `golang:1.26.11-alpine3.23@sha256:db99415e2625ce0ee03eed09edf7f32ae8c58dc399f28cc602a87fe7238119e4` (updated 2026-11-11 for GO-2026-6859, `net/http`, fixed in go1.26.11; previously `golang:1.26.10-alpine3.23@sha256:78e7f1e419545bc4b8b5ee8a40a99ddb5df1216d38681dbab2eeb1fdb2427f43`, verified 2026-11-10 as below)
- **What the digest is:** the multi-architecture **image index** (OCI). So the same reference serves amd64 and arm64.
- **How it was verified (2026-11-10):**
  - the registry's `Docker-Content-Digest` for `library/golang:1.26.10-alpine3.23`, read from `registry-1.docker.io`;
  - the Docker Hub API's `digest` for the tag (last updated 2026-11-06), which agreed;
  - the amd64 and arm64 image configurations set `GOLANG_VERSION=1.26.10` and `GOTOOLCHAIN=local`;
  - the base layer is `alpine-minirootfs-3.23.8`.
- **2026-11-11, by hand (the procedure below):** the registry's `Docker-Content-Digest` for `library/golang:1.26.11-alpine3.23` and the Docker Hub API's digest agreed on `sha256:db99415e2625ce0ee03eed09edf7f32ae8c58dc399f28cc602a87fe7238119e4`; the amd64 and arm64 configurations set `GOLANG_VERSION=1.26.11` and `GOTOOLCHAIN=local`.
- **The Go version** is 1.26.11, equal to `GO_VERSION` in the workflows.
- **Why the pin matters:**
  - a locally cached copy pulled before 2026-11-06 had another digest (`sha256:a07f231c…`);
  - the registry's digest for this tag also changed on 2026-11-01;
  - the tag alone does not reproduce what was tested.

### How the version and digest are updated together

- **Dependabot:** for an image pinned as `tag@digest`, a Dependabot PR replaces both, in every `FROM` line of the Dockerfile. `container-images.yml` builds and smoke-tests the result. Merging needs every required check green, as for any PR.
- **Patch releases only.** The `ignore` rule skips Go minor and major versions, such as 1.27. A new minor is a **deliberate toolchain change**: the Dockerfile tag and `GO_VERSION` in the workflows move together in one reviewed PR.
- **A Dependabot patch PR also needs a matching `GO_VERSION` bump.** For example, 1.26.10 to 1.26.11: change `GO_VERSION` in the workflows in the same PR, so that CI tests and scans the toolchain the image ships. The Dockerfile's comment says so.
- **By hand**, if ever needed:
  1. read the new tag's **index** digest from the registry, using `docker buildx imagetools inspect <tag>` or the registry's `Docker-Content-Digest`;
  2. cross-check it against Docker Hub;
  3. change the tag and the digest **together** in both stages.

### Limitations and residual risks

- **Whether Dependabot also proposes digest-only updates** (the same tag, rebuilt upstream) is **not verified here**. It will be seen on its first runs. Without such updates, a rebuilt tag is not followed until the next patch release. That is the intended trade: reproducibility first.
- **The Python image** has no CI build or test. Its security PRs, when they come, need a manual `docker build` and a check before merging. A CI job for it is a follow-up for when a Python service is deployed.
- **`renovate.json5`** still describes Renovate as the routine updater. It is inactive configuration: Renovate is not installed. It was left unchanged here: correcting or removing it is a separate change.
- **F14** (whether Dependabot rebases its open PRs, re-running full CI) is a separate owner decision and is unchanged. With at most one open Docker PR, its CI load is small.
