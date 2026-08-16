# Contributing

## Before your first change

```bash
task bootstrap        # install and verify toolchains
task verify           # confirm the repository is green before you touch it
pre-commit install    # optional but recommended
```

Starting from a green tree matters: if `task verify` fails on a fresh clone,
that is a platform-team P1, not something to work around.

---

## Branching

**Trunk-based, with short-lived branches and a merge queue.**

- Branch from `main`. Name it `<type>/<ticket>-<slug>`, e.g.
  `feat/CS-142-call-forward-setup`.
- **Branches live under 48 hours.** This is the single most important rule here.
  Long-lived branches are the root cause of merge pain, and the fix is feature
  flags rather than a longer branch.
- Squash-merge only. One commit per logical change on `main` keeps `git bisect`
  meaningful and revert atomic.
- Never fix on a release branch first. Hotfixes land on `main`, then are
  cherry-picked — the reverse is how a fix gets lost in the next release.

Types: `feat` `fix` `chore` `docs` `refactor` `perf` `test` `ci` `hotfix`
`spike` (spikes are never merged; capture the learning in an RFC and delete the
branch).

Release branches exist for **Android only** (`release/android/<minor>.x`).
Services deploy continuously from `main`; Android cannot, because Play review
latency and staged rollout mean a shipped APK lives for weeks.

---

## Commits

[Conventional Commits](https://www.conventionalcommits.org), enforced in CI.

```
<type>(<scope>): <subject>      # imperative mood, ≤72 chars, no trailing period

<body>                          # WHY, not what. Wrapped at 100.

Refs: CS-142
```

**The scope is required and comes from a closed list** in
`commitlint.config.mjs`. Open scopes drift — `telephony`, `telephony-gw` and
`tg` all appear within a quarter — and the drift destroys automated changelog
grouping. A new service adds its scope in the same PR that creates it.

Breaking changes need `!` after the scope *and* a `BREAKING CHANGE:` footer.
Anything touching `contracts/` additionally needs a linked ADR.

---

## What every change must include

**Tests.** No new code without them. A bug fix starts with a failing test that
reproduces the bug — write it first and watch it fail, or you have not proven it
tests anything.

**Documentation, in the same PR**, if the change touches a service's public
contract, adds an alert, or changes a data flow. A doc change in a follow-up PR
is a doc change that does not happen.

**An ADR**, if the change is expensive to undo in twelve months. Concretely: a
new service or service boundary, a new vendor with lock-in, a breaking contract
change, a datastore or retention/residency choice, a change to the auth model,
or any deviation from these conventions. Scaffold one with
`task docs:adr TITLE="…"`.

---

## Quality gates

Every gate is **blocking**. Warnings are errors, everywhere.

| Gate                          | Threshold                        |
| ----------------------------- | -------------------------------- |
| Coverage, new/changed lines   | ≥ 85%                            |
| Coverage, critical modules    | ≥ 90%                            |
| Coverage, overall             | must not decrease                |
| Contract breaking change      | `buf breaking` clean             |
| Codegen drift                 | generated output matches source  |
| Secret scan / SAST            | zero findings                     |
| Dependency CVE                | zero critical                     |
| Android APK size              | delta > 2% fails                 |

Critical modules: `session-orchestrator`, `fraud-engine`, `billing`, `identity`,
`core/telephony`.

**Waivers** need a `waiver/<gate>` label, a written justification, a named
owner, and an expiry date. Expired waivers auto-open a blocking issue. There are
no permanent bypasses.

---

## Code conventions

Universal:

- **Errors are values, never swallowed.** Go wraps with `%w`. Python uses the
  typed hierarchy from `platform`, never bare `except:`. Kotlin uses sealed
  `Result` and does not throw across a module boundary.
- **No naked I/O in domain logic.** External calls sit behind an interface owned
  by the domain layer. This is what makes the AI and telephony tiers testable
  without vendors.
- **Every network call has an explicit timeout, retry policy and circuit
  breaker.** A call without a timeout fails review. In a realtime voice system an
  unbounded call is an outage waiting for enough concurrency.
- **Structured logging only.** No interpolation into log messages. Every record
  carries `trace_id` and `call_session_id`.
- **PII never enters logs.** Enforced by schema annotations plus the redaction
  layer in `packages/*/platform` — but do not rely on it as a licence to be
  careless.
- **Public functions are documented**, including their *concurrency and blocking
  behaviour*. That last part is the one people skip and the one that causes
  incidents.
- **Comments explain WHY.** A comment restating the code is noise.

Per language: `gofumpt` + `golangci-lint` for Go; `ruff` + `mypy --strict` for
Python; `ktfmt` + `ktlint` + `detekt` for Kotlin. Suppressions
(`//nolint`, `# noqa`, `@Suppress`) require an inline reason — a bare
suppression fails lint.

**A file past ~400 lines, or a module past ~15 files, is a prompt to discuss
boundaries.** Not a hard failure.

---

## Monorepo rules

1. Dependencies flow **one direction**: `contracts` → `packages` →
   `services`/`android`. Never upward, never sideways between services.
2. A service may not import another service's internal code. Cross-service
   communication goes over the wire, through `contracts`.
3. Android `feature/` modules never depend on each other — only on `core/`.
4. **No `common`/`utils`/`helpers` grab-bag packages.** No cohesion, no owner,
   and they become a dependency magnet. Rejected without exception.
5. **No shared business logic across service boundaries.** If two services both
   need "is this subscription active", one owns it and the other asks over the
   wire. Sharing it makes them one service pretending to be two.
6. Generated code is committed and never hand-edited.
7. A new service is not done when it compiles — it is done when it is
   **operable**: contract, ADR, CODEOWNERS entry, Terraform, overlay,
   dashboards, alerts, runbook, SLO, CI wiring.
8. **Deleting code is a feature.** Flags carry expiry dates.

---

## Review

- Small PRs. Spanning more than five top-level directories needs justification.
- CODEOWNERS approval is required and is not a formality — those owners are
  accountable for that code.
- Style is never a review topic. Formatters own it; if you are arguing about
  whitespace, the formatter configuration is the thing to change.
- **Flaky tests are quarantined within 24 hours and fixed or deleted within a
  week.** Untrusted CI is worse than no CI.

---

## Getting help

- `docs/onboarding/` — day 1 to day 30
- `docs/adr/` — why things are the way they are
- `docs/runbooks/` — one per alert, one per service
- `task --list` — every available command
