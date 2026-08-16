# CallScreen Platform

An Android-first AI call screening assistant, built India-first.

This is the platform monorepo: the Android client, the Go services on the
realtime telephony path, the Python services in the AI tier, the shared
contracts they all speak, and the infrastructure that runs them.

> **Phase status: 2 — Project Bootstrap.**
> The engineering foundation is complete and verified. There is deliberately no
> product behaviour yet: no authentication, no telephony, no AI, no screening,
> no UI screens. Every service boots, reports health and shuts down gracefully,
> and nothing more. See [Phase status](#phase-status) for exactly what exists.

---

## Quick start

```bash
git clone <repo> && cd callscreen-platform
task bootstrap      # install and verify every toolchain dependency
task verify         # run the full local quality gate
```

If `task bootstrap` fails on a clean clone, that is a **P1 for the platform
team** — not something to work around. Report it.

### Required toolchains

| Tool       | Version  | Needed for                    |
| ---------- | -------- | ----------------------------- |
| Go         | 1.23+    | Go services and shared packages |
| Python     | 3.12     | AI tier (managed by `uv`)     |
| uv         | 0.5+     | Python workspace              |
| JDK        | 17       | Android (auto-provisioned by Gradle) |
| Android SDK| API 35   | Android client                |
| buf        | 1.72+    | Contracts                     |
| Node       | 22+      | Repository tooling            |
| pnpm       | 9+       | Repository tooling            |
| Task       | 3+       | The task runner itself        |
| Docker     | 24+      | Local stack                   |

`task bootstrap:check` reports which are missing.

---

## Layout

```
android/            Android client. Self-contained Gradle build.
services/go/        Telephony plane and application services.
services/python/    AI tier.
contracts/          Protobuf. THE integration boundary — single source of truth.
packages/           Shared libraries and generated bindings. Strict admission rules.
infra/              Terraform, Kubernetes, Helm, policy, observability.
docker/             Container builds and the local development stack.
tools/              Developer CLI, codegen, SIP harness, audio fixtures.
tests/              Cross-service tests only. Unit tests live beside their source.
docs/               ADRs, architecture, runbooks, compliance, onboarding.
```

Each directory's own README explains why it exists. The rules governing what may
be added to `packages/` are in
[docs/adr/0001](docs/adr/0001-monorepo-structure-and-tooling.md) — they are
deliberately strict, because uncontrolled sharing is the primary way a monorepo
rots.

---

## Everyday commands

Everything goes through `task`. Run `task --list` for the full set.

| Command             | Does                                                |
| ------------------- | --------------------------------------------------- |
| `task verify`       | The full local gate. **Run before pushing.**        |
| `task build`        | Build every artefact                                 |
| `task test`         | Run every test suite                                 |
| `task lint`         | Run every linter                                     |
| `task format`       | Auto-format the whole repository                     |
| `task up` / `down`  | Start / stop the local stack                         |
| `task contracts:generate` | Regenerate language bindings from `.proto`     |
| `task android:build:debug` | Fast Android inner loop                      |

### A note on `go build ./...`

It does not work from the repository root, and that is correct. The root has no
`go.mod`, so it is not inside any module, and Go rejects the pattern. Every Go
task iterates the module list instead — see the comment in `Taskfile.yml`.

---

## How this repository is built

Four things are worth knowing before your first change.

**Contracts come first.** `contracts/proto` is the single source of truth for
every cross-service type. Bindings for Go, Python, Kotlin and TypeScript are
generated from it and committed, and CI fails if the committed output does not
match a fresh generation. Never hand-edit anything under `packages/*/contracts-*`.

**Breaking a contract breaks users.** Android clients stay on an old version for
months and cannot be rolled back. `buf breaking` runs on every PR and is not a
formality: a wire-incompatible change is a product incident, not a build failure.

**Personal data is classified in the schema.** `contracts/proto/callscreen/common/v1/annotations.proto`
tags every field carrying personal data, and the platform libraries read those
annotations to drive log redaction, retention and residency automatically. This
is how DPDP compliance stays true as the code changes, rather than living in a
spreadsheet that drifts within a sprint.

**Convention plugins configure Android, not the root build file.** A module's
`build.gradle.kts` should declare its dependencies and nothing else. Everything
else comes from `android/build-logic`.

---

## Phase status

**What exists and is verified**

- Monorepo structure, conventions, and the Taskfile facade
- Contracts workspace: lint, format and four-language codegen all green
- `packages/go/platform` — config, structured logging with mandatory redaction,
  health, and the graceful-shutdown lifecycle. Zero external dependencies.
- `packages/go/eventbus` — Kafka topic and consumer-group naming contract
- `packages/python/platform` — the deliberate mirror of the Go module
- 8 Go services and 6 Python services, each booting configuration, logging,
  health and lifecycle
- Android build: version catalog, six convention plugins, 14 core modules, app
  shell, three build variants
- Docker builds, local stack, CI workflows, and the quality-gate configuration

**What does NOT exist yet, by design**

- Authentication, telephony, AI, call screening, UI screens, business logic
- Database schemas and migrations
- Feature modules under `android/feature/`

**Known gaps carried into Phase 3**

- `docs/architecture/` is empty. The Phase 0 architecture document was never
  produced, so ADRs 0002–0012 are outstanding and must be written before feature
  work begins. ADR-0001 is the only accepted record today.
- Container base images are not yet digest-pinned. Renovate resolves and pins
  them on its first run; the digests were deliberately left absent rather than
  guessed.
- Terraform, Kubernetes and Helm directories are structural only.

---

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md). The short version:

- Trunk-based. Branches live **under 48 hours**. Use feature flags, not long
  branches.
- Conventional Commits with a scope from the closed list in
  `commitlint.config.mjs`. CI enforces it.
- Warnings are errors. Every gate is blocking. Waivers require an owner and an
  expiry date.
- No new code without tests. A bug fix starts with a failing test.

## Security

Report vulnerabilities per [SECURITY.md](SECURITY.md). Never open a public
issue for one.

No secret ever enters this repository, a container image, or an APK. An APK is
a public artefact — treat every byte in it as published.
